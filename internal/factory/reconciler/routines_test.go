// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/runs"
)

func routineTask(name, routine, requestedBy string) *v1alpha1.Task {
	return &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "agent-system",
			CreationTimestamp: metav1.NewTime(now.Add(-time.Minute)),
			Labels:            map[string]string{v1alpha1.LabelRoutine: routine},
		},
		Spec: v1alpha1.TaskSpec{
			Repository: "Smana/cloud-native-ref",
			Source: v1alpha1.Source{
				Kind:          v1alpha1.SourceKindRoutine,
				Ref:           routine,
				Key:           "routine:Smana/cloud-native-ref:" + routine + ":slot1",
				RequestedBy:   requestedBy,
				Trust:         "trusted",
				ContentSHA256: strings.Repeat("b", 64),
			},
			Text:        "Sync docs with code.\n\nFINDINGS-DATA-intake12\n{\"v\":1}\nFINDINGS-DATA-intake12",
			DataClass:   "public",
			MaxMicroUSD: 2_000_000,
			Routine: &v1alpha1.TaskRoutine{
				Name:         routine,
				Mode:         config.RoutineModePR,
				PromptSource: "routine",
				PromptSHA:    strings.Repeat("d", 64),
				FindingIDs:   []string{"f1"},
			},
		},
	}
}

func TestRoutineYieldsToHumanPriority(t *testing.T) {
	for _, waitingKind := range []string{"issue", "api", "runlore"} {
		t.Run("yields to "+waitingKind, func(t *testing.T) {
			human := issueTask("aaaaaaa2", 7, "human task")
			human.Spec.Source.Kind = waitingKind
			human.Status.Phase = v1alpha1.PhaseQueued
			human.Status.Reason = "waiting_run_slot"
			if waitingKind != "issue" {
				human.Spec.Issue = 0
			}
			rt := routineTask("bbbbbbb3", "doc-drift", "system:scheduler")
			g := newRig(t, human, rt)
			g.r.Cfg.Current().Caps.ConcurrentRuns = 2

			got := g.reconcile(t, rt.Name, 3)
			if got.Status.Phase != v1alpha1.PhaseQueued || got.Status.Reason != "waiting_human_priority" {
				t.Fatalf("got phase=%s reason=%s, want Queued/waiting_human_priority", got.Status.Phase, got.Status.Reason)
			}
			if len(g.runs.specs) != 0 {
				t.Fatalf("expected no run started, got %+v", g.runs.specs)
			}
			if slices.Contains(g.metrics.recorded, "revision_deferred") {
				t.Fatalf("unexpected revision_deferred metric: %v", g.metrics.recorded)
			}
		})
	}

	for _, peerKind := range []string{"schedule", v1alpha1.SourceKindRoutine} {
		t.Run("does not yield to "+peerKind, func(t *testing.T) {
			peer := routineTask("aaaaaaa2", "other", "system:scheduler")
			peer.Spec.Source.Kind = peerKind
			if peerKind == "schedule" {
				peer.Spec.Routine = nil
			}
			peer.Status.Phase = v1alpha1.PhaseQueued
			peer.Status.Reason = "waiting_run_slot"

			rt := routineTask("bbbbbbb3", "doc-drift", "system:scheduler")
			g := newRig(t, peer, rt)
			g.r.Cfg.Current().Caps.ConcurrentRuns = 2

			got := g.reconcile(t, rt.Name, 3)
			if got.Status.Phase != v1alpha1.PhaseImplementing {
				t.Fatalf("got phase=%s reason=%s, want Implementing", got.Status.Phase, got.Status.Reason)
			}
		})
	}

	t.Run("full run slots report waiting_run_slot first", func(t *testing.T) {
		human := issueTask("aaaaaaa2", 7, "human task")
		human.Status.Phase = v1alpha1.PhaseQueued
		human.Status.Reason = "waiting_run_slot"
		rt := routineTask("bbbbbbb3", "doc-drift", "system:scheduler")
		g := newRig(t, human, rt)
		g.r.Cfg.Current().Caps.ConcurrentRuns = 1
		g.runs.runs["rrrrrrr1"] = runs.Run{ID: "rrrrrrr1", TaskID: "zzzzzzz9", Principal: runs.PrincipalFactory, Phase: "Running"}

		got := g.reconcile(t, rt.Name, 3)
		if got.Status.Phase != v1alpha1.PhaseQueued || got.Status.Reason != "waiting_run_slot" {
			t.Fatalf("got phase=%s reason=%s, want Queued/waiting_run_slot", got.Status.Phase, got.Status.Reason)
		}
	})
}

