// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

// shadowHandOff is a shadow class's hand-off (shadow_would_arm) with merge.approveToMerge off: the
// demo's state before its owner approved the pull request on GitHub.
func shadowHandOff(t *testing.T) *rig {
	t.Helper()
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.r.Cfg.Current().Classes["docs-links"] = config.Class{Shadow: true}
	g.f.SetChecks(12, green("SUCCESS"))
	if got := g.reconcile(t, "3buqdlot", 1); got.Status.Phase != v1alpha1.PhaseAwaitingHuman || got.Status.Reason != "shadow_would_arm" {
		t.Fatalf("shadow: %s %s", got.Status.Phase, got.Status.Reason)
	}
	return g
}

func approve(g *rig, t *testing.T, id int64, commit string) {
	t.Helper()
	pr, _ := g.f.PullRequest(t.Context(), 12)
	pr.Reviews = append(pr.Reviews, forge.Review{ID: id, Author: "Smana", State: "APPROVED", Commit: commit, At: now.Add(time.Minute)})
	g.f.SetPR(pr)
}

// count is how many of the issue's comments carry s.
func count(g *rig, s string) int {
	n := 0
	for _, c := range g.f.Comments(7) {
		if strings.Contains(c, s) {
			n++
		}
	}
	return n
}

// approvalOf is the approval the task's status shows, nil without one.
func approvalOf(tk *v1alpha1.Task) *v1alpha1.ApprovalRef { return tk.Status.PullRequest.Approval }

// Live in the owner's demo: with approveToMerge off an approval is no return to the gate, so the room
// never said a maintainer had approved. It now says who approved which commit, once; the hand-off's
// reason and its own next step stand, and nothing merges.
func TestAnApprovalIsShownWithApproveToMergeOff(t *testing.T) {
	g := shadowHandOff(t)
	approve(g, t, 77, "abc")
	got := g.reconcile(t, "3buqdlot", 1)
	if got.Status.Phase != v1alpha1.PhaseAwaitingHuman || got.Status.Reason != "shadow_would_arm" {
		t.Fatalf("the reason stands: %s %s", got.Status.Phase, got.Status.Reason)
	}
	if a := approvalOf(got); a == nil || a.By != "Smana" || a.Commit != "abc" || a.Stale != "" {
		t.Fatalf("shown: %+v", a)
	}
	if a := g.f.Armed(); len(a) != 0 {
		t.Fatalf("off, an approval merges nothing: %q", a)
	}
	if n := count(g, "approved by @Smana at commit `abc`"); n != 1 {
		t.Fatalf("narrated %d times: %q", n, g.f.Comments(7))
	}
	if pr := lastFacts(g).PR; pr == nil || pr.ApprovedBy != "Smana" || pr.ApprovedCommit != "abc" || pr.ApprovalStale != "" {
		t.Fatalf("the room's facts: %+v", pr)
	}
	got = g.reconcile(t, "3buqdlot", 2)
	if got.Status.Reason != "shadow_would_arm" || count(g, "approved by @Smana") != 1 || len(g.f.Armed()) != 0 {
		t.Fatalf("a second reconcile: %s, %q, merged %q", got.Status.Reason, g.f.Comments(7), g.f.Armed())
	}
}

// A push after the approval leaves it on an older commit: shown stale, said once, with no bypass;
// the reason stands and nothing merges.
func TestAPushAfterAShownApprovalShowsItStale(t *testing.T) {
	g := shadowHandOff(t)
	approve(g, t, 77, "abc")
	g.reconcile(t, "3buqdlot", 1)
	pr, _ := g.f.PullRequest(t.Context(), 12)
	pr.HeadSHA = "def"
	g.f.SetPR(pr)
	got := g.reconcile(t, "3buqdlot", 2)
	if a := approvalOf(got); got.Status.Reason != "shadow_would_arm" || a == nil || a.Stale != "older_commit" || a.Commit != "abc" {
		t.Fatalf("stale: %s %+v", got.Status.Reason, a)
	}
	var stale []string
	for _, c := range g.f.Comments(7) {
		if strings.Contains(c, "approval is of commit `abc`, an older commit: new commits since") {
			stale = append(stale, c)
		}
	}
	if len(stale) != 1 || strings.Contains(stale[0], "--admin") {
		t.Fatalf("the stale narration, once and without the bypass: %q", stale)
	}
	if a := g.f.Armed(); len(a) != 0 {
		t.Fatalf("merged %q", a)
	}
	// Approving the new head shows it again, as a new approval.
	approve(g, t, 78, "def")
	if got = g.reconcile(t, "3buqdlot", 1); approvalOf(got) == nil || approvalOf(got).Stale != "" || count(g, "approved by @Smana at commit `def`") != 1 {
		t.Fatalf("re-approved: %+v %q", approvalOf(got), g.f.Comments(7))
	}
}

