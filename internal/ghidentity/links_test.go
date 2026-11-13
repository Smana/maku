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

func (l *links) holder(_ context.Context, id int64) (string, bool, error) {
	if l.fail {
		return "", false, errors.New("store down")
	}
	for sub, r := range l.rows {
		if r.GitHubID == id {
			return sub, true, nil
		}
	}
	return "", false, nil
}

func (l *links) read(_ context.Context, sub string) (ghidentity.LinkRecord, bool, error) {
	l.reads++
	if l.fail {
		return ghidentity.LinkRecord{}, false, errors.New("store down")
	}
	r, ok := l.rows[sub]
	return r, ok, nil
}

// zitadel is the reader: a counting fake. zgone is deactivated in ZITADEL; zsteal's ZITADEL link
// names 4242, which zsub1 holds through an account link.
type zitadel struct{ linkReads, stateReads int }

func (z *zitadel) links(_ context.Context, sub string) ([]ghidentity.Link, error) {
	z.linkReads++
	switch sub {
	case "zonly":
		return []ghidentity.Link{{IDPID: "github", UserID: "9"}}, nil
	case "zsteal":
		return []ghidentity.Link{{IDPID: "github", UserID: "4242"}}, nil
	}
	return nil, nil
}

func (z *zitadel) active(_ context.Context, sub string) (bool, error) {
	z.stateReads++
	return sub != "zgone" && sub != "zsub-gone", nil
}

func resolver(l *links, z *zitadel, now *time.Time) *ghidentity.Resolver {
	logins := map[int64]string{4242: "dev-renamed", 9: "zitadel-user"}
	r := &ghidentity.Resolver{IDPID: "github", TTL: 5 * time.Minute, Now: func() time.Time { return *now },
		LoginOf: func(_ context.Context, _ string, id int64) (string, error) { return logins[id], nil }}
	var zl func(context.Context, string) ([]ghidentity.Link, error)
	var za func(context.Context, string) (bool, error)
	if z != nil {
		zl, za = z.links, z.active
	}
	r.Links = ghidentity.Chain(ghidentity.StoreLinks(l.read, "github"), zl, l.holder)
	r.Active = ghidentity.StoreActive(l.read, 168*time.Hour, r.Now, za)
	return r
}

// A linked sub resolves to the App's current login for its numeric id (a rename is harmless); an
// unlinked one to "", which leaves rooms admin-only for it; a store error is an error, never "".
func TestStoreLinksResolveTheCurrentLogin(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	l := &links{rows: map[string]ghidentity.LinkRecord{"zsub1": {GitHubID: 4242, LastSeenAt: now}}}
	r := resolver(l, nil, &now)
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
	r := resolver(l, nil, &now)
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

// The transition (D25): with the reader behind the link, a linked sub's login never searches
// ZITADEL's links, and an unlinked one falls back to it. The reader's user state still rules a linked
// sub too (#126): a deactivation applies at once, not after activeWindow. A GitHub id an account
// link holds is never reached by another sub through the reader.
func TestChainFallsBackToTheReader(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	l := &links{rows: map[string]ghidentity.LinkRecord{"zsub1": {GitHubID: 4242, LastSeenAt: now},
		"zsub-gone": {GitHubID: 5, LastSeenAt: now}}}
	z := &zitadel{}
	r := resolver(l, z, &now)
	ctx := t.Context()
	if got, err := r.Login(ctx, "zsub1", "o/r"); err != nil || got != "dev-renamed" || z.linkReads != 0 {
		t.Fatalf("linked: %q %v, ZITADEL's links read %d times", got, err, z.linkReads)
	}
	if z.stateReads == 0 {
		t.Fatal("a linked sub's state was not read from the reader")
	}
	if err := r.CheckActive(ctx, "zsub-gone"); !errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("linked, inside the window, deactivated in ZITADEL: %v", err)
	}
	if got, err := r.Login(ctx, "zonly", "o/r"); err != nil || got != "zitadel-user" || z.linkReads != 1 {
		t.Fatalf("unlinked, through the reader: %q %v (%d link reads)", got, err, z.linkReads)
	}
	if err := r.CheckActive(ctx, "zgone"); !errors.Is(err, ghidentity.ErrInactive) {
		t.Fatalf("the reader's state for an unlinked sub: %v", err)
	}
	if got, err := r.Login(ctx, "zsteal", "o/r"); err != nil || got != "" {
		t.Fatalf("a ZITADEL link to an id another account linked: %q %v, want no login", got, err)
	}
	l.fail = true
	r.Forget("zonly")
	if got, err := r.Login(ctx, "zonly", "o/r"); err == nil || got != "" {
		t.Fatalf("a store error on the holder check: %q %v", got, err)
	}
}
