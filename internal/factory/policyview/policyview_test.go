// SPDX-License-Identifier: Apache-2.0

package policyview

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

const base = `factory: aws-0
mergePolicies:
  - name: docs-links
    mode: shadow
    allowedFiles: ['^docs/.+\.md$', '^README\.md$']
    maxPatchFiles: 10
    holdoutChecks: [links]
    protectedFiles: ['^docs/specs/.+$']
    requiresApproval:
      - changedFiles: ['^docs/security/.+$']
    maxPerDay: 2
  - name: revert
    mode: shadow
`

func rig(sim bool) (*config.Config, *config.Repository, evidence.Report) {
	cfg := &config.Config{Factory: "aws-0", HarnessProfiles: map[string]config.HarnessProfile{"plain": {}},
		PolicyManagement: config.PolicyManagement{Simulation: sim},
		Policies:         map[string]config.ServedPolicy{"docs-links": {Generation: 1}, "revert": {Generation: 1}},
		Repositories:     []config.Repository{{Name: "Smana/cloud-native-ref"}}}
	f, _ := mergepolicy.Parse([]byte(base))
	rep := evidence.Report{Repository: "Smana/cloud-native-ref", File: base, Policy: f, DefaultBranch: "main",
		Classes: []evidence.ClassReport{{Class: "docs-links", Evidence: &evidence.Evidence{Shadow: evidence.ShadowTally{Refused: 3, RefusedMergedUnchanged: 2}}}}}
	return cfg, &cfg.Repositories[0], rep
}

// The editor's check is the factory's own: strict parsing, factory: read-only, the guardrails;
// a valid file comes back with its structural change, the counterfactual and the edit link.
func TestCheck(t *testing.T) {
	r := strings.Replace
	for name, tc := range map[string]struct {
		file, err, kind, why string
	}{
		"unchanged":            {base, "the proposal is the file as it is", "", ""},
		"not YAML":             {"factory: aws-0\nmergePolicies: [\n", "not YAML", "", ""},
		"an unknown condition": {r(base, "maxPatchFiles: 10", "maxFiles: 10", 1), "not a field", "", ""},
		"factory: edited":      {r(base, "factory: aws-0", "factory: gcp-0", 1), "factory: is read-only", "", ""},
		"no revert":            {r(base, "  - name: revert\n    mode: shadow\n", "", 1), "revert", "", ""},
		"the classes shape":    {"factory: aws-0\nclasses: {}\n", "write mergePolicies with maku policy export", "", ""},
		"a lower cap":          {r(base, "maxPatchFiles: 10", "maxPatchFiles: 5", 1), "", mergepolicy.KindTightening, ""},
		"promoted":             {r(base, "mode: shadow", "mode: live", 1), "", mergepolicy.KindRelaxation, "mode"},
		"a deny removed":       {r(base, "    protectedFiles: ['^docs/specs/.+$']\n", "", 1), "", mergepolicy.KindRelaxation, "protectedFiles"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, rc, rep := rig(false)
			pv := Check(cfg, rc, rep, tc.file)
			if tc.err != "" {
				if pv.Valid || !strings.Contains(strings.Join(pv.Errors, "\n"), tc.err) || pv.Proposal != nil {
					t.Fatalf("%+v", pv)
				}
				return
			}
			i := slices.IndexFunc(pv.Structural, func(c Change) bool { return c.Class == "docs-links" })
			if !pv.Valid || i < 0 || pv.Structural[i].Kind != tc.kind || !strings.Contains(strings.Join(pv.Structural[i].Why, ";"), tc.why) {
				t.Fatalf("%+v", pv)
			}
			if pv.Proposal.File != tc.file || pv.Proposal.EditURL != "https://github.com/Smana/cloud-native-ref/edit/main/.github/agent-factory.yaml" ||
				pv.Strictness[0] != (Strictness{Class: "docs-links", Refused: 3, RefusedMergedUnchanged: 2}) {
				t.Fatalf("%+v", pv)
			}
			if pv.Simulation.Enabled || len(pv.Simulation.Rows) != 0 {
				t.Fatal("simulation is policyManagement.simulation's")
			}
		})
	}
}

