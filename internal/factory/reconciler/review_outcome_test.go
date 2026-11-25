// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/minter"
	"github.com/Smana/maku/internal/hardening"
)

func reviewOutcomeRig(t *testing.T, pr forge.PR) (*rig, *v1alpha1.Task) {
	t.Helper()
	tk := &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot", Namespace: "agent-system"},
		Spec: v1alpha1.TaskSpec{
			Source: v1alpha1.Source{
				Kind:          "review",
				Ref:           "Smana/cloud-native-ref#42",
				Key:           "github:review:Smana/cloud-native-ref#42:gen1",
				RequestedBy:   "github:Smana",
				Trust:         "untrusted",
				ContentSHA256: strings.Repeat("a", 64),
			},
			Repository:  "Smana/cloud-native-ref",
			IssueAuthor: pr.Author,
			Text:        "# Add caching\n\nAdds an in-memory cache.",
			DataClass:   "public",
			Outcome:     config.OutcomeReview,
		},
	}
	g := shippedRig(t, tk)
	g.r.Cfg.Current().Outcomes = []string{config.OutcomeChange, config.OutcomeReview}
	g.f.SetPR(pr)
	return g, tk
}

func foreignPR() forge.PR {
	return forge.PR{
		Number:  42,
		URL:     "https://github.com/Smana/cloud-native-ref/pull/42",
		Title:   "Add caching",
		Body:    "Adds an in-memory cache.",
		State:   "OPEN",
		Author:  "contributor",
		BaseRef: "main",
		HeadRef: "feat/cache",
		HeadSHA: head,
	}
}

func TestAReviewTaskRunsTheReviewerAlone(t *testing.T) {
	pr := foreignPR()
	g, tk := reviewOutcomeRig(t, pr)

	// Received -> Triaged -> Queued -> Reviewing (reviewer alone)
	tk = g.reconcile(t, tk.Name, 3)
	if tk.Status.Phase != v1alpha1.PhaseReviewing {
		t.Fatalf("expected Reviewing, got %s/%s", tk.Status.Phase, tk.Status.Reason)
	}
	if tk.Spec.Template != "review" || tk.Spec.Outcome != config.OutcomeReview {
		t.Fatalf("template=%q outcome=%q, want review/review", tk.Spec.Template, tk.Spec.Outcome)
	}
	if len(g.runs.specs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(g.runs.specs))
	}
	spec := g.runs.specs[rid(0)]
	if spec.Role != "reviewer" || spec.TaskURL != pr.URL || spec.BaseRef != pr.HeadRef {
		t.Fatalf("unexpected reviewer spec: %+v", spec)
	}

	// Reviewer posts a verdict ("changes") in the room and finishes.
	g.log.verdict(rid(0), "changes", pr.HeadSHA, "Cache eviction skips expired entries.")
	g.runs.set(rid(0), "Succeeded")
	g.log.end(rid(0), "Succeeded", "agent_finished")

	tk = g.reconcile(t, tk.Name, 1)
	if tk.Status.Phase != v1alpha1.PhaseDone || tk.Status.Reason != ReasonReviewPosted {
		t.Fatalf("expected Done/review_posted, got %s/%s", tk.Status.Phase, tk.Status.Reason)
	}
	// Never opens a second run, never pushes, never merges.
	if len(g.runs.specs) != 1 {
		t.Fatalf("expected exactly 1 run (never a second run), got %d", len(g.runs.specs))
	}
	comments := g.f.Comments(42)
	var verdicts []string
	for _, body := range comments {
		if strings.Contains(body, "event=review-posted") {
			verdicts = append(verdicts, body)
		}
	}
	if len(verdicts) != 1 {
		t.Fatalf("expected 1 verdict comment on PR #42, got %d (all: %v)", len(verdicts), comments)
	}
	if !strings.Contains(verdicts[0], "changes") || !strings.Contains(verdicts[0], "Cache eviction skips expired entries") {
		t.Fatalf("verdict comment missing details: %q", verdicts[0])
	}
}