func TestRoutineUpkeepWIPCap(t *testing.T) {
	awaiting1 := routineTask("aaaaaaa2", "r1", "system:scheduler")
	awaiting1.Status.Phase = v1alpha1.PhaseAwaitingHuman
	awaiting2 := routineTask("ccccccc4", "r2", "system:scheduler")
	awaiting2.Status.Phase = v1alpha1.PhaseAwaitingHuman

	rt := routineTask("bbbbbbb3", "doc-drift", "system:scheduler")
	human := issueTask("ddddddd5", 7, "human issue")

	g := newRig(t, awaiting1, awaiting2, rt, human)
	g.r.Cfg.Current().Caps.AwaitingHumanWIP = 4 // UpkeepWIP() == 2
	g.r.Cfg.Current().Caps.ActiveTasks = 10

	gotRT := g.reconcile(t, rt.Name, 3)
	if gotRT.Status.Phase != v1alpha1.PhaseQueued || gotRT.Status.Reason != "waiting_upkeep_review_wip" {
		t.Fatalf("routine got phase=%s reason=%s, want Queued/waiting_upkeep_review_wip", gotRT.Status.Phase, gotRT.Status.Reason)
	}

	gotHuman := g.reconcile(t, human.Name, 3)
	if gotHuman.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatalf("human got phase=%s reason=%s, want Implementing", gotHuman.Status.Phase, gotHuman.Status.Reason)
	}

	t.Run("revision run on open routine PR bypasses upkeep WIP cap", func(t *testing.T) {
		rev := routineTask("eeeeeee6", "doc-drift", "system:scheduler")
		rev.Spec.Template = "pair"
		rev.Spec.Budget = v1alpha1.Budget{Tier: "standard", Model: "agent-default", RunTokens: 1_500_000, TaskTokens: 3_000_000, RunMinutes: 45}
		rev.Status.Phase = v1alpha1.PhaseQueued
		rev.Status.RoomRef = rev.Name
		rev.Status.Runs = []v1alpha1.RunRecord{{ID: runID(rev.Name, 0), Role: "implementer", Phase: "Succeeded"}}
		rev.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", HeadSHA: "abc"}

		g2 := newRig(t, awaiting1, awaiting2, rev, roomOf(rev.Name))
		g2.r.Cfg.Current().Caps.AwaitingHumanWIP = 4
		g2.r.Cfg.Current().Caps.ActiveTasks = 10
		g2.f.SetPR(forge.PR{Number: 12, State: "OPEN", HeadSHA: "abc", URL: "https://github.com/Smana/cloud-native-ref/pull/12"})

		gotRev := g2.reconcile(t, rev.Name, 1)
		if gotRev.Status.Phase != v1alpha1.PhaseImplementing {
			t.Fatalf("revision routine got phase=%s reason=%s, want Implementing", gotRev.Status.Phase, gotRev.Status.Reason)
		}
	})
}

