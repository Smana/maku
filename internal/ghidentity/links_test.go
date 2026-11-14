// SPDX-License-Identifier: Apache-2.0

package ghidentity_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Smana/maku/internal/ghidentity"
)

// links is the broker's github_links as the resolver reads them, with a count of the reads.
type links struct {
	rows  map[string]ghidentity.LinkRecord
	fail  bool
	reads int
}

func (l *links) read(_ context.Context, sub string) (ghidentity.LinkRecord, bool, error) {
	l.reads++
	if l.fail {
		return ghidentity.LinkRecord{}, false, errors.New("store down")
	}
	r, ok := l.rows[sub]
	return r, ok, nil
}

func resolver(l *links, now *time.Time) *ghidentity.Resolver {
	logins := map[int64]string{4242: "dev-renamed"}
	return &ghidentity.Resolver{IDPID: "github", TTL: 5 * time.Minute, Now: func() time.Time { return *now },
		LoginOf: func(_ context.Context, _ string, id int64) (string, error) { return logins[id], nil },
		Links:   ghidentity.StoreLinks(l.read, "github"),
		Active:  ghidentity.StoreActive(l.read, 168*time.Hour, func() time.Time { return *now })}
}

// A linked sub resolves to the App's current login for its numeric id (a rename is harmless); an
// unlinked one to "", which leaves rooms admin-only for it; a store error is an error, never "".
func TestStoreLinksResolveTheCurrentLogin(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	l := &links{rows: map[string]ghidentity.LinkRecord{"zsub1": {GitHubID: 4242, LastSeenAt: now}}}
	r := resolver(l, &now)
	ctx := t.Context()
	if got, err := r.Login(ctx, "zsub1", "o/r"); err != nil || got != "dev-renamed" {
		t.Fatalf("linked: %q %v", got, err)
	}
	if got, err := r.Login(ctx, "nobody", "o/r"); err != nil || got != "" {
		t.Fatalf("unlinked: %q %v", got, err)
	}
	l.fail = true
	if got, err := r.Login(ctx, "other", "o/r"); err == nil || got != "" {
		t.Fatalf("a store error: %q %v", got, err)
	}
}

// Active without an IdP admin API: a link unseen past the window is ErrInactive; a sub with no link
// is not refused (it may sign in to link); Forget after a touch makes it active again at once.
func TestStoreActiveWindow(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	l := &links{rows: map[string]ghidentity.LinkRecord{"zsub1": {GitHubID: 4242, LastSeenAt: now.Add(-169 * time.Hour)},
		"zsub2": {GitHubID: 7, LastSeenAt: now.Add(-167 * time.Hour)}}}
	r := resolver(l, &now)
	ctx := t.Context()
	if err := r.CheckActive(ctx, "zsub1"); !errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("unseen past the window: %v", err)
	}
	if _, err := r.Login(ctx, "zsub1", "o/r"); !errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("its login: %v", err)
	}
	if err := r.CheckActive(ctx, "zsub2"); err != nil {
		t.Fatalf("inside the window: %v", err)
	}
	if err := r.CheckActive(ctx, "nobody"); err != nil {
		t.Fatalf("no link: %v", err)
	}
	l.rows["zsub1"] = ghidentity.LinkRecord{GitHubID: 4242, LastSeenAt: now} // the human signed in
	r.Forget("zsub1")
	if err := r.CheckActive(ctx, "zsub1"); err != nil {
		t.Fatalf("after a touch: %v", err)
	}
	l.fail = true
	r.Forget("zsub2")
	if err := r.CheckActive(ctx, "zsub2"); err == nil || errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("a store error: %v", err)
	}
}
