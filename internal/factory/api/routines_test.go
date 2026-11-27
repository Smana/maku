// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/fmetrics"
	"github.com/Smana/maku/internal/factory/upkeep"
	"github.com/Smana/maku/internal/policy"
)

type fakeRoutineTrigger struct {
	calls []routineTriggerCall
	task  *factoryv1.Task
	err   error
}

type routineTriggerCall struct {
	repo, name, mode, login, key string
}

func (f *fakeRoutineTrigger) TriggerManual(_ context.Context, repo, name, modeOverride, login, idempotencyKey string) (*factoryv1.Task, error) {
	f.calls = append(f.calls, routineTriggerCall{repo: repo, name: name, mode: modeOverride, login: login, key: idempotencyKey})
	if f.err != nil {
		return nil, f.err
	}
	if f.task != nil {
		return f.task, nil
	}
	return &factoryv1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "aaaaaaaa", Namespace: "agent-system"},
		Spec: factoryv1.TaskSpec{
			Repository: repo,
			Source:     factoryv1.Source{Kind: factoryv1.SourceKindRoutine, Ref: name, RequestedBy: factoryv1.Person(login)},
			Routine:    &factoryv1.TaskRoutine{Name: name, Mode: config.RoutineModePR},
		},
		Status: factoryv1.TaskStatus{RoomRef: "aaaaaaaa"},
	}, nil
}

type routineReadWriteAccess struct {
	reads  map[string]bool
	writes map[string]bool
	errFor map[string]bool
}

func (a routineReadWriteAccess) CanRead(_ context.Context, p authn.Principal, repository string) (bool, error) {
	if a.errFor[p.ID] {
		return false, errors.New("github unavailable")
	}
	return a.reads[p.ID+"|"+repository], nil
}

func (a routineReadWriteAccess) CanWrite(_ context.Context, p authn.Principal, repository string) (bool, error) {
	if a.errFor[p.ID] {
		return false, errors.New("github unavailable")
	}
	return a.writes[p.ID+"|"+repository], nil
}

func (a routineReadWriteAccess) CanWriteLogin(_ context.Context, repository, login string) (bool, error) {
	if a.errFor[strings.ToLower(login)] {
		return false, errors.New("github unavailable")
	}
	return a.writes[strings.ToLower(login)+"|"+repository], nil
}

func routineScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = factoryv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	return s
}

