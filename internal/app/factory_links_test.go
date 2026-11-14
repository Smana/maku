// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/api"
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
// and an unlinked human resolves to no login (not_linked), never through another source (v4 D8).
func TestFactoryAccessFromTheBrokersLinks(t *testing.T) {
	b := &brokerLinks{}
	resolver := func(links brokerGitHubLinks) *ghidentity.Resolver {
		r := &ghidentity.Resolver{IDPID: githubIDP, TTL: time.Minute, Now: time.Now,
			LoginOf: func(_ context.Context, _ string, id int64) (string, error) {
				if id == 583231 {
					return "octocat", nil
				}
				return "", errors.New("unknown id")
			}}
		r.Links, r.Active = factoryLinks(links)
		return r
	}
	ctx := t.Context()
	r := resolver(b)
	if login, err := r.Login(ctx, "linked", "Smana/a"); err != nil || login != "octocat" {
		t.Fatalf("linked: %q %v", login, err)
	}
	if _, err := r.Login(ctx, "stale", "Smana/a"); !errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("an inactive link: %v", err)
	}
	if login, err := r.Login(ctx, "nobody", "Smana/a"); err != nil || login != "" {
		t.Fatalf("unlinked: %q %v", login, err)
	}
	if err := r.CheckActive(ctx, "nobody"); err != nil {
		t.Fatalf("unlinked is active (it may sign in to link): %v", err)
	}
	if ok, err := (api.GitHubAccess{Identity: r, Perms: everyoneWrites{}}).CanWrite(ctx, authn.Principal{Sub: "nobody"}, "Smana/a"); ok || err != nil {
		t.Fatalf("an unlinked human's run: %v %v, want refused (not_linked)", ok, err)
	}
	b.down = true
	if login, err := resolver(b).Login(ctx, "linked", "Smana/a"); err == nil || login != "" {
		t.Fatalf("a broker 503: %q %v", login, err)
	}
	b.down = false
	if login, err := resolver(nil).Login(ctx, "linked", "Smana/a"); err != nil || login != "" {
		t.Fatalf("no broker: %q %v, want no login", login, err)
	}
}

// everyoneWrites grants write to every login, so a refusal can only come from the missing link.
type everyoneWrites struct{}

func (everyoneWrites) CanWrite(context.Context, string, string) (bool, error) { return true, nil }
