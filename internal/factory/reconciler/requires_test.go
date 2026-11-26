// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

func TestARequiredTesterWithoutTestsPathsHandsOff(t *testing.T) {
	tk := issueTask("3buqdlot", 7, "# Update deploy\n\n## Acceptance check\n`task check` passes.")
	tk.Spec.Intake = &v1alpha1.TaskIntake{Class: "infra", Login: "Smana"}
	g := shippedRig(t, tk)
	g.r.Cfg.Current().Defaults.PredictedClass = "infra"
	rc := &g.r.Cfg.Current().Repositories[0]
	rc.Classes["infra"] = config.Class{
		Live:         true,
		AllowedFiles: []string{`^infrastructure/.*$`},
		Requires:     []string{"tests", "review"},
	}
	// No merge.contractCheck and no tests.paths in repository.
	tk = g.reconcile(t, "3buqdlot", 2) // Received -> Triaged -> Queued
	if tk.Status.Profile == nil || tk.Status.Profile.Requires == nil || !slices.Contains(tk.Status.Profile.Requires.Unmet, "tests") {
		t.Fatalf("expected unmet tests requirement on profile: %+v", tk.Status.Profile)
	}
	if tk.Spec.Template != "pair" {
		t.Fatalf("review requirement should still apply: template=%q", tk.Spec.Template)
	}
	comments := eventCommentBodies(g.f, 7)
	if !slices.ContainsFunc(comments, func(c string) bool { return strings.Contains(c, "required_stage_missing") }) {
		t.Fatalf("expected unmet requirement narration, got %q", comments)
	}

	// Merge gate hands off to a maintainer with required_stage_missing.
	head := strings.Repeat("a", 40)
	tk.Status.Verdict = "approve"
	tk.Status.Runs = []v1alpha1.RunRecord{{ID: rid(0), Role: "implementer"}, {ID: rid(1), Role: "reviewer", Verdict: "approve", HeadSHA: head}}
	d := DecideArm(ArmInputs{
		Cfg:          g.r.Cfg.Current(),
		Task:         tk,
		ReportedHead: head,
		Verifiers:    tk.Status.Runs[1:],
		DiffRead:     true,
		Stats:        []forge.FileStat{{Path: "infrastructure/app.yaml", Additions: 2}},
		PR: forge.PR{
			Number: 12, BaseRef: "main", HeadSHA: head, Author: g.r.Cfg.Current().AgentsLogin,
			HeadMessage: "update\n\nAgent-Run: " + rid(0),
		},
		Checks: forge.Checks{
			Runs: []forge.Check{
				{Name: "Pre-commit checks", App: forge.ActionsApp, State: "SUCCESS", Conclusion: "SUCCESS"},
				{Name: "Kubernetes validation", App: forge.ActionsApp, State: "SUCCESS", Conclusion: "SUCCESS"},
			},
			Statuses: []forge.Status{{Context: "policy-bot: main", State: "SUCCESS", Creator: "ogenki-merge-gate[bot]"}},
		},
	})
	if d.Verdict != "human" || d.Reason != reasonRequiredStageMissing {
		t.Fatalf("gate decision = %+v, want human/%s", d, reasonRequiredStageMissing)
	}
}

