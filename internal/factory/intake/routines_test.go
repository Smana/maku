// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/factory/ledger"
	"github.com/Smana/maku/internal/factory/taskid"
	"github.com/Smana/maku/internal/factory/upkeep"
)

func routineScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	return s
}

type fakeSkipMetrics struct {
	calls       []skipMetricCall
	promptReads []promptReadCall
	paused      map[string]bool
}

type skipMetricCall struct {
	repo, routine, reason string
}

type promptReadCall struct {
	repo, routine, outcome string
}

func (m *fakeSkipMetrics) RoutineSkip(_ context.Context, repository, routine, reason string) {
	m.calls = append(m.calls, skipMetricCall{repo: repository, routine: routine, reason: reason})
}

func (m *fakeSkipMetrics) RoutinePromptRead(_ context.Context, repository, routine, outcome string) {
	m.promptReads = append(m.promptReads, promptReadCall{repo: repository, routine: routine, outcome: outcome})
}

func (m *fakeSkipMetrics) RoutinePaused(_ context.Context, repository, routine string, paused bool) {
	if m.paused == nil {
		m.paused = map[string]bool{}
	}
	m.paused[repository+"/"+routine] = paused
}

type errorReader struct {
	client.Reader
	err error
}

func (r errorReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return r.err
}

type failingStateStore struct {
	getErr error
}

func (f failingStateStore) Get(context.Context, string, string) (upkeep.RoutineState, error) {
	return upkeep.RoutineState{}, f.getErr
}

func (f failingStateStore) RecordSkip(context.Context, string, string, string, string, time.Time) error {
	return nil
}

func baseRoutineCfg(rr config.ResolvedRoutine, bindings ...config.RoutineBinding) *config.Config {
	upkeepUSD := 10.0
	return &config.Config{
		Defaults: config.Defaults{DataClass: "public"},
		Caps:     config.Caps{MaxTextBytes: 14336},
		Budgets: config.Budgets{
			FactoryDailyUSD: 50.0,
			HumanDailyUSD:   20.0,
			UpkeepDailyUSD:  &upkeepUSD,
			PerPerson:       config.PerPerson{Enabled: true, DailyUSD: 5.0},
		},
		Generations: config.Generations{
			Repos: map[string]config.RepoGenerations{
				"acme/app": {
					Repository: 3,
					Routines: map[string]config.RoutineGeneration{
						rr.Name: {Routine: rr.RoutineGeneration, RoutineName: rr.Name},
					},
				},
			},
		},
		Repositories: []config.Repository{{
			Name:             "acme/app",
			Maintainers:      []string{"Alice"},
			Classes:          map[string]config.Class{"dead-links": {Shadow: true, Oracle: &config.Oracle{Probe: "dead-links", Workflow: "dead-links.yaml", MaxFindings: 10}}},
			Routines:         bindings,
			ResolvedRoutines: map[string]config.ResolvedRoutine{rr.Name: rr},
		}},
	}
}

func newRoutineScheduler(t *testing.T, f *forgetest.Fake, cfg *config.Config, now time.Time, objs ...client.Object) (*Scheduler, *fakeSkipMetrics, *upkeep.StateStore) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(routineScheme()).WithObjects(objs...).Build()
	sm := &fakeSkipMetrics{}
	st := &upkeep.StateStore{Client: c, Namespace: "maku", Now: func() time.Time { return now }}
	sch := &Scheduler{
		Forge:          f,
		Merger:         f,
		Client:         c,
		APIReader:      c,
		Ledger:         c,
		State:          st,
		RoutineMetrics: sm,
		Namespace:      "maku",
		Cfg:            cfg,
		Owned:          func(string) bool { return true },
		Stopped:        func(context.Context) bool { return false },
		Now:            func() time.Time { return now },
		Nonce:          func() string { return "abcd1234" },
		Errors:         &counter{},
	}
	return sch, sm, st
}

