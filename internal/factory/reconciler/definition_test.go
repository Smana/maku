// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/triage"
)

// TestTheSpecsMeasureIsExpressible proves the spec's last measure (WR11, Task I4):
// a workflow with a tester and a second read-only custom reviewer loads from config alone and runs
// tester -> implementer -> reviewer -> security-reviewer, posting the custom role's findings as a
// PR comment and honoring maxRounds on a custom read role when configured.
func TestTheSpecsMeasureIsExpressible(t *testing.T) {
	tk := issueTask("3buqdlot", 7, "# Add token check\n\n## Acceptance check\n`go test ./...` passes")
	g := newRig(t, tk)
	cf := g.r.Cfg.Current()
	cf.Roles = map[string]config.RoleDef{
		"security-reviewer": {
			Capability: config.CapabilityRead,
			Model:      "agent-default",
			Effort:     "high",
			Brief:      "roles/security-reviewer.md",
		},
	}
	cf.SetRoleBrief("security-reviewer", "Audit auth boundaries and token scopes.")
	if cf.HarnessProfiles == nil {
		cf.HarnessProfiles = map[string]config.HarnessProfile{}
	}
	cf.HarnessProfiles["thorough"] = config.HarnessProfile{
		Description: "Tester, implementer, reviewer and security reviewer.",
		Budget:      "frontier",
		Stages: []config.StageDef{
			{Role: "tester"},
			{Role: "implementer", Stall: true},
			{Role: "reviewer", MaxRounds: 2},
			{Role: "security-reviewer"},
		},
	}
	cf.Repositories[0].Workflow = config.RepositoryWorkflow{Default: "thorough", TestFirst: true}
	cf.Repositories[0].Merge.ContractCheck = "Unit tests"

	// Triage -> Triaged -> Queued -> tester run rid(0).
	g.f.SetFile(".github/agent-checks.yaml", "tests:\n  paths: [tests/]\n")
	g.reconcile(t, "3buqdlot", 3)
	if len(g.runs.specs) != 1 || g.runs.specs[rid(0)].Role != "tester" {
		t.Fatalf("first run = %+v, want tester", g.runs.specs[rid(0)])
	}

	// Tester finishes without contract (unreported) -> Queued -> implementer rid(1).
	g.finish(rid(0), "Succeeded", "agent_finished")
	g.reconcile(t, "3buqdlot", 1) // stamps cur.Finished and waits for contractPushWait
	at := g.r.Now()
	g.r.Now = func() time.Time { return at.Add(contractPushWait + time.Second) }
	tk = g.reconcile(t, "3buqdlot", 2) // testerEnded -> Queued -> implementer rid(1)
	if len(tk.Status.Runs) != 2 || g.runs.specs[rid(1)].Role != "implementer" {
		t.Fatalf("second run = %+v, want implementer (phase=%s)", g.runs.specs[rid(1)], tk.Status.Phase)
	}

	// Implementer opens PR #12 at head1 and succeeds -> Queued -> built-in reviewer rid(2).
	g.f.SetBranch("agent/3buqdlot", 12)
	g.f.SetPR(pr12At(head1))
	g.finish(rid(1), "Succeeded", "agent_finished")
	tk = g.reconcile(t, "3buqdlot", 2)
	if len(tk.Status.Runs) != 3 || g.runs.specs[rid(2)].Role != "reviewer" || g.runs.specs[rid(2)].Persona != "reviewer" {
		t.Fatalf("third run = %+v, want built-in reviewer", g.runs.specs[rid(2)])
	}

	// Built-in reviewer approves head1 -> Queued -> custom security-reviewer rid(3).
	g.log.verdict(rid(2), "approve", head1, "Looks solid.")
	g.finish(rid(2), "Succeeded", "agent_finished")
	tk = g.reconcile(t, "3buqdlot", 2)
	if len(tk.Status.Runs) != 4 {
		t.Fatalf("runs = %d, want 4 (phase=%s nextRole=%q)", len(tk.Status.Runs), tk.Status.Phase, tk.Status.NextRole)
	}
	secSpec := g.runs.specs[rid(3)]
	if secSpec.Role != "reviewer" || secSpec.Persona != "security-reviewer" || !strings.Contains(secSpec.TaskText, "Audit auth boundaries") {
		t.Fatalf("fourth run spec = %+v, want reviewer/security-reviewer with brief", secSpec)
	}

	// Custom security-reviewer reports findings (verdict: changes, maxRounds: 0 -> advisory comment only).
	// Built-in reviewer's Task.status.verdict ("approve") MUST remain untouched, findings MUST be
	// posted as a PR comment, and the task MUST advance to AwaitingCI.
	g.log.verdict(rid(3), "changes", head1, "Consider narrowing token lifetime to 5m.")
	g.finish(rid(3), "Succeeded", "agent_finished")
	tk = g.reconcile(t, "3buqdlot", 2)
	if tk.Status.Phase != v1alpha1.PhaseAwaitingCI {
		t.Fatalf("phase = %s, want AwaitingCI (reason=%s)", tk.Status.Phase, tk.Status.Reason)
	}
	if tk.Status.Verdict != "approve" {
		t.Fatalf("Task.status.verdict = %q, want approve (built-in reviewer only)", tk.Status.Verdict)
	}
	if tk.Status.Runs[3].Verdict != "changes" || tk.Status.Runs[3].Persona != "security-reviewer" {
		t.Fatalf("custom run record = %+v", tk.Status.Runs[3])
	}
	// Verify findings were posted as a comment on PR #12.
	foundComment := false
	for _, c := range g.f.Comments(12) {
		if strings.Contains(c, "security-reviewer") && strings.Contains(c, "Consider narrowing token lifetime") {
			foundComment = true
			break
		}
	}
	if !foundComment {
		t.Fatalf("expected PR #12 comment with security-reviewer findings, got comments=%v", g.f.Comments(12))
	}
}

