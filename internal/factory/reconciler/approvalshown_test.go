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
	g.r.Cfg.Classes["docs-links"] = config.Class{Shadow: true}
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

// Live in the owner's demo: with approveToMerge off an approval is no return to the gate, so the task
// kept shadow_would_arm and the room never said a maintainer had approved. It now says who approved
// which commit, once, and what remains; nothing merges.
func TestAnApprovalIsShownWithApproveToMergeOff(t *testing.T) {
	g := shadowHandOff(t)
	approve(g, t, 77, "abc")
	got := g.reconcile(t, "3buqdlot", 1)
	if got.Status.Phase != v1alpha1.PhaseAwaitingHuman || got.Status.Reason != "maintainer_approved" {
		t.Fatalf("shown: %s %s", got.Status.Phase, got.Status.Reason)
	}
	if a := g.f.Armed(); len(a) != 0 {
		t.Fatalf("off, an approval merges nothing: %q", a)
	}
	if n := count(g, "approved by @Smana at commit `abc`"); n != 1 {
		t.Fatalf("narrated %d times: %q", n, g.f.Comments(7))
	}
	if !strings.Contains(strings.Join(g.f.Comments(7), "\n"), "a maintainer merges it on GitHub") {
		t.Fatalf("what remains: %q", g.f.Comments(7))
	}
	if pr := lastFacts(g).PR; pr == nil || pr.ApprovedBy != "Smana" || pr.ApprovedCommit != "abc" {
		t.Fatalf("the room's facts: %+v", pr)
	}
	got = g.reconcile(t, "3buqdlot", 2)
	if got.Status.Reason != "maintainer_approved" || count(g, "approved by @Smana") != 1 || len(g.f.Armed()) != 0 {
		t.Fatalf("a second reconcile: %s, %q, merged %q", got.Status.Reason, g.f.Comments(7), g.f.Armed())
	}
}

// A push after the approval leaves it on an older commit: approval_stale, said once, nothing merges.
func TestAPushAfterAShownApprovalShowsItStale(t *testing.T) {
	g := shadowHandOff(t)
	approve(g, t, 77, "abc")
	g.reconcile(t, "3buqdlot", 1)
	pr, _ := g.f.PullRequest(t.Context(), 12)
	pr.HeadSHA = "def"
	g.f.SetPR(pr)
	got := g.reconcile(t, "3buqdlot", 2)
	if got.Status.Phase != v1alpha1.PhaseAwaitingHuman || got.Status.Reason != "approval_stale" {
		t.Fatalf("stale: %s %s", got.Status.Phase, got.Status.Reason)
	}
	if n := count(g, "approval is of commit `abc`, an older commit: new commits since"); n != 1 {
		t.Fatalf("narrated %d times: %q", n, g.f.Comments(7))
	}
	if a := g.f.Armed(); len(a) != 0 {
		t.Fatalf("merged %q", a)
	}
	// Approving the new head shows it again, as a new approval.
	approve(g, t, 78, "def")
	if got = g.reconcile(t, "3buqdlot", 1); got.Status.Reason != "maintainer_approved" || count(g, "approved by @Smana at commit `def`") != 1 {
		t.Fatalf("re-approved: %s %q", got.Status.Reason, g.f.Comments(7))
	}
}

// A maintainer's newer request for changes is a veto: no approval is shown over it; nor is anyone
// else's approval, or one that names no commit.
func TestAVetoHidesTheApproval(t *testing.T) {
	if _, st := shownApproval(forge.PR{HeadSHA: "abc", Reviews: []forge.Review{
		{ID: 1, Author: "Smana", State: "APPROVED", Commit: "abc", At: now},
		{ID: 2, Author: "Smana", State: "CHANGES_REQUESTED", Commit: "abc", At: now.Add(time.Second)},
	}}, &config.Repository{Maintainers: []string{"Smana"}}); st != "veto" {
		t.Fatalf("got %q", st)
	}
	if _, st := shownApproval(forge.PR{HeadSHA: "abc", Reviews: []forge.Review{
		{ID: 1, Author: "alice", State: "APPROVED", Commit: "abc", At: now},
	}}, &config.Repository{Maintainers: []string{"Smana"}}); st != "" {
		t.Fatalf("a non-maintainer's approval is advice: %q", st)
	}
	if _, st := shownApproval(forge.PR{HeadSHA: "abc", Reviews: []forge.Review{
		{ID: 1, Author: "Smana", State: "APPROVED", At: now},
	}}, &config.Repository{Maintainers: []string{"Smana"}}); st != "" {
		t.Fatalf("an approval naming no commit names nothing to show: %q", st)
	}
}

// With approveToMerge on, the approval is a return and the merger merges, as before: no shown reason
// stands in for it.
func TestAnApprovalWithApproveToMergeOnStillMerges(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.r.Cfg.Merge.ApproveToMerge = true
	g.r.Cfg.Classes["docs-links"] = config.Class{Shadow: true}
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
