// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/config"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/ghidentity"
	"github.com/Smana/maku/internal/github"
	"github.com/Smana/maku/internal/redact"
	"github.com/Smana/maku/internal/repoaccess"
)

// fakeZitadel serves one user's IdP links and state to the bearer of pat only, like ZITADEL's
// v2 API.
type fakeZitadel struct {
	mu    sync.Mutex
	pat   string
	links string // the result array
	state string // "" is USER_STATE_ACTIVE
	calls int
}

func (z *fakeZitadel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/users/") && !strings.Contains(r.URL.Path[len("/v2/users/"):], "/") &&
		r.Header.Get("Authorization") == "Bearer "+z.pat {
		state := z.state
		if state == "" {
			state = "USER_STATE_ACTIVE"
		}
		_, _ = w.Write([]byte(`{"user":{"userId":"` + r.URL.Path[len("/v2/users/"):] + `","state":"` + state + `"}}`))
		return
	}
	z.calls++
	if r.Method != http.MethodPost || r.URL.Path != "/v2/users/u1/links/_search" || r.Header.Get("Authorization") != "Bearer "+z.pat {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	_, _ = w.Write([]byte(`{"result":` + z.links + `}`))
}

func (z *fakeZitadel) set(pat, links string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.pat, z.links = pat, links
}

// fakeGitHubUsers is the factory App: GitHub id 583231 is octocat, who reads Smana/a.
type fakeGitHubUsers struct {
	mu    sync.Mutex
	repos []string // the owner/repo each UserLogin read through
}

// unseenBy is what the App answers for a repository it cannot see: Smana/gone is not installed,
// Smana/.. cannot be named in a path.
func unseenBy(owner, repo string) error {
	switch owner + "/" + repo {
	case "Smana/gone":
		return fmt.Errorf("github: %w", github.ErrNoInstallation)
	case "Smana/..":
		return github.ErrNotAPullRequest
	}
	return nil
}

func (g *fakeGitHubUsers) UserLogin(_ context.Context, owner, repo string, id int64) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.repos = append(g.repos, owner+"/"+repo)
	if err := unseenBy(owner, repo); err != nil {
		return "", err
	}
	if id != 583231 {
		return "", errors.New("unknown id")
	}
	return "octocat", nil
}

func (g *fakeGitHubUsers) Permission(_ context.Context, owner, repo, login string) (string, error) {
	if err := unseenBy(owner, repo); err != nil {
		return "", err
	}
	if owner+"/"+repo == "Smana/a" && login == "octocat" {
		return "read", nil
	}
	return "none", nil
}

func writeReader(t *testing.T, path, pat, idp string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"pat": pat, "tokenId": "t-1", "githubIdpId": idp})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFactoryAppUsesGitHubAPIURL(t *testing.T) {
	t.Setenv("ROOMS_GITHUB_API_URL", "https://e2e-stub.e2e.svc.cluster.local/")
	if got := factoryApp(t.TempDir()).API; got != "https://e2e-stub.e2e.svc.cluster.local/" {
		t.Fatalf("factory App API root = %q", got)
	}
}

