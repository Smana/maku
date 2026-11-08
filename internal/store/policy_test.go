// SPDX-License-Identifier: Apache-2.0

package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const canary = "Smana/cloud-native-ref"

var snapshot = json.RawMessage(`{"clean":20,"bad":0,"drill":"passed"}`)

// Stage 6: a promotion enters requested on its evidence, moves to pr_opened naming its pull
// request, then once to merged or abandoned, by someone, never back; one open per class.
func TestPromotionMovesOnceToItsEnd(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	p, err := s.RecordPromotion(ctx, Promotion{ID: "aaaaaaaa", Repository: canary, Class: "docs-links", Threshold: 20,
		Evidence: snapshot, RequestedBy: "Smana"})
	if err != nil || p.State != PromotionRequested || p.RequestedAt.IsZero() || !strings.Contains(string(p.Evidence), `"clean": 20`) {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := s.RecordPromotion(ctx, Promotion{ID: "bbbbbbbb", Repository: canary, Class: "docs-links", Threshold: 20,
		Evidence: snapshot, RequestedBy: "Smana"}); !errors.Is(err, ErrPromotionOpen) {
		t.Fatalf("a second open promotion: %v", err)
	}
	if _, err := s.MovePromotion(ctx, "aaaaaaaa", PromotionPROpened, 0, "Smana"); !errors.Is(err, ErrPolicyMoved) {
		t.Fatalf("pr_opened names its pull request: %v", err)
	}
	if p, err = s.MovePromotion(ctx, "aaaaaaaa", PromotionPROpened, 42, "Smana"); err != nil || p.PullRequest != 42 || p.ClosedBy != "Smana" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := s.MovePromotion(ctx, "aaaaaaaa", PromotionMerged, 43, "Smana"); !errors.Is(err, ErrPolicyMoved) {
		t.Fatalf("the pull request is the one named: %v", err)
	}
	if p, err = s.MovePromotion(ctx, "aaaaaaaa", PromotionMerged, 0, "Smana"); err != nil || p.State != PromotionMerged || p.PullRequest != 42 {
		t.Fatalf("%+v %v", p, err)
	}
	for _, st := range []string{PromotionAbandoned, PromotionRequested, PromotionPROpened} {
		if _, err := s.MovePromotion(ctx, "aaaaaaaa", st, 0, "Smana"); !errors.Is(err, ErrPolicyMoved) {
			t.Fatalf("a merged promotion moved to %s: %v", st, err)
		}
	}
	if _, err := s.MovePromotion(ctx, "zzzzzzzz", PromotionAbandoned, 0, "Smana"); !errors.Is(err, ErrNoPolicyRecord) {
		t.Fatal(err)
	}
	// The class is free again once its promotion ended.
	if _, err := s.RecordPromotion(ctx, Promotion{ID: "bbbbbbbb", Repository: canary, Class: "docs-links", Threshold: 20,
		Evidence: snapshot, RequestedBy: "Smana"}); err != nil {
		t.Fatal(err)
	}
	ps, es, err := s.PolicyAudit(ctx, canary)
	if err != nil || len(ps) != 2 || len(es) != 0 || ps[0].ID != "bbbbbbbb" {
		t.Fatalf("%+v %+v %v", ps, es, err)
	}
}

// MP7's exception: requested with a justification, decided once by an operator, and only an
// approved, unexpired exception of the class and minimum carries a promotion.
func TestExceptionCarriesAPromotionOnlyWhileApproved(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	ask := func(id, class string) Exception {
		e, err := s.RequestException(ctx, Exception{ID: id, Repository: canary, Class: class, Minimum: 10,
			Justification: "the canary's docs class is narrow: ten clean decisions bound it", RequestedBy: "Smana"})
		if err != nil || e.State != ExceptionRequested {
			t.Fatalf("%+v %v", e, err)
		}
		return e
	}
	promote := func(id, exc string, threshold int) error {
		_, err := s.RecordPromotion(ctx, Promotion{ID: id, Repository: canary, Class: "docs-links", Threshold: threshold,
			Evidence: snapshot, ExceptionID: exc, RequestedBy: "Smana"})
		return err
	}
	ask("cccccccc", "docs-links")
	if err := promote("pppppppp", "cccccccc", 10); !errors.Is(err, ErrExceptionUnusable) {
		t.Fatalf("a requested exception carried a promotion: %v", err)
	}
	if _, err := s.DecideException(ctx, "cccccccc", ExceptionApproved, "Smana", "", time.Now().Add(-time.Hour)); !errors.Is(err, ErrPolicyMoved) {
		t.Fatalf("an approval expires in the future: %v", err)
	}
	e, err := s.DecideException(ctx, "cccccccc", ExceptionApproved, "Smana", "drill passed on the canary", time.Now().Add(14*24*time.Hour))
	if err != nil || e.State != ExceptionApproved || e.DecidedBy != "Smana" || e.ExpiresAt == nil {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := s.DecideException(ctx, "cccccccc", ExceptionRefused, "Smana", "", time.Time{}); !errors.Is(err, ErrPolicyMoved) {
		t.Fatalf("decided twice: %v", err)
	}
	if err := promote("pppppppp", "cccccccc", 9); !errors.Is(err, ErrExceptionUnusable) {
		t.Fatalf("another minimum: %v", err)
	}
	ask("dddddddd", "revert")
	if _, err := s.DecideException(ctx, "dddddddd", ExceptionApproved, "Smana", "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := promote("pppppppp", "dddddddd", 10); !errors.Is(err, ErrExceptionUnusable) {
		t.Fatalf("another class's exception: %v", err)
	}
	if err := promote("pppppppp", "cccccccc", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestException(ctx, Exception{ID: "eeeeeeee", Repository: canary, Class: "docs-links", Minimum: 10,
		Justification: "short", RequestedBy: "Smana"}); err == nil {
		t.Fatal("a justification is written, not a word")
	}
}

// The broker records and moves, never rewrites what was asked or on what evidence.
func TestBrokerCannotRewriteAPromotion(t *testing.T) {
	ctx := t.Context()
	s, broker, _, _ := open(t)
	if _, err := s.RecordPromotion(ctx, Promotion{ID: "aaaaaaaa", Repository: canary, Class: "docs-links", Threshold: 20,
		Evidence: snapshot, RequestedBy: "Smana"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE policy_promotions SET evidence = '{}' WHERE promotion_id = 'aaaaaaaa'`,
		`UPDATE policy_promotions SET threshold = 1 WHERE promotion_id = 'aaaaaaaa'`,
		`UPDATE operator_exceptions SET justification = 'x' WHERE true`,
		`DELETE FROM policy_promotions`,
	} {
		if err := run(ctx, broker, q); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: %v", q, err)
		}
	}
}
