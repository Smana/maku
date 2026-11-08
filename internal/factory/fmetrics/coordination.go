// SPDX-License-Identifier: Apache-2.0

package fmetrics

import (
	"context"
	"regexp"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// Multi-repository coordination's seven series (v4 observability). Every label is a configured
// repository or a closed vocabulary: family and child ids, artifact names, plan and acceptance text,
// viewers and channels never reach one (v4 security 6). The switch off records nothing.

// CoordinationFamilyOutcomes are families_total's outcomes, by primary. A parent has no closed or
// stopped end in this release: factory/stop pauses it, so neither is counted.
func CoordinationFamilyOutcomes() []string { return []string{"filed", "done"} }

// CoordinationPlanVersionOutcomes are plan_versions_total's outcomes: a version every confirmation
// covers, one a newer accepted version replaced, and a confirmation refused as stale_plan.
func CoordinationPlanVersionOutcomes() []string {
	return []string{"confirmed", "superseded", "stale_refused"}
}

// CoordinationChildOutcomes are children_total's outcomes: a child Task created, its pull request
// landed, the child removed from the plan by an accepted edit, and its merge reverted.
func CoordinationChildOutcomes() []string {
	return []string{"admitted", "landed", "dropped", "reverted"}
}

// CoordinationIntegrationPurposes and CoordinationIntegrationResults bound integration_runs_total.
func CoordinationIntegrationPurposes() []string { return []string{"beforeDependentMerge", "final"} }

// CoordinationIntegrationResults are an integration attempt's ends: its typed pass, a typed failure
// or no result, and a run its infrastructure lost (the Task Escalated).
func CoordinationIntegrationResults() []string {
	return []string{"passed", "checks_failed", "infrastructure_failed"}
}

// CoordinationWaitReasons are what a family waits on: every confirmation, a producer's landed
// artifact, an integration attempt's result, and a maintainer acting on the one recovery.
func CoordinationWaitReasons() []string {
	return []string{"authorization", "artifact", "integration", "recovery"}
}

// CoordinationReasons is admission_refusals_total's closed reason list: every bounded code a
// filing, a child admission or resume, or an integration admission refuses with. coordination's and
// the reconciler's tests pin that each of their codes is here.
func CoordinationReasons() []string {
	return []string{
		// The repository checks (coordination/checks.go).
		"repository_not_allowed", "private_repository_unsupported", "app_unavailable", "repository_not_owned",
		"access_unverified", "repository_permission", "class_not_allowed", "policy_changed",
		// The service's.
		"coordination_off", "coordination_parent_conflict", "coordination_not_found", "coordination_parent_terminal",
		"planning_budget_exhausted", "coordination_prerequisites", "coordination_not_executing", "artifact_unverified",
		"recovery_not_proposed", "plan_revision_required", "integration_stale", "integration_manifest_invalid",
		// The plan's.
		"invalid_plan", "invalid_dependency", "incomplete_plan", "policy_unresolved", "unknown_class", "policy_mismatch",
		"roles_mismatch", "allocation_below_run", "allocation_above_ceiling", "budget_overflow", "budget_exceeded",
		"stale_plan", "plan_version", "primary_requires_new_parent", "invalid_confirmation", "planning_cost_unverified",
		"allocation_below_spend", "integration_runs_exhausted",
		// The reconciler's lineage checks.
		"cross_repository_lineage", "coordination_lineage", "coordination_parent_missing", "coordination_scope_changed",
	}
}

// classRE is a merge class's name as the config and the fences spell it.
var classRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func boundClass(c string) string {
	if !classRE.MatchString(c) {
		return other
	}
	return c
}

// coordinationWaitBuckets: 1 m, 5 m, 15 m, 1 h, 4 h, 1 d, 3 d, 7 d. Waits are human-paced.
var coordinationWaitBuckets = []float64{60, 300, 900, 3600, 14400, 86400, 259200, 604800}

// coordination holds the counters and the histogram; allocated_micro_usd is collected from the
// tasks at scrape time (registerCollected).
type coordination struct {
	families     metric.Int64Counter
	planVersions metric.Int64Counter
	refusals     metric.Int64Counter
	children     metric.Int64Counter
	integration  metric.Int64Counter
	wait         metric.Float64Histogram
}

func newCoordination(meter metric.Meter) (coordination, error) {
	var c coordination
	var err error
	if c.families, err = meter.Int64Counter("agent_factory_coordination_families_total",
		metric.WithDescription("Coordination families by primary and outcome: filed, done (v4)."),
	); err != nil {
		return c, err
	}
	if c.planVersions, err = meter.Int64Counter("agent_factory_coordination_plan_versions_total",
		metric.WithDescription("Coordination plan versions confirmed, superseded or refused stale, by primary (v4)."),
	); err != nil {
		return c, err
	}
	if c.refusals, err = meter.Int64Counter("agent_factory_coordination_admission_refusals_total",
		metric.WithDescription("Coordination filings, child admissions and integration admissions refused, by repository and bounded reason (v4)."),
	); err != nil {
		return c, err
	}
	if c.children, err = meter.Int64Counter("agent_factory_coordination_children_total",
		metric.WithDescription("Coordination children admitted, landed, dropped or reverted, by repository and class (v4)."),
	); err != nil {
		return c, err
	}
	if c.integration, err = meter.Int64Counter("agent_factory_coordination_integration_runs_total",
		metric.WithDescription("Coordination integration attempts by primary, purpose and result (v4)."),
	); err != nil {
		return c, err
	}
	c.wait, err = meter.Float64Histogram("agent_factory_coordination_wait_seconds",
		metric.WithDescription("Seconds a coordination family waited for authorization, an artifact, integration or recovery (v4)."),
		metric.WithExplicitBucketBoundaries(coordinationWaitBuckets...))
	return c, err
}

// CoordinationFamily counts one family's outcome on its primary.
func (s *Set) CoordinationFamily(ctx context.Context, repository, outcome string) {
	s.coord.families.Add(ctx, 1, metric.WithAttributes(attribute.String("repository", s.repository(repository)),
		attribute.String("outcome", oneOf(outcome, CoordinationFamilyOutcomes()))))
}

// CoordinationPlanVersion counts one plan version's outcome on its primary.
func (s *Set) CoordinationPlanVersion(ctx context.Context, repository, outcome string) {
	s.coord.planVersions.Add(ctx, 1, metric.WithAttributes(attribute.String("repository", s.repository(repository)),
		attribute.String("outcome", oneOf(outcome, CoordinationPlanVersionOutcomes()))))
}

// CoordinationAdmissionRefused counts one refusal on the repository it is about: the primary for a
// filing or an integration admission, the child's for a child's admission or resume.
func (s *Set) CoordinationAdmissionRefused(ctx context.Context, repository, reason string) {
	s.coord.refusals.Add(ctx, 1, metric.WithAttributes(attribute.String("repository", s.repository(repository)),
		attribute.String("reason", oneOf(reason, CoordinationReasons()))))
}

// CoordinationChild counts one child's outcome in its own repository, by its plan class.
func (s *Set) CoordinationChild(ctx context.Context, repository, class, outcome string) {
	s.coord.children.Add(ctx, 1, metric.WithAttributes(attribute.String("repository", s.repository(repository)),
		attribute.String("class", boundClass(class)), attribute.String("outcome", oneOf(outcome, CoordinationChildOutcomes()))))
}

// CoordinationIntegrationRun counts one integration attempt's end on the primary.
func (s *Set) CoordinationIntegrationRun(ctx context.Context, repository, purpose, result string) {
	s.coord.integration.Add(ctx, 1, metric.WithAttributes(attribute.String("repository", s.repository(repository)),
		attribute.String("purpose", oneOf(purpose, CoordinationIntegrationPurposes())),
		attribute.String("result", oneOf(result, CoordinationIntegrationResults()))))
}

// CoordinationWait records one ended wait, by what the family waited on.
func (s *Set) CoordinationWait(ctx context.Context, repository, reason string, d time.Duration) {
	s.coord.wait.Record(ctx, max(d, 0).Seconds(), metric.WithAttributes(attribute.String("repository", s.repository(repository)),
		attribute.String("reason", oneOf(reason, CoordinationWaitReasons()))))
}

// coordinationAllocations is t's confirmed static allocations by kind (MR8): planning, the
// children's lifetime allocations, and maxRuns × runMicroUSD for integration. Only a live parent
// whose current plan its parent confirmed holds any: a draft or an ended family holds nothing.
func coordinationAllocations(t *v1alpha1.Task) (planning, children, integration int64, ok bool) {
	cs := t.Status.Coordination
	if t.Spec.Kind != v1alpha1.TaskKindCoordination || v1alpha1.TerminalPhase(t.Status.Phase) || cs == nil || cs.Plan == nil {
		return 0, 0, 0, false
	}
	confirmed := false
	for _, c := range cs.Confirmations {
		confirmed = confirmed || c.PlanHash == cs.PlanHash
	}
	if !confirmed {
		return 0, 0, 0, false
	}
	p := cs.Plan
	for _, c := range p.Children {
		children += c.BudgetMicroUSD
	}
	return p.Budget.PlanningMicroUSD, children, int64(p.Integration.MaxRuns) * p.Integration.RunMicroUSD, true
}
