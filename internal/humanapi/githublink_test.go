// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/ghidentity"
	"github.com/Smana/maku/internal/store"
)

// memGitHub is the store's GitHub link tables, kept to the store's rules (store/githublinks_test.go
// proves those against PostgreSQL).
type memGitHub struct {
	mu     sync.Mutex
	states map[string]*store.GitHubState
	links  map[string]store.GitHubLink
	events []string
}

func newMemGitHub() *memGitHub {
	return &memGitHub{states: map[string]*store.GitHubState{}, links: map[string]store.GitHubLink{}}
}

func (m *memGitHub) StartGitHubLink(_ context.Context, st []byte, sub string, nonce []byte, exp, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, v := range m.states {
		if !v.Expires.After(now) {
			delete(m.states, k)
		} else if v.Sub == sub {
			n++
		}
	}
	if n >= store.MaxGitHubPending {
		return store.ErrTooManyPending
	}
	m.states[hex.EncodeToString(st)] = &store.GitHubState{Sub: sub, NonceSHA256: nonce, Expires: exp}
	return nil
}

func (m *memGitHub) GitHubState(_ context.Context, st []byte, now time.Time) (store.GitHubState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.states[hex.EncodeToString(st)]
	if !ok || !v.Expires.After(now) {
		return store.GitHubState{}, store.ErrStateInvalid
	}
	return *v, nil
}

func (m *memGitHub) SetGitHubStateUser(_ context.Context, st []byte, id int64, login string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.states[hex.EncodeToString(st)]
	if !ok || !v.Expires.After(now) || v.GitHubID != 0 {
		return store.ErrStateInvalid
	}
	v.GitHubID, v.Login = id, login
	return nil
}

func (m *memGitHub) ConfirmGitHubLink(_ context.Context, st []byte, sub string, now time.Time) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := hex.EncodeToString(st)
	v, ok := m.states[k]
	switch {
	case !ok || !v.Expires.After(now) || v.GitHubID == 0:
		return "", store.ErrStateInvalid
	case v.Sub != sub:
		return "", store.ErrSubMismatch
	}
	for s, l := range m.links {
		if l.GitHubID == v.GitHubID && s != sub {
			return "", store.ErrGitHubTaken
		}
	}
	act := store.GitHubLinked
	if _, had := m.links[sub]; had {
		act = store.GitHubRelinked
	}
	m.links[sub] = store.GitHubLink{Sub: sub, GitHubID: v.GitHubID, Login: v.Login, LinkedAt: now, LastSeenAt: now}
	m.events = append(m.events, sub+":"+act)
	delete(m.states, k)
	return act, nil
}

func (m *memGitHub) GitHubLinkOf(_ context.Context, sub string) (store.GitHubLink, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[sub]
	return l, ok, nil
}

func (m *memGitHub) UnlinkGitHub(_ context.Context, sub, actor string, admin bool, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.links[sub]; !ok {
		return false, nil
	}
	delete(m.links, sub)
	act := store.GitHubUnlinked
	if admin {
		act = store.GitHubAdminUnlinked
	}
	m.events = append(m.events, sub+":"+act+":"+actor)
	return true, nil
}

func (m *memGitHub) TouchGitHubLink(_ context.Context, sub string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[sub]
	if !ok || now.Sub(l.LastSeenAt) <= time.Hour {
		return false, nil
	}
	l.LastSeenAt = now
	m.links[sub] = l
	return true, nil
}

func (m *memGitHub) linked(sub string) (store.GitHubLink, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[sub]
	return l, ok
}

// fakeGitHub is GitHub's web flow: the code it issued for each challenge, the user each token is.
type fakeGitHub struct {
	mu         sync.Mutex
	challenges map[string]string // code -> challenge it was issued for
	users      map[string]int64  // code -> GitHub id
	exchanges  int
	revoked    []string
	fail       bool
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{challenges: map[string]string{}, users: map[string]int64{}}
}

func (f *fakeGitHub) AuthorizeURL(clientID, redirect, state, challenge string) string {
	return "https://github.example.test/login/oauth/authorize?" + url.Values{"client_id": {clientID}, "redirect_uri": {redirect},
		"state": {state}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "allow_signup": {"false"}}.Encode()
}