func TestARequirementFoundFromTheDiffRunsTheStageOnce(t *testing.T) {
	tk := issueTask("3buqdlot", 7, "# Quick tweak\n\n## Acceptance check\n`task check` passes.")
	g := shippedRig(t, tk)
	rc := &g.r.Cfg.Current().Repositories[0]
	rc.Workflow.Default = config.WorkflowQuick
	rc.Classes["infra"] = config.Class{
		Shadow:       true,
		AllowedFiles: []string{`^infrastructure/.*$`},
		Requires:     []string{"review"},
	}

	// Triage predicted review (not infra), so quick starts solo with no reviewer.
	tk = g.reconcile(t, "3buqdlot", 3) // Received -> Triaged -> Queued -> Implementing
	if tk.Spec.Template != "solo" {
		t.Fatalf("expected solo initially for quick, got %q", tk.Spec.Template)
	}
	head := strings.Repeat("b", 40)
	g.f.SetBranch("agent/3buqdlot", 12)
	g.f.SetPR(forge.PR{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", State: "OPEN", HeadSHA: head})
	g.f.SetFileStats(forge.FileStat{Path: "infrastructure/deploy.yaml", Additions: 3})
	g.runs.set(rid(0), "Succeeded")
	g.log.end(rid(0), "Succeeded", "agent_finished")

	// First push touched infrastructure/: late evaluation upgrades template to pair and queues reviewer.
	tk = g.reconcile(t, "3buqdlot", 2) // Implementing -> Queued (reviewer) -> Reviewing
	if tk.Spec.Template != "pair" || tk.Status.Phase != v1alpha1.PhaseReviewing || g.runs.specs[rid(1)].Role != "reviewer" {
		t.Fatalf("expected late review stage on diff match: template=%q phase=%s runs=%+v",
			tk.Spec.Template, tk.Status.Phase, g.runs.specs)
	}
	if tk.Status.Profile == nil || tk.Status.Profile.Requires == nil || !tk.Status.Profile.Requires.DiffChecked || tk.Status.Profile.Requires.By != "infra" {
		t.Fatalf("expected DiffChecked and By=infra: %+v", tk.Status.Profile)
	}
	foundNote := false
	for _, s := range g.log.states {
		if strings.Contains(s, "class `infra` requires review") {
			foundNote = true
			break
		}
	}
	if !foundNote {
		t.Fatalf("expected room narration explaining late stage, got %v", g.log.states)
	}
}

// A class bound from a MergePolicy, served the way the factory serves it: the merger App reaches
// the reconciler through Clients, per repository, never through r.Merger. Mirrors gcp-0's
// alert-rules (requires plan, shadow, file scope on the binding only) and task xfav2em3.
func TestABoundPolicyRequiringAPlanAddsTheScoperFromTheDiff(t *testing.T) {
	tk := issueTask("xfav2em3", 7, "# Name the threshold\n\nIn `observability/base/loggen/demo-vmrule.yaml`, name the threshold.\n\n## Acceptance\n- Only the summary changes.")
	g := shippedRig(t, tk)
	rc := &g.r.Cfg.Current().Repositories[0]
	rc.Workflow.Default = config.WorkflowStandard
	var p config.PolicySpec
	if err := json.Unmarshal([]byte(`{"evidence":"standard","monitor":{"followUpWindow":"2h","verification":{"within":"2h"}},"requires":["plan"]}`), &p); err != nil {
		t.Fatal(err)
	}
	cl, refusal := config.Resolve("alert-rules", p, config.Binding{
		Name: "alert-rules", Mode: config.ModeShadow,
		AllowedFiles:   []string{`^observability/.+\.ya?ml$`},
		ProtectedFiles: []string{`^docs/platform-constitution\.md$`},
	}, config.PolicyContext{}, rc.Merge)
	if refusal != nil {
		t.Fatalf("binding refused: %+v", refusal)
	}
	rc.Classes["alert-rules"] = cl
	g.r.Merger, g.r.Forge = nil, nil
	g.r.Clients = func(string) (TaskForge, forge.Merger, bool) { return g.f, g.f, true }

	tk = g.reconcile(t, "xfav2em3", 3) // Received -> Triaged -> Queued -> Implementing
	if tk.Spec.ScopeMode == string(config.ScopeScoper) || current(tk).Role != "implementer" {
		t.Fatalf("triage predicted another class, so no plan yet: scopeMode=%q role=%q", tk.Spec.ScopeMode, current(tk).Role)
	}
	head := strings.Repeat("c", 40)
	g.f.SetBranch("agent/xfav2em3", 12)
	g.f.SetPR(forge.PR{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", State: "OPEN", HeadSHA: head})
	g.f.SetFileStats(forge.FileStat{Path: "observability/base/loggen/demo-vmrule.yaml", Additions: 1, Deletions: 1})
	g.runs.set(runID("xfav2em3", 0), "Succeeded")
	g.log.end(runID("xfav2em3", 0), "Succeeded", "agent_finished")

	tk = g.reconcile(t, "xfav2em3", 2)
	if tk.Status.Profile == nil || tk.Status.Profile.Requires == nil || !tk.Status.Profile.Requires.DiffChecked {
		t.Fatalf("the first diff was never checked against the class: %+v", tk.Status.Profile)
	}
	if tk.Spec.ScopeMode != string(config.ScopeScoper) || tk.Status.Profile.Requires.By != "alert-rules" ||
		!slices.Contains(tk.Status.Profile.Requires.Stages, "plan") {
		t.Fatalf("alert-rules requires a plan: scopeMode=%q requires=%+v", tk.Spec.ScopeMode, tk.Status.Profile.Requires)
	}
	if next := g.runs.specs[runID("xfav2em3", 1)]; next.Role != "triager" || !scoperRun(tk) { // the scoper runs as the triager
		t.Fatalf("the run after the implementer is %q, want the scoper", next.Role)
	}
	if slices.ContainsFunc(tk.Status.Runs, func(rr v1alpha1.RunRecord) bool { return rr.Role == "reviewer" }) {
		t.Fatalf("the reviewer ran before the required plan: %+v", tk.Status.Runs)
	}
	foundNote := false
	for _, s := range g.log.states {
		foundNote = foundNote || strings.Contains(s, "class `alert-rules` requires plan")
	}
	if !foundNote {
		t.Fatalf("expected room narration of the late plan, got %v", g.log.states)
	}
}
