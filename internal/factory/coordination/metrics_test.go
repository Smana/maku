// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/fmetrics"
)

// fakeMetrics records what the service counts, one "<series> <repository> <value>" line each.
type fakeMetrics struct{ got []string }

func (f *fakeMetrics) CoordinationFamily(_ context.Context, repository, outcome string) {
	f.got = append(f.got, "family "+repository+" "+outcome)
}

func (f *fakeMetrics) CoordinationPlanVersion(_ context.Context, repository, outcome string) {
	f.got = append(f.got, "plan_version "+repository+" "+outcome)
}

func (f *fakeMetrics) CoordinationChild(_ context.Context, repository, class, outcome string) {
	f.got = append(f.got, "child "+repository+" "+class+" "+outcome)
}

func (f *fakeMetrics) CoordinationWait(_ context.Context, repository, reason string, d time.Duration) {
	f.got = append(f.got, "wait "+repository+" "+reason+" "+d.String())
}

func (f *fakeMetrics) count(line string) int {
	n := 0
	for _, g := range f.got {
		if g == line {
			n++
		}
	}
	return n
}

// Every bounded code the service, its repository checks and the plan refuse with is a value of
// maku_factory_coordination_admission_refusals_total's reason label, never other.
func TestCoordinationReasonsAreMetricReasons(t *testing.T) {
	plan := []error{ErrInvalidPlan, ErrInvalidDependency, ErrIncompletePlan, ErrPolicyUnresolved, ErrUnknownClass, ErrPolicyMismatch,
		ErrRolesMismatch, ErrAllocationBelowRun, ErrAllocationAboveCeiling, ErrBudgetOverflow, ErrBudgetExceeded, ErrStalePlan,
		ErrPlanVersion, ErrPrimaryRequiresNewParent, ErrInvalidConfirmation, ErrPlanningCostUnverified, ErrAllocationBelowSpend,
		ErrIntegrationRunsExhausted}
	for _, err := range slices.Concat(checkReasons, serviceReasons, plan) {
		if !slices.Contains(fmetrics.CoordinationReasons(), err.Error()) {
			t.Errorf("%s is not a metric reason", err)
		}
	}
}

// v4 observability: the service counts each plan version once at the confirmation that completes
// it (with its authorization wait), a refused stale confirmation, a version a newer one superseded
// with each child it dropped, and the family once when its filing completes; a replay counts
// nothing more.
func TestCoordinationServiceMetrics(t *testing.T) {
	r := newFilingRig(t)
	m := &fakeMetrics{}
	r.s.Metrics = m
	accepted := r.s.Now()
	p := draftPlan(t)
	mustDraft(t, r.s, p) // accepted then; each confirmation 10 minutes after the one before
	ctx := context.Background()
	for i, c := range []struct {
		a  Actor
		in CoordinationConfirmation
	}{{smana, parentConfirm(p)}, {smana, childConfirm(p, "contract")}, {other, childConfirm(p, "consumer")}} {
		r.s.Now = func() time.Time { return accepted.Add(time.Duration(i+1) * 10 * time.Minute) }
		if _, err := r.s.Confirm(ctx, c.a, c.in); err != nil {
			t.Fatalf("confirm: %v", err)
		}
	}
	if _, err := r.s.Confirm(ctx, other, childConfirm(p, "consumer")); err != nil { // a replay
		t.Fatal(err)
	}
	if m.count("plan_version "+repoA+" confirmed") != 1 || m.count("wait "+repoA+" authorization 30m0s") != 1 {
		t.Fatalf("one confirmed version and its 30-minute wait: %v", m.got)
	}
	stale := p.DeepCopy()
	stale.Title = "Another title"
	if _, err := r.s.Confirm(ctx, smana, parentConfirm(*stale)); !errors.Is(err, ErrStalePlan) {
		t.Fatalf("err = %v, want stale_plan", err)
	}
	if m.count("plan_version "+repoA+" stale_refused") != 1 {
		t.Fatalf("a stale confirmation is counted: %v", m.got)
	}
	for range 2 {
		if err := r.s.File(ctx, r.s.Cfg.Current(), p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if m.count("family "+repoA+" filed") != 1 {
		t.Fatalf("one filed family: %v", m.got)
	}
	q := *p.DeepCopy()
	q.Version = 2
	child(t, &q, "consumer").Scope.Task = "Another scope."
	for range 2 {
		if _, err := r.s.Draft(ctx, smana, q, PlanHash(q), PlanningReconciliation{AttemptID: "a2"}); err != nil {
			t.Fatal(err)
		}
	}
	if m.count("plan_version "+repoA+" superseded") != 1 || m.count("child "+repoB+" scripts dropped") != 0 {
		t.Fatalf("v1 superseded once, no child dropped: %v", m.got)
	}
}

// A version that removes a child counts it dropped in its own repository, by its plan class.
func TestCoordinationDroppedChildIsCounted(t *testing.T) {
	s, _ := testService(t, "plan")
	m := &fakeMetrics{}
	s.Metrics = m
	p := draftPlan(t)
	mustDraft(t, s, p)
	q := *p.DeepCopy()
	q.Version = 2
	q.Children = slices.DeleteFunc(q.Children, func(c v1alpha1.CoordinationChild) bool { return c.ID == "consumer" })
	q.MergeOrder = slices.DeleteFunc(q.MergeOrder, func(id string) bool { return id == "consumer" })
	if _, err := s.Draft(context.Background(), smana, q, PlanHash(q), PlanningReconciliation{AttemptID: "a2"}); err != nil {
		t.Fatal(err)
	}
	if m.count("child "+repoB+" scripts dropped") != 1 {
		t.Fatalf("the dropped child is counted: %v", m.got)
	}
}
