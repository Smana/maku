// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// Owner ruling on #500: maku/merge-gate is posted on agent pull requests only. An adopted pull
// request (Renovate's) follows the repository's normal rules: the factory posts nothing on it.
func TestTheGatePostsNothingOnAnAdoptedPullRequest(t *testing.T) {
	g := adoptRig(t)
	g.r.Cfg.Current().Merge.GateStatus = true
	cl := depsPatchClass()
	cl.Shadow, cl.Live = false, true
	g.r.Cfg.Current().Classes["deps-patch"] = cl
	if tk := g.reconcile(t, adoptTask, 1); tk.Status.Phase != v1alpha1.PhaseAutoMerging {
		t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
	}
	if got := g.f.Statuses(adoptHead); len(got) != 0 {
		t.Fatalf("posted on an adopted pull request: %+v", got)
	}
}