func TestRoutineSchedulerSkipOrder(t *testing.T) {
	now := time.Date(2026, 11, 26, 6, 15, 0, 0, time.UTC)
	slot := time.Date(2026, 11, 26, 6, 0, 0, 0, time.UTC)
	baseRR := config.ResolvedRoutine{
		Name:              "dead-code",
		Mode:              config.RoutineModePR,
		Cron:              "0 6 * * *",
		DataClass:         "public",
		DefaultPrompt:     "Remove dead code.",
		Probe:             config.ProbeSpec{Kind: config.ProbeNone, MaxFindings: 1},
		MaxOpenPRs:        1,
		MaxCostUSD:        2.0,
		Cooldown:          24 * time.Hour,
		RoutineGeneration: 7,
	}

	t.Run("kill_switch precedes not_owned", func(t *testing.T) {
		cfg := baseRoutineCfg(baseRR)
		sch, sm, st := newRoutineScheduler(t, forgetest.NewFake(), cfg, now)
		sch.Stopped = func(context.Context) bool { return true }
		sch.Owned = func(string) bool { return false }
		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(sm.calls) != 1 || sm.calls[0].reason != SkipKillSwitch {
			t.Fatalf("got metric calls %+v, want %s", sm.calls, SkipKillSwitch)
		}
		rs, err := st.Get(t.Context(), "acme/app", "dead-code")
		if err != nil || len(rs.Skips) != 1 || rs.Skips[0].Reason != SkipKillSwitch {
			t.Fatalf("state skips=%+v err=%v", rs.Skips, err)
		}
	})

	t.Run("not_owned precedes disabled and paused", func(t *testing.T) {
		rr := baseRR
		rr.Paused = true
		cfg := baseRoutineCfg(rr, config.RoutineBinding{Name: "dead-code", Mode: config.RoutineModeDisabled, Cron: "0 6 * * *"})
		sch, sm, _ := newRoutineScheduler(t, forgetest.NewFake(), cfg, now)
		sch.Owned = func(string) bool { return false }
		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(sm.calls) != 1 || sm.calls[0].reason != SkipNotOwned {
			t.Fatalf("got metric calls %+v, want %s", sm.calls, SkipNotOwned)
		}
	})

	t.Run("disabled binding emits disabled skip", func(t *testing.T) {
		cfg := &config.Config{
			Defaults: config.Defaults{DataClass: "public"},
			Repositories: []config.Repository{{
				Name:     "acme/app",
				Routines: []config.RoutineBinding{{Name: "dead-code", Mode: config.RoutineModeDisabled, Cron: "0 6 * * *", Paused: true}},
			}},
		}
		sch, sm, _ := newRoutineScheduler(t, forgetest.NewFake(), cfg, now)
		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(sm.calls) != 1 || sm.calls[0].reason != SkipDisabled {
			t.Fatalf("got metric calls %+v, want %s", sm.calls, SkipDisabled)
		}
	})

	t.Run("paused_manual from binding or StateStore", func(t *testing.T) {
		cfg := baseRoutineCfg(baseRR)
		sch, sm, st := newRoutineScheduler(t, forgetest.NewFake(), cfg, now)
		if err := st.SetPaused(t.Context(), "acme/app", "dead-code", true, "operator", "github:alice"); err != nil {
			t.Fatal(err)
		}
		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(sm.calls) != 1 || sm.calls[0].reason != SkipPausedManual {
			t.Fatalf("got metric calls %+v, want %s", sm.calls, SkipPausedManual)
		}
	})

	t.Run("open_cap precedes cooldown", func(t *testing.T) {
		openTask := &v1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "aaaaaaaa", Namespace: "maku"},
			Spec: v1alpha1.TaskSpec{
				Repository: "acme/app",
				Source:     v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "dead-code"},
				Routine:    &v1alpha1.TaskRoutine{Name: "dead-code", Mode: "pr", PromptSource: "routine", PromptSHA: strings.Repeat("a", 64)},
			},
			Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseReviewing},
		}
		recentClosed := &v1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "bbbbbbbb", Namespace: "maku"},
			Spec: v1alpha1.TaskSpec{
				Repository: "acme/app",
				Source:     v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "dead-code"},
				Routine:    &v1alpha1.TaskRoutine{Name: "dead-code", Mode: "pr", PromptSource: "routine", PromptSHA: strings.Repeat("a", 64)},
			},
			Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseClosed, PhaseSince: &metav1.Time{Time: now.Add(-time.Hour)}},
		}
		cfg := baseRoutineCfg(baseRR)
		sch, sm, _ := newRoutineScheduler(t, forgetest.NewFake(), cfg, now, openTask, recentClosed)
		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(sm.calls) != 1 || sm.calls[0].reason != SkipOpenCap {
			t.Fatalf("got metric calls %+v, want %s", sm.calls, SkipOpenCap)
		}
	})

	t.Run("cooldown uses PhaseSince for PhaseReverted and ignores PhaseNoOp", func(t *testing.T) {
		// Reverted 1 hour ago, even though merged/EndedAt was 10 days ago -> cooldown holds.
		revertedTask := &v1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "cccccccc", Namespace: "maku"},
			Spec: v1alpha1.TaskSpec{
				Repository: "acme/app",
				Source:     v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "dead-code"},
				Routine:    &v1alpha1.TaskRoutine{Name: "dead-code", Mode: "pr", PromptSource: "routine", PromptSHA: strings.Repeat("a", 64)},
			},
			Status: v1alpha1.TaskStatus{
				Phase:      v1alpha1.PhaseReverted,
				EndedAt:    &metav1.Time{Time: now.Add(-10 * 24 * time.Hour)},
				PhaseSince: &metav1.Time{Time: now.Add(-time.Hour)},
			},
		}
		cfg := baseRoutineCfg(baseRR)
		sch, sm, _ := newRoutineScheduler(t, forgetest.NewFake(), cfg, now, revertedTask)
		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(sm.calls) != 1 || sm.calls[0].reason != SkipCooldown {
			t.Fatalf("reverted PhaseSince should trigger cooldown, got %+v", sm.calls)
		}

		// PhaseNoOp 1 hour ago does NOT trigger cooldown.
		noopTask := revertedTask.DeepCopy()
		noopTask.Status.Phase = v1alpha1.PhaseNoOp
		sch2, sm2, _ := newRoutineScheduler(t, forgetest.NewFake(), cfg, now, noopTask)
		if err := sch2.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(sm2.calls) != 0 {
			t.Fatalf("PhaseNoOp must not trigger cooldown, got skips %+v", sm2.calls)
		}
		var l v1alpha1.TaskList
		_ = sch2.Client.List(t.Context(), &l)
		if len(l.Items) != 2 {
			t.Fatalf("expected new routine Task created after PhaseNoOp, got %d", len(l.Items))
		}
	})

	t.Run("upkeep_budget skips when spent+reserved >= upkeepDailyUSD or cap <= 0", func(t *testing.T) {
		spentRaw, _ := json.Marshal(map[string]int64{config.PrincipalUpkeep: 8_000_000})
		resRaw, _ := json.Marshal(ledger.Reservation{Principal: config.PrincipalFactory, Person: config.PrincipalUpkeep, MaxMicroUSD: 2_000_000})
		ledgerCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: ledger.Prefix + now.UTC().Format(ledger.DayLayout), Namespace: "maku"},
			Data:       map[string]string{ledger.Spent: string(spentRaw), ledger.Reserved + "r1": string(resRaw)},
		}
		cfg := baseRoutineCfg(baseRR)
		sch, sm, _ := newRoutineScheduler(t, forgetest.NewFake(), cfg, now, ledgerCM)
		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(sm.calls) != 1 || sm.calls[0].reason != SkipUpkeepBudget {
			t.Fatalf("got metric calls %+v, want %s", sm.calls, SkipUpkeepBudget)
		}

		// Explicit 0 upkeepDailyUSD also skips with upkeep_budget.
		zero := 0.0
		cfgZero := baseRoutineCfg(baseRR)
		cfgZero.Budgets.UpkeepDailyUSD = &zero
		schZero, smZero, _ := newRoutineScheduler(t, forgetest.NewFake(), cfgZero, now)
		if err := schZero.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(smZero.calls) != 1 || smZero.calls[0].reason != SkipUpkeepBudget {
			t.Fatalf("zero upkeepDailyUSD: got %+v, want %s", smZero.calls, SkipUpkeepBudget)
		}
	})

	t.Run("already fired slot within 1h window emits zero skips on re-tick and repeats skip metrics only once per slot", func(t *testing.T) {
		cfg := baseRoutineCfg(baseRR)
		sch, sm, st := newRoutineScheduler(t, forgetest.NewFake(), cfg, now)
		// First tick creates the Task.
		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		expectedName := taskid.Name(taskid.RoutineKey("acme/app", "dead-code", slot))
		var got v1alpha1.Task
		if err := sch.Client.Get(t.Context(), client.ObjectKey{Namespace: "maku", Name: expectedName}, &got); err != nil {
			t.Fatalf("expected task %s: %v", expectedName, err)
		}
		// Second tick 1 minute later (same 1h slot window): must NOT emit open_cap skip!
		sch.Now = func() time.Time { return now.Add(time.Minute) }
		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(sm.calls) != 0 {
			t.Fatalf("re-tick of already-fired slot emitted skips: %+v", sm.calls)
		}
		rs, _ := st.Get(t.Context(), "acme/app", "dead-code")
		if len(rs.Skips) != 0 {
			t.Fatalf("re-tick of already-fired slot persisted skips: %+v", rs.Skips)
		}

		// Repeated skipped ticks in the same slot emit metric only once.
		schSkip, smSkip, _ := newRoutineScheduler(t, forgetest.NewFake(), cfg, now)
		schSkip.Stopped = func(context.Context) bool { return true }
		for i := range 3 {
			schSkip.Now = func() time.Time { return now.Add(time.Duration(i) * time.Minute) }
			if err := schSkip.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if len(smSkip.calls) != 1 {
			t.Fatalf("expected 1 metric call across 3 ticks in same slot, got %d", len(smSkip.calls))
		}

		// Advancing across multiple slots bounds skipMetricSeen to O(repos * routines) while emitting once per new slot.
		for d := 1; d <= 5; d++ {
			nextNow := now.Add(time.Duration(d) * 24 * time.Hour)
			schSkip.Now = func() time.Time { return nextNow }
			if err := schSkip.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if len(smSkip.calls) != 6 {
			t.Fatalf("expected 6 metric calls across 6 distinct slots, got %d", len(smSkip.calls))
		}
		if got := len(schSkip.skipMetricSeen); got != 1 {
			t.Fatalf("skipMetricSeen must remain bounded to 1 entry per (repo, routine), got %d", got)
		}
	})

	t.Run("transient state or ledger errors fail closed", func(t *testing.T) {
		cfg := baseRoutineCfg(baseRR)
		sch, sm, _ := newRoutineScheduler(t, forgetest.NewFake(), cfg, now)
		sch.State = failingStateStore{getErr: errors.New("apiserver down")}
		if err := sch.Tick(t.Context()); err == nil {
			t.Fatal("expected error on State.Get failure")
		}
		var l v1alpha1.TaskList
		_ = sch.Client.List(t.Context(), &l)
		if len(l.Items) != 0 || len(sm.calls) != 0 {
			t.Fatalf("expected fail-closed with no task and no skip metric, tasks=%d skips=%d", len(l.Items), len(sm.calls))
		}

		sch2, sm2, _ := newRoutineScheduler(t, forgetest.NewFake(), cfg, now)
		sch2.Ledger = errorReader{err: errors.New("ledger read failed")}
		if err := sch2.Tick(t.Context()); err == nil {
			t.Fatal("expected error on Ledger read failure")
		}
		_ = sch2.Client.List(t.Context(), &l)
		if len(l.Items) != 0 || len(sm2.calls) != 0 {
			t.Fatalf("expected fail-closed on ledger error, tasks=%d skips=%d", len(l.Items), len(sm2.calls))
		}
	})
}

func TestRoutineProbesAndTaskFields(t *testing.T) {
	now := time.Date(2026, 11, 26, 6, 15, 0, 0, time.UTC)

	t.Run("renovate-red succeeds without MergePolicy", func(t *testing.T) {
		rr := config.ResolvedRoutine{
			Name:              "renovate-red",
			Mode:              config.RoutineModePR,
			Cron:              "0 6 * * *",
			DataClass:         "public",
			DefaultPrompt:     "Fix red Renovate PRs.",
			Probe:             config.ProbeSpec{Kind: config.ProbeRenovateRed, MaxFindings: 1},
			ProbeAuthor:       "renovate[bot]",
			MaxOpenPRs:        1,
			MaxCostUSD:        2.0,
			RoutineGeneration: 4,
		}
		cfg := baseRoutineCfg(rr)
		// Clear classes so rc.NeedsMerger() is false.
		cfg.Repositories[0].Classes = nil
		f := forgetest.NewFake()
		f.SetOpenPRs(forge.PRSummary{Number: 12, Author: "renovate[bot]", Created: now.Add(-30 * time.Hour)})
		f.SetChecks(12, forge.Checks{Runs: []forge.Check{{Name: "test", State: "FAILURE"}}})

		sch, _, _ := newRoutineScheduler(t, f, cfg, now)
		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		var l v1alpha1.TaskList
		_ = sch.Client.List(t.Context(), &l)
		if len(l.Items) != 1 {
			t.Fatalf("expected 1 renovate-red task without MergePolicy, got %d", len(l.Items))
		}
		tk := l.Items[0]
		sum := sha256.Sum256([]byte(rr.DefaultPrompt))
		wantPromptSHA := upkeep.EffectivePromptSHA(hex.EncodeToString(sum[:]), "")
		if tk.Labels[v1alpha1.LabelRoutine] != "renovate-red" ||
			tk.Spec.Source.Kind != v1alpha1.SourceKindRoutine ||
			tk.Spec.Source.Ref != "renovate-red" ||
			tk.Spec.Source.RequestedBy != "system:scheduler" ||
			tk.Spec.Source.Trust != "trusted" ||
			tk.Spec.MaxMicroUSD != 2_000_000 ||
			tk.Spec.Routine == nil ||
			tk.Spec.Routine.PromptSource != "routine" ||
			tk.Spec.Routine.PromptSHA != wantPromptSHA ||
			tk.Spec.Routine.RoutineGeneration != 4 ||
			!strings.Contains(tk.Spec.Text, "Red Renovate PRs: #12") {
			t.Fatalf("unexpected task fields: %+v labels=%v", tk.Spec, tk.Labels)
		}
	})

	t.Run("dead-links probe checks oracle class and caps findings", func(t *testing.T) {
		rr := config.ResolvedRoutine{
			Name:          "dead-links",
			Mode:          config.RoutineModePR,
			Cron:          "0 6 * * *",
			DataClass:     "public",
			DefaultPrompt: "Fix dead links.",
			Probe:         config.ProbeSpec{Kind: config.ProbeDeadLinks, MaxFindings: 1},
			OracleClass:   "dead-links",
			MaxOpenPRs:    1,
			MaxCostUSD:    2.0,
		}
		cfg := baseRoutineCfg(rr)
		cfg.Repositories[0].Classes = map[string]config.Class{
			"dead-links": {
				Shadow:       true,
				AllowedFiles: []string{`^docs/.+\.md$`},
				Oracle:       &config.Oracle{Probe: "dead-links", Workflow: "dead-links.yaml", MaxFindings: 10},
			},
		}
		f := forgetest.NewFake()
		art := []byte(`[
			{"file":"docs/a.md","line":1,"url":"https://example.com/1","status":"404","archive":"https://web.archive.org/web/1/https://example.com/1"},
			{"file":"docs/a.md","line":2,"url":"https://example.com/2","status":"404","archive":"https://web.archive.org/web/1/https://example.com/2"}
		]`)
		f.SetRuns([]forge.WorkflowRun{
			{ID: 2, Event: "schedule", Branch: "main", Conclusion: "success"},
			{ID: 1, Event: "schedule", Branch: "main", Conclusion: "success"},
		}, map[int64][]byte{1: art, 2: art})
		f.SetMainFiles(map[string]string{"docs/a.md": "[1](https://example.com/1) [2](https://example.com/2)"})

		sch, _, _ := newRoutineScheduler(t, f, cfg, now)
		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		var l v1alpha1.TaskList
		_ = sch.Client.List(t.Context(), &l)
		if len(l.Items) != 1 || len(l.Items[0].Spec.Findings) != 1 || l.Items[0].Spec.PredictedClass != "dead-links" {
			t.Fatalf("dead-links routine task: %+v", l.Items)
		}
	})

	t.Run("actions-findings consecutive agreement, skipIDs, fence, and refusal", func(t *testing.T) {
		rr := config.ResolvedRoutine{
			Name:           "dead-code",
			Mode:           config.RoutineModePR,
			Cron:           "0 6 * * *",
			DataClass:      "public",
			DefaultPrompt:  "Remove dead code.",
			Probe:          config.ProbeSpec{Kind: config.ProbeActionsFindings, RequireConsecutive: 2, MaxFindings: 1},
			ProbeWorkflow:  "deadcode.yaml",
			ProbeArtifact:  "findings",
			ProbeFile:      "findings.json",
			ProtectedPaths: []string{`^\.github/`},
			MaxOpenPRs:     1,
			MaxCostUSD:     2.0,
		}
		cfg := baseRoutineCfg(rr)
		// Prior closed task within 30 days skipped finding "id-old".
		oldClosed := &v1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "zzzzzzzz", Namespace: "maku"},
			Spec: v1alpha1.TaskSpec{
				Repository: "acme/app",
				Source:     v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "dead-code"},
				Routine: &v1alpha1.TaskRoutine{
					Name: "dead-code", Mode: "pr", PromptSource: "routine", PromptSHA: strings.Repeat("a", 64),
					FindingIDs: []string{"id-old"},
				},
			},
			Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseClosed, PhaseSince: &metav1.Time{Time: now.Add(-5 * 24 * time.Hour)}},
		}

		f := forgetest.NewFake()
		r2Art := []byte(`{"v":1,"items":[{"id":"id-old","file":"pkg/a.go","summary":"Old."},{"id":"id-prot","file":".github/ci.yml","summary":"Protected."},{"id":"id-new","file":"pkg/b.go","line":10,"summary":"Unused func."}]}`)
		r1Art := []byte(`{"v":1,"items":[{"id":"id-old","file":"pkg/a.go","summary":"Old."},{"id":"id-new","file":"pkg/b.go","line":9,"summary":"Unused func."}]}`)
		f.SetRuns([]forge.WorkflowRun{
			{ID: 2, Event: "schedule", Branch: "main", Conclusion: "success"},
			{ID: 1, Event: "schedule", Branch: "main", Conclusion: "success"},
		}, map[int64][]byte{2: r2Art, 1: r1Art})

		sch, _, _ := newRoutineScheduler(t, f, cfg, now, oldClosed)
		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		var l v1alpha1.TaskList
		_ = sch.Client.List(t.Context(), &l)
		if len(l.Items) != 2 {
			t.Fatalf("expected 1 new task alongside oldClosed, got %d", len(l.Items))
		}
		var created *v1alpha1.Task
		for i := range l.Items {
			if l.Items[i].Name != "zzzzzzzz" {
				created = &l.Items[i]
			}
		}
		if created == nil || len(created.Spec.Routine.FindingIDs) != 1 || created.Spec.Routine.FindingIDs[0] != "id-new" ||
			!strings.Contains(created.Spec.Text, "FINDINGS-DATA-abcd1234") {
			t.Fatalf("unexpected actions-findings task: %+v", created)
		}

		// Tampered/malformed findings artifact emits SkipProbeRefused and creates no task.
		fBad := forgetest.NewFake()
		fBad.SetRuns([]forge.WorkflowRun{
			{ID: 2, Event: "schedule", Branch: "main", Conclusion: "success"},
			{ID: 1, Event: "schedule", Branch: "main", Conclusion: "success"},
		}, map[int64][]byte{2: []byte(`{"v":1,"items":[{"id":"bad","file":"../etc/passwd","summary":"x"}]}`), 1: r1Art})
		schBad, smBad, _ := newRoutineScheduler(t, fBad, cfg, now)
		if err := schBad.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(smBad.calls) != 1 || smBad.calls[0].reason != SkipProbeRefused {
			t.Fatalf("expected %s, got %+v", SkipProbeRefused, smBad.calls)
		}
		// Re-tick on same refused run pair still skips with probe_refused and emits no second metric.
		schBad.Now = func() time.Time { return now.Add(time.Minute) }
		if err := schBad.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(smBad.calls) != 1 {
			t.Fatalf("expected deduped 1 metric call for refused pair, got %+v", smBad.calls)
		}
	})
}