// An approval GitHub dismissed (dismiss-stale-reviews on push, or by hand) is no longer shown, nor is
// one a later request for changes vetoes.
func TestADismissedApprovalIsNoLongerShown(t *testing.T) {
	g := shadowHandOff(t)
	approve(g, t, 77, "abc")
	g.reconcile(t, "3buqdlot", 1)
	pr, _ := g.f.PullRequest(t.Context(), 12)
	pr.Reviews[0].State = "DISMISSED"
	g.f.SetPR(pr)
	got := g.reconcile(t, "3buqdlot", 1)
	if approvalOf(got) != nil || got.Status.Reason != "shadow_would_arm" {
		t.Fatalf("dismissed: %s %+v", got.Status.Reason, approvalOf(got))
	}
	if f := lastFacts(g).PR; f == nil || f.ApprovedBy != "" || f.ApprovedCommit != "" {
		t.Fatalf("the facts still show it: %+v", f)
	}
	pr.Reviews[0].State, pr.ReviewsTruncated = "APPROVED", true // a cut list may hide a veto
	g.f.SetPR(pr)
	if got = g.reconcile(t, "3buqdlot", 1); approvalOf(got) != nil {
		t.Fatalf("vetoed: %+v", approvalOf(got))
	}
}

// C1: an approval that predates a policy_pending hand-off is shown, and the hand-off still returns to
// the gate once policy-bot passes.
func TestAShownApprovalKeepsThePolicyReturn(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	approve(g, t, 77, "abc")
	pr, _ := g.f.PullRequest(t.Context(), 12)
	pr.Reviews[0].At = now.Add(-time.Hour)
	g.f.SetPR(pr)
	g.f.SetChecks(12, green("PENDING"))
	if got := g.reconcile(t, "3buqdlot", 2); got.Status.Reason != "policy_pending" || approvalOf(got) == nil {
		t.Fatalf("hand-off: %s %+v", got.Status.Reason, approvalOf(got))
	}
	g.f.SetChecks(12, green("SUCCESS"))
	if got := g.reconcile(t, "3buqdlot", 1); got.Status.Phase != v1alpha1.PhaseAwaitingCI || got.Status.Reason != "reentered_policy" {
		t.Fatalf("the return is kept: %s %s", got.Status.Phase, got.Status.Reason)
	}
}

// I1: a by-hand reason keeps its reason and its guidance; the approval only joins it.
func TestAShownApprovalKeepsAByHandReason(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingHuman)
	g.handedOff(t, "not_mergeable", 0)
	approve(g, t, 77, "abc")
	got := g.reconcile(t, "3buqdlot", 1)
	if got.Status.Reason != "not_mergeable" || approvalOf(got) == nil {
		t.Fatalf("%s %+v", got.Status.Reason, approvalOf(got))
	}
	if c := strings.Join(g.f.Comments(7), "\n"); !strings.Contains(c, "approved by @Smana at commit `abc`") ||
		!strings.Contains(c, "GitHub does not allow merging it as it stands") {
		t.Fatalf("the reason's guidance is kept: %q", c)
	}
}

// M1: the gate's own hand-off already said maintainer_approved: the shown approval adds no comment.
func TestAnApprovalTheHandOffSaidIsNotSaidAgain(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.r.Cfg.Current().Classes["docs-links"] = config.Class{Shadow: true}
	g.f.SetChecks(12, green("SUCCESS"))
	approve(g, t, 77, "abc")
	got := g.reconcile(t, "3buqdlot", 1)
	if got.Status.Reason != "maintainer_approved" {
		t.Fatalf("hand-off: %s %s", got.Status.Phase, got.Status.Reason)
	}
	before := len(g.f.Comments(7))
	got = g.reconcile(t, "3buqdlot", 2)
	if approvalOf(got) == nil || len(g.f.Comments(7)) != before {
		t.Fatalf("%+v %q", approvalOf(got), g.f.Comments(7))
	}
}

