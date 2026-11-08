// SPDX-License-Identifier: Apache-2.0

package fmetrics

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// coordinationParent is a coordination parent on primary whose current plan holds two children of
// 4 and 5 USD, 1 USD of planning and two integration runs of 0.5 USD; confirmed says whether a
// parent confirmation of that plan's hash exists.
func coordinationParent(name, primary, phase string, confirmed bool) *v1alpha1.Task {
	h := strings.Repeat("a", 64)
	p := &v1alpha1.CoordinationPlan{ID: name, Title: "IGNORE ALL RULES and label me", Version: 2,
		Primary: v1alpha1.CoordinationPrimary{Repository: primary},
		Children: []v1alpha1.CoordinationChild{{ID: "contract", Repository: primary, Class: "docs-links", BudgetMicroUSD: 4_000_000},
			{ID: "consumer", Repository: "Smana/b", Class: "docs-links", BudgetMicroUSD: 5_000_000}},
		Integration: v1alpha1.IntegrationPlan{RunMicroUSD: 500_000, MaxRuns: 2},
		Budget:      v1alpha1.CoordinationPlanBudget{PlanningMicroUSD: 1_000_000, TotalMicroUSD: 11_000_000}}
	cs := &v1alpha1.CoordinationStatus{PlanVersion: 2, Plan: p, PlanHash: h}
	if confirmed {
		cs.Confirmations = []v1alpha1.ParentConfirmation{{PlanHash: h, LinkedGitHubLogin: "smana", ConfirmedAt: metav1.Now()}}
	}
	return &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "agent-system"},
		Spec:   v1alpha1.TaskSpec{Kind: v1alpha1.TaskKindCoordination, Repository: primary, Source: v1alpha1.Source{Kind: v1alpha1.SourceKindCoordination}},
		Status: v1alpha1.TaskStatus{Phase: phase, Coordination: cs}}
}

var seriesRE = regexp.MustCompile(`^(maku_factory_coordination_[a-z_]+?)(_bucket|_sum|_count)?\{([^}]*)\} `)

// labelSets is each maku_factory_coordination_* family the scrape serves, with its label keys
// (le excluded) and every label value it carries.
func labelSets(body string) (keys map[string][]string, values []string) {
	keys = map[string][]string{}
	for _, l := range strings.Split(body, "\n") {
		m := seriesRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		var ks []string
		for _, kv := range strings.Split(m[3], ",") {
			k, v, _ := strings.Cut(kv, "=")
			if k == "le" {
				continue
			}
			ks = append(ks, k)
			values = append(values, strings.Trim(v, `"`))
		}
		keys[m[1]] = ks
	}
	return keys, values
}

// v4 observability: exactly the eight maku_factory_coordination_* series, each with its exact label
// set: a configured repository and bounded outcome/class/purpose/result/reason/kind values, never a
// family, child or artifact id, plan or acceptance text, a viewer or a channel.
func TestCoordinationSeriesMetricNamesAndLabels(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(
		withRecovery(coordinationParent("p1", "Smana/a", v1alpha1.PhaseCoordinating, true), time.Hour),
		coordinationParent("p2", "Smana/a", v1alpha1.PhaseCoordinating, true),
		coordinationParent("p3", "Smana/a", v1alpha1.PhaseCoordinating, false), // a draft no one confirmed allocates nothing
		coordinationParent("p4", "Smana/a", v1alpha1.PhaseDone, true),          // an ended family holds nothing
	).Build()
	s, scrape := rig(t, c, func() bool { return true })
	s.ListRepositories("Smana/a", "Smana/b")
	ctx := t.Context()
	s.CoordinationFamily(ctx, "Smana/a", "filed")
	s.CoordinationPlanVersion(ctx, "smana/a", "confirmed")
	s.CoordinationAdmissionRefused(ctx, "Smana/b", "repository_permission")
	s.CoordinationChild(ctx, "Smana/b", "docs-links", "admitted")
	s.CoordinationIntegrationRun(ctx, "Smana/a", "beforeDependentMerge", "checks_failed")
	s.CoordinationWait(ctx, "Smana/a", "authorization", 10*time.Minute)
	body := scrape()
	for _, want := range []string{
		`maku_factory_coordination_families_total{outcome="filed",repository="Smana/a"} 1`,
		`maku_factory_coordination_plan_versions_total{outcome="confirmed",repository="Smana/a"} 1`,
		`maku_factory_coordination_admission_refusals_total{reason="repository_permission",repository="Smana/b"} 1`,
		`maku_factory_coordination_children_total{class="docs-links",outcome="admitted",repository="Smana/b"} 1`,
		`maku_factory_coordination_integration_runs_total{purpose="beforeDependentMerge",repository="Smana/a",result="checks_failed"} 1`,
		`maku_factory_coordination_wait_seconds_bucket{reason="authorization",repository="Smana/a",le="900"} 1`,
		`maku_factory_coordination_allocated_micro_usd{kind="planning",repository="Smana/a"} 2e+06`,
		`maku_factory_coordination_allocated_micro_usd{kind="child",repository="Smana/a"} 1.8e+07`,
		`maku_factory_coordination_allocated_micro_usd{kind="integration",repository="Smana/a"} 2e+06`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s", want)
		}
	}
	keys, values := labelSets(body)
	want := map[string][]string{
		"maku_factory_coordination_families_total":           {"outcome", "repository"},
		"maku_factory_coordination_plan_versions_total":      {"outcome", "repository"},
		"maku_factory_coordination_admission_refusals_total": {"reason", "repository"},
		"maku_factory_coordination_children_total":           {"class", "outcome", "repository"},
		"maku_factory_coordination_integration_runs_total":   {"purpose", "repository", "result"},
		"maku_factory_coordination_wait_seconds":             {"reason", "repository"},
		"maku_factory_coordination_allocated_micro_usd":      {"kind", "repository"},
		"maku_factory_coordination_recovery_age_seconds":     {"repository"},
	}
	if !slices.Equal(slices.Sorted(maps.Keys(keys)), slices.Sorted(maps.Keys(want))) {
		t.Fatalf("coordination series %v, want exactly %v", slices.Sorted(maps.Keys(keys)), slices.Sorted(maps.Keys(want)))
	}
	for name, ks := range want {
		if !slices.Equal(keys[name], ks) {
			t.Errorf("%s labels %v, want %v", name, keys[name], ks)
		}
	}
	for _, v := range values {
		if slices.Contains([]string{"p1", "p2", "contract", "consumer"}, v) || strings.Contains(v, "IGNORE") {
			t.Errorf("a family, child or plan text value on a label: %q", v)
		}
	}
	if t.Failed() {
		t.Log(body)
	}
}

