// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	grantHash  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	otherHash  = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	grantActor = "system:factory"
)

func newGrant(id string) NewGrant {
	return NewGrant{ID: id, Repository: "smana/cloud-native-ref", Class: "docs-broad",
		Files: []string{`docs/security/threat-model\.md`}, PolicyHash: grantHash[:12], OriginPR: 42,
		CreatedBy: "dana", Lifetime: 24 * time.Hour}
}

func grantID(n int) string { return strings.Repeat("a", 25) + string(rune('a'+n)) }

func eventsOf(t *testing.T, s *Store, id string) []string {
	t.Helper()
	evs, err := s.GrantEvents(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		out = append(out, fmt.Sprintf("%s@%d", e.Event, e.Version))
	}
	return out
}

// Merge policy stage 5: a grant is created active at version 1 with its created event, then moves
// only along its transitions, each with its event: suspended, reconfirmed (version 2, rebound),
// used, renewed (version 3), revoked for good.
func TestAGrantMovesAlongItsTransitions(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	g, err := s.CreateGrant(ctx, newGrant(grantID(0)))
	if err != nil {
		t.Fatal(err)
	}
	if g.Status != "active" || g.Version != 1 || !g.ExpiresAt.After(g.CreatedAt) || !g.LastUsedAt.IsZero() {
		t.Fatalf("%+v", g)
	}
	step := func(m GrantMove, wantErr error) Grant {
		t.Helper()
		m.Actor = grantActor
		got, err := s.MoveGrant(ctx, g.ID, m)
		if !errors.Is(err, wantErr) {
			t.Fatalf("%s: %v, want %v", m.Event, err, wantErr)
		}
		return got
	}
	step(GrantMove{Event: "reconfirmed", PolicyHash: otherHash}, ErrGrantState) // only a suspended grant is reconfirmed
	step(GrantMove{Event: "suspended", PolicyHash: otherHash}, nil)
	step(GrantMove{Event: "used", PR: 7}, ErrGrantState) // a suspended grant is never used
	if got := step(GrantMove{Event: "reconfirmed", PolicyHash: otherHash[:12]}, nil); got.Version != 2 || got.PolicyHash != otherHash[:12] {
		t.Fatalf("%+v", got)
	}
	step(GrantMove{Event: "used", Version: 1, PR: 7}, ErrGrantVersion) // bound to a version since left
	if got := step(GrantMove{Event: "used", Version: 2, PR: 7, Head: strings.Repeat("a", 40)}, nil); got.LastUsedPR != 7 || got.LastUsedAt.IsZero() || got.Version != 2 {
		t.Fatalf("%+v", got)
	}
	if got := step(GrantMove{Event: "renewed", Lifetime: 48 * time.Hour}, nil); got.Version != 3 || !got.ExpiresAt.After(g.ExpiresAt) {
		t.Fatalf("%+v", got)
	}
	step(GrantMove{Event: "revoked", Reason: "no longer wanted"}, nil)
	for _, ev := range []string{"renewed", "reconfirmed", "used", "revoked", "disabled", "suspended", "expired"} {
		step(GrantMove{Event: ev, PolicyHash: grantHash, Lifetime: time.Hour, PR: 1}, ErrGrantState) // revoked is for good
	}
	want := "created@1 suspended@1 reconfirmed@2 used@2 renewed@3 revoked@3"
	if got := strings.Join(eventsOf(t, s, g.ID), " "); got != want {
		t.Fatalf("events %q, want %q", got, want)
	}
	if _, err := s.MoveGrant(ctx, grantID(9), GrantMove{Event: "revoked", Actor: "dana"}); !errors.Is(err, ErrNoGrant) {
		t.Fatal(err)
	}
}

// The table refuses what the methods would never write, even from its owner: a grant planted
// suspended, a predicate rewritten, a move without its event, a renewal skipping a version or
// rebinding the policy. The broker may not even try to rewrite a predicate or delete a grant.
func TestTheGrantTableRefusesOffTransitionWrites(t *testing.T) {
	ctx := t.Context()
	s, broker, _, super := open(t)
	owner := as(super, "rooms_owner", "owner")
	g, err := s.CreateGrant(ctx, newGrant(grantID(1)))
	if err != nil {
		t.Fatal(err)
	}
	for name, sql := range map[string]string{
		"planted suspended": `INSERT INTO approval_grants (grant_id, version, repository, class, files, policy_hash, origin_pr,
			created_by, created_at, renewed_at, expires_at, status, status_at)
			VALUES ('bbbbbbbbbbbbbbbbbbbbbbbbbb', 1, 'smana/x', 'c', '{a}', '0123456789ab', 1, 'x', now(), now(), now() + interval '1 h', 'suspended', now())`,
		"the predicate rewritten":  `UPDATE approval_grants SET files = '{.*}' WHERE grant_id = '` + g.ID + `'`,
		"a move without its event": `UPDATE approval_grants SET status = 'revoked', status_at = now() WHERE grant_id = '` + g.ID + `'`,
		"a renewal skipping a version": `UPDATE approval_grants SET version = version + 2, renewed_at = now(), status_at = now(),
			expires_at = now() + interval '1 h' WHERE grant_id = '` + g.ID + `'`,
		"a policy rebound by a renewal": `UPDATE approval_grants SET version = version + 1, renewed_at = now(), status_at = now(),
			expires_at = now() + interval '1 h', policy_hash = 'ffffffffffff' WHERE grant_id = '` + g.ID + `'`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(ctx, owner, sql); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if err := run(ctx, broker, `UPDATE approval_grants SET files = '{.*}' WHERE grant_id = $1`, g.ID); err == nil {
		t.Fatal("the broker rewrote a predicate")
	}
	if err := run(ctx, broker, `DELETE FROM approval_grants WHERE grant_id = $1`, g.ID); err == nil {
		t.Fatal("the broker deleted a grant")
	}
}

// The audit is insert-only for every role, its owner included: no event is rewritten or removed.
func TestGrantEventsAreInsertOnly(t *testing.T) {
	ctx := t.Context()
	s, broker, _, super := open(t)
	g, err := s.CreateGrant(ctx, newGrant(grantID(2)))
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`UPDATE approval_grant_events SET reason = 'x'`,
		`DELETE FROM approval_grant_events`,
		`TRUNCATE approval_grant_events CASCADE`,
	} {
		if err := run(ctx, broker, sql); err == nil {
			t.Fatalf("the broker ran %s", sql)
		}
		if err := run(ctx, super, sql); err == nil {
			t.Fatalf("even the superuser ran %s", sql)
		}
	}
	if err := run(ctx, broker, `INSERT INTO approval_grant_events (grant_id, version, event, actor, at)
		VALUES ($1, 1, 'used', 'x', now() - interval '1 day')`, g.ID); err == nil {
		t.Fatal("an event was backdated")
	}
	if got := eventsOf(t, s, g.ID); len(got) != 1 {
		t.Fatalf("%q", got)
	}
}