func routineTestServer(t *testing.T, now time.Time, objs ...client.Object) (*Server, *upkeep.StateStore, *fakeRoutineTrigger) {
	t.Helper()
	cl := fake.NewClientBuilder().WithScheme(routineScheme()).WithObjects(objs...).Build()
	st := &upkeep.StateStore{Client: cl, Reader: cl, Namespace: "agent-system", Now: func() time.Time { return now }}
	trig := &fakeRoutineTrigger{}
	s := &Server{
		Auth: byToken{
			"maint":    authn.Principal{Kind: envelope.ActorHuman, ID: "human:1", Sub: "1", Groups: []string{"agents-member"}},
			"nonmaint": authn.Principal{Kind: envelope.ActorHuman, ID: "human:2", Sub: "2", Groups: []string{"agents-member"}},
			"unlinked": authn.Principal{Kind: envelope.ActorHuman, ID: "human:3", Sub: "3", Groups: []string{"agents-member"}},
			"nowrite":  authn.Principal{Kind: envelope.ActorHuman, ID: "human:4", Sub: "4", Groups: []string{"agents-member"}},
			"nogroup":  authn.Principal{Kind: envelope.ActorHuman, ID: "human:5", Sub: "5"},
			"outsider": authn.Principal{Kind: envelope.ActorHuman, ID: "human:6", Sub: "6", Groups: []string{"agents-member"}},
			"flaky":    authn.Principal{Kind: envelope.ActorHuman, ID: "human:9", Sub: "9", Groups: []string{"agents-member"}},
			"machine":  authn.Principal{Kind: envelope.ActorSystem, ID: "system:broker", Sub: "sa"},
			"slack":    authn.Principal{Kind: envelope.ActorSystem, ID: "system:slack-app", Sponsored: true, Sub: "1"},
			"internal": authn.Principal{Kind: envelope.ActorSystem, ID: "system:slack-app", Sponsored: true, Internal: true, Sub: "1"},
		},
		Groups: policy.Groups{Member: "agents-member", Admin: "agents-admin"},
		Cfg: &config.Config{
			Repositories: []config.Repository{
				{
					Name:        coordPrimary,
					Maintainers: []string{"Alice", "Bob"},
					Routines: []config.RoutineBinding{
						{Name: "dead-links", Mode: config.RoutineModePR, Cron: "0 3 * * *"},
						{Name: "old-job", Mode: config.RoutineModeDisabled, Paused: true, Cron: "0 4 * * *"},
					},
					ResolvedRoutines: map[string]config.ResolvedRoutine{
						"dead-links": {
							Name:          "dead-links",
							Description:   "Fix broken markdown links",
							Mode:          config.RoutineModePR,
							Cron:          "0 3 * * *",
							Workflow:      "docs",
							DefaultPrompt: "Fix dead links.",
							Probe:         config.ProbeSpec{Kind: config.ProbeDeadLinks, MaxFindings: 2},
							MaxOpenPRs:    2,
							MaxCostUSD:    3.5,
							Breaker:       config.ResolvedBreaker{Window: 10},
						},
					},
				},
				{
					Name:        coordOther,
					Maintainers: []string{"other"},
					ResolvedRoutines: map[string]config.ResolvedRoutine{
						"doc-drift": {
							Name:          "doc-drift",
							Mode:          config.RoutineModePR,
							Cron:          "0 5 * * *",
							DefaultPrompt: "Fix doc drift.",
							Probe:         config.ProbeSpec{Kind: config.ProbeActionsFindings, MaxFindings: 1},
							MaxOpenPRs:    1,
							MaxCostUSD:    2.0,
							Breaker:       config.ResolvedBreaker{Window: 10},
						},
					},
				},
			},
		},
		Access: routineReadWriteAccess{
			reads: map[string]bool{
				"human:1|" + coordPrimary: true,
				"human:2|" + coordPrimary: true,
				"human:3|" + coordPrimary: true,
				"human:4|" + coordPrimary: true,
				"human:5|" + coordPrimary: true,
				"human:1|" + coordOther:   false,
				"human:6|" + coordOther:   true,
			},
			writes: map[string]bool{
				"alice|" + coordPrimary: true,
				"bob|" + coordPrimary:   false,
			},
			errFor: map[string]bool{"human:9": true},
		},
		Logins: coordLogins{
			"1": "Alice",
			"2": "charlie",
			"4": "Bob",
			"6": "other",
		},
		Rooms:          cl,
		Tasks:          cl,
		Namespace:      "agent-system",
		RoutineState:   st,
		RoutineTrigger: trig,
		Now:            func() time.Time { return now },
	}
	return s, st, trig
}