func TestRoutineUpkeepBudgetIsolation(t *testing.T) {
	upkeepBudgets := func(perPerson bool) config.Budgets {
		return config.Budgets{
			FactoryDailyUSD:  42.50,
			HumanDailyUSD:    5.00,
			UpkeepDailyUSD:   new(2.00),
			EnforcePrincipal: false,
			PerPerson:        config.PerPerson{Enabled: perPerson, DailyUSD: 5.00},
		}
	}

	t.Run("cron routine reserves system:upkeep even when EnforcePrincipal and PerPerson are false", func(t *testing.T) {
		rt := routineTask("bbbbbbb3", "doc-drift", "system:scheduler")
		g := newRig(t, rt)
		g.r.Cfg.Current().Budgets = upkeepBudgets(false)

		got := g.reconcile(t, rt.Name, 3)
		if got.Status.Phase != v1alpha1.PhaseImplementing {
			t.Fatalf("got phase=%s reason=%s", got.Status.Phase, got.Status.Reason)
		}
		res := reservationOf(t, g, runID(rt.Name, 0))
		if res.Principal != runs.PrincipalFactory || res.Person != config.PrincipalUpkeep {
			t.Fatalf("unexpected reservation: %+v", res)
		}
		if g.runs.specs[runID(rt.Name, 0)].ChargedTo != config.PrincipalUpkeep {
			t.Fatalf("ChargedTo = %q, want %q", g.runs.specs[runID(rt.Name, 0)].ChargedTo, config.PrincipalUpkeep)
		}
	})

	t.Run("upkeep exhaustion escalates cron routine budget-upkeep while issue task admits", func(t *testing.T) {
		rt := routineTask("bbbbbbb3", "doc-drift", "system:scheduler")
		human := issueTask("aaaaaaa2", 7, "human issue")
		g := newRig(t, rt, human, spentLedger(map[string]int64{config.PrincipalUpkeep: 2_000_000}))
		g.r.Cfg.Current().Budgets = upkeepBudgets(false)

		gotRT := g.reconcile(t, rt.Name, 3)
		if gotRT.Status.Phase != v1alpha1.PhaseEscalated || gotRT.Status.Reason != "budget-upkeep" {
			t.Fatalf("got phase=%s reason=%s, want Escalated/budget-upkeep", gotRT.Status.Phase, gotRT.Status.Reason)
		}
		if !slices.Contains(g.metrics.bound, "revoked budget-upkeep "+config.PrincipalUpkeep) {
			t.Fatalf("missing bound revocation metric: %v", g.metrics.bound)
		}

		gotHuman := g.reconcile(t, human.Name, 3)
		if gotHuman.Status.Phase != v1alpha1.PhaseImplementing {
			t.Fatalf("human task blocked by upkeep exhaustion: phase=%s reason=%s", gotHuman.Status.Phase, gotHuman.Status.Reason)
		}
	})

	t.Run("manual routine charges person when PerPerson enabled and system:upkeep when disabled", func(t *testing.T) {
		// PerPerson enabled -> github:smana (unaffected by system:upkeep exhaustion).
		rt1 := routineTask("bbbbbbb3", "doc-drift", "github:Smana")
		g1 := newRig(t, rt1, spentLedger(map[string]int64{config.PrincipalUpkeep: 2_000_000}))
		g1.r.Cfg.Current().Budgets = upkeepBudgets(true)
		if got := g1.reconcile(t, rt1.Name, 3); got.Status.Phase != v1alpha1.PhaseImplementing {
			t.Fatalf("manual routine with perPerson=true blocked by upkeep spend: phase=%s reason=%s", got.Status.Phase, got.Status.Reason)
		}
		if got := g1.runs.specs[runID(rt1.Name, 0)].ChargedTo; got != "github:smana" {
			t.Fatalf("ChargedTo = %q, want github:smana", got)
		}

		// PerPerson enabled + EnforcePrincipal and github:smana spent -> budget-person.
		rt2 := routineTask("ccccccc4", "doc-drift", "github:Smana")
		g2 := newRig(t, rt2, spentLedger(map[string]int64{"github:smana": 5_000_000}))
		b2 := upkeepBudgets(true)
		b2.EnforcePrincipal = true
		g2.r.Cfg.Current().Budgets = b2
		if got := g2.reconcile(t, rt2.Name, 3); got.Status.Phase != v1alpha1.PhaseEscalated || got.Status.Reason != "budget-person" {
			t.Fatalf("got phase=%s reason=%s, want Escalated/budget-person", got.Status.Phase, got.Status.Reason)
		}

		// PerPerson disabled -> system:upkeep.
		rt3 := routineTask("ddddddd5", "doc-drift", "github:Smana")
		g3 := newRig(t, rt3)
		g3.r.Cfg.Current().Budgets = upkeepBudgets(false)
		if got := g3.reconcile(t, rt3.Name, 3); got.Status.Phase != v1alpha1.PhaseImplementing {
			t.Fatalf("got phase=%s reason=%s", got.Status.Phase, got.Status.Reason)
		}
		if got := g3.runs.specs[runID(rt3.Name, 0)].ChargedTo; got != config.PrincipalUpkeep {
			t.Fatalf("ChargedTo = %q, want %q", got, config.PrincipalUpkeep)
		}
	})

	t.Run("transient ledger read error fails closed for system:upkeep", func(t *testing.T) {
		rt := routineTask("bbbbbbb3", "doc-drift", "system:scheduler")
		g := newRig(t, rt)
		g.r.Cfg.Current().Budgets = upkeepBudgets(false)
		g.r.Ledger = interceptor.NewClient(g.c.(client.WithWatch), interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if strings.HasPrefix(key.Name, "agent-factory-ledger-") {
					return errors.New("ledger apiserver error")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		})
		_ = g.reconcile(t, rt.Name, 2) // Received -> Triaged -> Queued
		_, err := g.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: "agent-system", Name: rt.Name}})
		if err == nil {
			t.Fatal("expected transient ledger error to fail closed")
		}
		if len(g.runs.specs) != 0 {
			t.Fatalf("started run despite failed upkeep ledger read: %+v", g.runs.specs)
		}
	})
}