// TestCustomReadRoleMaxRoundsSendsBackToImplementer verifies that when a custom read role stage
// declares maxRounds > 0, a "changes" verdict sends the task back to the implementer like the
// built-in reviewer while still posting its findings comment and preserving Task.status.verdict.
func TestCustomReadRoleMaxRoundsSendsBackToImplementer(t *testing.T) {
	tk := issueTask("3buqdlot", 7, "# Fix auth\n\n## Acceptance check\n`go test ./...` passes")
	g := newRig(t, tk)
	cf := g.r.Cfg.Current()
	cf.Roles = map[string]config.RoleDef{
		"security-reviewer": {
			Capability: config.CapabilityRead,
			Brief:      "roles/security-reviewer.md",
		},
	}
	cf.SetRoleBrief("security-reviewer", "Check auth.")
	if cf.HarnessProfiles == nil {
		cf.HarnessProfiles = map[string]config.HarnessProfile{}
	}
	cf.HarnessProfiles["sec-loop"] = config.HarnessProfile{
		Budget: "standard",
		Stages: []config.StageDef{
			{Role: "implementer", Stall: true},
			{Role: "security-reviewer", MaxRounds: 1},
		},
	}
	cf.Repositories[0].Workflow = config.RepositoryWorkflow{Default: "sec-loop"}

	g.reconcile(t, "3buqdlot", 3) // implementer rid(0)
	g.f.SetBranch("agent/3buqdlot", 12)
	g.f.SetPR(pr12At(head1))
	g.finish(rid(0), "Succeeded", "agent_finished")
	tk = g.reconcile(t, "3buqdlot", 2) // security-reviewer rid(1)
	if tk.Status.Phase != v1alpha1.PhaseReviewing || g.runs.specs[rid(1)].Persona != "security-reviewer" {
		t.Fatalf("phase=%s spec=%+v", tk.Status.Phase, g.runs.specs[rid(1)])
	}

	g.log.verdict(rid(1), "changes", head1, "Escape SQL parameter.")
	g.finish(rid(1), "Succeeded", "agent_finished")
	tk = g.reconcile(t, "3buqdlot", 2) // back to Queued -> implementer rid(2)
	if tk.Status.Phase != v1alpha1.PhaseImplementing || len(tk.Status.Runs) != 3 || tk.Status.Runs[2].Role != "implementer" {
		t.Fatalf("expected revision implementer, got phase=%s runs=%+v", tk.Status.Phase, tk.Status.Runs)
	}
	if tk.Status.Verdict != "" {
		t.Fatalf("custom role wrote Task.status.verdict = %q, want empty", tk.Status.Verdict)
	}
}

