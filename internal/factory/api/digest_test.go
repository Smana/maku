// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/upkeep"
)

func digestTask(name, repo, routine, phase, reason, outcome string, prNum int, costMicro int64, at time.Time, ann map[string]string) *factoryv1.Task {
	t := &factoryv1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "agent-system",
			CreationTimestamp: metav1.NewTime(at.Add(-10 * time.Minute)),
			Annotations:       ann,
		},
		Spec: factoryv1.TaskSpec{
			Repository: repo,
			Text:       "UNTRUSTED_TEXT_" + name,
			Source:     factoryv1.Source{Kind: factoryv1.SourceKindRoutine, Ref: routine},
			Routine:    &factoryv1.TaskRoutine{Name: routine, Mode: config.RoutineModePR},
		},
		Status: factoryv1.TaskStatus{
			Phase:   phase,
			Reason:  reason,
			Outcome: outcome,
			EndedAt: &metav1.Time{Time: at},
			Usage:   factoryv1.Usage{CostMicroUSD: costMicro},
		},
	}
	if prNum > 0 {
		t.Status.PullRequest = &factoryv1.PullRequestRef{
			Number: prNum,
			URL:    "https://github.com/" + repo + "/pull/1",
		}
	}
	return t
}

func digestTestServer(t *testing.T, now time.Time, objs ...client.Object) (*Server, *upkeep.StateStore) {
	t.Helper()
	s, st, _ := routineTestServer(t, now, objs...)
	auth := s.Auth.(byToken)
	auth["nosponsor"] = authn.Principal{Kind: envelope.ActorSystem, ID: "system:unknown-app", Sponsored: true, Sub: "1"}
	cfg := s.Cfg.(*config.Config)
	cfg.API.Sponsored = []config.Sponsored{
		{Name: "slack-app", Sponsor: "Alice", Repositories: []string{coordPrimary}},
	}
	return s, st
}

