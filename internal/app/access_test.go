// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/Smana/maku/internal/config"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/ghidentity"
	"github.com/Smana/maku/internal/github"
	"github.com/Smana/maku/internal/redact"
	"github.com/Smana/maku/internal/repoaccess"
)

// linkTable is the broker's github_links as roomAccess reads them: sub to a GitHub id seen at seen.
func linkTable(seen time.Time, rows map[string]int64) ghidentity.LinkReader {
	return func(_ context.Context, sub string) (ghidentity.LinkRecord, bool, error) {
		id, ok := rows[sub]
		return ghidentity.LinkRecord{GitHubID: id, LastSeenAt: seen}, ok, nil
	}
}

// accessConfig is human.access with the GitHub account link, its one identity source (v4 D8).
func accessConfig(ttl time.Duration) config.HumanConfig {
	return config.HumanConfig{Issuer: "https://auth.example.test", Access: &config.AccessConfig{TTL: config.Duration{Duration: ttl}},
		GitHubLink: &config.GitHubLinkConfig{ActiveWindow: config.Duration{Duration: 168 * time.Hour}}}
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

func TestFactoryAppUsesGitHubAPIURL(t *testing.T) {
	t.Setenv("ROOMS_GITHUB_API_URL", "https://e2e-stub.e2e.svc.cluster.local/")
	if got := factoryApp(t.TempDir()).API; got != "https://e2e-stub.e2e.svc.cluster.local/" {
		t.Fatalf("factory App API root = %q", got)
	}
}

// D7's wiring: the login from the user's GitHub account link, resolved through the App at every
// cache miss, then the App's permission, both under the config's TTL.
func TestRoomAccess(t *testing.T) {
	now := time.Unix(0, 0)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	gh := &fakeGitHubUsers{}
	id, acc, err := roomAccess(accessConfig(3*time.Minute), gh, clock, linkTable(now, map[string]int64{"u1": 583231}))
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
}

// v4 step 8 PR F: the link is the only source. An unlinked human resolves to no login, so the room
// gate answers unlinked, and nothing else is asked; a link unseen past its window is inactive.
func TestRoomAccessRefusesAnUnlinkedHuman(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	seen := map[string]time.Time{"linked": now, "stale": now.Add(-200 * time.Hour)}
	reads := 0
	links := func(_ context.Context, sub string) (ghidentity.LinkRecord, bool, error) {
		reads++
		at, ok := seen[sub]
		return ghidentity.LinkRecord{GitHubID: 583231, LastSeenAt: at}, ok, nil
	}
	gh := &fakeGitHubUsers{}
	id, _, err := roomAccess(accessConfig(time.Minute), gh, func() time.Time { return now }, links)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if login, err := id.Login(ctx, "linked", "Smana/a"); err != nil || login != "octocat" {
		t.Fatalf("linked: %q %v", login, err)
	}
	before := len(gh.repos)
	if login, err := id.Login(ctx, "u1", "Smana/a"); err != nil || login != "" || len(gh.repos) != before {
		t.Fatalf("unlinked: %q %v, the App asked %d times", login, err, len(gh.repos)-before)
	}
	if err := id.CheckActive(ctx, "stale"); !errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("unseen past the window: %v", err)
	}
	if reads == 0 {
		t.Fatal("the links were never read")
	}
}

// A repository the App cannot see is one the caller cannot read, on both reads: the gate answers
// 404 for it, never a retryable 503.
func TestRoomAccessReadsAnUnseenRepositoryAsUnreadable(t *testing.T) {
	id, acc, err := roomAccess(accessConfig(time.Minute), &fakeGitHubUsers{}, time.Now, linkTable(time.Now(), map[string]int64{"u1": 583231}))
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
	links := linkTable(time.Now(), nil)
	if id, acc, err := roomAccess(config.HumanConfig{Issuer: "https://auth.example.test"}, &fakeGitHubUsers{}, time.Now, links); id != nil || acc != nil || err != nil {
		t.Fatalf("no human.access: %v %v %v; want both nil (admins-only)", id, acc, err)
	}
	h := accessConfig(time.Minute)
	if _, _, err := roomAccess(h, nil, time.Now, links); err == nil {
		t.Fatal("human.access without the factory App must stop start-up")
	}
	if _, _, err := roomAccess(h, &fakeGitHubUsers{}, time.Now, nil); err == nil {
		t.Fatal("human.githubLink without the store must stop start-up")
	}
	h.GitHubLink = nil
	if _, _, err := roomAccess(h, &fakeGitHubUsers{}, time.Now, links); err == nil || !strings.Contains(err.Error(), "human.githubLink") {
		t.Fatalf("human.access without human.githubLink: %v", err)
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
	humans := zitadelHumans("https://rooms.example.test")
	hub := fanout.New(fakeRoomLog{}, nil, log)
	add := func(manager.Runnable) error { return nil }
	h := accessConfig(time.Minute)
	h.Issuer, h.Groups = humanIssuer, config.GroupsConfig{Admin: "agents-admin", Member: "agents-member"}
	cfg := config.Config{Human: h}
	s, err := humanSide(cfg, humans, fake.NewClientBuilder().Build(), "agent-system", &fakeActLog{}, red, hub, fakeRuns{}, &fakeGitHubUsers{}, nil, add, m, log)
	if err != nil || s.Identity == nil || s.Access == nil || s.Identity.Links == nil || s.Identity.LoginOf == nil || s.Identity.Now == nil ||
		s.Access.Perm == nil || s.Access.Now == nil {
		t.Fatalf("%+v %+v %v", s.Identity, s.Access, err)
	}
	if _, err := humanSide(cfg, humans, fake.NewClientBuilder().Build(), "agent-system", &fakeActLog{}, red, hub, fakeRuns{}, nil, nil, add, m, log); err == nil {
		t.Fatal("human.access without ROOMS_GITHUB_APP_DIR must stop start-up")
	}
}
