// SPDX-License-Identifier: Apache-2.0

package store

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// ghState is a pending state for sub, expiring in ten minutes.
func ghState(t *testing.T, s *Store, state, sub string, now time.Time) {
	t.Helper()
	if err := s.StartGitHubLink(t.Context(), codeHash(state), sub, codeHash("nonce-"+state), now.Add(10*time.Minute), now); err != nil {
		t.Fatalf("start %s: %v", state, err)
	}
}

// A sub holds at most MaxGitHubPending live states; an expired one frees its place and reads as
// invalid.
func TestGitHubLinkPendingStatesAreBoundedAndShortLived(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	now := time.Now().UTC().Truncate(time.Second)
	for _, st := range []string{"a", "b", "c"} {
		ghState(t, s, st, "zsub1", now)
	}
	if err := s.StartGitHubLink(ctx, codeHash("d"), "zsub1", codeHash("nonce-d"), now.Add(10*time.Minute), now); !errors.Is(err, ErrTooManyPending) {
		t.Fatalf("a 4th pending state: %v, want ErrTooManyPending", err)
	}
	ghState(t, s, "other", "zsub2", now) // another sub's bound is its own
	got, err := s.GitHubState(ctx, codeHash("a"), now)
	if err != nil || got.Sub != "zsub1" || string(got.NonceSHA256) != string(codeHash("nonce-a")) || got.GitHubID != 0 {
		t.Fatalf("state a: %+v %v", got, err)
	}
	later := now.Add(11 * time.Minute)
	if _, err := s.GitHubState(ctx, codeHash("a"), later); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("an expired state reads: %v", err)
	}
	if _, err := s.GitHubState(ctx, codeHash("nope"), now); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("an unknown state reads: %v", err)
	}
	// The expired three no longer count.
	ghState(t, s, "e", "zsub1", later)
}

// The callback records who GitHub said the user is, once; the confirm by the same sub links, in one
// transaction with its event, and consumes the state.
func TestGitHubLinkConfirmLinksOnceWithItsEvent(t *testing.T) {
	ctx := t.Context()
	s, _, _, super := open(t)
	now := time.Now().UTC().Truncate(time.Second)
	ghState(t, s, "a", "zsub1", now)
	if _, err := s.ConfirmGitHubLink(ctx, codeHash("a"), "zsub1", now); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("a confirm before the callback: %v, want ErrStateInvalid", err)
	}
	if err := s.SetGitHubStateUser(ctx, codeHash("a"), 4242, "dev", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGitHubStateUser(ctx, codeHash("a"), 9999, "evil", now); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("a second callback on one state: %v, want ErrStateInvalid", err)
	}
	if got, err := s.GitHubState(ctx, codeHash("a"), now); err != nil || got.GitHubID != 4242 || got.Login != "dev" {
		t.Fatalf("state after the callback: %+v %v", got, err)
	}
	if _, err := s.ConfirmGitHubLink(ctx, codeHash("a"), "zsub2", now); !errors.Is(err, ErrSubMismatch) {
		t.Fatalf("a confirm by another sub: %v, want ErrSubMismatch", err)
	}
	if _, found, err := s.GitHubLinkOf(ctx, "zsub2"); err != nil || found {
		t.Fatalf("the other sub got a link: %v %v", found, err)
	}
	act, err := s.ConfirmGitHubLink(ctx, codeHash("a"), "zsub1", now)
	if err != nil || act != GitHubLinked {
		t.Fatalf("confirm: %q %v", act, err)
	}
	l, found, err := s.GitHubLinkOf(ctx, "zsub1")
	if err != nil || !found || l.GitHubID != 4242 || l.Login != "dev" || !l.LinkedAt.Equal(now) || !l.LastSeenAt.Equal(now) {
		t.Fatalf("the link: %+v %v %v", l, found, err)
	}
	if _, err := s.ConfirmGitHubLink(ctx, codeHash("a"), "zsub1", now); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("a replayed confirm: %v, want ErrStateInvalid", err)
	}
	if got := query(t, super, `SELECT string_agg(sub || ':' || github_id || ':' || act || ':' || actor, ',') FROM github_link_events`); got != "zsub1:4242:linked:zsub1" {
		t.Fatalf("events: %q", got)
	}
}