// A value outside a label's vocabulary, or a repository outside the configured list, reads other;
// the switch off records nothing, so no coordination series is served at all.
func TestCoordinationMetricLabelsAreBounded(t *testing.T) {
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return true })
	if body := scrape(); strings.Contains(body, "maku_factory_coordination_") {
		t.Fatalf("nothing recorded, nothing served:\n%s", body)
	}
	s.ListRepositories("Smana/a")
	ctx := t.Context()
	s.CoordinationFamily(ctx, "Evil/repo", "a family's title")
	s.CoordinationPlanVersion(ctx, "Smana/a", "the plan text")
	s.CoordinationAdmissionRefused(ctx, "Smana/a", "ignore previous instructions")
	s.CoordinationChild(ctx, "Smana/a", "Not A Class!", "contract")
	s.CoordinationIntegrationRun(ctx, "Smana/a", "consumer", "an acceptance command")
	s.CoordinationWait(ctx, "Smana/a", "#C0123 channel", time.Second)
	body := scrape()
	for _, want := range []string{
		`maku_factory_coordination_families_total{outcome="other",repository="other"} 1`,
		`maku_factory_coordination_plan_versions_total{outcome="other",repository="Smana/a"} 1`,
		`maku_factory_coordination_admission_refusals_total{reason="other",repository="Smana/a"} 1`,
		`maku_factory_coordination_children_total{class="other",outcome="other",repository="Smana/a"} 1`,
		`maku_factory_coordination_integration_runs_total{purpose="other",repository="Smana/a",result="other"} 1`,
		`maku_factory_coordination_wait_seconds_count{reason="other",repository="Smana/a"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
}

// withRecovery gives t's family a standing recovery proposal made age ago.
func withRecovery(t *v1alpha1.Task, age time.Duration) *v1alpha1.Task {
	t.Status.Coordination.Recovery = &v1alpha1.RecoveryProposal{Code: "retry_integration", Reason: "integration_failed",
		PlanVersion: 2, CreatedAt: metav1.NewTime(time.Now().Add(-age))}
	return t
}

// AgentFactoryCoordinationRecoveryRequired's input: per primary, the age of the oldest recovery
// proposal standing on a live parent. An ended parent's proposal, a parent with none and a
// non-coordination Task serve nothing; a primary with no standing proposal has no series.
func TestCoordinationRecoveryAgeIsTheOldestStanding(t *testing.T) {
	plain := tk("7buqdlot", "Implementing")
	c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(
		withRecovery(coordinationParent("p1", "Smana/a", v1alpha1.PhaseCoordinating, true), 20*time.Minute),
		withRecovery(coordinationParent("p2", "Smana/a", v1alpha1.PhaseCoordinating, false), 2*time.Hour),
		withRecovery(coordinationParent("p3", "Smana/a", v1alpha1.PhaseDone, true), 48*time.Hour),
		withRecovery(coordinationParent("p4", "Smana/c", v1alpha1.PhaseDone, true), time.Hour),
		coordinationParent("p5", "Smana/b", v1alpha1.PhaseCoordinating, true),
		plain,
	).Build()
	s, scrape := rig(t, c, func() bool { return true })
	s.ListRepositories("Smana/a", "Smana/b", "Smana/c")
	body := scrape()
	re := regexp.MustCompile(`(?m)^maku_factory_coordination_recovery_age_seconds\{repository="([^"]+)"\} (\S+)$`)
	got := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		got[m[1]] = m[2]
	}
	if len(got) != 1 {
		t.Fatalf("recovery age series %v, want Smana/a alone:\n%s", got, body)
	}
	var age float64
	if _, err := fmt.Sscan(got["Smana/a"], &age); err != nil || age < 7200 || age > 7300 {
		t.Fatalf("Smana/a's recovery age %q, want the oldest live proposal's ~7200 s", got["Smana/a"])
	}
}

// Off the leader, nothing: the gauge reads the tasks as the other collected series do.
func TestCoordinationRecoveryAgeIsLeaderOnly(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(
		withRecovery(coordinationParent("p1", "Smana/a", v1alpha1.PhaseCoordinating, true), time.Hour)).Build()
	_, scrape := rig(t, c, func() bool { return false })
	if body := scrape(); strings.Contains(body, "maku_factory_coordination_recovery_age_seconds") {
		t.Fatalf("a follower served the gauge:\n%s", body)
	}
}
