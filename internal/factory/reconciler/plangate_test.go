// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

func planGateRig(t *testing.T, author string) (*rig, *v1alpha1.Task) {
	t.Helper()
	tk := issueTask("3buqdlot", 7, "# Refactor storage\n\n## Acceptance check\n`go test ./...` passes.")
	tk.Spec.IssueAuthor = author
	tk.Spec.Outcome = config.OutcomePlanFirst
	g := shippedRig(t, tk)
	g.r.Cfg.Current().Outcomes = []string{config.OutcomeChange, config.OutcomePlanFirst}
	return g, tk
}

func endScoperReady(g *rig, t *testing.T, tk *v1alpha1.Task, runID string, sp *envelope.Split) *v1alpha1.Task {
	t.Helper()
	g.runs.set(runID, "Succeeded")
	sc := &envelope.Scope{
		State:      envelope.ScopeReady,
		Task:       "Refactor the storage layer without changing public signatures.",
		Touches:    []string{"internal/store/store.go"},
		Leave:      []string{"api/"},
		Plan:       []string{"Extract interface", "Update callers"},
		Acceptance: []envelope.Acceptance{{Check: "go test ./...", Proves: "all tests pass"}},
	}
	g.log.evs = append(g.log.evs, envelope.Event{
		Seq:   int64(len(g.log.evs) + 1),
		RunID: runID,
		Type:  envelope.Handoff,
		Actor: envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + runID, Role: "triager"},
		Payload: envelope.Must(envelope.HandoffPayload{
			FromRole: "triager",
			ToRole:   "implementer",
			Summary:  "plan ready",
			Commit:   strings.Repeat("c", 40),
			Scope:    sc,
			Split:    sp,
		}),
	})
	g.log.end(runID, "Succeeded", "agent_finished")
	return g.reconcile(t, tk.Name, 1)
}

func appendComment(g *rig, issue int, c forge.Comment) {
	existing, _ := g.f.AllComments(context.Background(), issue)
	g.f.SetComments(issue, append(existing, c)...)
}

func TestThePlanGateHoldsNoRun(t *testing.T) {
	g, tk := planGateRig(t, "alice")
	// Received -> Triaged -> Queued -> Implementing (scoper)
	tk = g.reconcile(t, tk.Name, 3)
	if tk.Status.Phase != v1alpha1.PhaseImplementing || len(tk.Status.Runs) != 1 || tk.Status.Runs[0].Role != "triager" {
		t.Fatalf("expected scoper running: phase=%s runs=%+v", tk.Status.Phase, tk.Status.Runs)
	}
	tk = endScoperReady(g, t, tk, rid(0), nil)
	if tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != ReasonPlanReview {
		t.Fatalf("expected AwaitingHuman/plan_review, got %s/%s", tk.Status.Phase, tk.Status.Reason)
	}
	if !slices.Contains(g.f.Added(7), LabelPlanProposed) {
		t.Fatalf("expected label %q on issue, got %v", LabelPlanProposed, g.f.Added(7))
	}
	// No implementer run was started; only the finished scoper run exists.
	if len(g.runs.specs) != 1 {
		t.Fatalf("expected no run started while waiting on plan gate, got %d runs", len(g.runs.specs))
	}
	// Plan comment carries bounded task, expected touches, acceptance checks, and all three answers.
	comments := eventCommentBodies(g.f, 7)
	joined := strings.Join(comments, "\n")
	for _, want := range []string{
		"Refactor the storage layer",
		"internal/store/store.go",
		"go test ./...",
		"/factory plan approve",
		"/factory plan revise",
		"/factory plan split",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("plan comment missing %q:\n%s", want, joined)
		}
	}
	// A subsequent reconcile while unanswered starts no run.
	tk = g.reconcile(t, tk.Name, 1)
	if tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || len(g.runs.specs) != 1 {
		t.Fatalf("expected task to hold in plan_review with no new run: phase=%s runs=%d", tk.Status.Phase, len(g.runs.specs))
	}
}

func TestApproveRunsTheApprovedScope(t *testing.T) {
	g, tk := planGateRig(t, "alice")
	tk = g.reconcile(t, tk.Name, 3)
	tk = endScoperReady(g, t, tk, rid(0), nil)

	appendComment(g, 7, forge.Comment{ID: 101, Author: "alice", Body: "/factory plan approve", At: now.Add(5 * time.Minute)})
	// AwaitingHuman -> Queued -> Implementing (implementer, NOT a second scoper)
	tk = g.reconcile(t, tk.Name, 2)
	if tk.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatalf("expected Implementing after approve, got %s/%s", tk.Status.Phase, tk.Status.Reason)
	}
	if tk.Spec.Outcome != config.OutcomeChange {
		t.Fatalf("expected Spec.Outcome=change after approve, got %q", tk.Spec.Outcome)
	}
	if !slices.Contains(g.f.Removed(7), LabelPlanProposed) {
		t.Fatalf("expected label %q removed after approve, removed=%v", LabelPlanProposed, g.f.Removed(7))
	}
	if len(tk.Status.Runs) != 2 || tk.Status.Runs[1].Role != "implementer" {
		t.Fatalf("expected implementer run on approved scope, got %+v", tk.Status.Runs)
	}
	if !strings.Contains(g.runs.specs[rid(1)].TaskText, "Refactor the storage layer") {
		t.Fatalf("implementer brief did not carry approved scope:\n%s", g.runs.specs[rid(1)].TaskText)
	}
}

