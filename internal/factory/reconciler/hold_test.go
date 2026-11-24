// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

// MR10: factory/hold hands a pull request to a human with held on every path that would merge
// it, and records no shadow would-merge; without the label the verdicts are unchanged.
func TestHoldLabelStopsEveryPath(t *testing.T) {
	approved := []forge.Review{{ID: 1, Author: "Smana", State: "APPROVED", Commit: head, At: now.Add(time.Minute)}}
	for name, c := range map[string]struct {
		mutate          func(*config.Config, *forge.PR)
		verdict, reason string
	}{
		"class conditions": {func(*config.Config, *forge.PR) {}, "arm", ""},
		"through policy-bot": {func(cfg *config.Config, _ *forge.PR) {
			cfg.Repositories[0].Merge.PolicyBot = new(true)
		}, "arm", ""},
		"maintainer approved": {func(cfg *config.Config, pr *forge.PR) {
			cfg.Repositories[0].Merge.ApproveToMerge = true
			pr.Reviews = approved
		}, "arm", "maintainer_approved"},
		"shadow class": {func(cfg *config.Config, _ *forge.PR) {
			cfg.Repositories[0].Classes["docs-links"] = config.Class{Shadow: true}
		}, "shadow", "shadow_would_arm"},
	} {
		for _, held := range []bool{false, true} {
			cfg, pr := armCfg(), agentPR()
			cfg.Repositories[0].Merge.PolicyBot = new(false)
			c.mutate(cfg, &pr)
			if held {
				pr.Labels = []string{"documentation", LabelHold}
			}
			d := DecideArm(ArmInputs{PR: pr, Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""), Cfg: cfg,
				ReportedHead: head, HeadPushedAt: pushedAt, Files: changed("[a](https://x/old)", "[a](https://x/new)"), DiffRead: true})
			want, reason := c.verdict, c.reason
			if held {
				want, reason = "human", "held"
			}
			if d.Verdict != want || d.Reason != reason {
				t.Errorf("%s, held %v: %s %s, want %s %s", name, held, d.Verdict, d.Reason, want, reason)
			}
		}
	}
}

// MR10: removing factory/hold lets the next pass decide only when a maintainer removed it. An
// implementer token can remove the label, but the held hand-off remains with a human.
func TestRemovingTheHoldReturnsToTheGate(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.f.SetChecks(12, green("SUCCESS"))
	pr := forge.PR{Number: 12, NodeID: "PR_12", State: "OPEN", Title: "docs: fix a link", Author: "ogenki-agents[bot]",
		BaseRef: "main", HeadRef: "agent/3buqdlot", HeadSHA: "abc", HeadMessage: "docs: fix\n\nAgent-Run: " + rid(0),
		HeadCommittedAt: now.Add(-time.Hour), Labels: []string{LabelHold}}
	g.f.SetPR(pr)
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != "held" || len(g.f.Armed()) != 0 {
		t.Fatalf("held: %s %s %v", tk.Status.Phase, tk.Status.Reason, g.f.Armed())
	}
	pr.Labels = nil
	g.f.SetPR(pr)
	g.f.SetEvents(12,
		forge.LabelEvent{Actor: "Smana", Label: LabelHold, At: now.Add(-2 * time.Minute)},
		forge.LabelEvent{Actor: "ogenki-agents[bot]", Label: LabelHold, At: now.Add(-time.Minute), Removed: true})
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != "held" {
		t.Fatalf("an agent lifted the hold: %s %s", tk.Status.Phase, tk.Status.Reason)
	}
	g.f.SetEvents(12,
		forge.LabelEvent{Actor: "Smana", Label: LabelHold, At: now.Add(-2 * time.Minute)},
		forge.LabelEvent{Actor: "Smana", Label: LabelHold, At: now.Add(-time.Minute), Removed: true})
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingCI || tk.Status.Reason != "reentered_unheld" {
		t.Fatalf("maintainer unheld: %s %s", tk.Status.Phase, tk.Status.Reason)
	}
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAutoMerging || len(g.f.Armed()) != 1 {
		t.Fatalf("decided again: %s %s %v", tk.Status.Phase, tk.Status.Reason, g.f.Armed())
	}
}
