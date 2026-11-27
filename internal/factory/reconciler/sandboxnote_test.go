// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// Developer review gap 3, "say it": the first implementer run's start comment says what the sandbox
// does not run and names CI as the judge; later runs' comments do not repeat it.
func TestTheFirstRunSaysWhatTheSandboxSkips(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	start(t, g)
	c := startComments(g)
	if len(c) != 1 || !strings.Contains(c[0], "started the implementer's run") || !strings.Contains(c[0], "does not run the repository's agent checks") ||
		!strings.Contains(c[0], "CI is the judge") {
		t.Fatalf("%q", c)
	}
}

func TestTheFirstRunListsTheCIOnlyChecks(t *testing.T) {
	g := checkedRig(t)
	g.f.SetMainFiles(map[string]string{".github/agent-checks.yaml": "version: 1\nsteps: [{name: unit, run: go test ./..., ci: test}]\n" +
		"ciOnly:\n  - {check: test, part: integration tests, why: they need Postgres}\n"})
	start(t, g)
	c := startComments(g)
	if len(c) != 1 || !strings.Contains(c[0], "`test`: integration tests (they need Postgres)") || !strings.Contains(c[0], "CI is the judge") {
		t.Fatalf("%q", c)
	}
}

func TestAReadyRepositoryNeedsNoNote(t *testing.T) {
	g := checkedRig(t)
	g.f.SetMainFiles(map[string]string{".github/agent-checks.yaml": "version: 1\nsteps: [{name: unit, run: go test ./..., ci: test}]\n"})
	start(t, g)
	if c := startComments(g); len(c) != 1 || strings.Contains(c[0], "CI is the judge") {
		t.Fatalf("%q", c)
	}
}

// checkedRig serves a repository whose default workflow runs the agent checks.
func checkedRig(t *testing.T) *rig {
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	g.r.Cfg.Current().HarnessProfiles = map[string]config.HarnessProfile{"checked": {Checks: true}, "standard": {}}
	g.r.Cfg.Current().Repositories[0].Workflow.Default = "checked"
	return g
}

// start reconciles until the first implementer run started.
func start(t *testing.T, g *rig) {
	t.Helper()
	for range 4 {
		if tk := g.reconcile(t, "3buqdlot", 2); tk.Status.Phase == v1alpha1.PhaseImplementing {
			return
		}
	}
	t.Fatal("no implementer run started")
}

// startComments are the issue's run-start comments.
func startComments(g *rig) []string {
	var out []string
	for _, c := range eventCommentBodies(g.f, 7) {
		if strings.Contains(c, "started the implementer's run") {
			out = append(out, c)
		}
	}
	return out
}
