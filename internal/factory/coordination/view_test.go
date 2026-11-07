// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

func TestCoordinationViewDerivesFromTasksAndLedger(t *testing.T) {
	p := validPlan(t)
	h := PlanHash(p)
	parent := v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: p.ID}}
	parent.Status.Phase, parent.Status.Reason = v1alpha1.PhaseCoordinating, "integration_pending"
	parent.Status.Coordination = &v1alpha1.CoordinationStatus{
		Plan: &p, PlanHash: h,
		Confirmations:       []v1alpha1.ParentConfirmation{{PlanHash: "old"}, {PlanHash: h, LinkedGitHubLogin: "Smana"}},
		Budget:              &v1alpha1.CoordinationBudgetStatus{TotalMicroUSD: p.Budget.TotalMicroUSD, AllocatedMicroUSD: 42},
		Children:            []v1alpha1.CoordinationChildStatus{{ID: "contract", Repository: "Smana/public-contracts", Task: "aaaaaaaa", Issue: 7}},
		IntegrationAttempts: []v1alpha1.IntegrationAttempt{{Task: "iiiiiiii", Purpose: "final", PlanHash: h, ManifestHash: "m", State: "pending"}},
	}
	child := v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "aaaaaaaa"}}
	child.Status.Phase, child.Status.RoomRef = v1alpha1.PhaseImplementing, "aaaaaaaa"
	child.Status.Runs = []v1alpha1.RunRecord{{ID: "r1"}, {ID: "r2"}}
	child.Status.PullRequest = &v1alpha1.PullRequestRef{URL: "https://github.com/Smana/public-contracts/pull/9", HeadSHA: "h"}
	integ := v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "iiiiiiii"}}
	integ.Status.Runs = []v1alpha1.RunRecord{{ID: "r3"}}
	integ.Status.Result = &v1alpha1.IntegrationResult{State: "passed", ManifestHash: "m"}
	ledger := LedgerUsage{ByRun: map[string]RunUsage{"r1": {SettledMicroUSD: 10}, "r2": {ReservedMicroUSD: 5}, "r3": {SettledMicroUSD: 3}}}

	v := View(parent, []v1alpha1.Task{child}, []v1alpha1.Task{integ}, ledger)

	if v.ParentConfirmation == nil || v.ParentConfirmation.LinkedGitHubLogin != "Smana" {
		t.Fatalf("parent confirmation = %+v, want the current hash's", v.ParentConfirmation)
	}
	if len(v.Children) != len(p.Children) {
		t.Fatalf("children = %d, want one per plan child", len(v.Children))
	}
	var c CoordinationChildView
	for _, x := range v.Children {
		if x.ID == "contract" {
			c = x
		}
	}
	if c.Task != "aaaaaaaa" || c.Phase != v1alpha1.PhaseImplementing || c.HeadSHA != "h" ||
		c.IssueURL != "https://github.com/Smana/public-contracts/issues/7" || c.SettledMicroUSD != 10 || c.ReservedMicroUSD != 5 {
		t.Fatalf("contract view = %+v", c)
	}
	if c.Fingerprint != ChildFingerprint(p, "contract") {
		t.Fatal("fingerprint is not the plan's")
	}
	b := v.Budget
	if b.ChildrenSettledMicroUSD != 10 || b.ChildrenReservedMicroUSD != 5 || b.IntegrationSettledMicroUSD != 3 || b.IntegrationRuns != 1 || b.AllocatedMicroUSD != 42 {
		t.Fatalf("budget = %+v", b)
	}
	if v.IntegrationAttempts[0].State != "passed" {
		t.Fatalf("attempt state = %q, want the Task's result for the same manifest", v.IntegrationAttempts[0].State)
	}
}

func TestCoordinationViewOfADraftWithoutPlanIsEmptyNotNil(t *testing.T) {
	v := View(v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "p"}}, nil, nil, LedgerUsage{})
	if v.Children == nil || v.Artifacts == nil || v.IntegrationAttempts == nil || v.MergeOrder == nil || v.ChildAuthorizations == nil {
		t.Fatal("a view's lists are empty, never null on the wire")
	}
}
