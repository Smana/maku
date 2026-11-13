// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"html/template"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/store"
)

// The GitHub account link (v4 D8, plan step 5 PR4). A signed-in web session starts a flow, GitHub
// authorizes it, and the callback, which needs no session (oauth2-proxy lets it through with
// skip-auth-route: its own cookie is SameSite=Strict), exchanges the code with PKCE, reads GET /user
// and revokes the token at once. The callback links nothing: the link is written by a same-origin
// confirm POST under the web session whose sub started the flow. A state is bound to the browser
// that started it by a cookie's nonce, so a link CSRF cannot hand an attacker's sub the victim's
// GitHub id, nor the victim's sub the attacker's. The broker keeps the numeric id, never a token.

// GitHubLinkTTL is how long a flow may run, from the start to the confirm.
const GitHubLinkTTL = 10 * time.Minute

// The link's paths. The callback is the one unauthenticated /link/ route, and the path oauth2-proxy's
// skip-auth-route names (GET=^/link/github/callback$): it is stable.
const (
	GitHubLinkPath     = "/link/github"
	GitHubCallbackPath = "/link/github/callback"
	// The flow's cookie: __Host-, so it is Secure, Path=/ and host-only. A sibling subdomain cannot
	// set it (a __Secure- cookie could be planted with Domain=.parent, cookie tossing), so a nonce in
	// the browser is this broker's own.
	githubCookie = "__Host-maku-ghlink"
)

// GitHubLinkStore is the part of the store the link reads and writes; *store.Store has it.
type GitHubLinkStore interface {
	StartGitHubLink(ctx context.Context, stateSHA256 []byte, sub string, nonceSHA256 []byte, expires, now time.Time) error
	GitHubState(ctx context.Context, stateSHA256 []byte, now time.Time) (store.GitHubState, error)
	SetGitHubStateUser(ctx context.Context, stateSHA256 []byte, githubID int64, login string, now time.Time) error
	ConfirmGitHubLink(ctx context.Context, stateSHA256 []byte, sub string, now time.Time) (string, error)
	GitHubLinkOf(ctx context.Context, sub string) (store.GitHubLink, bool, error)
	UnlinkGitHub(ctx context.Context, sub, actor string, admin bool, now time.Time) (bool, error)
	TouchGitHubLink(ctx context.Context, sub string, now time.Time) (bool, error)
}

// GitHubOAuth is GitHub's web flow; github.OAuth has it.
type GitHubOAuth interface {
	AuthorizeURL(clientID, redirect, state, challenge string) string
	ExchangeCode(ctx context.Context, clientID, clientSecret, code, verifier, redirect string) (string, error)
	User(ctx context.Context, token string) (int64, string, error)
	RevokeUserToken(ctx context.Context, clientID, clientSecret, token string) error
}

// GitHubLinker is human.githubLink: the store, GitHub's flow at WebOrigin, and the OAuth client,
// read at use so a rotated secret needs no restart. Changed, when set, is told each sub whose link
// was written or removed, so a cached login does not outlive it.
type GitHubLinker struct {
	Store        GitHubLinkStore
	OAuth        GitHubOAuth
	WebOrigin    string // GitHub's site, scheme://host: the start's redirect, which the page's form-action allows
	ClientID     func() (string, error)
	ClientSecret func() (string, error)
	Changed      func(sub string)
	Rand         io.Reader // nil: crypto/rand
}

// The link's outcomes, rooms_github_links_total{outcome}: a closed list.
const (
	ghStarted        = "started"
	ghLinked         = "linked"
	ghRelinked       = "relinked"
	ghUnlinked       = "unlinked"
	ghAdminUnlinked  = "admin_unlinked"
	ghStateInvalid   = "state_invalid"
	ghCookieMismatch = "cookie_mismatch"
	ghSubMismatch    = "sub_mismatch"
	ghTaken          = "github_account_taken"
	ghGitHubError    = "github_error"
	ghNotWeb         = "not_web"
	ghTooManyPending = "too_many_pending"
	ghBadOrigin      = "bad_origin"
	ghError          = "error"
)