// Concurrent moves serialize on the grant's row: of eight revokes one wins and seven are refused,
// and the audit holds one revoked event; eight lookups under a new policy suspend it once.
func TestConcurrentGrantMovesSerialize(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	race := func(n int, f func() error) (ok, refused int) {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for range n {
			wg.Go(func() {
				err := f()
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					ok++
				case errors.Is(err, ErrGrantState):
					refused++
				default:
					t.Error(err)
				}
			})
		}
		wg.Wait()
		return ok, refused
	}
	revoked, err := s.CreateGrant(ctx, newGrant(grantID(3)))
	if err != nil {
		t.Fatal(err)
	}
	if ok, refused := race(8, func() error {
		_, err := s.MoveGrant(context.WithoutCancel(ctx), revoked.ID, GrantMove{Event: "revoked", Actor: "dana"})
		return err
	}); ok != 1 || refused != 7 {
		t.Fatalf("%d revokes won, %d refused", ok, refused)
	}
	if got := strings.Join(eventsOf(t, s, revoked.ID), " "); got != "created@1 revoked@1" {
		t.Fatal(got)
	}
	n := newGrant(grantID(4))
	if _, err := s.CreateGrant(ctx, n); err != nil {
		t.Fatal(err)
	}
	if ok, _ := race(8, func() error {
		_, _, err := s.LookupGrants(context.WithoutCancel(ctx), n.Repository, n.Class, otherHash, time.Hour*24*90, grantActor)
		return err
	}); ok != 8 {
		t.Fatalf("%d lookups answered", ok)
	}
	if got := strings.Join(eventsOf(t, s, n.ID), " "); got != "created@1 suspended@1" {
		t.Fatal(got)
	}
}

// A lookup answers the active grants of the class under the policy decided under: one made under
// another policy is suspended, one past its expiry or the operator's maximum lifetime expired,
// each with its event; another class's grant is not read.
func TestLookupExpiresAndSuspendsBeforeItAnswers(t *testing.T) {
	ctx := t.Context()
	s, _, _, super := open(t)
	bound, stale, old, other := newGrant(grantID(5)), newGrant(grantID(6)), newGrant(grantID(7)), newGrant(grantID(8))
	stale.PolicyHash = otherHash[:12]
	other.Class = "deps-patch"
	for _, n := range []NewGrant{bound, stale, old, other} {
		if _, err := s.CreateGrant(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	// old was renewed two days ago: past a one-day maximum lifetime, however far its own expiry.
	forge(t, super, `UPDATE approval_grants SET renewed_at = now() - interval '2 days', expires_at = now() + interval '30 days'
		WHERE grant_id = '`+old.ID+`'`)
	active, events, err := s.LookupGrants(ctx, bound.Repository, bound.Class, grantHash, 24*time.Hour, grantActor)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != bound.ID {
		t.Fatalf("%+v", active)
	}
	if got := strings.Join(events, " "); got != "suspended expired" && got != "expired suspended" {
		t.Fatal(got)
	}
	for id, want := range map[string]string{stale.ID: "suspended", old.ID: "expired", other.ID: "active"} {
		if g, _ := s.Grant(ctx, id); g.Status != want {
			t.Fatalf("%s is %s, want %s", id, g.Status, want)
		}
	}
	// A suspended grant reconfirmed under the current policy is read again.
	if _, err := s.MoveGrant(ctx, stale.ID, GrantMove{Event: "reconfirmed", Actor: "dana", PolicyHash: grantHash[:12]}); err != nil {
		t.Fatal(err)
	}
	if active, _, _ := s.LookupGrants(ctx, bound.Repository, bound.Class, grantHash, 24*time.Hour, grantActor); len(active) != 2 {
		t.Fatalf("%+v", active)
	}
	if gs, err := s.Grants(ctx, GrantFilter{Repository: bound.Repository, Status: "active"}); err != nil || len(gs) != 3 {
		t.Fatalf("%d %v", len(gs), err)
	}
}