// TestTheGateReadsTheBuiltInReviewerOnly pins that DecideArm's merge gate reads the built-in
// reviewer's verdict ONLY and never treats a custom read role's approve as satisfying the gate.
func TestTheGateReadsTheBuiltInReviewerOnly(t *testing.T) {
	cfg := armCfg()
	cfg.Roles = map[string]config.RoleDef{
		"security-reviewer": {Capability: config.CapabilityRead, Brief: "roles/security-reviewer.md"},
	}

	t.Run("custom role approve cannot satisfy built-in reviewer requirement", func(t *testing.T) {
		tk := armTask("docs-links", "pair", "approve")
		verifs := []v1alpha1.RunRecord{
			{ID: "s3cr3v1w", Role: "reviewer", Persona: "security-reviewer", Trigger: "review", Verdict: "approve", HeadSHA: head},
		}
		d := DecideArm(ArmInputs{
			PR:           agentPR(),
			Checks:       green("SUCCESS"),
			Task:         tk,
			Cfg:          cfg,
			ReportedHead: head,
			Verifiers:    verifs,
			Files:        changed("[x](old.md)", "[x](new.md)"),
		})
		if d.Verdict != "human" || d.Reason != "no_approving_verdict" {
			t.Fatalf("got %+v, want human/no_approving_verdict", d)
		}
	})

	t.Run("fresh custom role approve cannot mask stale built-in reviewer approve", func(t *testing.T) {
		tk := armTask("docs-links", "pair", "approve")
		verifs := []v1alpha1.RunRecord{
			{ID: "r3vnwr3v", Role: "reviewer", Persona: "reviewer", Trigger: "review", Verdict: "approve", HeadSHA: oldHead},
			{ID: "s3cr3v1w", Role: "reviewer", Persona: "security-reviewer", Trigger: "review", Verdict: "approve", HeadSHA: head},
		}
		d := DecideArm(ArmInputs{
			PR:           agentPR(),
			Checks:       green("SUCCESS"),
			Task:         tk,
			Cfg:          cfg,
			ReportedHead: head,
			Verifiers:    verifs,
			Files:        changed("[x](old.md)", "[x](new.md)"),
		})
		if d.Verdict != "human" || d.Reason != "verdict_stale" {
			t.Fatalf("got %+v, want human/verdict_stale", d)
		}
	})

	t.Run("fresh built-in reviewer approve arms even when custom role posted changes", func(t *testing.T) {
		tk := armTask("docs-links", "pair", "approve")
		verifs := []v1alpha1.RunRecord{
			{ID: "r3vnwr3v", Role: "reviewer", Persona: "reviewer", Trigger: "review", Verdict: "approve", HeadSHA: head},
			{ID: "s3cr3v1w", Role: "reviewer", Persona: "security-reviewer", Trigger: "review", Verdict: "changes", HeadSHA: head},
		}
		d := DecideArm(ArmInputs{
			PR:           agentPR(),
			Checks:       green("SUCCESS"),
			Task:         tk,
			Cfg:          cfg,
			ReportedHead: head,
			Verifiers:    verifs,
			Files:        changed("[x](old.md)", "[x](new.md)"),
		})
		if d.Verdict != "arm" {
			t.Fatalf("got %+v, want arm (built-in reviewer approved fresh head)", d)
		}
	})

	t.Run("workflow with custom role only (no built-in reviewer) still fails verdictEvidence when class has no diff shape", func(t *testing.T) {
		cfg2 := armCfg()
		cfg2.Repositories[0].Classes = map[string]config.Class{"code": {Live: true}}
		tk := armTask("code", "solo", "")
		verifs := []v1alpha1.RunRecord{
			{ID: "s3cr3v1w", Role: "reviewer", Persona: "security-reviewer", Trigger: "review", Verdict: "approve", HeadSHA: head},
		}
		d := DecideArm(ArmInputs{
			PR:           agentPR(),
			Checks:       green("SUCCESS"),
			Task:         tk,
			Cfg:          cfg2,
			ReportedHead: head,
			Verifiers:    verifs,
			DiffRead:     true,
			Stats:        []forge.FileStat{{Path: "main.go", Additions: 1}},
		})
		if d.Verdict != "human" || (d.Reason != reasonVerdictMissing && !strings.Contains(d.Rows.Considered, "code:"+reasonVerdictMissing)) {
			t.Fatalf("got %+v, want human with %s", d, reasonVerdictMissing)
		}
	})
}

