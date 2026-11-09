// SPDX-License-Identifier: Apache-2.0

package fmetrics

import (
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// v2 D10: the task, outcome, intake, intervention and time-to-PR series carry a repository label
// whose values come from the config list only; anything else, an empty one included, is other.
func TestTheRepositoryLabelIsBoundedByTheList(t *testing.T) {
	onA, stray := tk("a", "Implementing"), tk("s", "Implementing")
	onA.Spec.Repository, stray.Spec.Repository = "smana/A", "Evil/repo"
	c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(onA, stray).Build()
	s, scrape := rig(t, c, func() bool { return true })
	s.ListRepositories("Smana/a", "Smana/b")
	b := WithRepository(t.Context(), "SMANA/b")
	s.PROutcome(b, "docs-links", "closed")
	s.TimeToPR(b, time.Minute, "issue", "standard", "solo")
	s.TaskTokens(b, 1000, "standard", "solo", "review")
	s.Intervention(b, "stop", "none", "none")
	s.IntakeError(b, "issue")
	s.PROutcome(WithRepository(t.Context(), "Evil/repo"), "docs-links", "closed")
	s.Intervention(t.Context(), "retry", "none", "none")
	out := scrape()
	for _, want := range []string{
		`maku_factory_pr_outcomes_total{class="docs-links",outcome="closed",principal="other",repository="Smana/b"`,
		`maku_factory_pr_outcomes_total{class="docs-links",outcome="closed",principal="other",repository="other"`,
		`maku_factory_time_to_pr_seconds_count{repository="Smana/b",source="issue",template="solo",tier="standard"}`,
		`maku_factory_human_interventions_total{cohort="none",kind="stop",profile="none",repository="Smana/b"`,
		`maku_factory_human_interventions_total{cohort="none",kind="retry",profile="none",repository="other"`,
		`maku_factory_intake_errors_total{repository="Smana/b",source="issue"`,
		`predicted_class="review",principal="system:factory",repository="Smana/a"`,
		`predicted_class="review",principal="system:factory",repository="other"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(out, "Evil/repo") || strings.Contains(out, "SMANA") {
		t.Fatal("a label value outside the list")
	}
}

// v2 D7 and the admission paths: one gauge per listed repository, and refusals counted by
// repository and reason; a reason outside the repository codes is not counted here.
func TestOwnershipAndAdmissionRefusals(t *testing.T) {
	s, scrape := rig(t, nil, func() bool { return true })
	s.ListRepositories("Smana/a", "Smana/b")
	s.RepositoryOwned(t.Context(), "Smana/a", true)
	s.RepositoryOwned(t.Context(), "smana/b", false)
	s.AdmissionRefused(WithRepository(t.Context(), "Smana/b"), "repository_not_owned")
	s.AdmissionRefused(WithRepository(t.Context(), "Smana/a"), "repository_permission")
	s.AdmissionRefused(WithRepository(t.Context(), "Smana/a"), "room_busy")
	out := scrape()
	for _, want := range []string{
		`maku_factory_repository_owned{repository="Smana/a"} 1`,
		`maku_factory_repository_owned{repository="Smana/b"} 0`,
		`maku_factory_admission_refusals_total{principal="other",reason="repository_not_owned",repository="Smana/b"} 1`,
		`maku_factory_admission_refusals_total{principal="other",reason="repository_permission",repository="Smana/a"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(out, "room_busy") {
		t.Fatal("only the repository refusals are counted")
	}
}

// Merge policy: bounded by the repository list and the closed reason list (MP18).
func TestPolicyMetricsAreBounded(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme()).Build()
	s, scrape := rig(t, c, func() bool { return true })
	s.ListRepositories("Smana/a")
	s.PolicyLoadError(t.Context(), "Smana/a", "invalid")
	s.PolicyLoadError(t.Context(), "Smana/a", "not-a-reason")
	s.PolicyDivergence(t.Context(), "Smana/a", 2)
	s.PolicyDualDecision(t.Context(), "Smana/a", "docs-links", "unexplained")
	s.PolicyDualDecision(t.Context(), "Smana/a", "docs-links", "not-an-agreement")
	s.ApprovalGrantsActive(t.Context(), "Smana/a", "docs-broad", 3)
	s.ApprovalGrantEvent(t.Context(), "Smana/a", "docs-broad", "disabled")
	s.ApprovalGrantEvent(t.Context(), "Smana/a", "docs-broad", "not-an-event")
	s.PolicyLoadError(t.Context(), "Smana/a", "stale")
	s.PolicyChange(t.Context(), "Smana/a", "docs-links", "relaxation")
	s.PolicyChange(t.Context(), "Smana/a", "docs-links", "not-a-change")
	out := scrape()
	for _, want := range []string{
		`maku_factory_policy_load_errors_total{reason="invalid",repository="Smana/a"`,
		`maku_factory_policy_central_divergence{repository="Smana/a"`,
		`maku_factory_policy_dual_decisions_total{agreement="unexplained",class="docs-links",repository="Smana/a"`,
		`maku_factory_approval_grants_active{class="docs-broad",repository="Smana/a"`,
		`maku_factory_approval_grant_events_total{class="docs-broad",event="disabled",repository="Smana/a"`,
		`maku_factory_policy_load_errors_total{reason="stale",repository="Smana/a"`,
		`maku_factory_policy_changes_total{change="relaxation",class="docs-links",repository="Smana/a"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	if strings.Contains(out, "not-a-reason") || strings.Contains(out, "not-an-agreement") || strings.Contains(out, "not-an-event") ||
		strings.Contains(out, "not-a-change") {
		t.Fatal("a reason outside the list")
	}
}