// authorize is the user approving at GitHub as id: a code bound to the challenge.
func (f *fakeGitHub) authorize(challenge string, id int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	code := "ghcode-" + challenge[:8]
	f.challenges[code], f.users[code] = challenge, id
	return code
}

func (f *fakeGitHub) ExchangeCode(_ context.Context, clientID, secret, code, verifier, redirect string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exchanges++
	if f.fail || clientID != "Iv1.client" || secret != "csecret" || redirect != linkOrigin+"/link/github/callback" {
		return "", errors.New("github oauth: the code exchange was refused: bad_verification_code")
	}
	if ch, ok := f.challenges[code]; !ok || ch != pkceChallenge(verifier) {
		return "", errors.New("github oauth: the code exchange was refused: bad_verification_code")
	}
	return "gho_" + code, nil
}

func (f *fakeGitHub) User(_ context.Context, token string) (int64, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.users[strings.TrimPrefix(token, "gho_")]
	return id, "gh" + strings.Repeat("x", int(id%3)), nil
}

func (f *fakeGitHub) RevokeUserToken(_ context.Context, _, _, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, token)
	return nil
}

func githubLinkOn(m *memGitHub, gh *fakeGitHub, logs *bytes.Buffer, now *time.Time) option {
	return func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.PublicURL = linkOrigin
		s.GitHubLink = &GitHubLinker{Store: m, OAuth: gh, WebOrigin: "https://github.example.test",
			ClientID: func() (string, error) { return "Iv1.client", nil }, ClientSecret: func() (string, error) { return "csecret", nil }}
		s.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		s.Now = func() time.Time { return *now }
	}
}

