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
	calls []skipMetricCall
}

type skipMetricCall struct {
	repo, routine, reason string
}

func (m *fakeSkipMetrics) RoutineSkip(_ context.Context, repository, routine, reason string) {
	m.calls = append(m.calls, skipMetricCall{repo: repository, routine: routine, reason: reason})
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
		wantPromptSHA := hex.EncodeToString(sum[:])
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

	t.Run("rejects propose/draft with ErrModePhase2 and widening with ErrModeWidening", func(t *testing.T) {
		cfg := baseRoutineCfg(baseRR)
		sch, _, _ := newRoutineScheduler(t, forgetest.NewFake(), cfg, now)

		if _, err := sch.TriggerManual(t.Context(), "acme/app", "dead-code", config.RoutineModePropose, "Alice", "k1"); !errors.Is(err, ErrModePhase2) {
			t.Fatalf("propose override: got %v, want ErrModePhase2", err)
		}
		if _, err := sch.TriggerManual(t.Context(), "acme/app", "dead-code", config.RoutineModeDraft, "Alice", "k2"); !errors.Is(err, ErrModePhase2) {
			t.Fatalf("draft override: got %v, want ErrModePhase2", err)
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

var _ = fmt.Sprintf