func TestTriggerManual(t *testing.T) {
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	baseRR := config.ResolvedRoutine{
		Name:              "dead-code",
		Mode:              config.RoutineModePR,
		Cron:              "0 6 * * *",
		DataClass:         "public",
		DefaultPrompt:     "Remove dead code.",
		Probe:             config.ProbeSpec{Kind: config.ProbeNone, MaxFindings: 1},
		MaxOpenPRs:        1,
		MaxCostUSD:        2.0,
		Cooldown:          24 * time.Hour,
		Paused:            true, // manual trigger must bypass paused_manual!
		RoutineGeneration: 2,
	}

	t.Run("allows propose and draft overrides, and rejects widening with ErrModeWidening", func(t *testing.T) {
		cfg := baseRoutineCfg(baseRR)
		sch, _, _ := newRoutineScheduler(t, forgetest.NewFake(), cfg, now)

		tkPropose, err := sch.TriggerManual(t.Context(), "acme/app", "dead-code", config.RoutineModePropose, "Alice", "k1")
		if err != nil || tkPropose.Spec.Routine == nil || tkPropose.Spec.Routine.Mode != config.RoutineModePropose {
			t.Fatalf("propose override: tk=%+v err=%v", tkPropose, err)
		}
		schDraft, _, _ := newRoutineScheduler(t, forgetest.NewFake(), cfg, now)
		tkDraft, err := schDraft.TriggerManual(t.Context(), "acme/app", "dead-code", config.RoutineModeDraft, "Alice", "k2")
		if err != nil || tkDraft.Spec.Routine == nil || tkDraft.Spec.Routine.Mode != config.RoutineModeDraft {
			t.Fatalf("draft override: tk=%+v err=%v", tkDraft, err)
		}
		if _, err := sch.TriggerManual(t.Context(), "acme/app", "dead-code", "bogus", "Alice", "k3"); !errors.Is(err, ErrInvalidMode) {
			t.Fatalf("unknown override: got %v, want ErrInvalidMode", err)
		}

		draftRR := baseRR
		draftRR.Mode = config.RoutineModeDraft
		schWiden, _, _ := newRoutineScheduler(t, forgetest.NewFake(), baseRoutineCfg(draftRR), now)
		if _, err := schWiden.TriggerManual(t.Context(), "acme/app", "dead-code", config.RoutineModePR, "Alice", "k4"); !errors.Is(err, ErrModeWidening) {
			t.Fatalf("widening draft->pr: got %v, want ErrModeWidening", err)
		}

		proposeRR := baseRR
		proposeRR.Mode = config.RoutineModePropose
		schWidenPropose, _, _ := newRoutineScheduler(t, forgetest.NewFake(), baseRoutineCfg(proposeRR), now)
		if _, err := schWidenPropose.TriggerManual(t.Context(), "acme/app", "dead-code", config.RoutineModeDraft, "Alice", "k5"); !errors.Is(err, ErrModeWidening) {
			t.Fatalf("widening propose->draft: got %v, want ErrModeWidening", err)
		}
	})

	t.Run("rejects bad idempotency keys, kill switch, unowned, disabled, missing", func(t *testing.T) {
		cfg := baseRoutineCfg(baseRR, config.RoutineBinding{Name: "off-routine", Mode: config.RoutineModeDisabled})
		sch, _, _ := newRoutineScheduler(t, forgetest.NewFake(), cfg, now)

		for _, badKey := range []string{"", "has space", "bad\nkey", strings.Repeat("x", 500)} {
			if _, err := sch.TriggerManual(t.Context(), "acme/app", "dead-code", "", "Alice", badKey); !errors.Is(err, ErrInvalidIdempotencyKey) {
				t.Fatalf("key %q: got %v, want ErrInvalidIdempotencyKey", badKey, err)
			}
		}
		if _, err := sch.TriggerManual(t.Context(), "acme/missing", "dead-code", "", "Alice", "k1"); !errors.Is(err, ErrRepoNotFound) {
			t.Fatalf("missing repo: %v", err)
		}
		if _, err := sch.TriggerManual(t.Context(), "acme/app", "off-routine", "", "Alice", "k1"); !errors.Is(err, ErrRoutineDisabled) {
			t.Fatalf("disabled routine: %v", err)
		}
		if _, err := sch.TriggerManual(t.Context(), "acme/app", "nope", "", "Alice", "k1"); !errors.Is(err, ErrRoutineNotFound) {
			t.Fatalf("missing routine: %v", err)
		}
		sch.Owned = func(string) bool { return false }
		if _, err := sch.TriggerManual(t.Context(), "acme/app", "dead-code", "", "Alice", "k1"); !errors.Is(err, ErrRepoNotOwned) {
			t.Fatalf("unowned: %v", err)
		}
		sch.Stopped = func(context.Context) bool { return true }
		if _, err := sch.TriggerManual(t.Context(), "acme/app", "dead-code", "", "Alice", "k1"); !errors.Is(err, ErrKillSwitch) {
			t.Fatalf("stopped: %v", err)
		}
	})

	t.Run("bypasses cooldown and paused_manual, sets github:<login>, honors idempotency even past open_cap", func(t *testing.T) {
		recentClosed := &v1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "cccccccc", Namespace: "maku"},
			Spec: v1alpha1.TaskSpec{
				Repository: "acme/app",
				Source:     v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "dead-code"},
				Routine:    &v1alpha1.TaskRoutine{Name: "dead-code", Mode: "pr", PromptSource: "routine", PromptSHA: strings.Repeat("a", 64)},
			},
			Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseClosed, PhaseSince: &metav1.Time{Time: now.Add(-time.Minute)}},
		}
		cfg := baseRoutineCfg(baseRR)
		sch, _, st := newRoutineScheduler(t, forgetest.NewFake(), cfg, now, recentClosed)
		if err := st.SetPaused(t.Context(), "acme/app", "dead-code", true, "manual", "github:alice"); err != nil {
			t.Fatal(err)
		}

		// When upkeepDailyUSD is exhausted, manual trigger with PerPerson.Enabled charges github:alice (Q15) and succeeds.
		spentRaw, _ := json.Marshal(map[string]int64{config.PrincipalUpkeep: 20_000_000})
		ledgerCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: ledger.Prefix + now.UTC().Format(ledger.DayLayout), Namespace: "maku"},
			Data:       map[string]string{ledger.Spent: string(spentRaw)},
		}
		if err := sch.Client.Create(t.Context(), ledgerCM); err != nil {
			t.Fatal(err)
		}

		tk, err := sch.TriggerManual(t.Context(), "acme/app", "dead-code", "pr", "Alice", "req-1")
		if err != nil {
			t.Fatalf("TriggerManual failed: %v", err)
		}
		if tk.Spec.Source.RequestedBy != "github:alice" || tk.Spec.Source.Key != "routine:acme/app:dead-code:manual:req-1" {
			t.Fatalf("unexpected source: %+v", tk.Spec.Source)
		}

		// Idempotent replay succeeds EVEN THOUGH open_cap (1) is now full!
		replay, err := sch.TriggerManual(t.Context(), "acme/app", "dead-code", "pr", "Alice", "req-1")
		if err != nil || replay.Name != tk.Name {
			t.Fatalf("idempotent replay failed: replay=%+v err=%v", replay, err)
		}

		// A new idempotency key fails with ErrOpenCap while req-1 is open.
		if _, err := sch.TriggerManual(t.Context(), "acme/app", "dead-code", "pr", "Alice", "req-2"); !errors.Is(err, ErrOpenCap) {
			t.Fatalf("expected ErrOpenCap, got %v", err)
		}
	})

	t.Run("AlreadyExists race conflict returns ErrIdempotencyConflict", func(t *testing.T) {
		cfg := baseRoutineCfg(baseRR)
		// Pre-seed a conflicting Task at the exact same Task name.
		key := taskid.ManualRoutineKey("acme/app", "dead-code", "race-1")
		conflict := &v1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: taskid.Name(key), Namespace: "maku"},
			Spec: v1alpha1.TaskSpec{
				Repository: "acme/other",
				Source:     v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Key: key},
				Routine:    &v1alpha1.TaskRoutine{Name: "dead-code", Mode: "pr", PromptSource: "routine", PromptSHA: strings.Repeat("a", 64)},
			},
		}
		// APIReader sees conflict while Create returns AlreadyExists.
		base := fake.NewClientBuilder().WithScheme(routineScheme()).WithObjects(conflict).Build()
		sch := &Scheduler{
			Forge:     forgetest.NewFake(),
			Merger:    forgetest.NewFake(),
			Client:    alreadyExistsOnCreateClient{Client: fake.NewClientBuilder().WithScheme(routineScheme()).Build(), name: conflict.Name},
			APIReader: base,
			Namespace: "maku",
			Cfg:       cfg,
			Owned:     func(string) bool { return true },
			Stopped:   func(context.Context) bool { return false },
			Now:       func() time.Time { return now },
		}
		if _, err := sch.TriggerManual(t.Context(), "acme/app", "dead-code", "pr", "Alice", "race-1"); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
		}
	})

	t.Run("Q15 budget pre-check charges system:upkeep when PerPerson disabled and github:<login> when enabled", func(t *testing.T) {
		spentRaw, _ := json.Marshal(map[string]int64{
			config.PrincipalUpkeep: 10_000_000,
			"github:alice":         5_000_000,
		})
		ledgerCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: ledger.Prefix + now.UTC().Format(ledger.DayLayout), Namespace: "maku"},
			Data:       map[string]string{ledger.Spent: string(spentRaw)},
		}

		// PerPerson disabled -> checks PrincipalUpkeep (10/10 exhausted).
		cfgOff := baseRoutineCfg(baseRR)
		cfgOff.Budgets.PerPerson.Enabled = false
		schOff, _, _ := newRoutineScheduler(t, forgetest.NewFake(), cfgOff, now, ledgerCM)
		if _, err := schOff.TriggerManual(t.Context(), "acme/app", "dead-code", "", "Bob", "req-off"); !errors.Is(err, ErrBudgetExhausted) {
			t.Fatalf("PerPerson=false exhausted upkeep: got %v, want ErrBudgetExhausted", err)
		}

		// PerPerson enabled + EnforcePrincipal=true -> checks github:alice (5/5 exhausted), while github:bob (0/5) passes.
		cfgOn := baseRoutineCfg(baseRR)
		cfgOn.Budgets.PerPerson.Enabled = true
		cfgOn.Budgets.EnforcePrincipal = true
		schOn, _, _ := newRoutineScheduler(t, forgetest.NewFake(), cfgOn, now, ledgerCM)
		if _, err := schOn.TriggerManual(t.Context(), "acme/app", "dead-code", "", "Alice", "req-alice"); !errors.Is(err, ErrBudgetExhausted) {
			t.Fatalf("Alice exhausted: got %v, want ErrBudgetExhausted", err)
		}
		tkBob, err := schOn.TriggerManual(t.Context(), "acme/app", "dead-code", "", "Bob", "req-bob")
		if err != nil || tkBob.Spec.Source.RequestedBy != "github:bob" {
			t.Fatalf("Bob should succeed: tk=%+v err=%v", tkBob, err)
		}

		// Zero upkeepDailyUSD returns ErrBudgetExhausted even when s.Ledger == nil.
		zero := 0.0
		cfgZero := baseRoutineCfg(baseRR)
		cfgZero.Budgets.PerPerson.Enabled = false
		cfgZero.Budgets.UpkeepDailyUSD = &zero
		schZero, _, _ := newRoutineScheduler(t, forgetest.NewFake(), cfgZero, now)
		schZero.Ledger = nil
		if _, err := schZero.TriggerManual(t.Context(), "acme/app", "dead-code", "", "Alice", "req-zero"); !errors.Is(err, ErrBudgetExhausted) {
			t.Fatalf("zero upkeepDailyUSD with nil Ledger: got %v, want ErrBudgetExhausted", err)
		}
	})
}