// ghReq is one request to the link; it returns the status, the body and the response's headers.
func ghReq(t *testing.T, e env, method, path string, h http.Header, form url.Values) (int, string, http.Header) {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(t.Context(), method, e.ts.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = h.Clone()
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	c := *e.ts.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	var b bytes.Buffer
	_, _ = b.ReadFrom(r.Body)
	return r.StatusCode, b.String(), r.Header
}

// post is a same-origin form POST as user, with cookie when set.
func ghPost(t *testing.T, e env, path, user, cookie string, form url.Values) (int, string, http.Header) {
	t.Helper()
	h := header(user, "Origin", linkOrigin)
	if cookie != "" {
		h.Set("Cookie", githubCookie+"="+cookie)
	}
	return ghReq(t, e, http.MethodPost, path, h, form)
}

// started is a started flow: the state and challenge GitHub was sent, the browser's cookie.
type started struct{ state, challenge, cookie string }

func ghStart(t *testing.T, e env, user string) started {
	t.Helper()
	status, _, h := ghPost(t, e, "/link/github/start", user, "", url.Values{})
	if status != http.StatusSeeOther {
		t.Fatalf("start: %d", status)
	}
	loc, err := url.Parse(h.Get("Location"))
	if err != nil || loc.Host != "github.example.test" || loc.Query().Get("code_challenge_method") != "S256" ||
		loc.Query().Get("redirect_uri") != linkOrigin+"/link/github/callback" || loc.Query().Get("client_id") != "Iv1.client" {
		t.Fatalf("start redirects to %q", h.Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range (&http.Response{Header: h}).Cookies() {
		if c.Name == githubCookie {
			cookie = c
		}
	}
	// __Host-: Secure, Path=/ and no Domain, so a sibling subdomain cannot plant its own nonce
	// (cookie tossing) and complete a flow in the victim's browser.
	if cookie == nil || cookie.Name != "__Host-maku-ghlink" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode ||
		cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge != 600 || !stateRE.MatchString(cookie.Value) {
		t.Fatalf("the flow's cookie: %+v", cookie)
	}
	if raw := h.Get("Set-Cookie"); strings.Contains(strings.ToLower(raw), "domain=") || !strings.HasPrefix(raw, "__Host-maku-ghlink=") {
		t.Fatalf("Set-Cookie: %s", raw)
	}
	return started{state: loc.Query().Get("state"), challenge: loc.Query().Get("code_challenge"), cookie: cookie.Value}
}

func ghCallback(t *testing.T, e env, code, state, cookie string) (int, string) {
	t.Helper()
	h := http.Header{} // no session: oauth2-proxy lets the callback through unauthenticated
	if cookie != "" {
		h.Set("Cookie", githubCookie+"="+cookie)
	}
	status, body, _ := ghReq(t, e, http.MethodGet, "/link/github/callback?"+url.Values{"code": {code}, "state": {state}}.Encode(), h, nil)
	return status, body
}

func ghEnv(t *testing.T) (env, *memGitHub, *fakeGitHub, *bytes.Buffer, *time.Time) {
	t.Helper()
	m, gh, logs := newMemGitHub(), newFakeGitHub(), &bytes.Buffer{}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	return setup(t, githubLinkOn(m, gh, logs, &now)), m, gh, logs, &now
}

// The whole flow: start sets the cookie and sends the S256 challenge; the callback with the cookie
// exchanges with the matching verifier, reads the user and revokes the token, and links nothing;
// confirm as the same sub links. Nothing secret reaches the log, and the pages run no script.
func TestGitHubLinkFlow(t *testing.T) {
	e, m, gh, logs, _ := ghEnv(t)
	status, page, h := ghReq(t, e, http.MethodGet, "/link/github", header("dana"), nil)
	if status != http.StatusOK || !strings.Contains(page, "None yet.") || !strings.Contains(page, `action="/link/github/start"`) ||
		strings.Contains(page, "<script") || h.Get("Content-Security-Policy") != strings.Replace(csp, "form-action 'self'", "form-action 'self' https://github.example.test", 1) {
		t.Fatalf("the page: %d %q\n%s", status, h.Get("Content-Security-Policy"), page)
	}
	f := ghStart(t, e, "dana")
	code := gh.authorize(f.challenge, 4242)
	status, page = ghCallback(t, e, code, f.state, f.cookie)
	if status != http.StatusOK || !strings.Contains(page, "GitHub says you are <strong>@gh</strong> (id 4242)") ||
		!strings.Contains(page, `name="state" value="`+f.state+`"`) || strings.Contains(page, "<script") {
		t.Fatalf("the confirmation: %d\n%s", status, page)
	}
	if gh.exchanges != 1 || len(gh.revoked) != 1 || gh.revoked[0] != "gho_"+code {
		t.Fatalf("exchanges %d, revoked %v", gh.exchanges, gh.revoked)
	}
	if _, ok := m.linked("dana"); ok {
		t.Fatal("the callback linked")
	}
	status, page, _ = ghPost(t, e, "/link/github/confirm", "dana", f.cookie, url.Values{"state": {f.state}})
	if l, ok := m.linked("dana"); status != http.StatusOK || !ok || l.GitHubID != 4242 || !strings.Contains(page, "Linked: rooms and the factory now know you as @gh.") ||
		!strings.Contains(page, "Linked to @gh (id 4242)") {
		t.Fatalf("confirm: %d %+v\n%s", status, l, page)
	}
	// A replay of the confirm, or of the callback, is state_invalid.
	if _, page, _ := ghPost(t, e, "/link/github/confirm", "dana", f.cookie, url.Values{"state": {f.state}}); !strings.Contains(page, refusalStateInvalid) {
		t.Fatalf("a replayed confirm:\n%s", page)
	}
	if _, page := ghCallback(t, e, code, f.state, f.cookie); !strings.Contains(page, refusalStateInvalid) || gh.exchanges != 1 {
		t.Fatalf("a replayed callback exchanged (%d):\n%s", gh.exchanges, page)
	}
	// A second flow relinks.
	f2 := ghStart(t, e, "dana")
	ghCallback(t, e, gh.authorize(f2.challenge, 7777), f2.state, f2.cookie)
	if _, page, _ := ghPost(t, e, "/link/github/confirm", "dana", f2.cookie, url.Values{"state": {f2.state}}); !strings.Contains(page, "Relinked") {
		t.Fatalf("relink:\n%s", page)
	}
	if got := strings.Join(m.events, ","); got != "dana:linked,dana:relinked" {
		t.Fatalf("events %s", got)
	}
	for _, secret := range []string{code, f.state, f.cookie, "gho_", "csecret", pkceVerifier(f.cookie, f.state)} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the log holds %q:\n%s", secret, logs.String())
		}
	}
	if got := scrape(t, e); !regexp.MustCompile(`rooms_github_links_total\{[^}]*outcome="linked"[^}]*\} 1`).MatchString(got) {
		t.Fatalf("the counter:\n%s", got)
	}
}

// The binding: a callback without the cookie, or with another browser's, never exchanges; a confirm
// under another sub writes nothing; an expired state is state_invalid.
func TestGitHubLinkBindsTheBrowserAndTheSub(t *testing.T) {
	e, m, gh, _, now := ghEnv(t)
	f := ghStart(t, e, "dana")
	other := ghStart(t, e, "eve")
	code := gh.authorize(f.challenge, 4242)
	for name, cookie := range map[string]string{"no cookie": "", "another flow's cookie": other.cookie} {
		if _, page := ghCallback(t, e, code, f.state, cookie); !strings.Contains(page, refusalCookie) {
			t.Fatalf("%s:\n%s", name, page)
		}
	}
	if gh.exchanges != 0 {
		t.Fatalf("exchanged %d times without the browser's cookie", gh.exchanges)
	}
	ghCallback(t, e, code, f.state, f.cookie)
	// eve, with dana's state and cookie (a link CSRF the other way round), is refused.
	if _, page, _ := ghPost(t, e, "/link/github/confirm", "eve", f.cookie, url.Values{"state": {f.state}}); !strings.Contains(page, "started by another rooms account") {
		t.Fatalf("sub mismatch:\n%s", page)
	}
	if _, ok := m.linked("eve"); ok {
		t.Fatal("eve was linked")
	}
	// The confirm needs the cookie too.
	if _, page, _ := ghPost(t, e, "/link/github/confirm", "dana", "", url.Values{"state": {f.state}}); !strings.Contains(page, refusalCookie) {
		t.Fatalf("a confirm without the cookie:\n%s", page)
	}
	*now = now.Add(11 * time.Minute)
	if _, page, _ := ghPost(t, e, "/link/github/confirm", "dana", f.cookie, url.Values{"state": {f.state}}); !strings.Contains(page, refusalStateInvalid) {
		t.Fatalf("an expired state:\n%s", page)
	}
	if _, page := ghCallback(t, e, "x", strings.Repeat("A", 43), f.cookie); !strings.Contains(page, refusalStateInvalid) {
		t.Fatalf("an unknown state:\n%s", page)
	}
}

// Only a web session links; a POST from another origin is refused; a GitHub error names no token;
// a GitHub account held by another sub is refused with the admin's way out.
func TestGitHubLinkRefusals(t *testing.T) {
	e, m, gh, _, _ := ghEnv(t)
	if status, body, _ := ghReq(t, e, http.MethodGet, "/link/github", header("dana", "X-Test-Client", "roomctl"), nil); status != http.StatusForbidden ||
		!strings.Contains(body, "web UI") {
		t.Fatalf("a CLI principal: %d %s", status, body)
	}
	for _, path := range []string{"/link/github/start", "/link/github/confirm", "/link/github/unlink"} {
		h := header("dana", "Origin", "https://evil.example.test")
		if status, _, _ := ghReq(t, e, http.MethodPost, path, h, url.Values{}); status != http.StatusForbidden {
			t.Fatalf("%s from another origin: %d", path, status)
		}
	}
	f := ghStart(t, e, "dana")
	gh.fail = true
	if _, page := ghCallback(t, e, gh.authorize(f.challenge, 4242), f.state, f.cookie); !strings.Contains(page, refusalGitHub) || strings.Contains(page, "gho_") ||
		strings.Contains(page, "bad_verification_code") {
		t.Fatalf("a GitHub error:\n%s", page)
	}
	gh.fail = false
	// 4242 is eve's: dana's confirm is refused, and the page names the admin's unlink.
	fe := ghStart(t, e, "eve")
	ghCallback(t, e, gh.authorize(fe.challenge, 4242), fe.state, fe.cookie)
	ghPost(t, e, "/link/github/confirm", "eve", fe.cookie, url.Values{"state": {fe.state}})
	fd := ghStart(t, e, "dana")
	ghCallback(t, e, gh.authorize(fd.challenge, 4242), fd.state, fd.cookie)
	if _, page, _ := ghPost(t, e, "/link/github/confirm", "dana", fd.cookie, url.Values{"state": {fd.state}}); !strings.Contains(page, "an admin must unlink it first") {
		t.Fatalf("taken:\n%s", page)
	}
	if _, ok := m.linked("dana"); ok {
		t.Fatal("dana took eve's GitHub account")
	}
	// Three flows in progress is the most.
	for range 3 {
		ghPost(t, e, "/link/github/start", "fay", "", url.Values{})
	}
	if _, page, _ := ghPost(t, e, "/link/github/start", "fay", "", url.Values{}); !strings.Contains(page, "Three GitHub links are already in progress") {
		t.Fatalf("a 4th flow:\n%s", page)
	}
}

// The account unlinks itself; an admin unlinks anyone's through the API, a member never does.
func TestGitHubUnlinkAndAdminUnlink(t *testing.T) {
	e, m, gh, _, _ := ghEnv(t)
	for i, who := range []string{"dana", "eve"} {
		f := ghStart(t, e, who)
		ghCallback(t, e, gh.authorize(f.challenge, int64(100+i)), f.state, f.cookie)
		ghPost(t, e, "/link/github/confirm", who, f.cookie, url.Values{"state": {f.state}})
	}
	if _, page, _ := ghPost(t, e, "/link/github/unlink", "dana", "", url.Values{}); !strings.Contains(page, "Unlinked") {
		t.Fatalf("unlink:\n%s", page)
	}
	del := func(h http.Header) (int, string) {
		status, body, _ := ghReq(t, e, http.MethodDelete, "/api/links/github/eve", h, nil)
		return status, body
	}
	if status, _ := del(header("dana", "Origin", linkOrigin)); status != http.StatusForbidden {
		t.Fatalf("a member's DELETE: %d", status)
	}
	if status, _ := del(header("root", "Origin", linkOrigin, "X-Test-Admin", "1", "X-Test-Client", "roomctl")); status != http.StatusForbidden {
		t.Fatalf("an admin's CLI DELETE: %d", status)
	}
	if status, body := del(header("root", "Origin", linkOrigin, "X-Test-Admin", "1")); status != http.StatusOK || !strings.Contains(body, `"unlinked":true`) {
		t.Fatalf("an admin's DELETE: %d %s", status, body)
	}
	if _, ok := m.linked("eve"); ok {
		t.Fatal("eve is still linked")
	}
	if got := strings.Join(m.events[2:], ","); got != "dana:unlinked:dana,eve:admin_unlinked:root" {
		t.Fatalf("events %s", got)
	}
}

// Each authenticated request of a linked human refreshes its link (at most every touchEvery per
// replica), and a link aged past activeWindow is active again on that request: the resolver's cached
// inactive state is dropped. An unlinked human is never refused for it.
func TestGitHubLinkTouchedByTheHumansRequests(t *testing.T) {
	e, m, _, _, now := ghEnv(t)
	m.links["dana"] = store.GitHubLink{Sub: "dana", GitHubID: 4242, Login: "dana", LinkedAt: now.Add(-200 * time.Hour), LastSeenAt: now.Add(-200 * time.Hour)}
	read := func(ctx context.Context, sub string) (ghidentity.LinkRecord, bool, error) {
		l, ok, err := m.GitHubLinkOf(ctx, sub)
		return ghidentity.LinkRecord{GitHubID: l.GitHubID, LastSeenAt: l.LastSeenAt}, ok, err
	}
	id := &ghidentity.Resolver{IDPID: "github", TTL: 5 * time.Minute, Now: func() time.Time { return *now },
		Links:   ghidentity.StoreLinks(read, "github"),
		Active:  ghidentity.StoreActive(read, 168*time.Hour, func() time.Time { return *now }, nil),
		LoginOf: func(context.Context, string, int64) (string, error) { return "dana", nil }}
	e.srv.Identity = id
	e.srv.GitHubLink.Changed = id.Forget
	// The Slack App acting for dana (no request of hers) finds her inactive, and caches it.
	if err := id.CheckActive(t.Context(), "dana"); !errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("aged past the window: %v", err)
	}
	if status, body, _ := ghReq(t, e, http.MethodGet, "/api/rooms", header("dana"), nil); status != http.StatusOK {
		t.Fatalf("dana's own request: %d %s", status, body)
	}
	if l, _ := m.linked("dana"); !l.LastSeenAt.Equal(*now) {
		t.Fatalf("not touched: %s", l.LastSeenAt)
	}
	if err := id.CheckActive(t.Context(), "dana"); err != nil {
		t.Fatalf("after her request: %v", err)
	}
	if status, _, _ := ghReq(t, e, http.MethodGet, "/api/rooms", header("nolink"), nil); status != http.StatusOK {
		t.Fatalf("an unlinked human: %d", status)
	}
}