// D7's wiring: the login from the user's GitHub link, read with the reader secret at every
// cache miss, then the App's permission, both under the config's TTL.
func TestRoomAccess(t *testing.T) {
	z := &fakeZitadel{}
	z.set("pat-1", `[{"idpId":"idp-google","userId":"g-1"},{"idpId":"idp-gh","userId":"583231"}]`)
	srv := httptest.NewTLSServer(z)
	t.Cleanup(srv.Close)
	reader := filepath.Join(t.TempDir(), "reader.json")
	writeReader(t, reader, "pat-1", "idp-gh")
	now := time.Unix(0, 0)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	later := func(d time.Duration) { mu.Lock(); defer mu.Unlock(); now = now.Add(d) }
	gh := &fakeGitHubUsers{}
	h := config.HumanConfig{Issuer: srv.URL, Access: &config.AccessConfig{ReaderFile: reader, TTL: config.Duration{Duration: 3 * time.Minute}}}

	id, acc, err := roomAccess(h, gh, srv.Client(), clock, nil, nil)
	if err != nil || id == nil || acc == nil {
		t.Fatalf("%v %v %v", id, acc, err)
	}
	if id.TTL != 3*time.Minute || acc.TTL != 3*time.Minute {
		t.Fatalf("TTLs %s %s, want the config's", id.TTL, acc.TTL)
	}
	ctx := t.Context()
	login, err := id.Login(ctx, "u1", "Smana/a")
	if err != nil || login != "octocat" || len(gh.repos) != 1 || gh.repos[0] != "Smana/a" {
		t.Fatalf("login %q, %v, through %v", login, err, gh.repos)
	}
	if ok, err := acc.CanRead(ctx, "Smana/a", login); !ok || err != nil {
		t.Fatalf("read %v, %v", ok, err)
	}
	if ok, err := acc.CanRead(ctx, "Smana/b", login); ok || err != nil {
		t.Fatalf("read %v, %v", ok, err)
	}

	// ZITADEL mints a new PAT and a rebuilt IdP gets a new id: the secret is read at use.
	z.set("pat-2", `[{"idpId":"idp-gh","userId":"999"},{"idpId":"idp-gh-2","userId":"583231"}]`)
	writeReader(t, reader, "pat-2", "idp-gh-2")
	later(4 * time.Minute)
	if login, err := id.Login(ctx, "u1", "Smana/a"); err != nil || login != "octocat" {
		t.Fatalf("after a rotation: %q, %v", login, err)
	}

	// #126: the same reader reads the user's state, and a deactivated user is refused.
	z.mu.Lock()
	z.state = "USER_STATE_INACTIVE"
	z.mu.Unlock()
	later(4 * time.Minute)
	if err := id.CheckActive(ctx, "u1"); !errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("a deactivated user: %v, want ErrInactive", err)
	}
	z.mu.Lock()
	z.state = ""
	z.mu.Unlock()
	later(4 * time.Minute)

	// No secret, or one without the IdP id, cannot be checked: fail closed.
	for _, body := range []string{"", `{"pat":"pat-2"}`, `{"githubIdpId":"idp-gh-2"}`, "not json"} {
		if err := os.WriteFile(reader, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		later(4 * time.Minute)
		if login, err := id.Login(ctx, "u1", "Smana/a"); err == nil || login != "" {
			t.Fatalf("reader %q: %q, %v", body, login, err)
		}
	}
	_ = os.Remove(reader)
	if _, err := id.Login(ctx, "u1", "Smana/a"); err == nil {
		t.Fatal("a missing reader secret must fail closed")
	}
}

// A repository the App cannot see is one the caller cannot read, on both reads: the gate answers
// 404 for it, never a retryable 503.
func TestRoomAccessReadsAnUnseenRepositoryAsUnreadable(t *testing.T) {
	z := &fakeZitadel{}
	z.set("pat-1", `[{"idpId":"idp-gh","userId":"583231"}]`)
	srv := httptest.NewTLSServer(z)
	t.Cleanup(srv.Close)
	reader := filepath.Join(t.TempDir(), "reader.json")
	writeReader(t, reader, "pat-1", "idp-gh")
	h := config.HumanConfig{Issuer: srv.URL, Access: &config.AccessConfig{ReaderFile: reader, TTL: config.Duration{Duration: time.Minute}}}
	id, acc, err := roomAccess(h, &fakeGitHubUsers{}, srv.Client(), time.Now, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"Smana/gone", "Smana/.."} {
		if _, err := id.Login(t.Context(), "u1", repo); !errors.Is(err, repoaccess.ErrNoRepository) {
			t.Errorf("Login through %s: %v", repo, err)
		}
		if ok, err := acc.CanRead(t.Context(), repo, "octocat"); ok || err != nil {
			t.Errorf("CanRead %s: %v, %v; want false, nil", repo, ok, err)
		}
	}
}

func TestRoomAccessIsOffOrComplete(t *testing.T) {
	if id, acc, err := roomAccess(config.HumanConfig{Issuer: "https://auth.example.test"}, &fakeGitHubUsers{}, nil, time.Now, nil, nil); id != nil || acc != nil || err != nil {
		t.Fatalf("no human.access: %v %v %v; want both nil (admins-only)", id, acc, err)
	}
	h := config.HumanConfig{Issuer: "https://auth.example.test", Access: &config.AccessConfig{ReaderFile: "/r.json", TTL: config.Duration{Duration: time.Minute}}}
	if _, _, err := roomAccess(h, nil, nil, time.Now, nil, nil); err == nil {
		t.Fatal("human.access without the factory App must stop start-up")
	}
}