func TestAReviewTaskNeverGetsAWriteToken(t *testing.T) {
	pr := foreignPR()
	g, tk := reviewOutcomeRig(t, pr)
	_ = g.reconcile(t, tk.Name, 3)

	if len(g.runs.specs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(g.runs.specs))
	}
	for id, spec := range g.runs.specs {
		if spec.Role != "reviewer" {
			t.Fatalf("run %s has role %q, want read-only reviewer", id, spec.Role)
		}
		perms, err := minter.Roles.For(spec.Role, hardening.Types())
		if err != nil {
			t.Fatalf("minter.Roles.For(%q): %v", spec.Role, err)
		}
		for scope, level := range perms {
			if level == "write" {
				t.Fatalf("reviewer run %s granted write permission %s=%s", id, scope, level)
			}
		}
	}
}

func TestTheGateIgnoresAVerdictOnAForeignPullRequest(t *testing.T) {
	cfg := armCfg()
	cfg.Templates["review"] = config.Template{Roles: []string{"reviewer"}}
	cfg.Repositories[0].Classes["deps-patch"] = config.Class{
		Live:      true,
		DiffShape: "semver-patch",
		Author:    &config.Author{Adopt: &config.Adopt{Logins: []string{"renovate[bot]"}, Branches: []string{"^renovate/"}}},
	}

	// Case 1: human-authored PR with a factory reviewer approve verdict on an agents class.
	foreign := agentPR()
	foreign.Author = "contributor"
	tk := armTask("docs-links", "pair", "approve")
	verifs := []v1alpha1.RunRecord{{ID: "r3vnwr3v", Role: "reviewer", Trigger: "review", Verdict: "approve", HeadSHA: head}}
	d := DecideArm(ArmInputs{
		PR:           foreign,
		Checks:       green("SUCCESS"),
		Task:         tk,
		Cfg:          cfg,
		ReportedHead: head,
		Verifiers:    verifs,
		DiffRead:     true,
	})
	if d.Verdict != "human" || d.Reason != "not_agent_authored" {
		t.Fatalf("foreign PR armed or wrong refusal: %+v", d)
	}

	// Case 2: a review-outcome task with an approve verdict on an adopted-author PR never arms either.
	reviewTask := &v1alpha1.Task{
		Spec: v1alpha1.TaskSpec{
			Source:         v1alpha1.Source{Kind: "review", Ref: "Smana/cloud-native-ref#42"},
			PredictedClass: "deps-patch",
			Template:       "review",
			Outcome:        config.OutcomeReview,
		},
		Status: v1alpha1.TaskStatus{
			Verdict: "approve",
			Runs:    verifs,
		},
	}
	adoptedPR := forge.PR{
		Author:  "renovate[bot]",
		BaseRef: "main",
		HeadRef: "renovate/go-mod",
		HeadSHA: head,
	}
	d2 := DecideArm(ArmInputs{
		PR:        adoptedPR,
		Checks:    green("SUCCESS"),
		Task:      reviewTask,
		Cfg:       cfg,
		Verifiers: verifs,
		Adoption:  Adoption{Read: true, CommitAuthors: []string{"renovate[bot]"}, Pushed: pushFactory},
		DiffRead:  true,
	})
	if d2.Verdict != "human" || d2.Reason != "not_agent_authored" {
		t.Fatalf("review task armed or wrong refusal: %+v", d2)
	}
}

// Live on gcp-0 (Maku v0.54.1): factory/review on a person's pull request, head
// docs/runbook-workflow-comparison-32, escalated loop_unsupported with no run, because the reviewer's
// claim named the head as spec.branch and the AgentRun schema admits agent/* only. The reviewer
// starts from the head (baseRef) on its own agent/<task> branch, and the read-only token stays.
func TestAReviewOfAPersonsBranchStartsTheReviewer(t *testing.T) {
	pr := foreignPR()
	pr.Author, pr.HeadRef = "Smana", "docs/runbook-workflow-comparison-32"
	g, tk := reviewOutcomeRig(t, pr)
	tk = g.reconcile(t, tk.Name, 3)
	if tk.Status.Phase != v1alpha1.PhaseReviewing {
		t.Fatalf("expected Reviewing, got %s/%s", tk.Status.Phase, tk.Status.Reason)
	}
	spec, ok := g.runs.specs[rid(0)]
	if !ok || spec.Role != "reviewer" || spec.BaseRef != pr.HeadRef || spec.Branch != "agent/"+tk.Name || spec.Head != pr.HeadSHA {
		t.Fatalf("unexpected reviewer spec: %+v", spec)
	}
}
