// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/summary"
)

// progressCase is one row of testdata/progress.json, which web/test/profile.test.ts runs too:
// the room page's Will run row and every Go surface word it the same way.
type progressCase struct {
	Name string             `json:"name"`
	Path []string           `json:"path"`
	Runs []envelope.RunFact `json:"runs"`
	Want string             `json:"want"`
}

func TestProgressMarksStagesThatRan(t *testing.T) {
	b, err := os.ReadFile("testdata/progress.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []progressCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			done, rest := Progress(c.Path, c.Runs)
			got := PathLine(WorkflowCard{Done: done, Path: rest}, func(s string) string { return s })
			if got != c.Want {
				t.Fatalf("PathLine = %q, want %q", got, c.Want)
			}
		})
	}
}

func TestWorkflowCardAfterStepUpShowsTheScoperDone(t *testing.T) {
	f := &envelope.TaskFacts{
		Plan:    []string{"implementer", "reviewer"},
		Profile: &envelope.ProfileFact{Name: "careful", Source: "escalation", Reason: "stalled"},
		Runs: RunsOf(&summary.Flow{Nodes: []summary.FlowNode{
			{Persona: "scoper", Role: "scoper", Outcome: envelope.OutcomeSucceeded},
			{Persona: "implementer", Role: "implementer", Outcome: envelope.OutcomeFailed},
		}}),
	}
	for _, format := range []Format{Text, Markdown, Slack} {
		var b strings.Builder
		if err := WorkflowCardText(&b, CardOf(f, "t"), Options{Format: format}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), "scoper ✓ · implementer → reviewer → CI → merge gate") {
			t.Fatalf("format %v: Will run lacks the done scoper:\n%s", format, b.String())
		}
	}
}
