// SPDX-License-Identifier: Apache-2.0

package ghidentity_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Smana/maku/internal/ghidentity"
)

// stateWorld is one user whose state the test flips, counting state reads.
type stateWorld struct {
	active, down bool
	reads        int
	now          time.Time
}

func (w *stateWorld) resolver() *ghidentity.Resolver {
	return &ghidentity.Resolver{IDPID: "gh-idp", TTL: 5 * time.Minute, Now: func() time.Time { return w.now },
		Active: func(context.Context, string) (bool, error) {
			w.reads++
			if w.down {
				return false, errors.New("zitadel down")
			}
			return w.active, nil
		},
		Links: func(context.Context, string) ([]ghidentity.Link, error) {
			return []ghidentity.Link{{IDPID: "gh-idp", UserID: "583231"}}, nil
		},
		LoginOf: func(context.Context, string, int64) (string, error) { return "octocat", nil }}
}

// #126: a deactivated user is refused once the cached active answer is a TTL old, and a younger
// one still serves.
func TestADeactivatedUserIsRefusedPastTheTTL(t *testing.T) {
	w := &stateWorld{active: true, now: time.Unix(0, 0)}
	r := w.resolver()
	ctx := context.Background()
	if got, err := r.Login(ctx, "u1", "Smana/x"); err != nil || got != "octocat" {
		t.Fatalf("an active user: %q %v", got, err)
	}
	w.active, w.now = false, w.now.Add(4*time.Minute)
	if got, err := r.Login(ctx, "u1", "Smana/x"); err != nil || got != "octocat" {
		t.Fatalf("a cached active answer younger than the TTL must serve: %q %v", got, err)
	}
	if err := r.CheckActive(ctx, "u1"); err != nil || w.reads != 1 {
		t.Fatalf("the state is cached with the links: %v after %d reads", err, w.reads)
	}
	w.now = w.now.Add(2 * time.Minute)
	if _, err := r.Login(ctx, "u1", "Smana/x"); !errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("past the TTL: %v, want ErrInactive", err)
	}
	if err := r.CheckActive(ctx, "u1"); !errors.Is(err, ghidentity.ErrInactive) || w.reads != 2 {
		t.Fatalf("an inactive answer is cached too: %v after %d reads", err, w.reads)
	}
}

func TestAStateReadErrorFailsClosed(t *testing.T) {
	w := &stateWorld{active: true, down: true, now: time.Unix(0, 0)}
	r := w.resolver()
	err := r.CheckActive(context.Background(), "u1")
	if err == nil || errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("a state read error: %v, want an error that is not ErrInactive", err)
	}
	if _, err := r.Login(context.Background(), "u1", "Smana/x"); err == nil {
		t.Fatal("Login must fail closed on a state error")
	}
	w.down = false
	if err := r.CheckActive(context.Background(), "u1"); err != nil || w.reads != 3 {
		t.Fatalf("the failure was cached: %v after %d reads", err, w.reads)
	}
}
