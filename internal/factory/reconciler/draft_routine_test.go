// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/triage"
)

func draftRoutineTask(name, routine string) *v1alpha1.Task {
	return &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "agent-system",
			CreationTimestamp: metav1.NewTime(now.Add(-time.Minute)),
			Labels:            map[string]string{v1alpha1.LabelRoutine: routine},
		},
		Spec: v1alpha1.TaskSpec{
			Repository:  "Smana/cloud-native-ref",
			DataClass:   "public",
			MaxMicroUSD: 1_500_000,
			Source: v1alpha1.Source{
				Kind:          v1alpha1.SourceKindRoutine,
				Ref:           routine,
				Key:           "routine:" + routine + ":1",
				RequestedBy:   "system:scheduler",
				Trust:         "trusted",
				ContentSHA256: strings.Repeat("b", 64),
			},
			Text: "Remove unused helper.",
			Routine: &v1alpha1.TaskRoutine{
				Name:         routine,
				Mode:         config.RoutineModeDraft,
				PromptSource: "repo",
				PromptPath:   ".github/agent-factory/upkeep/" + routine + ".md",
				PromptSHA:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			},
		},
	}
}

func draftRig(t *testing.T, tk *v1alpha1.Task) *rig {
	t.Helper()
	objs := []client.Object{tk}
	if tk.Status.RoomRef != "" {
		objs = append(objs, roomOf(tk.Name))
	}
	g := newRig(t, objs...)
	g.r.Merger = g.f
	g.r.Triage = triage.Static{Template: "pair"}
	c := g.r.Cfg.Current()
	c.AgentsLogin, c.FactoryLogin = "ogenki-agents[bot]", "ogenki-agent-factory[bot]"
	c.Templates["pair"] = config.Template{Roles: []string{"implementer", "reviewer"}}
	c.Repositories[0].Classes = map[string]config.Class{"docs-links": {Live: true}, "review": {Live: true}}
	c.Repositories[0].Merge = config.Merge{
		RequiredChecks: required,
		VerifyChecks:   required,
		PolicyBotLogin: "ogenki-merge-gate[bot]",
		PolicyBot:      new(true),
		GateStatus:     new(false),
		MergerLogin:    "ogenki-agent-merger[bot]",
		MaxPerDay:      10,
		FixRuns:        2,
		VerifyFor:      config.Duration{Duration: 30 * time.Minute},
		RevertWindow:   config.Duration{Duration: 168 * time.Hour},
		Breaker:        config.Breaker{Window: 10, MaxReverts: 1},
	}
	c.Repositories[0].ResolvedRoutines = map[string]config.ResolvedRoutine{
		tk.Spec.Routine.Name: {
			Name:       tk.Spec.Routine.Name,
			Mode:       config.RoutineModeDraft,
			Cron:       "0 4 * * 1",
			DataClass:  "public",
			Workflow:   "pair",
			RepoPrompt: true,
		},
	}
	g.f.SetFile(tk.Spec.Routine.PromptPath, "## Scope\nCheck dead code.\n\n## Verify\n- go test ./...\n")
	return g
}