func TestReviseRunsTheScoperOnceMore(t *testing.T) {
	g, tk := planGateRig(t, "alice")
	tk = g.reconcile(t, tk.Name, 3)
	tk = endScoperReady(g, t, tk, rid(0), nil)

	appendComment(g, 7, forge.Comment{ID: 102, Author: "alice", Body: "/factory plan revise Keep the cache layer untouched", At: now.Add(5 * time.Minute)})
	// AwaitingHuman -> Queued -> Implementing (second scoper run with note)
	tk = g.reconcile(t, tk.Name, 2)
	if tk.Status.Phase != v1alpha1.PhaseImplementing || len(tk.Status.Runs) != 2 || tk.Status.Runs[1].Role != "triager" {
		t.Fatalf("expected second scoper run after revise: phase=%s runs=%+v", tk.Status.Phase, tk.Status.Runs)
	}
	if !strings.Contains(g.runs.specs[rid(1)].TaskText, "Keep the cache layer untouched") {
		t.Fatalf("second scoper brief did not include revision note:\n%s", g.runs.specs[rid(1)].TaskText)
	}
	// Second scoper finishes ready -> enters plan gate again.
	tk = endScoperReady(g, t, tk, rid(1), nil)
	if tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != ReasonPlanReview {
		t.Fatalf("expected plan gate again after revised scoper, got %s/%s", tk.Status.Phase, tk.Status.Reason)
	}
	if tk.Status.Plan == nil || tk.Status.Plan.Revisions != 1 {
		t.Fatalf("expected Revisions=1, got %+v", tk.Status.Plan)
	}
}

func TestASecondReviseGoesToAMaintainer(t *testing.T) {
	g, tk := planGateRig(t, "alice")
	tk = g.reconcile(t, tk.Name, 3)
	tk = endScoperReady(g, t, tk, rid(0), nil)

	// First revise -> runs scoper once more -> back to plan gate.
	appendComment(g, 7, forge.Comment{ID: 102, Author: "alice", Body: "/factory plan revise First adjustment", At: now.Add(5 * time.Minute)})
	tk = g.reconcile(t, tk.Name, 2)
	tk = endScoperReady(g, t, tk, rid(1), nil)

	// Second revise -> escalates to a maintainer instead of starting a third scoper.
	appendComment(g, 7, forge.Comment{ID: 103, Author: "alice", Body: "/factory plan revise Second adjustment", At: now.Add(10 * time.Minute)})
	tk = g.reconcile(t, tk.Name, 1)
	if tk.Status.Phase != v1alpha1.PhaseEscalated || tk.Status.Reason != ReasonPlanRevisionLimit {
		t.Fatalf("expected Escalated/%s on second revise, got %s/%s", ReasonPlanRevisionLimit, tk.Status.Phase, tk.Status.Reason)
	}
	if len(g.runs.specs) != 2 {
		t.Fatalf("expected no third run after second revise, got %d", len(g.runs.specs))
	}
}

