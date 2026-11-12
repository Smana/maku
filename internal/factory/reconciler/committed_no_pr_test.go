// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
)

// #476: a run that finished without a pull request is a no-op only when it changed nothing.
func TestRunEndedWithoutPullRequest(t *testing.T) {
	zero := strings.Repeat("0", 40)
	for name, c := range map[string]struct {
		note, push   bool
		phase, why   string
		says, notSay string
	}{
		"no commit is a no-op":            {false, false, v1alpha1.PhaseNoOp, "no_pr", "no pull request", ""},
		"a commit never pushed is lost":   {true, false, v1alpha1.PhaseEscalated, "committed_no_pr", "work was lost", ""},
		"a commit with the branch pushed": {true, true, v1alpha1.PhaseEscalated, "pushed_no_pr", "pushed its branch", ""},
		"a push without a harness note":   {false, true, v1alpha1.PhaseEscalated, "pushed_no_pr", "pushed its branch", ""},
	} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t, issueTask("3buqdlot", 7, "x"))
			g.reconcile(t, "3buqdlot", 3)
			if c.note {
				g.harnessNote(rid(0), "Harness (implementer): the agent committed.")
			}
			if c.push {
				g.f.SetActivity("agent/3buqdlot", forge.Push{Type: "branch_creation", Actor: "agents[bot]", Before: zero, After: strings.Repeat("a", 40)})
			} else {
				g.f.SetActivity("agent/3buqdlot")
			}
			g.runs.set(rid(0), "Succeeded")
			g.log.end(rid(0), "Succeeded", "agent_finished")
			tk := g.reconcile(t, "3buqdlot", 1)
			if tk.Status.Phase != c.phase || tk.Status.Reason != c.why {
				t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
			}
			g.r.Now = func() time.Time { return now.Add(g.r.settleWindow(g.r.Cfg.Current())) } // a terminal ending settles first (#42)
			g.reconcile(t, "3buqdlot", 1)
			if got := strings.Join(eventCommentBodies(g.f, 7), "\n"); !strings.Contains(got, c.says) {
				t.Errorf("%q does not say %q", got, c.says)
			}
		})
	}
}
