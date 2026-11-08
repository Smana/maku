// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"reflect"
	"slices"
	"strings"
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

// MR8: a view's allocations are the Task facts: the parent's static total when its status has no
// budget record yet, and each child's frozen lifetime cap.
func TestCoordinationViewBudgetAllocations(t *testing.T) {
	p := validPlan(t)
	parent := v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: p.ID}}
	parent.Status.Coordination = &v1alpha1.CoordinationStatus{Plan: &p, PlanHash: PlanHash(p),
		Children: []v1alpha1.CoordinationChildStatus{{ID: "contract", Repository: "Smana/public-contracts", Task: "aaaaaaaa"}}}
	child := v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "aaaaaaaa"}, Spec: v1alpha1.TaskSpec{Budget: v1alpha1.Budget{TaskMicroUSD: 5_100_000}}}
	v := View(parent, []v1alpha1.Task{child}, nil, LedgerUsage{})
	if v.Budget.AllocatedMicroUSD != 12_750_000 || v.Children[0].AllocationMicroUSD != 5_100_000 {
		t.Fatalf("budget %+v child %+v", v.Budget, v.Children[0])
	}
}

// jsonKeys is the wire names of struct type v's fields, in declaration order.
func jsonKeys(v any) []string {
	var out []string
	rt := reflect.TypeOf(v)
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		out = append(out, name)
	}
	return out
}