// One GitHub id per sub and one sub per GitHub id: an id held by another sub is refused with nothing
// written; relinking one's own sub replaces its link.
func TestGitHubLinkUniqueness(t *testing.T) {
	ctx := t.Context()
	s, _, _, super := open(t)
	now := time.Now().UTC().Truncate(time.Second)
	link := func(state, sub string, id int64, login string) (string, error) {
		ghState(t, s, state, sub, now)
		if err := s.SetGitHubStateUser(ctx, codeHash(state), id, login, now); err != nil {
			t.Fatal(err)
		}
		return s.ConfirmGitHubLink(ctx, codeHash(state), sub, now)
	}
	if _, err := link("a", "zsub1", 4242, "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := link("b", "zsub2", 4242, "dev"); !errors.Is(err, ErrGitHubTaken) {
		t.Fatalf("an id held by another sub: %v, want ErrGitHubTaken", err)
	}
	if _, found, _ := s.GitHubLinkOf(ctx, "zsub2"); found {
		t.Fatal("the refused sub has a link")
	}
	if act, err := link("c", "zsub1", 7777, "dev2"); err != nil || act != GitHubRelinked {
		t.Fatalf("relink: %q %v", act, err)
	}
	if l, _, _ := s.GitHubLinkOf(ctx, "zsub1"); l.GitHubID != 7777 || l.Login != "dev2" {
		t.Fatalf("after relink: %+v", l)
	}
	// 4242 is free again.
	if act, err := link("d", "zsub2", 4242, "dev"); err != nil || act != GitHubLinked {
		t.Fatalf("the freed id: %q %v", act, err)
	}
	if got := query(t, super, `SELECT string_agg(act, ',' ORDER BY id) FROM github_link_events`); got != "linked,relinked,linked" {
		t.Fatalf("events: %q", got)
	}
}

// Unlink and AdminUnlink remove the link and write their event; the history cannot be rewritten by
// the broker's role.
func TestGitHubUnlinkAndTheAppendOnlyHistory(t *testing.T) {
	ctx := t.Context()
	s, broker, _, super := open(t)
	now := time.Now().UTC().Truncate(time.Second)
	for i, sub := range []string{"zsub1", "zsub2"} {
		st := sub + "-state"
		ghState(t, s, st, sub, now)
		if err := s.SetGitHubStateUser(ctx, codeHash(st), int64(100+i), "dev", now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ConfirmGitHubLink(ctx, codeHash(st), sub, now); err != nil {
			t.Fatal(err)
		}
	}
	if gone, err := s.UnlinkGitHub(ctx, "zsub1", "zsub1", false, now); err != nil || !gone {
		t.Fatalf("unlink: %v %v", gone, err)
	}
	if gone, err := s.UnlinkGitHub(ctx, "zsub1", "zsub1", false, now); err != nil || gone {
		t.Fatalf("a second unlink: %v %v", gone, err)
	}
	if gone, err := s.UnlinkGitHub(ctx, "zsub2", "zadmin", true, now); err != nil || !gone {
		t.Fatalf("admin unlink: %v %v", gone, err)
	}
	if got := query(t, super, `SELECT string_agg(sub || ':' || act || ':' || actor, ',' ORDER BY id) FROM github_link_events WHERE act <> 'linked'`); got != "zsub1:unlinked:zsub1,zsub2:admin_unlinked:zadmin" {
		t.Fatalf("events: %q", got)
	}
	for name, sql := range map[string]string{
		"an UPDATE": `UPDATE github_link_events SET actor = 'x'`,
		"a DELETE":  `DELETE FROM github_link_events`,
	} {
		err := run(ctx, broker, sql)
		if code := pgCode(err); code != "42501" {
			t.Errorf("the broker's role ran %s: %v", name, err)
		}
	}
}

// last_seen_at moves only when it is over an hour old, so a busy human writes once an hour.
func TestTouchGitHubLinkIsHourly(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	now := time.Now().UTC().Truncate(time.Second)
	ghState(t, s, "a", "zsub1", now)
	if err := s.SetGitHubStateUser(ctx, codeHash("a"), 4242, "dev", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmGitHubLink(ctx, codeHash("a"), "zsub1", now); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		at     time.Duration
		moved  bool
		seenAt time.Duration
	}{
		{30 * time.Minute, false, 0},
		{61 * time.Minute, true, 61 * time.Minute},
		{90 * time.Minute, false, 61 * time.Minute},
		{3 * time.Hour, true, 3 * time.Hour},
	} {
		moved, err := s.TouchGitHubLink(ctx, "zsub1", now.Add(c.at))
		if err != nil || moved != c.moved {
			t.Fatalf("touch at +%s: %v %v, want moved %v", c.at, moved, err, c.moved)
		}
		if l, _, _ := s.GitHubLinkOf(ctx, "zsub1"); !l.LastSeenAt.Equal(now.Add(c.seenAt)) {
			t.Fatalf("after +%s: last seen %s, want +%s", c.at, l.LastSeenAt, c.seenAt)
		}
	}
	if moved, err := s.TouchGitHubLink(ctx, "nobody", now); err != nil || moved {
		t.Fatalf("an unlinked sub: %v %v", moved, err)
	}
}

// pgCode is err's SQLSTATE, "" when it is none.
func pgCode(err error) string {
	if pg, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pg.Code
	}
	return ""
}