func TestRoutinePRLabellingBriefAndOutcomeMetric(t *testing.T) {
	rt := routineTask("bbbbbbb3", "doc-drift", "system:scheduler")
	brief := FirstBrief(rt, "n0nce234")
	if !strings.Contains(brief, "Source: routine doc-drift.") {
		t.Fatalf("FirstBrief missing routine source line:\n%s", brief)
	}
	if !strings.Contains(brief, "TASK-DATA-n0nce234") || !strings.Contains(brief, "FINDINGS-DATA-intake12") {
		t.Fatalf("FirstBrief mangled fences:\n%s", brief)
	}

	g := newRig(t, rt)
	got := g.reconcile(t, rt.Name, 3)
	if got.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatalf("got phase=%s reason=%s", got.Status.Phase, got.Status.Reason)
	}
	runID0 := runID(rt.Name, 0)
	g.runs.set(runID0, "Succeeded")
	g.log.end(runID0, "Succeeded", "agent_finished")
	g.f.SetBranch("agent/"+rt.Name, 42)
	g.f.SetPR(forge.PR{Number: 42, State: "OPEN", HeadSHA: "head1", URL: "https://github.com/Smana/cloud-native-ref/pull/42"})

	got = g.reconcile(t, rt.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseAwaitingCI {
		t.Fatalf("got phase=%s reason=%s", got.Status.Phase, got.Status.Reason)
	}
	labels := g.f.Added(42)
	if !slices.Contains(labels, "factory/class:review") || !slices.Contains(labels, "factory/routine:doc-drift") {
		t.Fatalf("PR labels = %v, want factory/class:review and factory/routine:doc-drift", labels)
	}

	// Merge PR and settle usage to trigger count() -> RoutineRun metric.
	g.f.SetPR(forge.PR{Number: 42, State: "MERGED", MergedBy: "Smana", HeadSHA: "head1", URL: "https://github.com/Smana/cloud-native-ref/pull/42"})
	got = g.reconcile(t, rt.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseDone {
		t.Fatalf("got phase=%s reason=%s", got.Status.Phase, got.Status.Reason)
	}
	// Advance clock past settleWindow to settle usage and fire count().
	g.r.Now = func() time.Time { return now.Add(g.r.settleWindow(g.r.Cfg.Current())) }
	_ = g.reconcile(t, rt.Name, 1)
	if !slices.Contains(g.metrics.recorded, "routine_run Smana/cloud-native-ref doc-drift accepted 0") {
		t.Fatalf("recorded metrics missing routine_run: %v", g.metrics.recorded)
	}
}

func TestRoutineEgressSubsetAndScoperCeiling(t *testing.T) {
	rt := routineTask("bbbbbbb3", "doc-drift", "system:scheduler")
	rt.Spec.MaxMicroUSD = 600_000
	g := newRig(t, rt)
	profiles, _, _ := config.ShippedWorkflows()
	careful := profiles[config.WorkflowCareful]
	for i := range careful.Stages {
		if careful.Stages[i].Role == "implementer" {
			careful.Stages[i].Checks = true
		}
	}
	profiles[config.WorkflowCareful] = careful
	c := g.r.Cfg.Current()
	c.HarnessProfiles = profiles
	c.Repositories[0].Checks = config.Checks{Egress: []string{"golang", "pypi"}}
	c.Repositories[0].ResolvedRoutines = map[string]config.ResolvedRoutine{
		"doc-drift": {
			Name:     "doc-drift",
			Workflow: "careful",
			Egress:   []string{"golang"},
		},
	}
	// Triage and advance through careful workflow (which uses scoper).
	got := g.reconcile(t, rt.Name, 3)
	if got.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatalf("got phase=%s reason=%s", got.Status.Phase, got.Status.Reason)
	}
	scoperSpec := g.runs.specs[runID(rt.Name, 0)]
	if scoperSpec.Persona != "scoper" {
		t.Fatalf("expected scoper run first, got %+v", scoperSpec)
	}
	if scoperSpec.MaxTokens > got.Spec.Budget.RunTokens || got.Spec.Budget.RunTokens <= 0 {
		t.Fatalf("scoper MaxTokens=%d exceeded RunTokens=%d", scoperSpec.MaxTokens, got.Spec.Budget.RunTokens)
	}

	// Verify implementerSpec picks routine egress subset ["golang"] instead of repo ["golang", "pypi"].
	impl := g.r.implementerSpec(c, got, "brief")
	if !impl.Checks || !slices.Equal(impl.EgressProfiles, []string{"golang"}) {
		t.Fatalf("EgressProfiles = %v (checks=%v), want [golang]", impl.EgressProfiles, impl.Checks)
	}
}