// humanSide carries the D7 check to the server, and refuses to start half-wired.
func TestHumanSideWiresRoomAccess(t *testing.T) {
	red, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	_, m := newMetrics(t)
	log := slog.New(slog.DiscardHandler)
	humans := authn.NewHumans(authn.NewVerifierWithKeyfunc(humanIssuer, nil), idFile(""), idFile(""), idFile(""), "https://rooms.example.test")
	hub := fanout.New(fakeRoomLog{}, nil, log)
	add := func(manager.Runnable) error { return nil }
	cfg := config.Config{Human: config.HumanConfig{Issuer: humanIssuer, Groups: config.GroupsConfig{Admin: "agents-admin", Member: "agents-member"},
		Access: &config.AccessConfig{ReaderFile: "/r.json", TTL: config.Duration{Duration: time.Minute}}}}
	s, err := humanSide(cfg, humans, fake.NewClientBuilder().Build(), "agent-system", &fakeActLog{}, red, hub, fakeRuns{}, &fakeGitHubUsers{}, nil, add, m, log)
	if err != nil || s.Identity == nil || s.Access == nil || s.Identity.Links == nil || s.Identity.LoginOf == nil || s.Identity.Now == nil ||
		s.Access.Perm == nil || s.Access.Now == nil {
		t.Fatalf("%+v %+v %v", s.Identity, s.Access, err)
	}
	if _, err := humanSide(cfg, humans, fake.NewClientBuilder().Build(), "agent-system", &fakeActLog{}, red, hub, fakeRuns{}, nil, nil, add, m, log); err == nil {
		t.Fatal("human.access without ROOMS_GITHUB_APP_DIR must stop start-up")
	}
}

// The GitHub account link as D7's source (plan step 5 PR5): a linked sub's login never asks ZITADEL;
// with the reader beside it, an unlinked sub falls back to the reader; a link unseen past the window
// is inactive. A link source with no store stops start-up.
func TestRoomAccessFromTheGitHubLink(t *testing.T) {
	z := &fakeZitadel{}
	z.set("pat-1", `[{"idpId":"idp-gh","userId":"583231"}]`)
	var hits atomic.Int64 // every request to ZITADEL, the user-state reads included
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); z.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)
	reader := filepath.Join(t.TempDir(), "reader.json")
	writeReader(t, reader, "pat-1", "idp-gh")
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	reads := 0
	links := func(_ context.Context, sub string) (ghidentity.LinkRecord, bool, error) {
		reads++
		switch sub {
		case "linked":
			return ghidentity.LinkRecord{GitHubID: 583231, LastSeenAt: now}, true, nil
		case "stale":
			return ghidentity.LinkRecord{GitHubID: 583231, LastSeenAt: now.Add(-200 * time.Hour)}, true, nil
		}
		return ghidentity.LinkRecord{}, false, nil
	}
	holders := func(_ context.Context, id int64) (string, bool, error) {
		if id == 583231 {
			return "linked", true, nil
		}
		return "", false, nil
	}
	gl := &config.GitHubLinkConfig{ActiveWindow: config.Duration{Duration: 168 * time.Hour}}
	ttl := config.Duration{Duration: time.Minute}
	h := config.HumanConfig{Issuer: srv.URL, GitHubLink: gl, Access: &config.AccessConfig{TTL: ttl}}
	id, _, err := roomAccess(h, &fakeGitHubUsers{}, nil, func() time.Time { return now }, links, holders)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if login, err := id.Login(ctx, "linked", "Smana/a"); err != nil || login != "octocat" {
		t.Fatalf("linked: %q %v", login, err)
	}
	if login, err := id.Login(ctx, "u1", "Smana/a"); err != nil || login != "" {
		t.Fatalf("unlinked, no reader: %q %v", login, err)
	}
	if err := id.CheckActive(ctx, "stale"); !errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("unseen past the window: %v", err)
	}
	// The reader behind the link.
	h.Access.ReaderFile = reader
	id, _, err = roomAccess(h, &fakeGitHubUsers{}, srv.Client(), func() time.Time { return now }, links, holders)
	if err != nil {
		t.Fatal(err)
	}
	searches := func() int { z.mu.Lock(); defer z.mu.Unlock(); return z.calls } // ZITADEL link searches only
	before, states := searches(), hits.Load()
	if login, err := id.Login(ctx, "linked", "Smana/a"); err != nil || login != "octocat" || searches() != before {
		t.Fatalf("linked: %q %v, ZITADEL's links searched %d times", login, err, searches()-before)
	}
	if hits.Load() == states {
		t.Fatal("the reader's user state was not read for a linked sub")
	}
	// u1's ZITADEL link names 583231, which "linked" holds through its account link: dropped.
	if login, err := id.Login(ctx, "u1", "Smana/a"); err != nil || login != "" || searches() == before {
		t.Fatalf("an id another account linked, through the reader: %q %v", login, err)
	}
	// #126: a linked sub deactivated in ZITADEL is refused at once, inside the window.
	z.mu.Lock()
	z.state = "USER_STATE_INACTIVE"
	z.mu.Unlock()
	id.Forget("linked")
	if err := id.CheckActive(ctx, "linked"); !errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("linked, deactivated in ZITADEL: %v", err)
	}
	if _, _, err := roomAccess(h, &fakeGitHubUsers{}, srv.Client(), time.Now, nil, nil); err == nil {
		t.Fatal("human.githubLink without the store must stop start-up")
	}
}