// The experience plan's consumers (the parent rail, inbox and summaries) read these exact fields:
// renaming, dropping or adding one is a contract change, made in both plans. The view is
// deterministic, unfiltered, keeps the plan's child order, sums the budget layers as MR8 states
// them, carries each attempt's exact subjects, one recovery record and the bounded incidents.
func TestCoordinationViewContract(t *testing.T) {
	for name, c := range map[string]struct {
		v    any
		want []string
	}{
		"view": {CoordinationView{}, []string{"parentTask", "phase", "reason", "plan", "planHash", "parentConfirmation",
			"childAuthorizations", "children", "artifacts", "integrationAttempts", "mergeOrder", "budget", "recovery", "postCompletionIncidents"}},
		"child": {CoordinationChildView{}, []string{"id", "repository", "task", "room", "issueURL", "prURL", "phase", "reason",
			"fingerprint", "dependsOn", "consumes", "headSHA", "mergeCommitSHA", "allocationMicroUSD", "settledMicroUSD",
			"reservedMicroUSD", "integrationAcceptedHead", "integrationRelease"}},
		"attempt": {IntegrationAttemptView{}, []string{"task", "purpose", "planHash", "manifestHash", "state", "reason", "subjects",
			"runMicroUSD", "costMicroUSD"}},
		"budget": {CoordinationBudgetView{}, []string{"planningMicroUSD", "planningReservedMicroUSD", "planningSettledMicroUSD",
			"totalMicroUSD", "allocatedMicroUSD", "childrenReservedMicroUSD", "childrenSettledMicroUSD", "integrationReservedMicroUSD",
			"integrationSettledMicroUSD", "integrationMaxRuns", "integrationRuns"}},
	} {
		if got := jsonKeys(c.v); !slices.Equal(got, c.want) {
			t.Errorf("%s fields %v, want %v", name, got, c.want)
		}
	}

	p := validPlan(t)
	h := PlanHash(p)
	head, merge := strings.Repeat("1", 40), strings.Repeat("2", 40)
	subjects := []v1alpha1.IntegrationSubject{{ChildID: "contract", Repository: p.Children[0].Repository, MergeCommitSHA: merge},
		{ChildID: "consumer", Repository: p.Children[1].Repository, HeadSHA: head}}
	incidents := make([]v1alpha1.CoordinationIncident, 0, v1alpha1.MaxCoordinationIncidents)
	for i := range v1alpha1.MaxCoordinationIncidents + 2 {
		incidents = v1alpha1.AppendNewest(incidents, v1alpha1.CoordinationIncident{ChildID: "contract", Reason: "reverted",
			MergeCommitSHA: strings.Repeat(string(rune('a'+i)), 40)}, v1alpha1.MaxCoordinationIncidents)
	}
	parent := v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: p.ID}}
	parent.Status.Phase, parent.Status.Reason = v1alpha1.PhaseCoordinating, ReasonFiled
	parent.Status.Coordination = &v1alpha1.CoordinationStatus{Plan: &p, PlanHash: h,
		Budget: &v1alpha1.CoordinationBudgetStatus{TotalMicroUSD: p.Budget.TotalMicroUSD, AllocatedMicroUSD: 12_750_000,
			PlanningReservedMicroUSD: 100, PlanningSettledMicroUSD: 200},
		Children: []v1alpha1.CoordinationChildStatus{
			{ID: "consumer", Repository: p.Children[1].Repository, Task: "bbbbbbbb", Issue: 8},
			{ID: "contract", Repository: p.Children[0].Repository, Task: "aaaaaaaa", Issue: 7}},
		IntegrationAttempts: []v1alpha1.IntegrationAttempt{{Task: "iiiiiiii", Purpose: PurposeBeforeDependentMerge, PlanHash: h,
			ManifestHash: strings.Repeat("m", 64), State: ResultPending, Subjects: subjects, RunMicroUSD: 510_000}},
		MergeOrder: []string{"contract"},
		Recovery:   &v1alpha1.RecoveryProposal{Code: RecoveryRetryIntegration, IntegrationTask: "iiiiiiii", Reason: "integration_failed", PlanVersion: p.Version},
		Incidents:  incidents,
	}
	kid := func(name string, runs ...string) v1alpha1.Task {
		k := v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.TaskSpec{Budget: v1alpha1.Budget{TaskMicroUSD: 5_100_000}}}
		for _, r := range runs {
			k.Status.Runs = append(k.Status.Runs, v1alpha1.RunRecord{ID: r})
		}
		return k
	}
	contract, consumer := kid("aaaaaaaa", "r1"), kid("bbbbbbbb", "r2", "r3")
	integ := kid("iiiiiiii", "r4")
	ledger := LedgerUsage{ByRun: map[string]RunUsage{"r1": {SettledMicroUSD: 1_000}, "r2": {ReservedMicroUSD: 20}, "r3": {SettledMicroUSD: 300},
		"r4": {ReservedMicroUSD: 4, SettledMicroUSD: 5}}}

	v := View(parent, []v1alpha1.Task{consumer, contract}, []v1alpha1.Task{integ}, ledger)
	if again := View(parent, []v1alpha1.Task{consumer, contract}, []v1alpha1.Task{integ}, ledger); !reflect.DeepEqual(v, again) {
		t.Fatal("the view is not deterministic")
	}
	if len(v.Children) != 2 || v.Children[0].ID != p.Children[0].ID || v.Children[1].ID != p.Children[1].ID {
		t.Fatalf("children %+v, want the plan's order", v.Children)
	}
	want := CoordinationBudgetView{PlanningMicroUSD: p.Budget.PlanningMicroUSD, PlanningReservedMicroUSD: 100, PlanningSettledMicroUSD: 200,
		TotalMicroUSD: p.Budget.TotalMicroUSD, AllocatedMicroUSD: 12_750_000, ChildrenReservedMicroUSD: 20, ChildrenSettledMicroUSD: 1_300,
		IntegrationReservedMicroUSD: 4, IntegrationSettledMicroUSD: 5, IntegrationMaxRuns: int(p.Integration.MaxRuns), IntegrationRuns: 1}
	if v.Budget != want {
		t.Fatalf("budget %+v, want %+v", v.Budget, want)
	}
	if total, ok := Allocated(p); !ok || total != p.Budget.PlanningMicroUSD+p.Children[0].BudgetMicroUSD+p.Children[1].BudgetMicroUSD+
		int64(p.Integration.MaxRuns)*p.Integration.RunMicroUSD || total > p.Budget.TotalMicroUSD {
		t.Fatalf("allocated %d: planning + Σ children + maxRuns × runMicroUSD, within the total", total)
	}
	if a := v.IntegrationAttempts; len(a) != 1 || !reflect.DeepEqual(a[0].Subjects, subjects) {
		t.Fatalf("attempts %+v, want the exact subjects", a)
	}
	if v.Recovery == nil || *v.Recovery != *parent.Status.Coordination.Recovery || v.Recovery == parent.Status.Coordination.Recovery {
		t.Fatalf("recovery %+v, want one copied record", v.Recovery)
	}
	if n := len(v.PostCompletionIncidents); n != v1alpha1.MaxCoordinationIncidents ||
		v.PostCompletionIncidents[n-1].MergeCommitSHA != strings.Repeat("f", 40) {
		t.Fatalf("incidents %+v, want the newest %d", v.PostCompletionIncidents, v1alpha1.MaxCoordinationIncidents)
	}
}