// stateRE is a state or a nonce as minted: 32 random bytes, base64url, unpadded.
var stateRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func sha(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// pkceVerifier is the flow's PKCE verifier, derived from the browser's nonce and the state: it is
// stored nowhere, and only the browser that holds the cookie, with the state GitHub sends back,
// lets the broker compute it. 43 base64url characters, as RFC 7636 requires.
func pkceVerifier(nonce, state string) string {
	m := hmac.New(sha256.New, []byte(nonce))
	_, _ = io.WriteString(m, "maku-github-link-pkce\x00"+state)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// pkceChallenge is the S256 challenge of verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Server) ghCounted(ctx context.Context, outcome string) {
	if s.Metrics != nil && s.Metrics.GitHubLinks != nil {
		s.Metrics.GitHubLinks.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}

// githubPage is what /link/github renders.
type githubPage struct {
	Notice, Refusal string
	Confirm         *githubConfirm
	Link            *store.GitHubLink
	SignedIn        bool
}

type githubConfirm struct {
	State, Login string
	ID           int64
}

// The page: no script, the room UI's CSP but for form-action, which also allows GitHub's site (the
// start's 303 goes there), and forms that post to this origin only.
var githubTemplate = template.Must(template.New("github").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Link GitHub</title><link rel="stylesheet" href="/link/github.css"></head>
<body><main>
<h1>Link your GitHub account</h1>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
{{if .Refusal}}<p class="refusal" role="alert">{{.Refusal}}</p>{{end}}
{{with .Confirm}}
<section class="confirm">
<p>GitHub says you are <strong>@{{.Login}}</strong> (id {{.ID}}). Link this GitHub account to the account you are signed in to the rooms with?</p>
<p class="warn">Confirm only if you started this link yourself, a moment ago. Rooms and the factory then treat you as @{{.Login}}: you see a repository's rooms and file its tasks only if @{{.Login}} may.</p>
<form method="post" action="/link/github/confirm"><input type="hidden" name="state" value="{{.State}}"><button type="submit">Link</button></form>
</section>
{{end}}
{{if .SignedIn}}
<h2>Your GitHub link</h2>
{{with .Link}}<p>Linked to @{{.Login}} (id {{.GitHubID}}) since {{.LinkedAt.Format "2006-01-02"}}.</p>
<form method="post" action="/link/github/unlink"><button type="submit">Unlink</button></form>
<form method="post" action="/link/github/start"><button type="submit">Link another GitHub account</button></form>
{{else}}<p>None yet.</p>
<form method="post" action="/link/github/start"><button type="submit">Link GitHub</button></form>{{end}}
{{end}}
</main></body></html>
`))

// githubCSP is the page's CSP: csp's, with GitHub's site added to form-action.
func (s *Server) githubCSP() string {
	return strings.Replace(csp, "form-action 'self'", "form-action 'self' "+s.GitHubLink.WebOrigin, 1)
}

func (s *Server) githubHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", s.githubCSP())
		w.Header().Set("Referrer-Policy", "same-origin") // a form POST then carries the real Origin
		w.Header().Set("Cache-Control", "no-store")      // a state rides the page and the URL
		h.ServeHTTP(w, r)
	})
}

// githubLinkRoutes adds /link/github's routes and the admin's unlink.
func (s *Server) githubLinkRoutes(mux *http.ServeMux) {
	mux.Handle("GET "+GitHubLinkPath, s.githubHeaders(http.HandlerFunc(s.githubGet)))
	mux.Handle("POST /link/github/start", s.githubHeaders(http.HandlerFunc(s.githubStart)))
	mux.Handle("GET "+GitHubCallbackPath, s.githubHeaders(http.HandlerFunc(s.githubCallback)))
	mux.Handle("POST /link/github/confirm", s.githubHeaders(http.HandlerFunc(s.githubConfirm)))
	mux.Handle("POST /link/github/unlink", s.githubHeaders(http.HandlerFunc(s.githubUnlink)))
	mux.Handle("GET /link/github.css", withCSP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = io.WriteString(w, linkCSS)
	})))
	mux.HandleFunc("DELETE /api/links/github/{sub}", s.githubAdminUnlink)
}

// ghHuman is the web session behind a link request (ruling P18: a maku or MCP bearer never links).
func (s *Server) ghHuman(w http.ResponseWriter, r *http.Request) (authn.Principal, bool) {
	return s.webHuman(w, r, func(ctx context.Context) { s.ghCounted(ctx, ghNotWeb) })
}

// ghSameOrigin is sameOrigin, counted on the GitHub link's counter.
func (s *Server) ghSameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if want, o := origin(s.PublicURL), r.Header.Get("Origin"); want != "" && o != "" && origin(o) == want {
		return true
	}
	s.ghCounted(r.Context(), ghBadOrigin)
	http.Error(w, "forbidden: cross-origin", http.StatusForbidden)
	return false
}

func (s *Server) renderGitHub(w http.ResponseWriter, r *http.Request, sub string, page githubPage) {
	if sub != "" {
		l, found, err := s.GitHubLink.Store.GitHubLinkOf(r.Context(), sub)
		if err != nil {
			s.log().Warn("github link unreadable", "err", err)
			s.ghCounted(r.Context(), ghError)
			http.Error(w, "unavailable: try again", http.StatusServiceUnavailable)
			return
		}
		page.SignedIn = true
		if found {
			page.Link = &l
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := githubTemplate.Execute(w, page); err != nil {
		s.log().Warn("github link page", "err", err)
	}
}

// githubGet is GET /link/github: the account's link, and the forms to link or unlink.
func (s *Server) githubGet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.ghHuman(w, r)
	if !ok {
		return
	}
	s.renderGitHub(w, r, p.Sub, githubPage{})
}

func (s *Server) random() (string, error) {
	b := make([]byte, 32)
	rd := s.GitHubLink.Rand
	if rd == nil {
		rd = rand.Reader
	}
	if _, err := io.ReadFull(rd, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) callbackURL() string {
	return strings.TrimSuffix(s.PublicURL, "/") + GitHubCallbackPath
}

// githubStart is POST /link/github/start: a new flow for the web session's sub, its nonce in a
// cookie, and a 303 to GitHub's authorize page with the state and the PKCE challenge.
func (s *Server) githubStart(w http.ResponseWriter, r *http.Request) {
	if !s.ghSameOrigin(w, r) {
		return
	}
	p, ok := s.ghHuman(w, r)
	if !ok || !postForm(w, r) {
		return
	}
	ctx := r.Context()
	clientID, err := s.GitHubLink.ClientID()
	if err != nil {
		s.log().Warn("github link client unreadable", "err", err)
		s.ghCounted(ctx, ghError)
		http.Error(w, "unavailable: the GitHub link is not configured", http.StatusServiceUnavailable)
		return
	}
	state, err := s.random()
	nonce, err2 := s.random()
	if err != nil || err2 != nil {
		s.ghCounted(ctx, ghError)
		http.Error(w, "unavailable: try again", http.StatusServiceUnavailable)
		return
	}
	now := s.now()
	switch err := s.GitHubLink.Store.StartGitHubLink(ctx, sha(state), p.Sub, sha(nonce), now.Add(GitHubLinkTTL), now); {
	case errors.Is(err, store.ErrTooManyPending):
		s.ghCounted(ctx, ghTooManyPending)
		s.renderGitHub(w, r, p.Sub, githubPage{Refusal: "Three GitHub links are already in progress for your account: finish one, or wait ten minutes."})
		return
	case err != nil:
		s.log().Warn("github link start failed", "err", err)
		s.ghCounted(ctx, ghError)
		http.Error(w, "unavailable: try again", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: githubCookie, Value: nonce, Path: "/", MaxAge: int(GitHubLinkTTL / time.Second),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	s.ghCounted(ctx, ghStarted)
	http.Redirect(w, r, s.GitHubLink.OAuth.AuthorizeURL(clientID, s.callbackURL(), state, pkceChallenge(pkceVerifier(nonce, state))), http.StatusSeeOther)
}

// nonceMatches is whether r carries the cookie whose nonce st was started with.
func nonceMatches(r *http.Request, st store.GitHubState) (string, bool) {
	c, err := r.Cookie(githubCookie)
	if err != nil || !stateRE.MatchString(c.Value) {
		return "", false
	}
	return c.Value, subtle.ConstantTimeCompare(sha(c.Value), st.NonceSHA256) == 1
}

const (
	refusalStateInvalid = "This GitHub link expired or was already used: start again from /link/github."
	refusalCookie       = "This GitHub link was not started in this browser: start again from /link/github, in the browser you sign in to the rooms with."
	refusalGitHub       = "GitHub did not complete the link: start again from /link/github."
)

// githubCallback is GET /link/github/callback, the one route of the flow without a session. It
// requires the browser's nonce to match the state, exchanges the code, reads who the user is and
// revokes the token; it records them on the state and renders the confirmation. It links nothing.
func (s *Server) githubCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	state := q.Get("state")
	if !stateRE.MatchString(state) {
		s.ghCounted(ctx, ghStateInvalid)
		s.renderGitHub(w, r, "", githubPage{Refusal: refusalStateInvalid})
		return
	}
	now := s.now()
	st, err := s.GitHubLink.Store.GitHubState(ctx, sha(state), now)
	switch {
	case errors.Is(err, store.ErrStateInvalid):
		s.ghCounted(ctx, ghStateInvalid)
		s.renderGitHub(w, r, "", githubPage{Refusal: refusalStateInvalid})
		return
	case err != nil:
		s.log().Warn("github link state unreadable", "err", err)
		s.ghCounted(ctx, ghError)
		http.Error(w, "unavailable: try again", http.StatusServiceUnavailable)
		return
	}
	nonce, ok := nonceMatches(r, st)
	if !ok {
		s.ghCounted(ctx, ghCookieMismatch)
		s.renderGitHub(w, r, "", githubPage{Refusal: refusalCookie})
		return
	}
	if st.GitHubID != 0 { // a replayed callback
		s.ghCounted(ctx, ghStateInvalid)
		s.renderGitHub(w, r, "", githubPage{Refusal: refusalStateInvalid})
		return
	}
	code := q.Get("code")
	if q.Get("error") != "" || code == "" || len(code) > 256 {
		s.ghCounted(ctx, ghGitHubError)
		s.renderGitHub(w, r, "", githubPage{Refusal: refusalGitHub})
		return
	}
	id, login, err := s.githubUser(ctx, code, pkceVerifier(nonce, state))
	if err != nil {
		s.log().Warn("github link: GitHub refused", "err", err)
		s.ghCounted(ctx, ghGitHubError)
		s.renderGitHub(w, r, "", githubPage{Refusal: refusalGitHub})
		return
	}
	switch err := s.GitHubLink.Store.SetGitHubStateUser(ctx, sha(state), id, login, now); {
	case errors.Is(err, store.ErrStateInvalid): // a racing callback, or the flow just expired
		s.ghCounted(ctx, ghStateInvalid)
		s.renderGitHub(w, r, "", githubPage{Refusal: refusalStateInvalid})
		return
	case err != nil:
		s.log().Warn("github link state unwritable", "err", err)
		s.ghCounted(ctx, ghError)
		http.Error(w, "unavailable: try again", http.StatusServiceUnavailable)
		return
	}
	s.renderGitHub(w, r, "", githubPage{Confirm: &githubConfirm{State: state, Login: login, ID: id}})
}

// githubUser exchanges code, reads the user and revokes the token. A revocation GitHub refuses is
// logged, never fatal: the broker has already dropped the token, and the link is the id only.
func (s *Server) githubUser(ctx context.Context, code, verifier string) (int64, string, error) {
	clientID, err := s.GitHubLink.ClientID()
	if err != nil {
		return 0, "", err
	}
	secret, err := s.GitHubLink.ClientSecret()
	if err != nil {
		return 0, "", err
	}
	o := s.GitHubLink.OAuth
	tok, err := o.ExchangeCode(ctx, clientID, secret, code, verifier, s.callbackURL())
	if err != nil {
		return 0, "", err
	}
	id, login, userErr := o.User(ctx, tok)
	if err := o.RevokeUserToken(ctx, clientID, secret, tok); err != nil {
		s.log().Warn("github link: the user token was not revoked", "err", err)
	}
	return id, login, userErr
}

// githubConfirm is POST /link/github/confirm: the link, under the web session whose sub started the
// flow, from the browser that holds its nonce.
func (s *Server) githubConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.ghSameOrigin(w, r) {
		return
	}
	p, ok := s.ghHuman(w, r)
	if !ok || !postForm(w, r) {
		return
	}
	ctx := r.Context()
	state := r.PostForm.Get("state")
	if !stateRE.MatchString(state) {
		s.ghCounted(ctx, ghStateInvalid)
		s.renderGitHub(w, r, p.Sub, githubPage{Refusal: refusalStateInvalid})
		return
	}
	now := s.now()
	st, err := s.GitHubLink.Store.GitHubState(ctx, sha(state), now)
	if err == nil {
		if _, ok := nonceMatches(r, st); !ok {
			s.ghCounted(ctx, ghCookieMismatch)
			s.renderGitHub(w, r, p.Sub, githubPage{Refusal: refusalCookie})
			return
		}
	}
	act := ""
	if err == nil {
		act, err = s.GitHubLink.Store.ConfirmGitHubLink(ctx, sha(state), p.Sub, now)
	}
	switch {
	case errors.Is(err, store.ErrStateInvalid):
		s.ghCounted(ctx, ghStateInvalid)
		s.renderGitHub(w, r, p.Sub, githubPage{Refusal: refusalStateInvalid})
		return
	case errors.Is(err, store.ErrSubMismatch):
		s.ghCounted(ctx, ghSubMismatch)
		s.renderGitHub(w, r, p.Sub, githubPage{Refusal: "This GitHub link was started by another rooms account than the one you are signed in with: nothing was linked."})
		return
	case errors.Is(err, store.ErrGitHubTaken):
		s.ghCounted(ctx, ghTaken)
		s.renderGitHub(w, r, p.Sub, githubPage{Refusal: "This GitHub account is linked to another rooms account: an admin must unlink it first."})
		return
	case err != nil:
		s.log().Warn("github link confirm failed", "err", err)
		s.ghCounted(ctx, ghError)
		http.Error(w, "unavailable: try again", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: githubCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	s.githubChanged(p.Sub)
	outcome, notice := ghLinked, "Linked: rooms and the factory now know you as @"+st.Login+"."
	if act == store.GitHubRelinked {
		outcome, notice = ghRelinked, "Relinked: rooms and the factory now know you as @"+st.Login+"."
	}
	s.ghCounted(ctx, outcome)
	s.renderGitHub(w, r, p.Sub, githubPage{Notice: notice})
}

func (s *Server) githubChanged(sub string) {
	if s.GitHubLink.Changed != nil {
		s.GitHubLink.Changed(sub)
	}
}

// githubUnlink is POST /link/github/unlink: the web session's own link goes.
func (s *Server) githubUnlink(w http.ResponseWriter, r *http.Request) {
	if !s.ghSameOrigin(w, r) {
		return
	}
	p, ok := s.ghHuman(w, r)
	if !ok || !postForm(w, r) {
		return
	}
	gone, err := s.GitHubLink.Store.UnlinkGitHub(r.Context(), p.Sub, p.Sub, false, s.now())
	if err != nil {
		s.log().Warn("github unlink failed", "err", err)
		s.ghCounted(r.Context(), ghError)
		http.Error(w, "unavailable: try again", http.StatusServiceUnavailable)
		return
	}
	page := githubPage{Notice: "Nothing to unlink: your account has no GitHub link."}
	if gone {
		s.githubChanged(p.Sub)
		s.ghCounted(r.Context(), ghUnlinked)
		page.Notice = "Unlinked: rooms and the factory no longer know you on GitHub."
	}
	s.renderGitHub(w, r, p.Sub, page)
}

// githubAdminUnlink is DELETE /api/links/github/{sub}: an admin's web session removes another
// account's link, for instance to free a GitHub account linked to the wrong one.
func (s *Server) githubAdminUnlink(w http.ResponseWriter, r *http.Request) {
	if !s.ghSameOrigin(w, r) {
		return
	}
	p, ok := s.ghHuman(w, r)
	if !ok {
		return
	}
	if !s.Groups.IsAdmin(p) {
		http.Error(w, "forbidden: admins only", http.StatusForbidden)
		return
	}
	sub := r.PathValue("sub")
	if len(sub) == 0 || len(sub) > 128 {
		http.Error(w, "bad request: sub", http.StatusBadRequest)
		return
	}
	gone, err := s.GitHubLink.Store.UnlinkGitHub(r.Context(), sub, p.Sub, true, s.now())
	if err != nil {
		s.log().Warn("github admin unlink failed", "err", err)
		s.ghCounted(r.Context(), ghError)
		http.Error(w, "unavailable: try again", http.StatusServiceUnavailable)
		return
	}
	if gone {
		s.githubChanged(sub)
		s.ghCounted(r.Context(), ghAdminUnlinked)
	}
	writeJSON(w, map[string]any{"unlinked": gone})
}

// touchEvery bounds how often one replica asks the store to refresh a sub's last_seen_at; the store
// itself writes at most hourly.
const (
	touchEvery    = 10 * time.Minute
	maxTouchCache = 10_000
)

// touchGitHub refreshes the authenticated human's link before their request is checked for activity
// (plan PR5): a human presenting a token is active by construction, since the IdP stops minting
// tokens for a deactivated user. A refresh that moved the link drops the resolver's cached state, so
// a link that had aged past activeWindow is active again on this very request. A failed refresh is
// logged; the activity check that follows reads the store and fails closed on its own.
func (s *Server) touchGitHub(ctx context.Context, p authn.Principal) {
	if s.GitHubLink == nil || p.Kind != envelope.ActorHuman || p.Sub == "" {
		return
	}
	now := s.now()
	s.touchMu.Lock()
	if last, ok := s.touched[p.Sub]; ok && now.Sub(last) < touchEvery {
		s.touchMu.Unlock()
		return
	}
	if s.touched == nil || len(s.touched) >= maxTouchCache {
		s.touched = map[string]time.Time{}
	}
	s.touched[p.Sub] = now
	s.touchMu.Unlock()
	moved, err := s.GitHubLink.Store.TouchGitHubLink(ctx, p.Sub, now)
	if err != nil {
		s.log().Warn("github link not refreshed", "err", err)
		return
	}
	if moved {
		s.githubChanged(p.Sub)
	}
}