func TestAnExpiredPlanGateSaysItIsParked(t *testing.T) {
	g, tk := planGateRig(t, "alice")
	tk = g.reconcile(t, tk.Name, 3)
	tk = endScoperReady(g, t, tk, rid(0), nil)

	// Advance clock past DefaultPlanGateHours (72h).
	clock := now.Add(73 * time.Hour)
	g.r.Now = func() time.Time { return clock }
	tk = g.reconcile(t, tk.Name, 1)

	// Stays AwaitingHuman/plan_review (never expires into a run or stop on its own).
	if tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != ReasonPlanReview {
		t.Fatalf("parked plan gate should stay AwaitingHuman/plan_review, got %s/%s", tk.Status.Phase, tk.Status.Reason)
	}
	if tk.Status.Plan == nil || !tk.Status.Plan.Parked {
		t.Fatalf("expected Plan.Parked=true, got %+v", tk.Status.Plan)
	}
	wantStatus := "parked: the plan was not answered in 72 hours; any of the three answers resumes it"
	if tk.Status.StatusComment == nil || !strings.Contains(tk.Status.StatusComment.Body, wantStatus) {
		t.Fatalf("status comment missing %q:\n%+v", wantStatus, tk.Status.StatusComment)
	}
	// One needs-you notification was posted, and reconciling again does not repeat it.
	parkedCount := 0
	for _, body := range eventCommentBodies(g.f, 7) {
		if strings.Contains(body, "needs you") {
			parkedCount++
		}
	}
	if parkedCount != 1 {
		t.Fatalf("expected 1 parked needs-you notification, got %d", parkedCount)
	}
	tk = g.reconcile(t, tk.Name, 1)
	parkedCount = 0
	for _, body := range eventCommentBodies(g.f, 7) {
		if strings.Contains(body, "needs you") {
			parkedCount++
		}
	}
	if parkedCount != 1 {
		t.Fatalf("expected parked notification posted once, got %d", parkedCount)
	}
	// Answering later still resumes it.
	appendComment(g, 7, forge.Comment{ID: 109, Author: "alice", Body: "/factory plan approve", At: clock.Add(time.Minute)})
	tk = g.reconcile(t, tk.Name, 2)
	if tk.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatalf("expected parked plan to resume on approve, got %s/%s", tk.Status.Phase, tk.Status.Reason)
	}
}

func TestPlanGateAutoApprovesAndSaysSo(t *testing.T) {
	g, tk := planGateRig(t, "alice")
	g.r.Cfg.Current().Repositories[0].Workflow.PlanGate = "auto"

	tk = g.reconcile(t, tk.Name, 3)
	tk = endScoperReady(g, t, tk, rid(0), nil)
	if tk.Status.Phase != v1alpha1.PhaseQueued {
		t.Fatalf("expected planGate: auto to queue implementer at once, got %s/%s", tk.Status.Phase, tk.Status.Reason)
	}
	if slices.Contains(g.f.Added(7), LabelPlanProposed) {
		t.Fatalf("auto plan gate should not apply %q", LabelPlanProposed)
	}
	foundRoomNote := false
	for _, s := range g.log.states {
		if strings.Contains(s, "planGate") && strings.Contains(s, "auto") {
			foundRoomNote = true
			break
		}
	}
	if !foundRoomNote {
		t.Fatalf("expected room note for planGate: auto, got %v", g.log.states)
	}
}

func TestOnlyTheAuthorOrAMaintainerAnswersThePlan(t *testing.T) {
	g, tk := planGateRig(t, "alice")
	tk = g.reconcile(t, tk.Name, 3)
	tk = endScoperReady(g, t, tk, rid(0), nil)

	// Random bystander cannot approve.
	appendComment(g, 7, forge.Comment{ID: 201, Author: "stranger", Body: "/factory plan approve", At: now.Add(5 * time.Minute)})
	tk = g.reconcile(t, tk.Name, 1)
	if tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != ReasonPlanReview {
		t.Fatalf("stranger should not be able to approve plan: %s/%s", tk.Status.Phase, tk.Status.Reason)
	}

	// Repository maintainer (Smana) can approve.
	appendComment(g, 7, forge.Comment{ID: 202, Author: "Smana", Body: "/factory plan approve", At: now.Add(6 * time.Minute)})
	tk = g.reconcile(t, tk.Name, 1)
	if tk.Status.Phase != v1alpha1.PhaseQueued {
		t.Fatalf("maintainer should be able to approve plan: %s/%s", tk.Status.Phase, tk.Status.Reason)
	}
}

func TestClassRequiringPlanMakesTaskPlanFirst(t *testing.T) {
	tk := issueTask("3buqdlot", 7, "# Update infra\n\n## Acceptance check\n`task check` passes.")
	tk.Spec.IssueAuthor = "alice"
	tk.Spec.Intake = &v1alpha1.TaskIntake{Class: "infra", Login: "Smana"}
	g := shippedRig(t, tk)
	g.r.Cfg.Current().Defaults.PredictedClass = "infra"
	g.r.Cfg.Current().Repositories[0].Classes["infra"] = config.Class{
		Shadow:       true,
		AllowedFiles: []string{`^infrastructure/.*$`},
		Requires:     []string{"plan"},
	}

	tk = g.reconcile(t, tk.Name, 3) // Received -> Triaged -> Queued -> Implementing (scoper)
	if tk.Spec.Outcome != config.OutcomePlanFirst {
		t.Fatalf("class requiring plan should set Outcome=plan-first, got %q", tk.Spec.Outcome)
	}
	tk = endScoperReady(g, t, tk, rid(0), nil)
	if tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != ReasonPlanReview {
		t.Fatalf("expected plan gate after scoper, got %s/%s", tk.Status.Phase, tk.Status.Reason)
	}
}