func TestDigestAuthAndFiltering(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s, _ := digestTestServer(t, now)
	h := s.Handler()

	for name, tc := range map[string]struct {
		token, path string
		code        int
		reason      string
	}{
		"unauthenticated":             {"", "/v1/routines/digest", http.StatusUnauthorized, "unauthenticated"},
		"machine forbidden":           {"machine", "/v1/routines/digest", http.StatusForbidden, "human_only"},
		"internal forbidden":          {"internal", "/v1/routines/digest", http.StatusForbidden, "human_only"},
		"unknown sponsored forbidden": {"nosponsor", "/v1/routines/digest", http.StatusForbidden, "human_only"},
		"missing group forbidden":     {"nogroup", "/v1/routines/digest", http.StatusForbidden, "not_permitted"},
		"bad window 0d":               {"maint", "/v1/routines/digest?window=0d", http.StatusBadRequest, "bad_window"},
		"bad window 31d":              {"maint", "/v1/routines/digest?window=31d", http.StatusBadRequest, "bad_window"},
		"bad window negative":         {"maint", "/v1/routines/digest?window=-7d", http.StatusBadRequest, "bad_window"},
		"bad window leading zero":     {"maint", "/v1/routines/digest?window=07d", http.StatusBadRequest, "bad_window"},
		"bad window non-days":         {"maint", "/v1/routines/digest?window=7h", http.StatusBadRequest, "bad_window"},
		"bad window text":             {"maint", "/v1/routines/digest?window=week", http.StatusBadRequest, "bad_window"},
		"bad repository traversal":    {"maint", "/v1/routines/digest?repository=../evil", http.StatusBadRequest, "bad_repository"},
		"bad repositories list":       {"maint", "/v1/routines/digest?repositories=" + coordPrimary + ",bad", http.StatusBadRequest, "bad_repository"},
		"human unreadable repo 404":   {"maint", "/v1/routines/digest?repository=" + coordOther, http.StatusNotFound, "not_found"},
		"human unknown repo 404":      {"maint", "/v1/routines/digest?repository=Smana/missing", http.StatusNotFound, "not_found"},
		"human flaky lookup 503":      {"flaky", "/v1/routines/digest?repository=" + coordPrimary, http.StatusServiceUnavailable, "link_unverified"},
		"sponsored out-of-scope 404":  {"slack", "/v1/routines/digest?repositories=" + coordOther, http.StatusNotFound, "not_found"},
		"sponsored mixed scope 404":   {"slack", "/v1/routines/digest?repositories=" + coordPrimary + "," + coordOther, http.StatusNotFound, "not_found"},
	} {
		t.Run(name, func(t *testing.T) {
			w := doRoutineReq(h, http.MethodGet, tc.token, tc.path, "")
			if w.Code != tc.code || !strings.Contains(w.Body.String(), `"`+tc.reason+`"`) {
				t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), tc.code, tc.reason)
			}
		})
	}

	// Human reader sees only readable repo.
	w := doRoutineReq(h, http.MethodGet, "maint", "/v1/routines/digest", "")
	if w.Code != http.StatusOK {
		t.Fatalf("maint status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var d RoutineDigest
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Window != "7d" || len(d.Repositories) != 1 || d.Repositories[0].Repository != coordPrimary {
		t.Fatalf("maint digest = %+v", d)
	}

	// Outsider sees only coordOther.
	w = doRoutineReq(h, http.MethodGet, "outsider", "/v1/routines/digest", "")
	if w.Code != http.StatusOK {
		t.Fatalf("outsider status=%d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Repositories) != 1 || d.Repositories[0].Repository != coordOther {
		t.Fatalf("outsider digest = %+v", d)
	}

	// Sponsored caller sees strictly its configured repositories, and case-insensitive filter works.
	w = doRoutineReq(h, http.MethodGet, "slack", "/v1/routines/digest?repositories=smana/public-contracts", "")
	if w.Code != http.StatusOK {
		t.Fatalf("slack status=%d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Repositories) != 1 || d.Repositories[0].Repository != coordPrimary {
		t.Fatalf("slack digest = %+v", d)
	}
}

func TestDigestAggregationAndMetrics(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	tasks := []client.Object{
		// 2 merged PRs (total cost $1.00 + $0.50 = $1.50).
		digestTask("t-merged-1", coordPrimary, "dead-links", factoryv1.PhaseDone, "merged", "", 11, 1_000_000, now.Add(-24*time.Hour), nil),
		digestTask("t-merged-2", coordPrimary, "dead-links", factoryv1.PhaseDone, "merged", "accepted", 12, 500_000, now.Add(-48*time.Hour), nil),
		// 1 pending propose task -> NOT merged, NOT closed, still counts in Runs + CostUSD.
		digestTask("t-prop-pending", coordPrimary, "dead-links", factoryv1.PhaseDone, "proposal_ready", "", 0, 200_000, now.Add(-12*time.Hour), nil),
		// 1 PhaseDone with Outcome=failed -> counts as Closed.
		digestTask("t-done-fail", coordPrimary, "dead-links", factoryv1.PhaseDone, "failed", "failed", 13, 300_000, now.Add(-10*time.Hour), nil),
		// 1 NoOp -> counts as NoOps.
		digestTask("t-noop", coordPrimary, "dead-links", factoryv1.PhaseNoOp, "no_op", "", 0, 100_000, now.Add(-6*time.Hour), nil),
		// 1 PhaseClosed stale -> counts as Stale + PRsOpened.
		digestTask("t-stale", coordPrimary, "dead-links", factoryv1.PhaseClosed, "stale", "", 14, 400_000, now.Add(-5*time.Hour), nil),
		// 1 PhaseClosed non-stale -> counts as Closed + PRsOpened.
		digestTask("t-closed", coordPrimary, "dead-links", factoryv1.PhaseClosed, "closed_unmerged", "", 15, 250_000, now.Add(-4*time.Hour), nil),
		// 1 PhaseReverted (non-drill) -> counts as Closed + PRsOpened.
		digestTask("t-reverted", coordPrimary, "dead-links", factoryv1.PhaseReverted, "reverted", "", 16, 250_000, now.Add(-3*time.Hour), nil),
		// 1 PhaseReverted (drill) -> excluded from Closed, still counts Runs + PRsOpened + CostUSD.
		digestTask("t-drill", coordPrimary, "dead-links", factoryv1.PhaseReverted, "reverted", "", 17, 100_000, now.Add(-2*time.Hour), map[string]string{factoryv1.AnnotationDrill: "true"}),
		// 1 PhaseEscalated -> counts as Escalated.
		digestTask("t-esc", coordPrimary, "dead-links", factoryv1.PhaseEscalated, "gate_failed", "", 0, 400_000, now.Add(-1*time.Hour), nil),
		// 1 old task outside 7d (at -10d), inside 30d.
		digestTask("t-old", coordPrimary, "dead-links", factoryv1.PhaseDone, "merged", "", 18, 500_000, now.Add(-10*24*time.Hour), nil),
		// 1 task on old-job (zero merges) to test nil MergeRate / nil USDPerMerged.
		digestTask("t-oldjob-noop", coordPrimary, "old-job", factoryv1.PhaseNoOp, "no_op", "", 0, 100_000, now.Add(-2*time.Hour), nil),
	}
	s, st := digestTestServer(t, now, tasks...)

	// Put breaker-paused state on dead-links and manual-paused state on old-job.
	if err := st.Mutate(t.Context(), coordPrimary, "dead-links", func(rs *upkeep.RoutineState, _ time.Time) bool {
		rs.BreakerState = upkeep.BreakerPaused
		rs.BreakerReason = "low_merge_rate"
		rs.PausedReason = "SECRET_MANUAL_NOTE"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPaused(t.Context(), coordPrimary, "old-job", true, "MANUAL_PROSE_SHOULD_NOT_LEAK", "alice"); err != nil {
		t.Fatal(err)
	}

	h := s.Handler()
	w := doRoutineReq(h, http.MethodGet, "slack", "/v1/routines/digest", "")
	if w.Code != http.StatusOK {
		t.Fatalf("digest 7d status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, forbidden := range []string{"UNTRUSTED_TEXT_", "SECRET_MANUAL_NOTE", "MANUAL_PROSE_SHOULD_NOT_LEAK"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("digest leaked untrusted text %q: %s", forbidden, body)
		}
	}

	var d RoutineDigest
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Window != "7d" || !d.GeneratedAt.Equal(now) || len(d.Repositories) != 1 {
		t.Fatalf("digest header = %+v", d)
	}
	routines := d.Repositories[0].Routines
	if len(routines) != 2 {
		t.Fatalf("routines len = %d: %+v", len(routines), routines)
	}

	// Sorted order: dead-links, old-job.
	dl := routines[0]
	if dl.Name != "dead-links" {
		t.Fatalf("first routine = %q", dl.Name)
	}
	// 10 tasks in 7d: 2 merged, 1 pending propose, 1 done-failed (closed), 1 noop, 1 stale,
	// 1 closed, 1 reverted (closed), 1 drill-reverted, 1 escalated.
	// PRsOpened = 7 (11,12,13,14,15,16,17).
	// Closed = 3 (t-done-fail, t-closed, t-reverted).
	// Stale = 1 (t-stale).
	// Escalated = 1 (t-esc).
	// CostUSD = 1.0+0.5+0.2+0.3+0.1+0.4+0.25+0.25+0.1+0.4 = 3.50.
	if dl.Runs != 10 || dl.NoOps != 1 || dl.PRsOpened != 7 || dl.Merged != 2 || dl.Closed != 3 || dl.Stale != 1 || dl.Escalated != 1 {
		t.Fatalf("dead-links counts = %+v", dl)
	}
	if math.Abs(dl.CostUSD-3.50) > 1e-9 {
		t.Fatalf("CostUSD = %v, want 3.5", dl.CostUSD)
	}
	// MergeRate = 2 / (2 + 3 + 1) = 1/3.
	if dl.MergeRate == nil || math.Abs(*dl.MergeRate-1.0/3.0) > 1e-9 {
		t.Fatalf("MergeRate = %v, want 1/3", dl.MergeRate)
	}
	// USDPerMerged = 3.50 / 2 = 1.75.
	if dl.USDPerMerged == nil || math.Abs(*dl.USDPerMerged-1.75) > 1e-9 {
		t.Fatalf("USDPerMerged = %v, want 1.75", dl.USDPerMerged)
	}
	if !dl.Paused || dl.BreakerReason != "low_merge_rate" {
		t.Fatalf("paused=%t breakerReason=%q", dl.Paused, dl.BreakerReason)
	}

	oj := routines[1]
	if oj.Name != "old-job" || oj.Runs != 1 || oj.NoOps != 1 || oj.Merged != 0 {
		t.Fatalf("old-job = %+v", oj)
	}
	if oj.MergeRate != nil || oj.USDPerMerged != nil {
		t.Fatalf("expected nil MergeRate & USDPerMerged, got %v %v", oj.MergeRate, oj.USDPerMerged)
	}
	if !oj.Paused || oj.BreakerReason != "" {
		t.Fatalf("old-job paused=%t breakerReason=%q", oj.Paused, oj.BreakerReason)
	}

	// 30d window includes t-old (11 runs, 3 merged, $4.00).
	w30 := doRoutineReq(h, http.MethodGet, "slack", "/v1/routines/digest?window=30d", "")
	if w30.Code != http.StatusOK {
		t.Fatalf("30d status=%d body=%s", w30.Code, w30.Body.String())
	}
	var d30 RoutineDigest
	if err := json.Unmarshal(w30.Body.Bytes(), &d30); err != nil {
		t.Fatal(err)
	}
	dl30 := d30.Repositories[0].Routines[0]
	if d30.Window != "30d" || dl30.Runs != 11 || dl30.Merged != 3 || math.Abs(dl30.CostUSD-4.0) > 1e-9 {
		t.Fatalf("30d dead-links = %+v", dl30)
	}
}
