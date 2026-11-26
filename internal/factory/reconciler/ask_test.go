// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

func shippedRig(t *testing.T, tk *v1alpha1.Task) *rig {
	t.Helper()
	g := newRig(t, tk)
	c := g.r.Cfg.Current()
	c.HarnessProfiles, c.Templates, c.Loops = config.FillShippedWorkflows(nil, nil, nil, false)
	c.Tiers["light"] = config.Tier{Model: "tier-light", RunTokens: 300_000, TaskTokens: 600_000, RunMinutes: 20}
	return g
}

func TestAStandardTaskWithNoAcceptanceIsAskedBeforeAnyRun(t *testing.T) {
	tk := issueTask("3buqdlot", 7, "# Fix the status section\n\nIt looks stale.")
	g := shippedRig(t, tk)
	tk = g.reconcile(t, "3buqdlot", 2)
	if tk.Status.Phase != v1alpha1.PhaseEscalated || tk.Status.Reason != ReasonNeedsAuthor {
		t.Fatalf("got %s %q, want Escalated needs_author", tk.Status.Phase, tk.Status.Reason)
	}
	if len(g.runs.specs) != 0 || len(tk.Status.Runs) != 0 || tk.Status.Usage.CostMicroUSD != 0 {
		t.Fatalf("no run or spend may happen before ask: specs=%d runs=%d usd=%d",
			len(g.runs.specs), len(tk.Status.Runs), tk.Status.Usage.CostMicroUSD)
	}
	if !slices.Equal(g.f.Added(7), []string{LabelNeedsAuthor}) {
		t.Fatalf("labels added = %v, want [%s]", g.f.Added(7), LabelNeedsAuthor)
	}
	comments := eventCommentBodies(g.f, 7)
	if len(comments) != 1 || !strings.Contains(comments[0], "Which command or observation proves it is done?") {
		t.Fatalf("comments = %q", comments)
	}
	// The question has a room from the start (gcp-0 drill, #538): maku needs lists it, and maku
	// answer and maku cancel reach it, not only the issue.
	var room roomv1.Room
	if err := g.c.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: tk.Name}, &room); err != nil || tk.Status.RoomRef != tk.Name {
		t.Fatalf("room %q: %v", tk.Status.RoomRef, err)
	}
	if l := tk.Status.Facts; l == nil || !l.Posted {
		t.Fatalf("the bounced task's facts reach its room: %+v", l)
	}
}

func TestATaskWithAcceptanceRunsUnasked(t *testing.T) {
	tk := issueTask("3buqdlot", 7, "# Fix the link\n\n## Acceptance check\n`task docs` passes.")
	g := shippedRig(t, tk)
	tk = g.reconcile(t, "3buqdlot", 3)
	if tk.Status.Phase != v1alpha1.PhaseImplementing || len(g.runs.specs) != 1 {
		t.Fatalf("got phase=%s runs=%d", tk.Status.Phase, len(g.runs.specs))
	}
	if eff := stageEffectOf(tk, "ask"); eff != "not_needed" {
		t.Fatalf("ask effect = %q, want not_needed", eff)
	}
	if len(g.f.Added(7)) != 0 {
		t.Fatalf("no label expected, got %v", g.f.Added(7))
	}
}

func TestAScheduledTaskIsNeverAsked(t *testing.T) {
	tk := issueTask("3buqdlot", 0, "# Weekly doc check\n\nLook for stale headings.")
	tk.Spec.Source.Kind = "schedule"
	g := shippedRig(t, tk)
	tk = g.reconcile(t, "3buqdlot", 3)
	if tk.Status.Phase != v1alpha1.PhaseImplementing || len(g.runs.specs) != 1 {
		t.Fatalf("scheduled task should run unasked: phase=%s reason=%q runs=%d", tk.Status.Phase, tk.Status.Reason, len(g.runs.specs))
	}
	if eff := stageEffectOf(tk, "ask"); eff != "asked_assumed" {
		t.Fatalf("ask effect = %q, want asked_assumed", eff)
	}
}

func TestACarefulTaskIsAskedOnce(t *testing.T) {
	tk := issueTask("3buqdlot", 7, "# Fix the status section\n\nIt looks stale.")
	g := shippedRig(t, tk)
	g.r.Cfg.Current().Repositories[0].Workflow.Default = config.WorkflowCareful
	tk = g.reconcile(t, "3buqdlot", 2)
	if tk.Status.Phase != v1alpha1.PhaseEscalated || tk.Status.Reason != ReasonNeedsAuthor || len(g.runs.specs) != 0 {
		t.Fatalf("expected intake bounce with 0 runs: %s %q runs=%d", tk.Status.Phase, tk.Status.Reason, len(g.runs.specs))
	}
	// Maintainer comments /factory retry: task proceeds as assumed, runs scoper once, and scoper's
	// ready handoff is NOT rewritten into a second acceptance question.
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	g.f.SetComments(7, retryBy(77, "Smana", -30*time.Minute))
	tk = g.reconcile(t, "3buqdlot", 3) // Escalated -> Triaged -> Queued -> Implementing (scoper)
	if tk.Status.Phase != v1alpha1.PhaseImplementing || len(g.runs.specs) != 1 || g.runs.specs[rid(0)].Role != "triager" {
		t.Fatalf("expected scoper run after retry: phase=%s runs=%+v", tk.Status.Phase, g.runs.specs)
	}
	if eff := stageEffectOf(tk, "ask"); eff != "asked_assumed" {
		t.Fatalf("ask effect after retry = %q, want asked_assumed", eff)
	}
	scopeHandoff(g, 0, readyScope())
	tk = endScoper(t, g, "Succeeded", "agent_finished")
	if tk.Status.Phase != v1alpha1.PhaseQueued || tk.Status.Scope == nil || tk.Status.Scope.State != "ready" {
		t.Fatalf("scoper ready must not bounce a second time: phase=%s reason=%q scope=%+v",
			tk.Status.Phase, tk.Status.Reason, tk.Status.Scope)
	}
}

func TestAnAnsweredPlanIsNotAskedAgain(t *testing.T) {
	tk := issueTask("3buqdlot", 7, "# Update config docs\n\nDone when docs/reference/config.md lists planTier.")
	tk.Spec.Intake = &v1alpha1.TaskIntake{Class: "review", Login: "Smana"}
	g := shippedRig(t, tk)
	tk = g.reconcile(t, "3buqdlot", 3)
	if tk.Status.Phase != v1alpha1.PhaseImplementing || len(g.runs.specs) != 1 {
		t.Fatalf("answered plan should run unasked: phase=%s reason=%q runs=%d", tk.Status.Phase, tk.Status.Reason, len(g.runs.specs))
	}
	if eff := stageEffectOf(tk, "ask"); eff != "asked_answered" {
		t.Fatalf("ask effect = %q, want asked_answered", eff)
	}
}