// TestTheHardCodedOrderAndTheCompiledListAgreeOnThePresets verifies that for every shipped preset
// (quick, standard, careful, with and without testFirst), the compiled stage list drives the exact
// same stage order as the pre-WR11 hard-coded order.
func TestTheHardCodedOrderAndTheCompiledListAgreeOnThePresets(t *testing.T) {
	profiles, templates, loops := config.ShippedWorkflows()
	cfg := &config.Config{
		HarnessProfiles: profiles,
		Templates:       templates,
		Loops:           loops,
		Tiers: map[string]config.Tier{
			"light":    {Model: "tier-light", RunTokens: 300_000, TaskTokens: 600_000, RunMinutes: 20},
			"standard": {Model: "agent-default", RunTokens: 1_500_000, TaskTokens: 3_000_000, RunMinutes: 45},
			"frontier": {Model: "tier-frontier", RunTokens: 4_000_000, TaskTokens: 8_000_000, RunMinutes: 90},
		},
		Repositories: []config.Repository{
			{Name: "acme/off"},
			{Name: "acme/on", Workflow: config.RepositoryWorkflow{TestFirst: true}, Merge: config.Merge{ContractCheck: "test"}},
		},
	}

	cases := []struct {
		name      string
		repo      string
		workflow  string
		wantOrder []string
	}{
		{"quick", "acme/off", config.WorkflowQuick, []string{"implementer"}},
		{"quick/testFirst", "acme/on", config.WorkflowQuick, []string{"implementer"}},
		{"standard", "acme/off", config.WorkflowStandard, []string{"implementer", "reviewer"}},
		{"standard/testFirst", "acme/on", config.WorkflowStandard, []string{"implementer", "reviewer"}},
		{"careful/testFirst-off", "acme/off", config.WorkflowCareful, []string{"scoper", "implementer", "reviewer"}},
		{"careful/testFirst-on", "acme/on", config.WorkflowCareful, []string{"scoper", "tester", "implementer", "reviewer"}},
	}

	r := &Reconciler{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc := cfg.RepositoryFor(tc.repo)
			p, _ := cfg.Workflow(tc.workflow)
			tk := &v1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot"},
				Spec: v1alpha1.TaskSpec{
					Repository: tc.repo,
					DataClass:  "public",
					Budget:     triage.BudgetFor(cfg, "standard"),
				},
				Status: v1alpha1.TaskStatus{
					Profile: &v1alpha1.ProfileStatus{
						Name:      tc.workflow,
						TestFirst: cfg.TestFirstIn(rc, tc.workflow),
					},
				},
			}
			r.profileSpec(cfg, tk, tc.workflow, p)

			// Simulate the hard-coded pre-I4 order: scoperDue -> testerDue -> implementer -> reviewer.
			var hardCoded []string
			if scoperDue(tk) {
				hardCoded = append(hardCoded, "scoper")
			}
			if r.testerDue(cfg, tk) {
				hardCoded = append(hardCoded, "tester")
			}
			hardCoded = append(hardCoded, "implementer")
			if slices.Contains(cfg.Templates[tk.Spec.Template].Roles, "reviewer") {
				hardCoded = append(hardCoded, "reviewer")
			}

			// Simulate the compiled stage list order used by queued() and nextVerifier().
			var compiledOrder []string
			for _, st := range r.compiledStagesForTask(cfg, tk) {
				switch st.Role {
				case "scoper":
					if scoperDue(tk) {
						compiledOrder = append(compiledOrder, "scoper")
					}
				case "tester":
					if r.testerDue(cfg, tk) {
						compiledOrder = append(compiledOrder, "tester")
					}
				case "implementer":
					compiledOrder = append(compiledOrder, "implementer")
				}
			}
			for cur := "implementer"; ; {
				next := r.nextVerifier(cfg, tk, cur)
				if next == "" {
					break
				}
				compiledOrder = append(compiledOrder, next)
				cur = next
			}

			if !slices.Equal(compiledOrder, tc.wantOrder) {
				t.Fatalf("compiled order = %v, want %v", compiledOrder, tc.wantOrder)
			}
			if !slices.Equal(compiledOrder, hardCoded) {
				t.Fatalf("compiled order %v disagrees with hard-coded order %v", compiledOrder, hardCoded)
			}
		})
	}
}
