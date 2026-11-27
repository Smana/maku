// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/triage"
)

// #798 (interim): OpenCode cannot run a Vertex alias, so a task that resolved to OpenCode by the
// default, the treatment or a maintainer's label runs on OpenHands once its run's model is one, said
// once on the issue and recorded as harnessSource model; a non-Vertex alias keeps OpenCode.
func TestOpenCodeNeverRunsAVertexAlias(t *testing.T) {
	for name, c := range map[string]struct {
		set                    func(*config.Config)
		labels                 []string
		model, harness, source string
	}{
		"the default and a model label": {
			set: func(c *config.Config) { c.Defaults.Harness = config.HarnessOpenCode }, labels: []string{"model:gemini-standard"},
			model: "gemini-standard", harness: config.HarnessOpenHands, source: triage.HarnessSourceModel},
		"the treatment harness and model": {
			set: func(c *config.Config) {
				c.Evaluation.ControlPercent, c.Evaluation.TreatmentHarness, c.Evaluation.TreatmentModel = 0, config.HarnessOpenCode, "gemini-light"
			}, model: "gemini-light", harness: config.HarnessOpenHands, source: triage.HarnessSourceModel},
		"a harness label and a model label": {
			labels: []string{"harness:opencode", "model:gemini-standard"},
			model:  "gemini-standard", harness: config.HarnessOpenHands, source: triage.HarnessSourceModel},
		"a non-Vertex alias keeps OpenCode": {
			set: func(c *config.Config) { c.Defaults.Harness = config.HarnessOpenCode }, labels: []string{"model:mistral-standard"},
			model: "mistral-standard", harness: config.HarnessOpenCode, source: triage.HarnessSourceDefault},
	} {
		t.Run(name, func(t *testing.T) {
			g := newRig(t, issueTask("3buqdlot", 7, "fix it"))
			cf := g.r.Cfg.Current()
			if cf.HarnessProfiles == nil {
				cf.HarnessProfiles = map[string]config.HarnessProfile{}
			}
			cf.HarnessProfiles[config.WorkflowStandard] = config.HarnessProfile{Template: "solo"} // a served workflow: triage records a profile
			if c.set != nil {
				c.set(cf)
			}
			g.r.Triage = triage.Classify{Forge: g.f}
			g.f.SetIssue(forge.Issue{Number: 7, Labels: c.labels})
			var evs []forge.LabelEvent
			for _, l := range c.labels {
				evs = append(evs, forge.LabelEvent{Actor: "Smana", Label: l, At: now})
			}
			g.f.SetEvents(7, evs...)
			tk := g.reconcile(t, "3buqdlot", 3) // Received → Triaged → Queued → Implementing
			s, ok := g.runs.specs[rid(0)]
			if !ok || s.Role != "implementer" || s.Model != c.model {
				t.Fatalf("the implementer run: %+v (phase %s)", s, tk.Status.Phase)
			}
			if s.Harness != c.harness || tk.Status.Runs[0].Provenance.Harness != c.harness {
				t.Fatalf("harness %q, provenance %q, want %q", s.Harness, tk.Status.Runs[0].Provenance.Harness, c.harness)
			}
			if p := tk.Status.Profile; c.harness == config.HarnessOpenHands && (p.Harness != config.HarnessOpenHands || p.HarnessSource != c.source) {
				t.Fatalf("profile %+v: the task moves to OpenHands, recorded as %q", p, c.source)
			}
			said := 0
			for _, m := range g.f.Comments(7) {
				if strings.Contains(m, "OpenCode can't use `"+c.model+"` yet (#798); this task runs on OpenHands.") {
					said++
				}
			}
			if want := map[bool]int{true: 1, false: 0}[c.harness == config.HarnessOpenHands]; said != want {
				t.Fatalf("said %d times, want %d: %v", said, want, g.f.Comments(7))
			}
		})
	}
}

func TestOpenCodeCannot(t *testing.T) {
	for model, want := range map[string]bool{"gemini-standard": true, "gemini-light": true, "mistral-standard": false,
		"kimi-frontier": false, "agent-default": false, "": false} {
		if got := config.OpenCodeCannot(model); got != want {
			t.Errorf("%q: %v", model, got)
		}
	}
}
