// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/minter"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/runwatch"
)

// TestACustomRoleNeverGetsAWriteToken proves that a custom role declared with capability: read runs
// under the reviewer run role, stamps its persona on the AgentRun claim, carries its brief/model/
// effort, and maps exclusively to read-only GitHub token permissions in minter.Roles.
func TestACustomRoleNeverGetsAWriteToken(t *testing.T) {
	cfg := &config.Config{
		Tiers: map[string]config.Tier{
			"light":    {Model: "tier-light", RunTokens: 300_000, TaskTokens: 600_000, RunMinutes: 20},
			"standard": {Model: "agent-default", RunTokens: 1_500_000, TaskTokens: 3_000_000, RunMinutes: 45},
			"frontier": {Model: "tier-frontier", RunTokens: 4_000_000, TaskTokens: 8_000_000, RunMinutes: 90},
		},
		Templates: map[string]config.Template{
			"pair": {Roles: []string{"implementer", "reviewer"}, MaxReviewRounds: 2},
		},
		Roles: map[string]config.RoleDef{
			"security-reviewer": {
				Capability: config.CapabilityRead,
				Model:      "agent-default",
				Effort:     "high",
				Brief:      "roles/security-reviewer.md",
			},
		},
		Repositories: []config.Repository{{Name: "acme/app", Maintainers: []string{"alice"}}},
	}
	cfg.SetRoleBrief("security-reviewer", "Check auth boundaries and secret handling.")

	tk := &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"},
		Spec: v1alpha1.TaskSpec{
			Repository: "acme/app",
			DataClass:  "public",
			Template:   "pair",
			Budget:     v1alpha1.Budget{Tier: "standard", Model: "agent-default", RunTokens: 1_500_000, TaskTokens: 3_000_000, RunMinutes: 45},
		},
		Status: v1alpha1.TaskStatus{
			RoomRef:     "3buqdlot",
			PullRequest: &v1alpha1.PullRequestRef{Number: 12, URL: "https://github.com/acme/app/pull/12"},
		},
	}

	r := &Reconciler{}
	spec := r.verifierSpec(cfg, tk, "security-reviewer")
	spec.RunID = "7f3cq2xz"

	if spec.Role != "reviewer" {
		t.Fatalf("spec.Role = %q, want reviewer (read-only run role)", spec.Role)
	}
	if spec.Persona != "security-reviewer" {
		t.Fatalf("spec.Persona = %q, want security-reviewer", spec.Persona)
	}
	if spec.Model != "agent-default" || spec.Effort != "high" {
		t.Fatalf("model=%q effort=%q, want agent-default / high", spec.Model, spec.Effort)
	}
	if !strings.Contains(spec.TaskText, "Check auth boundaries") {
		t.Fatalf("spec.TaskText = %q, want custom role brief", spec.TaskText)
	}

	// Build the claim and verify spec.role stays reviewer while persona rides the annotation.
	if err := spec.Validate(); err != nil {
		t.Fatalf("spec.Validate(): %v", err)
	}
	claim := runs.Build(runwatch.DefaultKind, spec)
	gotRole, _, _ := unstructured.NestedString(claim.Object, "spec", "role")
	if gotRole != "reviewer" {
		t.Fatalf("claim spec.role = %q, want reviewer", gotRole)
	}
	if gotPersona := claim.GetAnnotations()[runs.AnnPersona]; gotPersona != "security-reviewer" {
		t.Fatalf("claim %s = %q, want security-reviewer", runs.AnnPersona, gotPersona)
	}
	if gotText, _, _ := unstructured.NestedString(claim.Object, "spec", "task", "text"); !strings.Contains(gotText, "Check auth boundaries") {
		t.Fatalf("claim spec.task.text = %q, want brief beside pull request URL", gotText)
	}
	parsed, ok := runs.FromUnstructured(runwatch.DefaultKind, claim)
	if !ok || parsed.Role != "reviewer" || parsed.Persona != "security-reviewer" {
		t.Fatalf("FromUnstructured = (%+v, %v), want reviewer / security-reviewer", parsed, ok)
	}

	// Verify the minter grants strictly read-only permissions for the claim's run role.
	perms, err := minter.Roles.For(gotRole, nil)
	if err != nil {
		t.Fatalf("minter.Roles.For(%q): %v", gotRole, err)
	}
	for scope, level := range perms {
		if level != "read" {
			t.Fatalf("custom read role got non-read permission %s=%q", scope, level)
		}
	}
}