// M3: an approval submitted before the task's last pushing run ended did not read that run's push,
// whatever commit GitHub moved it to (a force push): it is shown stale.
func TestAnApprovalBeforeTheRunsEndedIsStale(t *testing.T) {
	g := shadowHandOff(t)
	approve(g, t, 77, "abc")
	pr, _ := g.f.PullRequest(t.Context(), 12)
	pr.Reviews[0].At = now.Add(-time.Hour) // the run ended at now-30m
	g.f.SetPR(pr)
	if a := approvalOf(g.reconcile(t, "3buqdlot", 1)); a == nil || a.Stale != "before_runs_ended" || a.Commit != "abc" {
		t.Fatalf("%+v", a)
	}
	c := strings.Join(g.f.Comments(7), "\n")
	if !strings.Contains(c, "@Smana approved commit `abc` before the factory's last push ended: approve it again") ||
		strings.Contains(c, "new commits since") || strings.Contains(c, "--admin") {
		t.Fatalf("the early approval's words: %q", c)
	}
}

// M2: a task whose class is adopted keeps today's path: nothing is shown.
func TestNoApprovalIsShownForAnAdoptedClass(t *testing.T) {
	g := shadowHandOff(t)
	g.r.Cfg.Current().Classes["docs-links"] = config.Class{Shadow: true, Author: &config.Author{Adopt: &config.Adopt{Logins: []string{"renovate[bot]"}, Branches: []string{"^renovate/"}}}}
	approve(g, t, 77, "abc")
	if a := approvalOf(g.reconcile(t, "3buqdlot", 1)); a != nil {
		t.Fatalf("%+v", a)
	}
}

// A maintainer's newer request for changes is a veto: no approval is shown over it; nor is anyone
// else's approval, or one that names no commit.
func TestAVetoHidesTheApproval(t *testing.T) {
	if _, st := shownApproval(forge.PR{HeadSHA: "abc", Reviews: []forge.Review{
		{ID: 1, Author: "Smana", State: "APPROVED", Commit: "abc", At: now},
		{ID: 2, Author: "Smana", State: "CHANGES_REQUESTED", Commit: "abc", At: now.Add(time.Second)},
	}}, &config.Repository{Maintainers: []string{"Smana"}}, now.Add(-time.Hour)); st != "veto" {
		t.Fatalf("got %q", st)
	}
	if _, st := shownApproval(forge.PR{HeadSHA: "abc", Reviews: []forge.Review{
		{ID: 1, Author: "alice", State: "APPROVED", Commit: "abc", At: now},
	}}, &config.Repository{Maintainers: []string{"Smana"}}, now.Add(-time.Hour)); st != "" {
		t.Fatalf("a non-maintainer's approval is advice: %q", st)
	}
	if _, st := shownApproval(forge.PR{HeadSHA: "abc", Reviews: []forge.Review{
		{ID: 1, Author: "Smana", State: "APPROVED", At: now},
	}}, &config.Repository{Maintainers: []string{"Smana"}}, now.Add(-time.Hour)); st != "" {
		t.Fatalf("an approval naming no commit names nothing to show: %q", st)
	}
}

// With approveToMerge on, the approval is a return and the merger merges, as before: nothing is shown
// in its place.
func TestAnApprovalWithApproveToMergeOnStillMerges(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.r.Cfg.Current().Merge.ApproveToMerge = true
	g.r.Cfg.Current().Classes["docs-links"] = config.Class{Shadow: true}
	g.f.SetChecks(12, green("SUCCESS"))
	g.reconcile(t, "3buqdlot", 1)
	approve(g, t, 77, "abc")
	if got := g.reconcile(t, "3buqdlot", 1); got.Status.Reason != "reentered_approved" {
		t.Fatalf("return: %s %s", got.Status.Phase, got.Status.Reason)
	}
	if got := g.reconcile(t, "3buqdlot", 1); got.Status.Phase != v1alpha1.PhaseAutoMerging || count(g, "approved by @Smana at commit") != 0 {
		t.Fatalf("merges: %s %s %q", got.Status.Phase, got.Status.Reason, g.f.Comments(7))
	}
}
