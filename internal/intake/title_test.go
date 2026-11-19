// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/intake"
)

// aws-0, glm-5.3, 2026-11-19: the planner's titles for two stated requests were refused
// invalid_draft. A title the broker can mend is repaired, never refused.
func TestRepairTitle(t *testing.T) {
	long := "docs: " + strings.Repeat("a", 300)
	for _, c := range []struct {
		name, title, class, body, want string
	}{
		{"valid stays", "docs(reference): the Slack app budget is wrong", "docs", validBody, "docs(reference): the Slack app budget is wrong"},
		{"no area, class docs", "The Slack app budget on the config reference is wrong", "docs", validBody, "docs: The Slack app budget on the config reference is wrong"},
		{"area with a space", "Harness reference: a careful task splits into 5 parts, the factory caps it at 4", "docs-links", validBody,
			"docs: Harness reference: a careful task splits into 5 parts, the factory caps it at 4"},
		{"trailing newline", "docs: the budget is wrong\n", "docs", validBody, "docs: the budget is wrong"},
		{"two lines", "docs: the budget\nis wrong", "docs", validBody, "docs: the budget is wrong"},
		{"markdown", "**docs**: the `dailyUSD` value is wrong", "docs", validBody, "docs: the dailyUSD value is wrong"},
		{"heading and quotes", "## \"docs: the value is wrong\"", "docs", validBody, "docs: the value is wrong"},
		{"no space after colon", "docs:the value is wrong", "docs", validBody, "docs: the value is wrong"},
		{"area from the body's path", "The values are wrong", "careful",
			"## What is wrong\ntooling/aws-0/agent-factory/helm-values-sponsored.yaml differs.\n## What it should be\nx\n## Acceptance check\ny",
			"tooling: The values are wrong"},
		{"chore otherwise", "The values are wrong", "careful", validBody, "chore: The values are wrong"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := intake.RepairTitle(c.title, c.class, c.body)
			if got != c.want {
				t.Fatalf("RepairTitle(%q) = %q, want %q", c.title, got, c.want)
			}
			if _, err := sanitize.CheckDraft(got, validBody); err != nil {
				t.Fatalf("the repaired title is refused: %v", err)
			}
		})
	}
	if got := intake.RepairTitle(long, "docs", validBody); len([]rune(got)) > 256 {
		t.Fatalf("a long title is not capped: %d runes", len([]rune(got)))
	}
	// Nothing to mend: the title stays as it is, and the check refuses it.
	if got := intake.RepairTitle("**", "docs", validBody); got != "**" {
		t.Fatalf("an empty repair = %q", got)
	}
}

// Compose repairs a title before the contract check.
func TestComposeRepairsTheTitle(t *testing.T) {
	d := intake.Draft{Repository: "Smana/ai-native-ref",
		Issues: []intake.DraftIssue{{Title: "Config reference: the Slack app budget is wrong\n", Body: validBody, Class: "docs-links"}}}
	p, err := intake.Compose(d, composeIn())
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Issues[0].Title; got != "docs: Config reference: the Slack app budget is wrong" {
		t.Fatalf("title = %q", got)
	}
}
