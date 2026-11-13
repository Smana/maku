// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/ghidentity"
)

// brokerLinks is GET /v1/links/github as the factory's broker client answers it.
type brokerLinks struct {
	reads atomic.Int64
	down  bool
}

func (b *brokerLinks) GitHubLink(_ context.Context, sub string) (rooms.GitHubLink, bool, error) {
	b.reads.Add(1)
	if b.down {
		return rooms.GitHubLink{}, false, errors.New("rooms: HTTP 503 log_unavailable")
	}
	switch sub {
	case "linked":
		return rooms.GitHubLink{GitHubID: 583231, Active: true}, true, nil
	case "stale":
		return rooms.GitHubLink{GitHubID: 583231, Active: false}, true, nil
	}
	return rooms.GitHubLink{}, false, nil
}

// The factory's access over api.access.links: broker (plan step 5 Task 12): a linked human's login
// through the broker's link, the broker's activity verdict as the state (an inactive link is
// ErrInactive, which the API answers not_linked), a broker that cannot answer an error (link_unverified),
// and with the reader behind it an unlinked human falls back to ZITADEL, a linked one never asks it.
func TestFactoryAccessFromTheBrokersLinks(t *testing.T) {
	b := &brokerLinks{}
	resolver := func(a *config.Access, issuer string) *ghidentity.Resolver {
		r := &ghidentity.Resolver{IDPID: githubIDP, TTL: time.Minute, Now: time.Now,
			LoginOf: func(_ context.Context, _ string, id int64) (string, error) {
				if id == 583231 {
					return "octocat", nil
				}
				return "", errors.New("unknown id")
			}}
		r.Links, r.Active = factoryLinks(a, issuer, b, nil)
		return r
	}
	ctx := t.Context()
	r := resolver(&config.Access{Links: config.LinksBroker, TTL: config.Duration{Duration: time.Minute}}, "")
	if login, err := r.Login(ctx, "linked", "Smana/a"); err != nil || login != "octocat" {
		t.Fatalf("linked: %q %v", login, err)
	}
	if _, err := r.Login(ctx, "stale", "Smana/a"); !errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("an inactive link: %v", err)
	}
	if login, err := r.Login(ctx, "nobody", "Smana/a"); err != nil || login != "" {
		t.Fatalf("unlinked: %q %v", login, err)
	}
	b.down = true
	if login, err := resolver(&config.Access{Links: config.LinksBroker}, "").Login(ctx, "linked", "Smana/a"); err == nil || login != "" {
		t.Fatalf("a broker 503: %q %v", login, err)
	}
	b.down = false
	// The reader behind the link: a linked human's login never searches ZITADEL's links, but its
	// user state still rules (#126), so a deactivation applies at once.
	z := &fakeZitadel{}
	z.set("pat-1", `[{"idpId":"idp-gh","userId":"583231"}]`)
	srv := httptest.NewTLSServer(z)
	t.Cleanup(srv.Close)
	reader := filepath.Join(t.TempDir(), "reader.json")
	writeReader(t, reader, "pat-1", "idp-gh")
	a := &config.Access{Links: config.LinksBroker, ReaderFile: reader}
	r = &ghidentity.Resolver{IDPID: githubIDP, TTL: time.Minute, Now: time.Now,
		LoginOf: func(context.Context, string, int64) (string, error) { return "octocat", nil }}
	r.Links, r.Active = factoryLinks(a, srv.URL, b, srv.Client())
	searches := func() int { z.mu.Lock(); defer z.mu.Unlock(); return z.calls }
	if login, err := r.Login(ctx, "linked", "Smana/a"); err != nil || login != "octocat" || searches() != 0 {
		t.Fatalf("linked with the reader behind: %q %v, ZITADEL's links searched %d times", login, err, searches())
	}
	if login, err := r.Login(ctx, "u1", "Smana/a"); err != nil || login != "octocat" || searches() != 1 {
		t.Fatalf("unlinked, through the reader: %q %v", login, err)
	}
	z.mu.Lock()
	z.state = "USER_STATE_INACTIVE"
	z.mu.Unlock()
	r.Forget("linked")
	if err := r.CheckActive(ctx, "linked"); !errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("linked, deactivated in ZITADEL: %v", err)
	}
}