// Scenario 1 & 2: draft-mode routine runs implementer -> reviewer -> CI while draft, holds draft_pr
// after CI passes (ignoring policy-bot/approveToMerge), and re-enters without consuming GateReentries
// once marked Ready for review (even with maintainer engagement).
func TestDraftRoutineRunsReviewerAndCIHoldsAndReentersOnReady(t *testing.T) {
	tk := draftRoutineTask("draft23a", "dead-code")
	g := draftRig(t, tk)

	// Implementer starts with PRDraft=true and routine provenance.
	got := g.reconcile(t, tk.Name, 3)
	if got.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatalf("got phase=%s reason=%s, want Implementing", got.Status.Phase, got.Status.Reason)
	}
	implID := runID(tk.Name, 0)
	implSpec := g.runs.specs[implID]
	if !implSpec.PRDraft || implSpec.Routine != "dead-code" || implSpec.RoutinePromptSHA != "0123456789ab" {
		t.Fatalf("unexpected implementer spec: %+v", implSpec)
	}

	// Implementer opens a draft PR and succeeds.
	g.f.SetBranch("agent/"+tk.Name, 12)
	g.f.SetActivity("agent/"+tk.Name, forge.Push{Type: "branch_creation", Actor: "ogenki-agents[bot]", After: "abc1234", At: now})
	g.f.SetPR(forge.PR{
		Number: 12, NodeID: "PR_12", URL: "https://github.com/Smana/cloud-native-ref/pull/12",
		State: "OPEN", Draft: true, Title: "chore: remove dead helper", Author: "ogenki-agents[bot]",
		BaseRef: "main", HeadRef: "agent/" + tk.Name, HeadSHA: "abc1234", HeadCommittedAt: now.Add(-time.Minute),
		HeadMessage: "chore: remove dead helper\n\nAgent-Run: " + implID,
	})
	g.f.SetFiles(map[string]string{testFile: "[x](https://example.com/old)"}, map[string]string{testFile: "[x](https://example.com/new)"})
	g.runs.set(implID, "Succeeded")
	g.log.handoff(implID, "abc1234")
	g.log.end(implID, "Succeeded", "agent_finished")

	// Reviewer starts on the draft PR (PRDraft=false on reviewer).
	got = g.reconcile(t, tk.Name, 2)
	if got.Status.Phase != v1alpha1.PhaseReviewing || got.Status.PullRequest == nil || !got.Status.PullRequest.WasDraft {
		t.Fatalf("after implementer: phase=%s pr=%+v", got.Status.Phase, got.Status.PullRequest)
	}
	revID := runID(tk.Name, 1)
	revSpec := g.runs.specs[revID]
	if revSpec.Role != "reviewer" || revSpec.PRDraft || revSpec.Routine != "dead-code" {
		t.Fatalf("unexpected reviewer spec: %+v", revSpec)
	}

	// Reviewer approves while PR is still draft -> AwaitingCI.
	g.runs.set(revID, "Succeeded")
	g.log.verdict(revID, "approve", "abc1234", "LGTM")
	g.log.end(revID, "Succeeded", "agent_finished")
	got = g.reconcile(t, tk.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseAwaitingCI {
		t.Fatalf("after reviewer approve: phase=%s reason=%s", got.Status.Phase, got.Status.Reason)
	}

	// While CI is pending, gate waits as ci_pending.
	g.f.SetChecks(12, forge.Checks{Runs: []forge.Check{
		{Name: required[0], State: "PENDING"},
		{Name: required[1], State: "SUCCESS"},
	}})
	got = g.reconcile(t, tk.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseAwaitingCI || got.Status.Reason != "ci_pending" {
		t.Fatalf("ci pending on draft: %s/%s", got.Status.Phase, got.Status.Reason)
	}

	// Once CI is green, gate holds draft_pr even if policy-bot is pending and a maintainer approved.
	g.r.Cfg.Current().Repositories[0].Merge.ApproveToMerge = true
	pr, _ := g.f.PullRequest(t.Context(), 12)
	pr.Reviews = []forge.Review{{ID: 77, Author: "Smana", State: "APPROVED", Commit: "abc1234", At: now.Add(10 * time.Minute)}}
	g.f.SetPR(pr)
	g.f.SetChecks(12, green("PENDING"))
	got = g.reconcile(t, tk.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseAwaitingHuman || got.Status.Reason != "draft_pr" || len(g.f.Armed()) != 0 {
		t.Fatalf("after green CI on draft: %s/%s armed=%v", got.Status.Phase, got.Status.Reason, g.f.Armed())
	}

	// Approval alone while still draft must NOT bounce back to AwaitingCI.
	got = g.reconcile(t, tk.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseAwaitingHuman || got.Status.Reason != "draft_pr" {
		t.Fatalf("draft PR bounced on approval while still draft: %s/%s", got.Status.Phase, got.Status.Reason)
	}

	// Maintainer leaves a comment, GateReentries is at maxGateReentries, and marks PR ready for review.
	pr, _ = g.f.PullRequest(t.Context(), 12)
	pr.Comments = []forge.Comment{{ID: 88, Author: "Smana", Body: "Ready to ship", At: now.Add(11 * time.Minute)}}
	g.f.SetPR(pr)
	g.f.SetDraft(12, false)
	{
		var cur v1alpha1.Task
		if err := g.c.Get(t.Context(), client.ObjectKey{Namespace: "agent-system", Name: tk.Name}, &cur); err != nil {
			t.Fatal(err)
		}
		cur.Status.GateReentries = maxGateReentries
		if err := g.c.Status().Update(t.Context(), &cur); err != nil {
			t.Fatal(err)
		}
	}

	got = g.reconcile(t, tk.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseAwaitingCI || got.Status.Reason != "reentered_ready" || got.Status.GateReentries != maxGateReentries {
		t.Fatalf("after ready for review: phase=%s reason=%s reentries=%d", got.Status.Phase, got.Status.Reason, got.Status.GateReentries)
	}

	// Next gate pass enters normal MergePolicy evaluation and merges!
	g.f.SetChecks(12, green("SUCCESS"))
	got = g.reconcile(t, tk.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseVerifying && got.Status.Phase != v1alpha1.PhaseAutoMerging && len(g.f.Armed()) == 0 {
		t.Fatalf("expected normal merge after ready, got %s/%s armed=%v", got.Status.Phase, got.Status.Reason, g.f.Armed())
	}
}

// Scenario 3: draft-mode routine opening a non-draft PR from the start hands off draft_mode_mismatch
// before CI/arming, and does not return on approval. Also verifies !NeedsMerger() path.
func TestDraftRoutineMismatchHandsOffBeforeArmAndBlocksApprovalReturn(t *testing.T) {
	tk := draftRoutineTask("draft23b", "dead-code")
	tk.Spec.Template = "solo"
	tk.Spec.PredictedClass = "docs-links"
	tk.Status = v1alpha1.TaskStatus{
		Phase:   v1alpha1.PhaseAwaitingCI,
		RoomRef: tk.Name,
		PullRequest: &v1alpha1.PullRequestRef{
			Number:  12,
			URL:     "https://github.com/Smana/cloud-native-ref/pull/12",
			NodeID:  "PR_12",
			HeadSHA: "abc",
		},
		Runs: []v1alpha1.RunRecord{{ID: runID(tk.Name, 0), Role: "implementer", Trigger: "initial", Phase: "Succeeded"}},
	}
	g := draftRig(t, tk)
	g.r.Cfg.Current().Repositories[0].Merge.ApproveToMerge = true
	g.f.SetPR(forge.PR{
		Number: 12, NodeID: "PR_12", State: "OPEN", Draft: false,
		Author: "ogenki-agents[bot]", BaseRef: "main", HeadRef: "agent/" + tk.Name, HeadSHA: "abc",
		HeadMessage: "fix\n\nAgent-Run: " + runID(tk.Name, 0),
		Reviews:     []forge.Review{{ID: 77, Author: "Smana", State: "APPROVED", Commit: "abc", At: now}},
	})
	g.f.SetChecks(12, green("SUCCESS"))
	g.log.handoff(runID(tk.Name, 0), "abc")

	got := g.reconcile(t, tk.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseAwaitingHuman || got.Status.Reason != "draft_mode_mismatch" || len(g.f.Armed()) != 0 {
		t.Fatalf("non-draft from start: %s/%s armed=%v", got.Status.Phase, got.Status.Reason, g.f.Armed())
	}
	// Approval must not return draft_mode_mismatch.
	got = g.reconcile(t, tk.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseAwaitingHuman || got.Status.Reason != "draft_mode_mismatch" {
		t.Fatalf("draft_mode_mismatch returned on approval: %s/%s", got.Status.Phase, got.Status.Reason)
	}

	// With merge gate off (!NeedsMerger()), draft_mode_mismatch and draft_pr still take precedence over merge_gate_off.
	tkOff := draftRoutineTask("draft23c", "dead-code")
	tkOff.Spec.Template = "solo"
	tkOff.Spec.PredictedClass = "docs-links"
	tkOff.Status = v1alpha1.TaskStatus{
		Phase:       v1alpha1.PhaseAwaitingCI,
		RoomRef:     tkOff.Name,
		PullRequest: &v1alpha1.PullRequestRef{Number: 13, URL: "https://github.com/Smana/cloud-native-ref/pull/13", HeadSHA: "abc"},
		Runs:        []v1alpha1.RunRecord{{ID: runID(tkOff.Name, 0), Role: "implementer", Trigger: "initial", Phase: "Succeeded"}},
	}
	gOff := newRig(t, tkOff, roomOf(tkOff.Name))
	gOff.r.Cfg.Current().Repositories[0].Classes = map[string]config.Class{"docs-links": {Live: false}}
	gOff.f.SetPR(forge.PR{Number: 13, State: "OPEN", Draft: false, HeadRef: "agent/" + tkOff.Name, HeadSHA: "abc"})
	gotOff := gOff.reconcile(t, tkOff.Name, 1)
	if gotOff.Status.Phase != v1alpha1.PhaseAwaitingHuman || gotOff.Status.Reason != "draft_mode_mismatch" {
		t.Fatalf("gate off non-draft: %s/%s", gotOff.Status.Phase, gotOff.Status.Reason)
	}
}

// Scenario 4: Marking a PR Ready early during PhaseReviewing or ci_pending does NOT trigger draft_mode_mismatch.
func TestDraftRoutineEarlyReadyDoesNotMismatch(t *testing.T) {
	tk := draftRoutineTask("draft23d", "dead-code")
	tk.Spec.Template = "solo"
	tk.Spec.PredictedClass = "docs-links"
	tk.Status = v1alpha1.TaskStatus{
		Phase:   v1alpha1.PhaseAwaitingCI,
		RoomRef: tk.Name,
		PullRequest: &v1alpha1.PullRequestRef{
			Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", NodeID: "PR_12", HeadSHA: "abc",
		},
		Runs: []v1alpha1.RunRecord{{ID: runID(tk.Name, 0), Role: "implementer", Trigger: "initial", Phase: "Succeeded"}},
	}
	g := draftRig(t, tk)
	g.f.SetPR(forge.PR{
		Number: 12, NodeID: "PR_12", State: "OPEN", Draft: true,
		Author: "ogenki-agents[bot]", BaseRef: "main", HeadRef: "agent/" + tk.Name, HeadSHA: "abc",
		HeadMessage: "fix\n\nAgent-Run: " + runID(tk.Name, 0),
	})
	g.f.SetFiles(map[string]string{testFile: "[x](https://example.com/old)"}, map[string]string{testFile: "[x](https://example.com/new)"})
	g.f.SetChecks(12, forge.Checks{Runs: []forge.Check{{Name: required[0], State: "PENDING"}, {Name: required[1], State: "SUCCESS"}}})
	g.log.handoff(runID(tk.Name, 0), "abc")

	// First pass observes Draft=true during ci_pending -> stamps WasDraft=true.
	got := g.reconcile(t, tk.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseAwaitingCI || got.Status.Reason != "ci_pending" || !got.Status.PullRequest.WasDraft {
		t.Fatalf("ci_pending pass: %s/%s wasDraft=%v", got.Status.Phase, got.Status.Reason, got.Status.PullRequest.WasDraft)
	}

	// Maintainer marks Ready before CI finishes, then CI passes -> merges normally without draft_mode_mismatch.
	g.f.SetDraft(12, false)
	g.f.SetChecks(12, green("SUCCESS"))
	got = g.reconcile(t, tk.Name, 1)
	if got.Status.Reason == "draft_mode_mismatch" || (got.Status.Phase != v1alpha1.PhaseVerifying && len(g.f.Armed()) == 0) {
		t.Fatalf("early ready triggered mismatch or stalled: %s/%s armed=%v", got.Status.Phase, got.Status.Reason, g.f.Armed())
	}
}

// Scenario 5: Draft PR suppresses 48h @maintainer reminders, while 14d stale close still fires unless
// a maintainer acts or approves.
func TestDraftPRSuppresses48hRemindersAndPreserves14dStaleClose(t *testing.T) {
	newDraftAwaiting := func(name string, age time.Duration) (*v1alpha1.Task, *rig) {
		tk := draftRoutineTask(name, "dead-code")
		tk.Spec.Issue = 7
		tk.Spec.Template = "solo"
		tk.Spec.PredictedClass = "review"
		since := metav1.NewTime(now.Add(-age))
		tk.Status = v1alpha1.TaskStatus{
			Phase:      v1alpha1.PhaseAwaitingHuman,
			Reason:     "draft_pr",
			PhaseSince: &since,
			RoomRef:    tk.Name,
			PullRequest: &v1alpha1.PullRequestRef{
				Number:   12,
				URL:      "https://github.com/Smana/cloud-native-ref/pull/12",
				HeadSHA:  "abc",
				WasDraft: true,
			},
			Runs: []v1alpha1.RunRecord{{ID: runID(tk.Name, 0), Role: "implementer", Trigger: "initial", Phase: "Succeeded"}},
		}
		g := draftRig(t, tk)
		g.f.SetPR(forge.PR{
			Number: 12, State: "OPEN", Draft: true,
			HeadRef: "agent/" + tk.Name, HeadSHA: "abc",
		})
		return tk, g
	}

	// Subcase A: 49h and 13d post 0 reminders; 14d closes as stale without posting any reminder.
	tk, g := newDraftAwaiting("draft23e", 49*time.Hour)
	got := g.reconcile(t, tk.Name, 2)
	if got.Status.Phase != v1alpha1.PhaseAwaitingHuman || len(eventCommentBodies(g.f, 7)) != 0 || len(got.Status.Outbox) != 0 {
		t.Fatalf("49h draft reminder leaked: phase=%s comments=%q outbox=%+v", got.Status.Phase, eventCommentBodies(g.f, 7), got.Status.Outbox)
	}
	g.r.Now = func() time.Time { return now.Add(11 * 24 * time.Hour) } // ~13d total
	got = g.reconcile(t, tk.Name, 2)
	if got.Status.Phase != v1alpha1.PhaseAwaitingHuman || g.f.Closed(12) || len(eventCommentBodies(g.f, 7)) != 0 {
		t.Fatalf("13d draft closed early or reminded: phase=%s closed=%v comments=%q", got.Status.Phase, g.f.Closed(12), eventCommentBodies(g.f, 7))
	}
	g.r.Now = func() time.Time { return now.Add(12*24*time.Hour + time.Minute) } // >14d total
	got = g.reconcile(t, tk.Name, 1)
	if got.Status.Phase != v1alpha1.PhaseClosed || got.Status.Reason != "stale" || !g.f.Closed(12) || !slices.Contains(g.f.Added(12), "factory/stale") {
		t.Fatalf("14d draft stale close failed: phase=%s reason=%s closed=%v labels=%v", got.Status.Phase, got.Status.Reason, g.f.Closed(12), g.f.Added(12))
	}
	for _, c := range eventCommentBodies(g.f, 7) {
		if strings.Contains(c, "no maintainer activity") {
			t.Fatalf("unexpected reminder comment posted for draft PR: %q", c)
		}
	}

	// Subcase B: maintainer activity within 14d prevents stale close.
	tkAct, gAct := newDraftAwaiting("draft23f", 15*24*time.Hour)
	pr, _ := gAct.f.PullRequest(t.Context(), 12)
	pr.Comments = []forge.Comment{{ID: 500, Author: "Smana", Body: "keeping this draft", At: now.Add(-2 * 24 * time.Hour)}}
	gAct.f.SetPR(pr)
	gotAct := gAct.reconcile(t, tkAct.Name, 2)
	if gotAct.Status.Phase != v1alpha1.PhaseAwaitingHuman || gAct.f.Closed(12) {
		t.Fatalf("maintainer comment failed to keep draft PR open: %s closed=%v", gotAct.Status.Phase, gAct.f.Closed(12))
	}

	// Subcase C: maintainer approval prevents stale close.
	tkApp, gApp := newDraftAwaiting("draft23g", 15*24*time.Hour)
	prApp, _ := gApp.f.PullRequest(t.Context(), 12)
	prApp.Reviews = []forge.Review{{ID: 501, Author: "Smana", State: "APPROVED", Commit: "abc", At: now.Add(-15 * 24 * time.Hour)}}
	gApp.f.SetPR(prApp)
	gotApp := gApp.reconcile(t, tkApp.Name, 2)
	if gotApp.Status.Phase != v1alpha1.PhaseAwaitingHuman || gApp.f.Closed(12) {
		t.Fatalf("maintainer approval failed to prevent draft stale close: %s closed=%v", gotApp.Status.Phase, gApp.f.Closed(12))
	}
}