func doRoutineReq(h http.Handler, method, token, target, body string, headers ...string) *httptest.ResponseRecorder {
	var r *bytes.Reader
	if body != "" {
		r = bytes.NewReader([]byte(body))
	} else {
		r = bytes.NewReader(nil)
	}
	req := httptest.NewRequestWithContext(context.Background(), method, target, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestRoutinesAuthAndReadFiltering(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s, _, _ := routineTestServer(t, now)
	h := s.Handler()

	for name, tc := range map[string]struct {
		token, path string
		code        int
		reason      string
	}{
		"unauthenticated list":       {"", "/v1/routines", http.StatusUnauthorized, "unauthenticated"},
		"machine forbidden":          {"machine", "/v1/routines", http.StatusForbidden, "human_only"},
		"sponsored forbidden":        {"slack", "/v1/routines", http.StatusForbidden, "human_only"},
		"internal forbidden":         {"internal", "/v1/routines", http.StatusForbidden, "human_only"},
		"missing group forbidden":    {"nogroup", "/v1/routines", http.StatusForbidden, "not_permitted"},
		"detail unreadable repo 404": {"outsider", "/v1/routines/" + coordPrimary + "/dead-links", http.StatusNotFound, "not_found"},
		"detail unknown repo 404":    {"maint", "/v1/routines/Smana/missing/dead-links", http.StatusNotFound, "not_found"},
		"detail unknown routine 404": {"maint", "/v1/routines/" + coordPrimary + "/missing", http.StatusNotFound, "not_found"},
		"detail flaky lookup 503":    {"flaky", "/v1/routines/" + coordPrimary + "/dead-links", http.StatusServiceUnavailable, "link_unverified"},
		"list query unreadable 404":  {"outsider", "/v1/routines?repository=" + coordPrimary, http.StatusNotFound, "not_found"},
		"list query repo alias 404":  {"outsider", "/v1/routines?repo=" + coordPrimary, http.StatusNotFound, "not_found"},
		"list query flaky 503":       {"flaky", "/v1/routines?repo=" + coordPrimary, http.StatusServiceUnavailable, "link_unverified"},
	} {
		t.Run(name, func(t *testing.T) {
			w := doRoutineReq(h, http.MethodGet, tc.token, tc.path, "")
			if w.Code != tc.code || !strings.Contains(w.Body.String(), `"`+tc.reason+`"`) {
				t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), tc.code, tc.reason)
			}
		})
	}

	// List as maint sees only coordPrimary (dead-links + old-job), never coordOther.
	w := doRoutineReq(h, http.MethodGet, "maint", "/v1/routines", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var listResp struct {
		Routines []RoutineOverview `json:"routines"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Routines) != 2 {
		t.Fatalf("got %d routines (%+v), want 2", len(listResp.Routines), listResp.Routines)
	}
	for _, rt := range listResp.Routines {
		if rt.Repository != coordPrimary {
			t.Fatalf("leaked unreadable repo %q", rt.Repository)
		}
		if !rt.CanWrite {
			t.Fatalf("expected CanWrite=true for maintainer Alice on %s", rt.Routine)
		}
	}

	// Flaky list (no ?repo filter) fails closed to empty list.
	w = doRoutineReq(h, http.MethodGet, "flaky", "/v1/routines", "")
	if w.Code != http.StatusOK {
		t.Fatalf("flaky list status=%d body=%s", w.Code, w.Body.String())
	}
	listResp.Routines = nil
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Routines) != 0 {
		t.Fatalf("flaky list leaked %+v", listResp.Routines)
	}

	// Non-maintainer reader sees CanWrite=false.
	w = doRoutineReq(h, http.MethodGet, "nonmaint", "/v1/routines?repo="+coordPrimary, "")
	if w.Code != http.StatusOK {
		t.Fatalf("nonmaint list status=%d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil || len(listResp.Routines) != 2 {
		t.Fatalf("nonmaint list err=%v resp=%+v", err, listResp)
	}
	if listResp.Routines[0].CanWrite {
		t.Fatal("nonmaint should have CanWrite=false")
	}
}

func TestRoutinesSparklineMetricsAndDetail(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	day := func(daysAgo int) *metav1.Time {
		t := metav1.NewTime(now.AddDate(0, 0, -daysAgo))
		return &t
	}
	mkTask := func(name string, daysAgo int, phase, outcome string, microUSD int64, prNum int) *factoryv1.Task {
		ended := day(daysAgo)
		if !factoryv1.TerminalPhase(phase) && phase != factoryv1.PhaseEscalated {
			ended = nil
		}
		var pr *factoryv1.PullRequestRef
		if prNum > 0 {
			pr = &factoryv1.PullRequestRef{Number: prNum, URL: fmt.Sprintf("https://github.com/%s/pull/%d", coordPrimary, prNum)}
		}
		return &factoryv1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Namespace:         "agent-system",
				CreationTimestamp: *day(daysAgo),
			},
			Spec: factoryv1.TaskSpec{
				Repository: coordPrimary,
				Source: factoryv1.Source{
					Kind:        factoryv1.SourceKindRoutine,
					Ref:         "dead-links",
					RequestedBy: "system:scheduler",
				},
				Routine: &factoryv1.TaskRoutine{
					Name:         "dead-links",
					Mode:         config.RoutineModePR,
					PromptSHA:    "abc123",
					PromptSource: "routine",
				},
			},
			Status: factoryv1.TaskStatus{
				Phase:       phase,
				Outcome:     outcome,
				EndedAt:     ended,
				PhaseSince:  day(daysAgo),
				RoomRef:     name,
				PullRequest: pr,
				Usage:       factoryv1.Usage{CostMicroUSD: microUSD},
			},
		}
	}

	objs := []client.Object{
		mkTask("t-open", 0, factoryv1.PhaseImplementing, "", 200_000, 0),
		mkTask("t-merged", 0, factoryv1.PhaseDone, "accepted", 1_000_000, 101),
		mkTask("t-noop", 1, factoryv1.PhaseNoOp, "no_op", 100_000, 0),
		mkTask("t-done-failed", 2, factoryv1.PhaseDone, "failed", 300_000, 0),
		// PhaseReverted keeps Status.Outcome="accepted" from first terminal transition: Phase must win.
		mkTask("t-reverted", 3, factoryv1.PhaseReverted, "accepted", 400_000, 102),
		mkTask("t-closed", 5, factoryv1.PhaseClosed, "closed", 500_000, 103),
		// Outside 14-day sparkline, still in rolling/USD totals.
		mkTask("t-old-merged", 20, factoryv1.PhaseDone, "merged_human", 500_000, 99),
	}

	s, st, _ := routineTestServer(t, now, objs...)
	if err := st.RecordSkip(t.Context(), coordPrimary, "dead-links", "slot-1", "open_cap", now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordSkip(t.Context(), coordPrimary, "dead-links", "slot-2", "upkeep_budget", now.Add(-1*time.Hour)); err != nil {
		t.Fatal(err)
	}

	h := s.Handler()
	w := doRoutineReq(h, http.MethodGet, "maint", "/v1/routines/"+coordPrimary+"/dead-links", "")
	if w.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", w.Code, w.Body.String())
	}
	var detail RoutineDetail
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.OpenCount != 1 {
		t.Fatalf("openCount = %d, want 1", detail.OpenCount)
	}
	if detail.NextRunAt == nil || !detail.NextRunAt.Equal(time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("nextRunAt = %v, want 2026-11-27T03:00:00Z", detail.NextRunAt)
	}
	if len(detail.Sparkline) != 14 {
		t.Fatalf("sparkline len = %d, want 14", len(detail.Sparkline))
	}
	if detail.Sparkline[0].Date != "2026-11-13" || detail.Sparkline[13].Date != "2026-11-26" {
		t.Fatalf("sparkline range = %s..%s", detail.Sparkline[0].Date, detail.Sparkline[13].Date)
	}
	if detail.Sparkline[13].Merged != 1 || detail.Sparkline[12].NoOp != 1 || detail.Sparkline[11].Escalated != 1 ||
		detail.Sparkline[10].Closed != 1 || detail.Sparkline[8].Closed != 1 {
		t.Fatalf("sparkline buckets mismatch: %+v", detail.Sparkline)
	}
	if detail.RollingMerged != 2 || detail.RollingClosed != 2 || detail.RollingNoOp != 1 {
		t.Fatalf("rolling counts = (%d, %d, %d), want (2, 2, 1)", detail.RollingMerged, detail.RollingClosed, detail.RollingNoOp)
	}
	if detail.MergeRate == nil || *detail.MergeRate != 0.5 {
		t.Fatalf("mergeRate = %v, want 0.5", detail.MergeRate)
	}
	// Total cost: (0.2 + 1.0 + 0.1 + 0.3 + 0.4 + 0.5 + 0.5) = 3.0 across 2 merged -> 1.5
	if detail.USDPerMerged == nil || *detail.USDPerMerged != 1.5 {
		t.Fatalf("usdPerMerged = %v, want 1.5", detail.USDPerMerged)
	}
	sum := sha256.Sum256([]byte("Fix dead links."))
	if detail.PromptSHA != hex.EncodeToString(sum[:]) {
		t.Fatalf("promptSHA = %q, want %q", detail.PromptSHA, hex.EncodeToString(sum[:]))
	}
	if detail.PromptEditURL != "https://github.com/Smana/public-contracts/edit/main/.github/agent-factory/upkeep/dead-links.md" {
		t.Fatalf("promptEditURL = %q", detail.PromptEditURL)
	}
	if detail.DefaultPrompt != "Fix dead links." {
		t.Fatalf("defaultPrompt = %q", detail.DefaultPrompt)
	}
	if detail.BreakerState != "ok" {
		t.Fatalf("breakerState = %q", detail.BreakerState)
	}
	if len(detail.Runs) != 7 {
		t.Fatalf("runs len = %d, want 7", len(detail.Runs))
	}
	// Verify reverted outcome overrides frozen "accepted".
	var foundReverted bool
	for _, r := range detail.Runs {
		if r.Name == "t-reverted" {
			foundReverted = true
			if r.Outcome != "reverted" {
				t.Fatalf("t-reverted outcome = %q, want reverted", r.Outcome)
			}
		}
	}
	if !foundReverted {
		t.Fatal("t-reverted missing from runs")
	}
	// Skips must be newest-first.
	if len(detail.Skips) != 2 || detail.Skips[0].Reason != "upkeep_budget" || detail.Skips[1].Reason != "open_cap" {
		t.Fatalf("skips order = %+v", detail.Skips)
	}
}

func TestRoutinesDetailCapsRunsAt50(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	objs := make([]client.Object, 0, 55)
	for i := range 55 {
		ts := metav1.NewTime(now.Add(-time.Duration(i) * time.Minute))
		objs = append(objs, &factoryv1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("t-%02d", i), Namespace: "agent-system", CreationTimestamp: ts},
			Spec: factoryv1.TaskSpec{
				Repository: coordPrimary,
				Source:     factoryv1.Source{Kind: factoryv1.SourceKindRoutine, Ref: "dead-links"},
				Routine:    &factoryv1.TaskRoutine{Name: "dead-links", Mode: config.RoutineModePR},
			},
			Status: factoryv1.TaskStatus{Phase: factoryv1.PhaseNoOp, EndedAt: &ts},
		})
	}
	s, _, _ := routineTestServer(t, now, objs...)
	w := doRoutineReq(s.Handler(), http.MethodGet, "maint", "/v1/routines/"+coordPrimary+"/dead-links", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var detail RoutineDetail
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Runs) != 50 {
		t.Fatalf("runs len = %d, want 50", len(detail.Runs))
	}
	if detail.Runs[0].Name != "t-00" || detail.Runs[49].Name != "t-49" {
		t.Fatalf("runs ordering = %s .. %s", detail.Runs[0].Name, detail.Runs[49].Name)
	}
}

func TestRoutinesManualTriggerAndSentinelMapping(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s, _, trig := routineTestServer(t, now)
	s.PrivateRefused = func(repo string) bool { return strings.EqualFold(repo, coordPrimary) && false }
	h := s.Handler()
	target := "/v1/routines/" + coordPrimary + "/dead-links/runs"

	// Auth / permission refusals.
	for name, tc := range map[string]struct {
		token, body string
		headers     []string
		code        int
		reason      string
	}{
		"unauthenticated":      {"", `{}`, nil, http.StatusUnauthorized, "unauthenticated"},
		"unlinked":             {"unlinked", `{}`, nil, http.StatusForbidden, "not_linked"},
		"non-maintainer":       {"nonmaint", `{}`, nil, http.StatusForbidden, "not_a_maintainer"},
		"no github write":      {"nowrite", `{}`, nil, http.StatusForbidden, "repository_permission"},
		"unknown json field":   {"maint", `{"extra":1}`, nil, http.StatusBadRequest, "bad_json"},
		"trailing json":        {"maint", `{}{}`, nil, http.StatusBadRequest, "bad_json"},
		"conflicting idem key": {"maint", `{"idempotencyKey":"b"}`, []string{"Idempotency-Key", "a"}, http.StatusBadRequest, "bad_idempotency_key"},
	} {
		t.Run(name, func(t *testing.T) {
			w := doRoutineReq(h, http.MethodPost, tc.token, target, tc.body, tc.headers...)
			if w.Code != tc.code || !strings.Contains(w.Body.String(), `"`+tc.reason+`"`) {
				t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), tc.code, tc.reason)
			}
		})
	}

	// Private code unacknowledged refusal.
	s.PrivateRefused = func(string) bool { return true }
	w := doRoutineReq(h, http.MethodPost, "maint", target, `{"idempotencyKey":"k1"}`)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"private_code_unacknowledged"`) {
		t.Fatalf("privateRefused got %d %s", w.Code, w.Body.String())
	}
	s.PrivateRefused = nil

	// Happy path via Idempotency-Key header.
	w = doRoutineReq(h, http.MethodPost, "maint", target, `{"mode":"pr"}`, "Idempotency-Key", "cli-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("happy status=%d body=%s", w.Code, w.Body.String())
	}
	if len(trig.calls) != 1 || trig.calls[0] != (routineTriggerCall{repo: coordPrimary, name: "dead-links", mode: "pr", login: "Alice", key: "cli-1"}) {
		t.Fatalf("trigger calls = %+v", trig.calls)
	}
	var created map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created["task"] != "aaaaaaaa" || created["room"] != "aaaaaaaa" || created["requestedBy"] != "github:alice" {
		t.Fatalf("created = %+v", created)
	}

	// All 14 sentinel mappings.
	for name, tc := range map[string]struct {
		err    error
		code   int
		reason string
	}{
		"phase2":          {ErrRoutineModePhase2, http.StatusBadRequest, "mode_requires_phase2"},
		"widening":        {ErrRoutineModeWidening, http.StatusBadRequest, "mode_widening"},
		"invalid_mode":    {ErrRoutineInvalidMode, http.StatusBadRequest, "invalid_mode"},
		"bad_idem":        {ErrRoutineBadIdempotencyKey, http.StatusBadRequest, "bad_idempotency_key"},
		"idem_conflict":   {ErrRoutineIdempotencyConflict, http.StatusConflict, "idempotency_conflict"},
		"kill_switch":     {ErrRoutineKillSwitch, http.StatusServiceUnavailable, "kill_switch"},
		"repo_not_found":  {ErrRoutineRepoNotFound, http.StatusNotFound, "not_found"},
		"not_found":       {ErrRoutineNotFound, http.StatusNotFound, "not_found"},
		"not_owned":       {ErrRoutineRepoNotOwned, http.StatusConflict, "repository_not_owned"},
		"disabled":        {ErrRoutineDisabled, http.StatusConflict, "routine_disabled"},
		"open_cap":        {ErrRoutineOpenCap, http.StatusConflict, "open_cap"},
		"budget":          {ErrRoutineBudgetExhausted, http.StatusTooManyRequests, "budget_exhausted"},
		"probe_empty":     {ErrRoutineProbeEmpty, http.StatusConflict, "probe_empty"},
		"probe_refused":   {ErrRoutineProbeRefused, http.StatusUnprocessableEntity, "probe_refused"},
		"unexpected_fail": {errors.New("boom"), http.StatusServiceUnavailable, "routines_unavailable"},
	} {
		t.Run("sentinel_"+name, func(t *testing.T) {
			trig.err = fmt.Errorf("wrapped: %w", tc.err)
			w := doRoutineReq(h, http.MethodPost, "maint", target, `{"idempotencyKey":"k"}`)
			if w.Code != tc.code || !strings.Contains(w.Body.String(), `"`+tc.reason+`"`) {
				t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), tc.code, tc.reason)
			}
		})
	}
}

