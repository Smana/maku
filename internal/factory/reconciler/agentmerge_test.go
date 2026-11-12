// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
)

// PR12: until H3 an implementer or tester token holds contents: write, which can merge a pull
// request whose required checks are green. A merge by the agents App is recorded as agent_merged
// and logged at error level, and otherwise ends as a human merge does; a maintainer's merge is
// still human_merged.
func TestAnAgentsMergeIsRecordedAsAgentMerged(t *testing.T) {
	for name, c := range map[string]struct {
		by, outcome string
		logged      bool
	}{
		"the agents App": {"ogenki-agents[bot]", "agent_merged", true},
		"a maintainer":   {"Smana", "human_merged", false},
	} {
		t.Run(name, func(t *testing.T) {
			g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
			var buf bytes.Buffer
			g.r.Log = slog.New(slog.NewTextHandler(&buf, nil))
			g.f.SetPR(forge.PR{Number: 12, NodeID: "PR_12", State: "MERGED", MergedBy: c.by, MergeCommitSHA: "def",
				Author: "ogenki-agents[bot]", HeadRef: "agent/3buqdlot", HeadSHA: "abc"})
			tk := g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Phase != v1alpha1.PhaseDone || tk.Status.Reason != "merged" || tk.Status.PullRequest.MergedBy != c.by {
				t.Fatalf("%s %s %+v", tk.Status.Phase, tk.Status.Reason, tk.Status.PullRequest)
			}
			if !slices.Contains(g.metrics.recorded, "pr docs-links "+c.outcome) {
				t.Fatalf("want %s: %q", c.outcome, g.metrics.recorded)
			}
			if other := map[string]string{"agent_merged": "human_merged", "human_merged": "agent_merged"}[c.outcome]; slices.Contains(g.metrics.recorded, "pr docs-links "+other) {
				t.Fatalf("counted %s too: %q", other, g.metrics.recorded)
			}
			if got := strings.Contains(buf.String(), "level=ERROR") && strings.Contains(buf.String(), "agents App merged"); got != c.logged {
				t.Fatalf("logged %t, want %t:\n%s", got, c.logged, buf.String())
			}
		})
	}
}