// With policyManagement.simulation on, every conformance fixture is decided under the central
// config and the proposal by the gate's own evaluator, and counted by change.
func TestCheckSimulatesOnTheFixtures(t *testing.T) {
	cfg, rc, rep := rig(true)
	pv := Check(cfg, rc, rep, strings.Replace(base, "maxPatchFiles: 10", "maxPatchFiles: 5", 1))
	total := 0
	for _, n := range pv.Simulation.Counts {
		total += n
	}
	if !pv.Simulation.Enabled || len(pv.Simulation.Rows) == 0 || total != len(pv.Simulation.Rows) {
		t.Fatalf("%+v", pv.Simulation)
	}
	for _, row := range pv.Simulation.Rows {
		if !slices.Contains(Changes(), row.Change) {
			t.Fatalf("%+v", row)
		}
	}
}

func comment(id int64, author, at, body string) Comment {
	ts, _ := time.Parse(time.RFC3339, at)
	return Comment{ID: id, Author: author, At: ts, Body: body}
}

// A decision's timeline: each stage before the deciding one passed, that one refused, the rest
// not reached; an arm passes them all; someone else's record is not the factory's.
func TestDecisionsTimeline(t *testing.T) {
	refused := "x <!-- agent-factory scope=task event=human-patch_too_large-2 -->\n\n<!-- agent-factory-decision head=a24 class=docs-links " +
		"decision=human:patch_too_large files=1/3 lines=30/20 shape=ok holdout=n/a oracle=n/a age=n/a evidence=ok paused=- capped=- reviewer=glm-5.3 -->"
	shadow := "<!-- agent-factory-decision head=a25 class=docs-links decision=shadow:shadow_would_arm files=1/3 lines=2/20 shape=ok " +
		"holdout=ok oracle=n/a age=n/a evidence=ok paused=false capped=false policy=0123456789ab dual=agree reviewer=glm-5.3 -->"
	tl := Decisions("Smana/cloud-native-ref", 24, "ogenki-agent-factory[bot]", []Comment{
		comment(2, "ogenki-agent-factory[bot]", "2026-10-02T00:00:00Z", shadow),
		comment(1, "ogenki-agent-factory", "2026-10-01T00:00:00Z", refused),
		comment(3, "someone", "2026-10-03T00:00:00Z", shadow),
	})
	if len(tl.Decisions) != 2 {
		t.Fatalf("%+v", tl)
	}
	d := tl.Decisions[0]
	status := map[string]string{}
	for _, s := range d.Steps {
		status[s.Row] = s.Status
	}
	if d.Verdict != "human" || d.Reason != "patch_too_large" || status["fixed_base"] != "passed" || status["author_mode"] != "passed" ||
		status["class"] != "refused" || status["deny"] != "not_reached" || status["grant"] != "not_reached" || status["breaker_and_caps"] != "not_reached" ||
		d.CommentURL != "https://github.com/Smana/cloud-native-ref/pull/24#issuecomment-1" {
		t.Fatalf("%+v", d)
	}
	if i := slices.IndexFunc(d.Steps, func(s Step) bool { return s.Row == "condition: modified lines" }); i < 0 || d.Steps[i].Detail != "30/20" {
		t.Fatalf("%+v", d.Steps)
	}
	a := tl.Decisions[1]
	for _, s := range a.Steps {
		if s.Status == "refused" || s.Status == "not_reached" {
			t.Fatalf("a shadow decision passes every stage: %+v", s)
		}
	}
	if a.Revisions[0].Detail != "sha256 0123456789ab, dual=agree" || d.Revisions[0].Detail != "not recorded: neither dual evaluation nor repository authority was on" {
		t.Fatalf("%+v %+v", a.Revisions, d.Revisions)
	}
}