func TestRoutinesPauseAndResume(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s, st, _ := routineTestServer(t, now)
	rd := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(rd))
	m, err := fmetrics.New(mp.Meter("test"), nil, "", func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	m.SetRoutines(map[string][]string{coordPrimary: {"dead-links", "old-job"}})
	s.Metrics = m
	h := s.Handler()

	pauseURL := "/v1/routines/" + coordPrimary + "/dead-links/pause"
	resumeURL := "/v1/routines/" + coordPrimary + "/dead-links/resume"

	// Non-maintainer & no-write refused.
	if w := doRoutineReq(h, http.MethodPost, "nonmaint", pauseURL, `{}`); w.Code != http.StatusForbidden {
		t.Fatalf("nonmaint pause = %d", w.Code)
	}
	if w := doRoutineReq(h, http.MethodPost, "nowrite", resumeURL, `{}`); w.Code != http.StatusForbidden {
		t.Fatalf("nowrite resume = %d", w.Code)
	}
	// Control chars in reason rejected.
	if w := doRoutineReq(h, http.MethodPost, "maint", pauseURL, `{"reason":"bad\nreason"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"bad_reason"`) {
		t.Fatalf("bad reason = %d %s", w.Code, w.Body.String())
	}

	// Pause with reason.
	w := doRoutineReq(h, http.MethodPost, "maint", pauseURL, `{"reason":"ci broken"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("pause = %d %s", w.Code, w.Body.String())
	}
	gotState, err := st.Get(t.Context(), coordPrimary, "dead-links")
	if err != nil || !gotState.Paused || gotState.PausedReason != "ci broken" || gotState.PausedBy != "github:alice" {
		t.Fatalf("state after pause = %+v err=%v", gotState, err)
	}

	// Overview reflects paused & nil NextRunAt.
	w = doRoutineReq(h, http.MethodGet, "maint", "/v1/routines/"+coordPrimary+"/dead-links", "")
	var det RoutineDetail
	if err := json.Unmarshal(w.Body.Bytes(), &det); err != nil {
		t.Fatal(err)
	}
	if !det.Paused || det.NextRunAt != nil || det.PausedReason != "ci broken" {
		t.Fatalf("detail while paused = %+v", det)
	}

	// Resume clears operational pause.
	w = doRoutineReq(h, http.MethodPost, "maint", resumeURL, `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("resume = %d %s", w.Code, w.Body.String())
	}
	gotState, err = st.Get(t.Context(), coordPrimary, "dead-links")
	if err != nil || gotState.Paused {
		t.Fatalf("state after resume = %+v err=%v", gotState, err)
	}

	// Declaratively paused routine stays paused after resume.
	w = doRoutineReq(h, http.MethodPost, "maint", "/v1/routines/"+coordPrimary+"/old-job/resume", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("declarative resume = %d %s", w.Code, w.Body.String())
	}
	var resBody map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resBody)
	if resBody["paused"] != true || resBody["declarativePaused"] != true {
		t.Fatalf("declarative resume body = %+v", resBody)
	}
}
