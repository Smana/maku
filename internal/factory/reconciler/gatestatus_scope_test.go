// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
)

// Owner ruling on #500, amended by its re-review: the gate's decisions are posted on agent pull
// requests only, but the merger's own merge of an adopted pull request carries success just
// before the merge call, so a ruleset requiring maku/merge-gate reads it as the merger's merge.
func TestAnAdoptedMergeCarriesOnlyTheMergersSuccess(t *testing.T) {
	g := adoptRig(t)
	g.r.Cfg.Current().Repositories[0].Merge.GateStatus = true
	cl := depsPatchClass()
	cl.Shadow, cl.Live = false, true
	g.r.Cfg.Current().Repositories[0].Classes["deps-patch"] = cl
	if tk := g.reconcile(t, adoptTask, 1); tk.Status.Phase != v1alpha1.PhaseAutoMerging {
		t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
	}
	if got, want := g.f.GateSequence(), []string{"status " + adoptHead + " success", "merge PR_30 " + adoptHead}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// An adopted pull request the gate hands to a human gets no status: it follows the repository's
// normal rules.
func TestAnAdoptedHandOffPostsNothing(t *testing.T) {
	g := adoptRig(t)
	g.r.Cfg.Current().Repositories[0].Merge.GateStatus = true
	g.r.Cfg.Current().Repositories[0].Classes["deps-patch"] = depsPatchClass() // in shadow: handed to a human
	g.reconcile(t, adoptTask, 1)
	if got := g.f.Statuses(adoptHead); len(got) != 0 {
		t.Fatalf("posted on an adopted hand-off: %+v", got)
	}
}

// Re-review of #500: a green revert is merged by the merger at its head with maku/merge-gate's
// success posted just before, so a rollback is never stalled by a ruleset requiring it.
func TestAGreenRevertCarriesTheMergersSuccess(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseVerifying)
	g.r.Cfg.Current().Repositories[0].Merge.GateStatus = true
	var tk v1alpha1.Task
	_ = g.c.Get(t.Context(), client.ObjectKey{Namespace: "agent-system", Name: "3buqdlot"}, &tk)
	merged := metav1.NewTime(now)
	tk.Status.PullRequest.MergeCommitSHA, tk.Status.PullRequest.AutoMerged, tk.Status.PullRequest.MergedAt = "m1", true, &merged
	_ = g.c.Status().Update(t.Context(), &tk)
	red := green("SUCCESS")
	red.Runs[0].State = "FAILURE"
	g.f.SetCommitChecks("m1", red.Runs...)
	got := g.reconcile(t, "3buqdlot", 1)
	rv := got.Status.PullRequest.RevertNumber
	g.f.SetPR(forge.PR{Number: rv, NodeID: "PR_revert", State: "OPEN", HeadSHA: "r1"})
	g.f.SetChecks(rv, green("SUCCESS"))
	g.reconcile(t, "3buqdlot", 1)
	seq := g.f.GateSequence()
	i, j := slices.Index(seq, "status r1 success"), slices.Index(seq, "merge PR_revert r1")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("success before the revert's merge: %q", seq)
	}
	if s := g.f.Statuses("r1"); s[len(s)-1].Description != "revert: merging" {
		t.Fatalf("%+v", s)
	}
}