type alreadyExistsOnCreateClient struct {
	client.Client
	name string
}

func (c alreadyExistsOnCreateClient) Create(context.Context, client.Object, ...client.CreateOption) error {
	return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "tasks"}, c.name)
}

func TestRoutineSchedulerSyncsPausedMetrics(t *testing.T) {
	// Use a time far from the 03:00 cron slot so sync happens even outside the 1h slot window.
	now := time.Date(2026, 11, 26, 15, 0, 0, 0, time.UTC)
	declPaused := config.ResolvedRoutine{
		Name:   "decl-paused",
		Mode:   config.RoutineModePR,
		Paused: true,
		Cron:   "0 3 * * *",
	}
	statePaused := config.ResolvedRoutine{
		Name: "op-paused",
		Mode: config.RoutineModePR,
		Cron: "0 3 * * *",
	}
	active := config.ResolvedRoutine{
		Name: "active-job",
		Mode: config.RoutineModePR,
		Cron: "0 3 * * *",
	}
	cfg := baseRoutineCfg(declPaused)
	cfg.Repositories[0].ResolvedRoutines["op-paused"] = statePaused
	cfg.Repositories[0].ResolvedRoutines["active-job"] = active

	sch, sm, st := newRoutineScheduler(t, forgetest.NewFake(), cfg, now)
	if err := st.SetPaused(t.Context(), "acme/app", "op-paused", true, "manual", "github:alice"); err != nil {
		t.Fatal(err)
	}
	if err := sch.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !sm.paused["acme/app/decl-paused"] || !sm.paused["acme/app/op-paused"] || sm.paused["acme/app/active-job"] {
		t.Fatalf("paused metrics after tick = %+v", sm.paused)
	}
	// Operational unpause updates gauge to false on next tick while declarative remains true.
	if err := st.SetPaused(t.Context(), "acme/app", "op-paused", false, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := sch.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !sm.paused["acme/app/decl-paused"] || sm.paused["acme/app/op-paused"] || sm.paused["acme/app/active-job"] {
		t.Fatalf("paused metrics after resume = %+v", sm.paused)
	}
}

func TestRoutinePromptAndSkillIntake(t *testing.T) {
	now := time.Date(2026, 11, 26, 6, 15, 0, 0, time.UTC)
	baseRR := config.ResolvedRoutine{
		Name:          "dead-code",
		Mode:          config.RoutineModePR,
		Cron:          "0 6 * * *",
		DataClass:     "public",
		DefaultPrompt: "Remove dead code.",
		Probe:         config.ProbeSpec{Kind: config.ProbeNone, MaxFindings: 1},
		RepoPrompt:    true,
		Skill:         "docs-audit",
		MaxOpenPRs:    1,
		MaxCostUSD:    2.0,
	}

	t.Run("tick pins repo prompt and skill EffectiveSHA", func(t *testing.T) {
		f := forgetest.NewFake()
		f.SetFile(".github/agent-factory/upkeep/dead-code.md", "## Verify\n- go test ./...\n")
		f.SetFile(".agents/skills/docs-audit/SKILL.md", "# Docs audit\nCheck headings.\n")
		_, wantPromptSHA, _, _ := f.DefaultBranchFile(t.Context(), ".github/agent-factory/upkeep/dead-code.md")
		_, wantSkillSHA, _, _ := f.DefaultBranchFile(t.Context(), ".agents/skills/docs-audit/SKILL.md")

		sch, sm, _ := newRoutineScheduler(t, f, baseRoutineCfg(baseRR), now)
		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		var l v1alpha1.TaskList
		_ = sch.Client.List(t.Context(), &l)
		if len(l.Items) != 1 {
			t.Fatalf("tasks=%d, want 1", len(l.Items))
		}
		r := l.Items[0].Spec.Routine
		wantEff := upkeep.EffectivePromptSHA(wantPromptSHA, wantSkillSHA)
		if r == nil || r.PromptSource != "repo" || r.PromptPath != ".github/agent-factory/upkeep/dead-code.md" ||
			r.PromptSHA != wantEff || r.Skill != "docs-audit" || r.SkillSHA != wantSkillSHA {
			t.Fatalf("unexpected task routine spec: %+v", r)
		}
		if len(sm.promptReads) != 1 || sm.promptReads[0].outcome != "repo" {
			t.Fatalf("promptReads = %+v", sm.promptReads)
		}
	})

	t.Run("tick skips fail-closed on invalid prompt, missing skill, invalid skill", func(t *testing.T) {
		for name, tc := range map[string]struct {
			setup       func(*forgetest.Fake)
			wantSkip    string
			wantOutcome string
		}{
			"invalid prompt": {
				setup: func(f *forgetest.Fake) {
					f.SetFile(".github/agent-factory/upkeep/dead-code.md", "## Scope\nno verify\n")
					f.SetFile(".agents/skills/docs-audit/SKILL.md", "# skill\n")
				},
				wantSkip:    SkipPromptInvalid,
				wantOutcome: "invalid",
			},
			"missing skill": {
				setup: func(f *forgetest.Fake) {
					f.SetFile(".github/agent-factory/upkeep/dead-code.md", "## Verify\n- ok\n")
				},
				wantSkip:    SkipSkillMissing,
				wantOutcome: "skill_missing",
			},
			"invalid skill": {
				setup: func(f *forgetest.Fake) {
					f.SetFile(".github/agent-factory/upkeep/dead-code.md", "## Verify\n- ok\n")
					f.SetFile(".agents/skills/docs-audit/SKILL.md", "bad\r\nskill")
				},
				wantSkip:    SkipSkillInvalid,
				wantOutcome: "skill_invalid",
			},
		} {
			t.Run(name, func(t *testing.T) {
				f := forgetest.NewFake()
				tc.setup(f)
				sch, sm, _ := newRoutineScheduler(t, f, baseRoutineCfg(baseRR), now)
				if err := sch.Tick(t.Context()); err != nil {
					t.Fatal(err)
				}
				var l v1alpha1.TaskList
				_ = sch.Client.List(t.Context(), &l)
				if len(l.Items) != 0 {
					t.Fatalf("expected 0 tasks created, got %d", len(l.Items))
				}
				if len(sm.calls) != 1 || sm.calls[0].reason != tc.wantSkip {
					t.Fatalf("skips = %+v, want %s", sm.calls, tc.wantSkip)
				}
				if len(sm.promptReads) != 1 || sm.promptReads[0].outcome != tc.wantOutcome {
					t.Fatalf("promptReads = %+v, want %s", sm.promptReads, tc.wantOutcome)
				}
			})
		}
	})

	t.Run("TriggerManual fails closed on invalid prompt and missing/invalid skill", func(t *testing.T) {
		fBadPrompt := forgetest.NewFake()
		fBadPrompt.SetFile(".github/agent-factory/upkeep/dead-code.md", "no verify")
		sch1, _, _ := newRoutineScheduler(t, fBadPrompt, baseRoutineCfg(baseRR), now)
		if _, err := sch1.TriggerManual(t.Context(), "acme/app", "dead-code", "", "Alice", "k1"); !errors.Is(err, ErrPromptInvalid) {
			t.Fatalf("got %v, want ErrPromptInvalid", err)
		}

		fMissingSkill := forgetest.NewFake()
		sch2, _, _ := newRoutineScheduler(t, fMissingSkill, baseRoutineCfg(baseRR), now)
		if _, err := sch2.TriggerManual(t.Context(), "acme/app", "dead-code", "", "Alice", "k2"); !errors.Is(err, ErrSkillMissing) {
			t.Fatalf("got %v, want ErrSkillMissing", err)
		}

		fBadSkill := forgetest.NewFake()
		fBadSkill.SetFile(".agents/skills/docs-audit/SKILL.md", "bad\u200bskill")
		sch3, _, _ := newRoutineScheduler(t, fBadSkill, baseRoutineCfg(baseRR), now)
		if _, err := sch3.TriggerManual(t.Context(), "acme/app", "dead-code", "", "Alice", "k3"); !errors.Is(err, ErrSkillInvalid) {
			t.Fatalf("got %v, want ErrSkillInvalid", err)
		}
	})
}

func TestProposeOpenIssuesCountTowardMaxOpenPRs(t *testing.T) {
	now := time.Date(2026, 11, 26, 6, 15, 0, 0, time.UTC)
	rr := config.ResolvedRoutine{
		Name:          "doc-drift",
		Mode:          config.RoutineModePropose,
		Cron:          "0 6 * * *",
		DataClass:     "public",
		DefaultPrompt: "Audit docs.",
		Probe:         config.ProbeSpec{Kind: config.ProbeNone, MaxFindings: 1},
		MaxOpenPRs:    1,
		MaxCostUSD:    2.0,
	}
	cfg := baseRoutineCfg(rr)

	// Open factory/proposed + factory/routine:doc-drift issue blocks both Tick and TriggerManual.
	f := forgetest.NewFake()
	f.SetIssue(forge.Issue{Number: 91, State: "OPEN", Labels: []string{"factory/proposed", "factory/routine:doc-drift"}})
	sch, sm, _ := newRoutineScheduler(t, f, cfg, now)
	if err := sch.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	var l v1alpha1.TaskList
	_ = sch.Client.List(t.Context(), &l)
	if len(l.Items) != 0 {
		t.Fatalf("expected 0 tasks when open factory/proposed issue exists, got %d", len(l.Items))
	}
	if len(sm.calls) != 1 || sm.calls[0].reason != SkipOpenCap {
		t.Fatalf("skips = %+v, want %s", sm.calls, SkipOpenCap)
	}
	if _, err := sch.TriggerManual(t.Context(), "acme/app", "doc-drift", "", "Alice", "k-open"); !errors.Is(err, ErrOpenCap) {
		t.Fatalf("TriggerManual got %v, want ErrOpenCap", err)
	}

	// Closed factory/proposed issue does NOT count toward maxOpenPRs.
	fClosed := forgetest.NewFake()
	fClosed.SetIssue(forge.Issue{Number: 92, State: "CLOSED", Labels: []string{"factory/proposed", "factory/routine:doc-drift"}})
	sch2, _, _ := newRoutineScheduler(t, fClosed, cfg, now)
	if err := sch2.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = sch2.Client.List(t.Context(), &l)
	if len(l.Items) != 1 {
		t.Fatalf("expected 1 task when proposed issue is closed, got %d", len(l.Items))
	}
}

func TestRoutineSchedulerBreakerPausePromptResetAndManualTrial(t *testing.T) {
	now := time.Date(2026, 11, 26, 6, 15, 0, 0, time.UTC)
	prompt1 := "## Scope\nCheck v1\n\n## Verify\n- go test ./...\n"
	prompt2 := "## Scope\nCheck v2\n\n## Verify\n- go test ./...\n"
	skill1 := "# Skill v1\n"
	skill2 := "# Skill v2\n"

	baseRR := config.ResolvedRoutine{
		Name:          "dead-code",
		Mode:          config.RoutineModePR,
		Cron:          "0 6 * * *",
		DataClass:     "public",
		DefaultPrompt: "Remove dead code.",
		RepoPrompt:    true,
		Skill:         "docs-audit",
		Probe:         config.ProbeSpec{Kind: config.ProbeNone, MaxFindings: 1},
		MaxOpenPRs:    3,
		MaxCostUSD:    2.0,
		Breaker: config.ResolvedBreaker{
			Window:                 10,
			MinSamples:             5,
			MinMergeRate:           0.25,
			MaxConsecutiveClosed:   3,
			MaxConsecutiveFailures: 3,
		},
	}

	t.Run("unrelated commit stays breaker_paused; prompt or skill SHA change transitions to half_open with openCap=1", func(t *testing.T) {
		f := forgetest.NewFake()
		f.SetFile(".github/agent-factory/upkeep/dead-code.md", prompt1)
		f.SetFile(".agents/skills/docs-audit/SKILL.md", skill1)

		cfg := baseRoutineCfg(baseRR)
		sch, sm, st := newRoutineScheduler(t, f, cfg, now)
		sch.Breaker = &upkeep.Breaker{Store: st, Now: func() time.Time { return now }}
		loaded1, err := upkeep.LoadPrompt(t.Context(), f, "acme/app", baseRR)
		if err != nil {
			t.Fatal(err)
		}

		if err := st.Mutate(t.Context(), "acme/app", "dead-code", func(s *upkeep.RoutineState, n time.Time) bool {
			s.BreakerState = upkeep.BreakerPaused
			s.BreakerReason = upkeep.BreakerReasonConsecutiveClosed
			s.TrippedPromptSHA = loaded1.EffectiveSHA
			s.TrippedAt = &n
			return true
		}); err != nil {
			t.Fatal(err)
		}

		// Same EffectiveSHA (unrelated commit to main) -> skipped with SkipBreakerPaused.
		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(sm.calls) != 1 || sm.calls[0].reason != SkipBreakerPaused {
			t.Fatalf("expected %s, got %+v", SkipBreakerPaused, sm.calls)
		}
		var l v1alpha1.TaskList
		_ = sch.Client.List(t.Context(), &l)
		if len(l.Items) != 0 {
			t.Fatalf("expected 0 tasks while breaker_paused, got %d", len(l.Items))
		}

		// Manual TriggerManual while breaker-paused executes 1 trial without unpausing cron.
		trial, err := sch.TriggerManual(t.Context(), "acme/app", "dead-code", "", "Alice", "trial-1")
		if err != nil || trial == nil {
			t.Fatalf("TriggerManual while breaker_paused failed: %v", err)
		}
		gotSt, _ := st.Get(t.Context(), "acme/app", "dead-code")
		if gotSt.BreakerState != upkeep.BreakerPaused {
			t.Fatalf("TriggerManual must keep BreakerState=paused, got %q", gotSt.BreakerState)
		}
		// Second manual trigger while first trial is open hits ErrOpenCap (openCap=1 even when MaxOpenPRs=3).
		if _, err := sch.TriggerManual(t.Context(), "acme/app", "dead-code", "", "Alice", "trial-2"); !errors.Is(err, ErrOpenCap) {
			t.Fatalf("expected ErrOpenCap on 2nd trial while paused, got %v", err)
		}

		// Close trial-1 so openWork drops to 0, then edit skill -> EffectiveSHA changes -> half_open!
		ended := metav1.NewTime(now)
		trial.Status.Phase = v1alpha1.PhaseClosed
		trial.Status.EndedAt = &ended
		if err := sch.Client.Update(t.Context(), trial); err != nil {
			t.Fatal(err)
		}
		f.SetFile(".agents/skills/docs-audit/SKILL.md", skill2)
		now2 := now.Add(5 * time.Minute)
		sch.Now = func() time.Time { return now2 }

		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		gotSt, _ = st.Get(t.Context(), "acme/app", "dead-code")
		if gotSt.BreakerState != upkeep.BreakerHalfOpen {
			t.Fatalf("expected half_open after skill edit, got %+v", gotSt)
		}
		if sm.paused["acme/app/dead-code"] {
			t.Fatalf("expected RoutinePaused(false) on half_open transition")
		}
		_ = sch.Client.List(t.Context(), &l)
		if len(l.Items) != 2 { // trial-1 + half_open cron task
			t.Fatalf("expected half_open cron trial task, got %d tasks", len(l.Items))
		}
		_ = prompt2
	})

	t.Run("manual pause is NOT unpaused by prompt SHA change", func(t *testing.T) {
		f := forgetest.NewFake()
		f.SetFile(".github/agent-factory/upkeep/dead-code.md", prompt2)
		f.SetFile(".agents/skills/docs-audit/SKILL.md", skill1)

		cfg := baseRoutineCfg(baseRR)
		sch, sm, st := newRoutineScheduler(t, f, cfg, now)
		sch.Breaker = &upkeep.Breaker{Store: st, Now: func() time.Time { return now }}
		if err := st.Mutate(t.Context(), "acme/app", "dead-code", func(s *upkeep.RoutineState, _ time.Time) bool {
			s.Paused = true
			s.PausedReason = "manual"
			s.PausedBy = "github:alice"
			s.BreakerState = upkeep.BreakerPaused
			s.TrippedPromptSHA = strings.Repeat("0", 64)
			return true
		}); err != nil {
			t.Fatal(err)
		}

		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(sm.calls) != 1 || sm.calls[0].reason != SkipPausedManual {
			t.Fatalf("expected %s, got %+v", SkipPausedManual, sm.calls)
		}
		gotSt, _ := st.Get(t.Context(), "acme/app", "dead-code")
		if gotSt.BreakerState != upkeep.BreakerPaused {
			t.Fatalf("manual pause must prevent half_open transition, got %+v", gotSt)
		}
	})

	t.Run("cold-start Reconstruct from GitHub PRs trips breaker on first tick", func(t *testing.T) {
		f := forgetest.NewFake()
		f.SetFile(".github/agent-factory/upkeep/dead-code.md", prompt1)
		f.SetFile(".agents/skills/docs-audit/SKILL.md", skill1)
		for i := range 3 {
			f.SetPR(forge.PR{
				Number: 20 + i,
				State:  "CLOSED",
				Labels: []string{"factory/routine:dead-code", "factory/stale"},
			})
		}

		cfg := baseRoutineCfg(baseRR)
		sch, sm, st := newRoutineScheduler(t, f, cfg, now)
		sch.Breaker = &upkeep.Breaker{Store: st, Now: func() time.Time { return now }}

		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(sm.calls) != 1 || sm.calls[0].reason != SkipBreakerPaused {
			t.Fatalf("expected cold-start reconstruct to trip and skip %s, got %+v", SkipBreakerPaused, sm.calls)
		}
		gotSt, _ := st.Get(t.Context(), "acme/app", "dead-code")
		if gotSt.BreakerState != upkeep.BreakerPaused || gotSt.BreakerReason != upkeep.BreakerReasonConsecutiveClosed {
			t.Fatalf("unexpected reconstructed state: %+v", gotSt)
		}
	})

	t.Run("cold-start runs reconstructRoutineBreaker before syncProposedIssuesBreaker", func(t *testing.T) {
		f := forgetest.NewFake()
		f.SetFile(".github/agent-factory/upkeep/dead-code.md", prompt1)
		f.SetFile(".agents/skills/docs-audit/SKILL.md", skill1)
		f.SetPR(forge.PR{Number: 11, State: "CLOSED", Labels: []string{"factory/routine:dead-code"}})
		f.SetPR(forge.PR{Number: 12, State: "CLOSED", Labels: []string{"factory/routine:dead-code"}})
		f.SetIssue(forge.Issue{
			Number:      50,
			State:       "CLOSED",
			StateReason: "not_planned",
			Labels:      []string{"factory/proposed", "factory/routine:dead-code"},
			CreatedAt:   now.Add(-2 * time.Hour),
			ClosedAt:    now.Add(-time.Hour),
		})
		propTask := &v1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "proptask50", Namespace: "maku"},
			Spec: v1alpha1.TaskSpec{
				Repository: "acme/app",
				Issue:      50,
				Source:     v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "dead-code"},
				Routine: &v1alpha1.TaskRoutine{
					Name:      "dead-code",
					Mode:      config.RoutineModePropose,
					PromptSHA: strings.Repeat("a", 64),
				},
			},
			Status: v1alpha1.TaskStatus{
				Phase:   v1alpha1.PhaseDone,
				Reason:  "proposal_filed",
				EndedAt: &metav1.Time{Time: now.Add(-2 * time.Hour)},
			},
		}

		cfg := baseRoutineCfg(baseRR)
		sch, sm, st := newRoutineScheduler(t, f, cfg, now, propTask)
		sch.Breaker = &upkeep.Breaker{Store: st, Now: func() time.Time { return now }}
		var pausedCalls []string
		sch.OnRoutinePaused = func(_ context.Context, repo, routine, reason string) {
			pausedCalls = append(pausedCalls, repo+"/"+routine+":"+reason)
		}

		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		gotSt, err := st.Get(t.Context(), "acme/app", "dead-code")
		if err != nil {
			t.Fatal(err)
		}
		if gotSt.BreakerState != upkeep.BreakerPaused || len(gotSt.BreakerHistory) != 3 {
			t.Fatalf("expected reconstruct before sync to keep all 3 entries and trip paused, got state=%q history=%+v", gotSt.BreakerState, gotSt.BreakerHistory)
		}
		if len(sm.calls) != 1 || sm.calls[0].reason != SkipBreakerPaused {
			t.Fatalf("expected %s, got %+v", SkipBreakerPaused, sm.calls)
		}
		if len(pausedCalls) != 1 || pausedCalls[0] != "acme/app/dead-code:consecutive_closed" {
			t.Fatalf("OnRoutinePaused calls = %+v", pausedCalls)
		}
	})

	t.Run("closing published internal propose stub keeps proposal_accepted", func(t *testing.T) {
		f := forgetest.NewFake()
		f.SetFile(".github/agent-factory/upkeep/dead-code.md", prompt1)
		f.SetFile(".agents/skills/docs-audit/SKILL.md", skill1)
		f.SetIssue(forge.Issue{
			Number:      50,
			State:       "CLOSED",
			StateReason: "not_planned",
			Labels:      []string{"factory/proposed", "factory/routine:dead-code"}, // factory/publish removed after publish
			CreatedAt:   now.Add(-2 * time.Hour),
			ClosedAt:    now.Add(-time.Minute),
		})
		pubTask := &v1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "pubstub50", Namespace: "maku"},
			Spec: v1alpha1.TaskSpec{
				Repository: "acme/app",
				Issue:      50,
				Source:     v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "dead-code"},
				Routine: &v1alpha1.TaskRoutine{
					Name:      "dead-code",
					Mode:      config.RoutineModePropose,
					PromptSHA: strings.Repeat("a", 64),
				},
			},
			Status: v1alpha1.TaskStatus{
				Phase:     v1alpha1.PhaseDone,
				Reason:    "proposal_ready",
				EndedAt:   &metav1.Time{Time: now.Add(-2 * time.Hour)},
				Published: &v1alpha1.Published{Issue: 51, By: "alice", At: metav1.NewTime(now.Add(-time.Hour))},
			},
		}

		cfg := baseRoutineCfg(baseRR)
		sch, _, st := newRoutineScheduler(t, f, cfg, now, pubTask)
		breaker := &upkeep.Breaker{Store: st, Now: func() time.Time { return now }}
		sch.Breaker = breaker
		if _, err := breaker.Record(t.Context(), "acme/app", "dead-code", baseRR.Breaker, upkeep.BreakerOutcome{
			ID:        "proposal:50",
			Outcome:   upkeep.OutcomeProposalAccepted,
			PromptSHA: strings.Repeat("a", 64),
			At:        now.Add(-time.Hour),
		}); err != nil {
			t.Fatal(err)
		}

		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		gotSt, err := st.Get(t.Context(), "acme/app", "dead-code")
		if err != nil {
			t.Fatal(err)
		}
		if len(gotSt.BreakerHistory) != 1 || gotSt.BreakerHistory[0].Outcome != upkeep.OutcomeProposalAccepted {
			t.Fatalf("closing published stub flipped outcome: %+v", gotSt.BreakerHistory)
		}
	})

	t.Run("syncProposedIssuesBreaker handles zero LastEditedAt post-reset closes and >100 issues without write loops", func(t *testing.T) {
		f := forgetest.NewFake()
		f.SetFile(".github/agent-factory/upkeep/dead-code.md", prompt1)
		f.SetFile(".agents/skills/docs-audit/SKILL.md", skill1)
		resetAt := now.Add(-time.Hour)

		// Issue #60 created BEFORE BreakerResetAt, closed AFTER BreakerResetAt, with LastEditedAt zero.
		f.SetIssue(forge.Issue{
			Number:      60,
			State:       "CLOSED",
			StateReason: "not_planned",
			Labels:      []string{"factory/proposed", "factory/routine:dead-code"},
			CreatedAt:   resetAt.Add(-2 * time.Hour),
			ClosedAt:    resetAt.Add(10 * time.Minute),
		})
		// Issue #61 created AFTER #60, closed BEFORE #60 (tests ClosedAt ordering).
		f.SetIssue(forge.Issue{
			Number:      61,
			State:       "CLOSED",
			StateReason: "completed",
			Labels:      []string{"factory/proposed", "factory/routine:dead-code"},
			CreatedAt:   resetAt.Add(-time.Hour),
			ClosedAt:    resetAt.Add(5 * time.Minute),
		})
		// Issues #62 and #63 rejected after #60 -> consecutive_closed = 3 (with #60 after #61)!
		f.SetIssue(forge.Issue{
			Number:      62,
			State:       "CLOSED",
			StateReason: "not_planned",
			Labels:      []string{"factory/proposed", "factory/routine:dead-code"},
			CreatedAt:   resetAt.Add(-3 * time.Hour),
			ClosedAt:    resetAt.Add(20 * time.Minute),
		})
		f.SetIssue(forge.Issue{
			Number:      63,
			State:       "CLOSED",
			StateReason: "not_planned",
			Labels:      []string{"factory/proposed", "factory/routine:dead-code"},
			CreatedAt:   resetAt.Add(-4 * time.Hour),
			ClosedAt:    resetAt.Add(30 * time.Minute),
		})
		loaded1, err := upkeep.LoadPrompt(t.Context(), f, "acme/app", baseRR)
		if err != nil {
			t.Fatal(err)
		}
		var tasks []client.Object
		for _, num := range []int{60, 61, 62, 63} {
			tasks = append(tasks, &v1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("proptask%d", num), Namespace: "maku"},
				Spec: v1alpha1.TaskSpec{
					Repository: "acme/app",
					Issue:      num,
					Source:     v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "dead-code"},
					Routine: &v1alpha1.TaskRoutine{
						Name:      "dead-code",
						Mode:      config.RoutineModePropose,
						PromptSHA: loaded1.EffectiveSHA,
					},
				},
				Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone, Reason: "proposal_filed"},
			})
		}

		cfg := baseRoutineCfg(baseRR)
		sch, _, st := newRoutineScheduler(t, f, cfg, now, tasks...)
		sch.Breaker = &upkeep.Breaker{Store: st, Now: func() time.Time { return now }}
		var pausedCalls []string
		sch.OnRoutinePaused = func(_ context.Context, repo, routine, reason string) {
			pausedCalls = append(pausedCalls, repo+"/"+routine+":"+reason)
		}
		if err := st.Mutate(t.Context(), "acme/app", "dead-code", func(s *upkeep.RoutineState, _ time.Time) bool {
			s.BreakerState = upkeep.BreakerOK
			s.BreakerResetAt = &resetAt
			return true
		}); err != nil {
			t.Fatal(err)
		}

		if err := sch.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		gotSt, _ := st.Get(t.Context(), "acme/app", "dead-code")
		if gotSt.BreakerState != upkeep.BreakerPaused || gotSt.BreakerReason != upkeep.BreakerReasonConsecutiveClosed {
			t.Fatalf("expected post-reset closes ordered by ClosedAt to trip breaker, got %+v", gotSt)
		}
		if len(pausedCalls) != 1 || pausedCalls[0] != "acme/app/dead-code:consecutive_closed" {
			t.Fatalf("OnRoutinePaused calls = %+v", pausedCalls)
		}

		// >100 historical closed issues: steady-state ticks must perform zero ConfigMap writes.
		fMany := forgetest.NewFake()
		fMany.SetFile(".github/agent-factory/upkeep/dead-code.md", prompt1)
		fMany.SetFile(".agents/skills/docs-audit/SKILL.md", skill1)
		var manyTasks []client.Object
		for i := 1; i <= 105; i++ {
			closedAt := now.Add(-time.Duration(106-i) * time.Minute)
			fMany.SetIssue(forge.Issue{
				Number:      i,
				State:       "CLOSED",
				StateReason: "completed",
				Labels:      []string{"factory/proposed", "factory/routine:dead-code"},
				CreatedAt:   closedAt.Add(-time.Hour),
				ClosedAt:    closedAt,
			})
			manyTasks = append(manyTasks, &v1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("manytask%03d", i), Namespace: "maku"},
				Spec: v1alpha1.TaskSpec{
					Repository: "acme/app",
					Issue:      i,
					Source:     v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, Ref: "dead-code"},
					Routine:    &v1alpha1.TaskRoutine{Name: "dead-code", Mode: config.RoutineModePropose, PromptSHA: strings.Repeat("a", 64)},
				},
				Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone, Reason: "proposal_filed"},
			})
		}
		baseClient := fake.NewClientBuilder().WithScheme(routineScheme()).WithObjects(manyTasks...).Build()
		cc := &countingClient{Client: baseClient}
		stMany := &upkeep.StateStore{Client: cc, Reader: cc, Namespace: "maku", Now: func() time.Time { return now }}
		schMany := &Scheduler{
			Forge:          fMany,
			Merger:         fMany,
			Client:         cc,
			APIReader:      cc,
			Ledger:         cc,
			State:          stMany,
			Breaker:        &upkeep.Breaker{Store: stMany, Now: func() time.Time { return now }},
			RoutineMetrics: &fakeSkipMetrics{},
			Namespace:      "maku",
			Cfg:            cfg,
			Owned:          func(string) bool { return true },
			Stopped:        func(context.Context) bool { return false },
			Now:            func() time.Time { return now },
			Nonce:          func() string { return "abcd1234" },
			Errors:         &counter{},
		}
		if err := schMany.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		writesAfterFirstTick := cc.writes
		for tick := 1; tick <= 3; tick++ {
			tNow := now.Add(time.Duration(tick) * time.Minute)
			schMany.Now = func() time.Time { return tNow }
			if err := schMany.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if cc.writes != writesAfterFirstTick {
			t.Fatalf("ConfigMap writes looped past 100 entries: after tick1=%d, after tick4=%d", writesAfterFirstTick, cc.writes)
		}
	})
}

type countingClient struct {
	client.Client
	writes int
}

func (c *countingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*corev1.ConfigMap); ok {
		c.writes++
	}
	return c.Client.Create(ctx, obj, opts...)
}

func (c *countingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if _, ok := obj.(*corev1.ConfigMap); ok {
		c.writes++
	}
	return c.Client.Update(ctx, obj, opts...)
}

var _ = fmt.Sprintf