// Stages 4 and 5 on the timeline: a decision a grant let arm names the grant and its version, one
// that used none says so, and a policy that decided under repository authority is that policy,
// not a comparison.
func TestTheTimelineShowsTheGrantAndTheDecidingPolicy(t *testing.T) {
	granted := "<!-- agent-factory-decision head=a26 class=docs-broad decision=arm:armed files=1/10 lines=2/200 shape=ok " +
		"holdout=ok oracle=n/a age=n/a evidence=ok paused=false capped=false policy=0123456789ab grant=g7:2 reviewer=glm-5.3 -->"
	plain := "<!-- agent-factory-decision head=a27 class=docs-broad decision=arm:armed files=1/10 lines=2/200 shape=ok " +
		"holdout=ok oracle=n/a age=n/a evidence=ok paused=false capped=false reviewer=glm-5.3 -->"
	tl := Decisions("Smana/cloud-native-ref", 25, "ogenki-agent-factory[bot]", []Comment{
		comment(1, "ogenki-agent-factory[bot]", "2026-10-01T00:00:00Z", granted),
		comment(2, "ogenki-agent-factory[bot]", "2026-10-02T00:00:00Z", plain),
	})
	step := func(d Decision, row string) Step {
		i := slices.IndexFunc(d.Steps, func(s Step) bool { return s.Row == row })
		if i < 0 {
			t.Fatalf("no %s in %+v", row, d.Steps)
		}
		return d.Steps[i]
	}
	g, p := tl.Decisions[0], tl.Decisions[1]
	if s := step(g, "grant"); s.Status != "recorded" || s.Detail != "grant g7, version 2" {
		t.Fatalf("%+v", s)
	}
	if s := step(p, "grant"); s.Status != "recorded" || s.Detail != "no grant used" {
		t.Fatalf("%+v", s)
	}
	if g.Revisions[0].Detail != "sha256 0123456789ab, the policy that decided" || g.Revisions[3].Detail != "g7:2" || p.Revisions[3].Detail != "none" {
		t.Fatalf("%+v", g.Revisions)
	}
}

// Each error is one entry naming its binding, so a reader can point at the binding's line.
func TestCheckSplitsParseErrorsByBinding(t *testing.T) {
	cfg, rc, rep := rig(false)
	file := strings.ReplaceAll(base, "mode: shadow", "mode: armed")
	pv := Check(cfg, rc, rep, file)
	if pv.Valid || len(pv.Errors) != 2 || !strings.Contains(pv.Errors[0], "binding docs-links: mode") || !strings.HasPrefix(pv.Errors[1], "binding revert: mode") {
		t.Fatalf("%q", pv.Errors)
	}
}

// A record carries the MergePolicy generation it was decided under as policygen=; an older
// record has none and reads as generation 0, with the rest of the timeline unchanged.
func TestDecisionsReadThePolicyGeneration(t *testing.T) {
	with := "<!-- agent-factory-decision head=a30 class=docs-broad decision=arm:armed files=1/10 lines=2/200 shape=ok " +
		"holdout=ok oracle=n/a age=n/a evidence=ok paused=false capped=false policy=0123456789ab policygen=4 reviewer=glm-5.3 -->"
	old := "<!-- agent-factory-decision head=a31 class=docs-broad decision=arm:armed files=1/10 lines=2/200 shape=ok " +
		"holdout=ok oracle=n/a age=n/a evidence=ok paused=false capped=false reviewer=glm-5.3 -->"
	tl := Decisions("Smana/cloud-native-ref", 26, "ogenki-agent-factory[bot]", []Comment{
		comment(1, "ogenki-agent-factory[bot]", "2026-10-01T00:00:00Z", with),
		comment(2, "ogenki-agent-factory[bot]", "2026-10-02T00:00:00Z", old),
	})
	if len(tl.Decisions) != 2 || tl.Decisions[0].PolicyGeneration != 4 || tl.Decisions[1].PolicyGeneration != 0 {
		t.Fatalf("%+v", tl.Decisions)
	}
	if d := tl.Decisions[0]; d.Verdict != "arm" || !strings.Contains(d.Revisions[0].Detail, "0123456789ab") {
		t.Fatalf("%+v", d)
	}
}
