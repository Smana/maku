// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

// approvedGateInputs is an approved head under approveToMerge with policy-bot off, changing one
// file under .github/.
func approvedGateInputs() ArmInputs {
	off := false
	cfg := armCfg()
	cfg.Repositories[0].Merge.ApproveToMerge, cfg.Repositories[0].Merge.PolicyBot, cfg.Repositories[0].Merge.GateStatus = true, &off, true
	cfg.Repositories[0].Classes["docs-links"] = config.Class{Shadow: true}
	pr := agentPR()
	pr.Reviews = []forge.Review{{ID: 1, Author: "Smana", State: "APPROVED", Commit: head, At: now.Add(time.Minute)}}
	return ArmInputs{PR: pr, Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""), Cfg: cfg,
		ReportedHead: head, HeadPushedAt: pushedAt, Files: changed("[a](https://x/old)", "[a](https://x/new)"),
		Stats: []forge.FileStat{{Path: ".github/workflows/ci.yaml", Status: "modified"}}, DiffRead: true}
}

// Re-review of #500: under a repository-authoritative policy, the gate paths are its classes'
// protectedFiles too, every variant included, and the check runs once over the ranked decision,
// so no any-branch variant arms an approval over a gate path.
func TestAGatePathUnderARepositoryPolicy(t *testing.T) {
	gate := config.Class{Shadow: true, ProtectedFiles: []string{`^\.github/`}}
	open := config.Class{Shadow: true}
	for name, c := range map[string]map[string]mergepolicy.Resolved{
		"another class of the policy":      {"docs-links": {Class: open}, "workflows": {Class: config.Class{ProtectedFiles: []string{`^\.github/`}}}},
		"one variant of the decided class": {"docs-links": {Class: open, Branches: []config.Class{open, gate}}},
		"the decided class itself":         {"docs-links": {Class: gate}},
	} {
		t.Run(name, func(t *testing.T) {
			in := approvedGateInputs()
			d := decideUnder(in, gatePolicy{on: true, snap: snapshotOf(c)})
			if d.Verdict != "human" || d.Reason != "gate_path" {
				t.Fatalf("%s %s, want human gate_path", d.Verdict, d.Reason)
			}
		})
	}
	// The variants alone, as a simulation decides them (DecideArmUnder).
	if d := DecideArmUnder(approvedGateInputs(), []config.Class{open, gate}, nil); d.Reason != "gate_path" {
		t.Fatalf("variants: %s %s, want gate_path", d.Verdict, d.Reason)
	}
	// No gate path anywhere: the approval arms.
	if d := decideUnder(approvedGateInputs(), gatePolicy{on: true, snap: snapshotOf(map[string]mergepolicy.Resolved{"docs-links": {Class: open}})}); d.Verdict != "arm" {
		t.Fatalf("%s %s, want arm", d.Verdict, d.Reason)
	}
}

// Review of #500, policy-bot's S9 kept without it: with merge.policyBot false, a change to a gate
// path (any class's protectedFiles: the .policy.yml gate list moved there) is never armed by the
// gate, a maintainer's approval included. It stays a human merge on GitHub.
func TestAnApprovedGatePathIsNeverArmedWithoutPolicyBot(t *testing.T) {
	off := false
	for name, c := range map[string]struct {
		policyBot       *bool
		stats           []forge.FileStat
		verdict, reason string
	}{
		"a gate path":                    {&off, []forge.FileStat{{Path: ".github/workflows/ci.yaml", Status: "modified"}}, "human", "gate_path"},
		"a rename from a gate path":      {&off, []forge.FileStat{{Path: "docs/ci.yaml", Previous: ".github/workflows/ci.yaml", Status: "renamed"}}, "human", "gate_path"},
		"no gate path":                   {&off, []forge.FileStat{{Path: "docs/guide.md", Status: "modified"}}, "arm", "maintainer_approved"},
		"policy-bot judges its own list": {nil, []forge.FileStat{{Path: ".github/workflows/ci.yaml", Status: "modified"}}, "arm", "maintainer_approved"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := armCfg()
			cfg.Repositories[0].Merge.ApproveToMerge, cfg.Repositories[0].Merge.PolicyBot, cfg.Repositories[0].Merge.GateStatus = true, c.policyBot, true
			cfg.Repositories[0].Classes["docs-links"] = config.Class{Shadow: true}
			cfg.Repositories[0].Classes["workflows"] = config.Class{ProtectedFiles: []string{`^\.github/`}}
			pr := agentPR()
			pr.Reviews = []forge.Review{{ID: 1, Author: "Smana", State: "APPROVED", Commit: head, At: now.Add(time.Minute)}}
			d := DecideArm(ArmInputs{PR: pr, Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""), Cfg: cfg,
				ReportedHead: head, HeadPushedAt: pushedAt, Files: changed("[a](https://x/old)", "[a](https://x/new)"),
				Stats: c.stats, DiffRead: true})
			if d.Verdict != c.verdict || d.Reason != c.reason {
				t.Fatalf("%s %s, want %s %s", d.Verdict, d.Reason, c.verdict, c.reason)
			}
		})
	}
}

// The reconciler reads the diff of an approved head when policy-bot is off, so the gate-path rule
// has a diff to decide on, and an approved gate path is handed off, never merged.
func TestTheGateReadsAnApprovedDiffWithoutPolicyBot(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	off := false
	g.r.Cfg.Current().Repositories[0].Merge.ApproveToMerge, g.r.Cfg.Current().Repositories[0].Merge.PolicyBot, g.r.Cfg.Current().Repositories[0].Merge.GateStatus = true, &off, true
	g.r.Cfg.Current().Repositories[0].Classes = map[string]config.Class{"docs-links": {Shadow: true}, "workflows": {ProtectedFiles: []string{`^\.github/`}}}
	g.f.SetFiles(map[string]string{".github/workflows/ci.yaml": "on: push\n"}, map[string]string{".github/workflows/ci.yaml": "on: [push]\n"})
	g.f.SetChecks(12, green("SUCCESS"))
	g.reconcile(t, "3buqdlot", 1)
	approve(g, t, 77, "abc")
	tk := g.reconcile(t, "3buqdlot", 3)
	if tk.Status.Phase == v1alpha1.PhaseAutoMerging || len(g.f.Armed()) != 0 {
		t.Fatalf("an approved gate path merged: %s %s %v", tk.Status.Phase, tk.Status.Reason, g.f.Armed())
	}
	if tk.Status.Reason != "gate_path" {
		t.Fatalf("handed off as %s %s, want gate_path", tk.Status.Phase, tk.Status.Reason)
	}
}
