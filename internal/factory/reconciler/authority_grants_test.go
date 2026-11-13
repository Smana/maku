// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/grant"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

// Stages 4 and 5 together: a cut-over repository's gate decides on its own policy, so its
// approval subset's grants are read there, immediately before the decision, under the hash of the
// policy that decides. Dual evaluation is not run for it, so it cannot be where they are read.
func TestACutOverRepositoryConsultsItsGrants(t *testing.T) {
	for name, tc := range map[string]struct {
		grants        *fakeGrants
		phase, reason string
		grant         bool
	}{
		"a covering grant": {&fakeGrants{list: []grant.Grant{rigGrant()}}, v1alpha1.PhaseAutoMerging, "", true},
		"no grant":         {&fakeGrants{}, v1alpha1.PhaseAwaitingHuman, reasonApprovalRequired, false},
		"a failed lookup":  {&fakeGrants{list: []grant.Grant{rigGrant()}, err: errors.New("broker down")}, v1alpha1.PhaseAwaitingHuman, reasonApprovalRequired, false},
	} {
		t.Run(name, func(t *testing.T) {
			snap, _ := approvalSnapshot()
			g := cutOverRig(t, snap, nil)
			g.r.Cfg.Current().PolicyManagement.ApprovalMemory = true
			g.r.Grants = tc.grants
			files := changed("[x](https://example.com/old)", "[x](https://example.com/new)")
			g.f.SetFiles(files.Base, files.Head)
			tk := g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Phase != tc.phase || (tc.reason != "" && tk.Status.Reason != tc.reason) {
				t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
			}
			if len(tc.grants.calls) == 0 || !strings.HasPrefix(tc.grants.calls[0], "lookup smana/cloud-native-ref docs-links "+rigHash+" ") {
				t.Fatalf("%q", tc.grants.calls)
			}
			if c := strings.Join(g.f.Comments(12), "\n"); tc.grant != recordHas(c, " policy=0123456789ab grant=aaaaaaaaaaaaaaaaaaaaaaaaaa:1") {
				t.Fatalf("%s", c)
			}
		})
	}
}

// The pre-merge re-read of a cut-over repository's grant binds to the policy that decides it, its
// authority snapshot; a policy that gives no authority holds no grant (MP13).
func TestAGrantOfACutOverRepositoryIsReadAgainUnderItsAuthority(t *testing.T) {
	tk := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Repository: "Smana/cloud-native-ref", PredictedClass: "docs-links"}}
	armed := ArmDecision{Verdict: "arm", Rows: rigDecisionRows("aaaaaaaaaaaaaaaaaaaaaaaaaa:1")}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"the policy decides": {nil, true},
		"the policy is gone": {mergepolicy.ErrStale, false},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{Repositories: []config.Repository{{Name: "Smana/cloud-native-ref", ClassAuthority: config.ClassAuthorityRepository}},
				PolicyManagement: config.PolicyManagement{Read: true, RepositoryAuthority: true, ApprovalMemory: true}}
			f := &fakeGrants{list: []grant.Grant{rigGrant()}}
			r := &Reconciler{Cfg: cfg, Grants: f, Metrics: &fakeMetrics{}, Now: func() time.Time { return now },
				Authority: func(string) (mergepolicy.Snapshot, error) { s, _ := approvalSnapshot(); return s, tc.err },
				Policy: func(string) (mergepolicy.Snapshot, bool) {
					t.Fatal("a cut-over repository's grant read under the dual-evaluation snapshot")
					return mergepolicy.Snapshot{}, false
				}}
			if got := r.grantHolds(t.Context(), r.Cfg.Current(), tk, armed); got != tc.want {
				t.Fatalf("got %v, calls %q", got, f.calls)
			}
			if tc.want && !slices.Contains(f.calls, "lookup smana/cloud-native-ref docs-links "+rigHash+" "+config.DefaultGrantLifetime.String()) {
				t.Fatalf("%q", f.calls)
			}
		})
	}
}
