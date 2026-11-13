// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/grant"
)

// noStandingSuccess fails when the last maku/merge-gate posted on head is success: under a ruleset
// requiring it with 0 approvals, that is a standing permit to merge a head the factory did not.
func noStandingSuccess(t *testing.T, g *rig, head string) {
	t.Helper()
	got := g.f.Statuses(head)
	if len(got) == 0 {
		t.Fatalf("no status posted on %s", head)
	}
	if last := got[len(got)-1]; last.State == "success" {
		t.Fatalf("success left standing on %s: %+v", head, got)
	}
}

// laterFailingGrants answers the decision's lookup and fails every later one: the grant is
// revoked between the decision and the pre-merge re-read.
type laterFailingGrants struct{ fakeGrants }

func (f *laterFailingGrants) LookupGrants(ctx context.Context, repository, class, hash string, maxLifetime time.Duration) ([]grant.Grant, []string, error) {
	l, ev, err := f.fakeGrants.LookupGrants(ctx, repository, class, hash, maxLifetime)
	if len(f.calls) > 1 {
		return nil, nil, errors.New("broker down")
	}
	return l, ev, err
}

// Review of #500: success is posted last, after every check that can still hold an arm, and a
// path that does not merge never leaves it standing.
func TestTheGateNeverLeavesASuccessItDidNotMerge(t *testing.T) {
	t.Run("a grant gone before the merge call", func(t *testing.T) {
		snap, _ := approvalSnapshot()
		g := cutOverRig(t, snap, nil)
		g.r.Cfg.Current().PolicyManagement.ApprovalMemory = true
		g.r.Cfg.Current().Repositories[0].Merge.GateStatus = true
		f := &laterFailingGrants{fakeGrants{list: []grant.Grant{rigGrant()}}}
		g.r.Grants = f
		files := changed("[x](https://example.com/old)", "[x](https://example.com/new)")
		g.f.SetFiles(files.Base, files.Head)
		tk := g.reconcile(t, "3buqdlot", 1)
		if tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != reasonApprovalRequired || len(f.calls) < 2 {
			t.Fatalf("%s %s, calls %q", tk.Status.Phase, tk.Status.Reason, f.calls)
		}
		noStandingSuccess(t, g, "abc")
		for _, s := range g.f.Statuses("abc") {
			if s.State == "success" {
				t.Fatalf("success posted before the grant was read again: %+v", g.f.Statuses("abc"))
			}
		}
	})

	t.Run("an integration hold", func(t *testing.T) {
		g, parent := holdRig(t, false)
		g.r.Cfg.Current().Repositories[0].Merge.GateStatus = true
		g.pass(t, parent.Name, headH1)
		g.r.Client = &racingClient{Client: g.c, parent: parent.Name, race: func() {
			p, _ := g.coordTask(t, parent.Name)
			p.Status.Coordination.Children[1].IntegrationRelease = nil
			if err := g.c.Status().Update(t.Context(), p); err != nil {
				t.Fatal(err)
			}
		}}
		if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Reason != reasonIntegrationPending || len(g.f.Armed()) != 0 {
			t.Fatalf("%s/%s %v", tk.Status.Phase, tk.Status.Reason, g.f.Armed())
		}
		noStandingSuccess(t, g, headH1)
		for _, s := range g.f.Statuses(headH1) {
			if s.State == "success" {
				t.Fatalf("success posted before the hold: %+v", g.f.Statuses(headH1))
			}
		}
	})

	for name, refusal := range map[string]error{
		"a 405, not mergeable": forge.ErrNotMergeable,
		"a 409, head moved":    forge.ErrHeadMoved,
	} {
		t.Run(name, func(t *testing.T) {
			g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
			g.r.Cfg.Current().Repositories[0].Merge.GateStatus = true
			g.f.SetChecks(12, green("SUCCESS"))
			g.f.RefuseMerge("PR_12", refusal)
			g.reconcile(t, "3buqdlot", 1)
			noStandingSuccess(t, g, "abc")
		})
	}

	t.Run("a merge call that fails", func(t *testing.T) {
		g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
		g.r.Cfg.Current().Repositories[0].Merge.GateStatus = true
		g.f.SetChecks(12, green("SUCCESS"))
		g.f.RefuseMerge("PR_12", errors.New("502"))
		if _, err := g.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "agent-system", Name: "3buqdlot"}}); err == nil {
			t.Fatal("a failed merge call is the pass's error")
		}
		noStandingSuccess(t, g, "abc")
	})
}

// Review of #500: under gateStatus, an arm whose success cannot be posted does not merge; the pass
// fails and the next one tries again.
func TestAFailedArmStatusAbortsTheMerge(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.r.Cfg.Current().Repositories[0].Merge.GateStatus = true
	g.f.SetChecks(12, green("SUCCESS"))
	g.f.SetStatusError(errors.New("403: statuses: write"))
	if _, err := g.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "agent-system", Name: "3buqdlot"}}); err == nil {
		t.Fatal("the pass goes on without the status")
	}
	if pr, _ := g.f.PullRequest(t.Context(), 12); pr.State != "OPEN" || len(g.f.Armed()) != 0 {
		t.Fatalf("merged without its status: %s %v", pr.State, g.f.Armed())
	}
	g.f.SetStatusError(nil)
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAutoMerging {
		t.Fatalf("the next pass merges: %s %s", tk.Status.Phase, tk.Status.Reason)
	}
}
