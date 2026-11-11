// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"strings"
	"testing"
	"time"
)

func TestAnswerBodyAcceptance(t *testing.T) {
	const head = "## What is wrong\nx\n\n## What it should be\ny\n"
	for _, tc := range []struct{ name, body, want string }{
		{"missing section is created at the end", head,
			head + "\n## Acceptance check\n\ntask check\n\n## Decisions\n\nD1: Acceptance check: task check"},
		{"missing section is created before Decisions", head + "\n## Decisions\n\nD4: earlier\n",
			head + "\n## Acceptance check\n\ntask check\n\n## Decisions\n\nD4: earlier\nD5: Acceptance check: task check"},
		{"missing section goes after What it should be, before other sections", head + "\n## Notes\nn\n",
			head + "\n## Acceptance check\n\ntask check\n\n## Notes\nn\n\n## Decisions\n\nD1: Acceptance check: task check"},
		{"marker is replaced inside the section", head + "\n## Acceptance check\n[NEEDS CLARIFICATION: g1 which command?]\n\nFix it.",
			head + "\n## Acceptance check\ntask check\n\nFix it.\n\n## Decisions\n\nD1: Acceptance check: task check"},
		{"existing text without a marker gets the answer appended", head + "\n## Acceptance check\nrun it\n\n## Decisions\n\nD1: old",
			head + "\n## Acceptance check\nrun it\n\ntask check\n\n## Decisions\n\nD1: old\nD2: Acceptance check: task check"},
		{"heading-only section is filled", head + "\n## Acceptance check\n",
			head + "\n## Acceptance check\n\ntask check\n\n## Decisions\n\nD1: Acceptance check: task check"},
		{"section without a trailing newline gets the answer appended", "## What it should be\ny\n\n## Acceptance check\nrun it",
			"## What it should be\ny\n\n## Acceptance check\nrun it\n\ntask check\n\n## Decisions\n\nD1: Acceptance check: task check"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AnswerBody(tc.body, "task check", true)
			if err != nil || got != tc.want {
				t.Fatalf("AnswerBody() = %q, %v\nwant %q", got, err, tc.want)
			}
		})
	}
	t.Run("a multi-line or blank answer is refused", func(t *testing.T) {
		for _, a := range []string{"a\nb", "  "} {
			if got, err := AnswerBody(head, a, true); err == nil || got != "" {
				t.Fatalf("AnswerBody(%q) = %q, %v", a, got, err)
			}
		}
	})
	t.Run("a non-acceptance answer only appends a Decision", func(t *testing.T) {
		got, err := AnswerBody(needAnswerBody, "Use Linux", false)
		if err != nil || got != needAnswerBody+"\n\n## Decisions\n\nD1: Use Linux" {
			t.Fatalf("AnswerBody() = %q, %v", got, err)
		}
	})
}

func TestIsAcceptanceQuestion(t *testing.T) {
	for q, want := range map[string]bool{
		"The issue states no acceptance check: which command or observation proves it is done?": true,
		"Which dashboard?": false,
	} {
		if got := IsAcceptanceQuestion(q); got != want {
			t.Errorf("IsAcceptanceQuestion(%q) = %v", q, got)
		}
	}
}

const noAcceptanceBody = "## What is wrong\nx\n\n## What it should be\ny"

func acceptanceBrief() map[string]any {
	return briefQuestion("The issue states no acceptance check: which command or observation proves it is done?",
		"No acceptance stated; proceed on the scoper's own: task docs passes")
}

func TestNeedAnswerCreatesAcceptance(t *testing.T) {
	t.Run("writes the section and the Decision, shows the diff", func(t *testing.T) {
		initial := needAnswerSnapshot()
		initial.Body = noAcceptanceBody
		issues := &fakeIssueEditor{snapshots: []IssueSnapshot{initial}}
		rig := newNeedAnswerRig(t, issues, nil, acceptanceBrief())
		got := rig.relay.needAnswer(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "issue": 7, "room": "3buqdlot"}))
		needAnswerResult(t, got)
		want := noAcceptanceBody + "\n\n## Acceptance check\n\nUse Linux\n\n## Decisions\n\nD1: Acceptance check: Use Linux"
		if len(issues.edits) != 1 || issues.edits[0].body != want {
			t.Fatalf("edits = %#v; want body %q", issues.edits, want)
		}
		message := rig.form["message"].(string)
		for _, w := range []string{"+ ## Acceptance check", "+ <answer>", "+ D1: Acceptance check: <answer>", "  ## What it should be", "blank"} {
			if !strings.Contains(message, w) {
				t.Errorf("form does not show %q:\n%s", w, message)
			}
		}
	})

	t.Run("a blank answer accepts the scoper's assumption", func(t *testing.T) {
		initial := needAnswerSnapshot()
		initial.Body = noAcceptanceBody
		issues := &fakeIssueEditor{snapshots: []IssueSnapshot{initial}}
		rig := newNeedAnswerRig(t, issues, nil, acceptanceBrief())
		rig.answer = ""
		got := rig.relay.needAnswer(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "issue": 7, "room": "3buqdlot"}))
		needAnswerResult(t, got)
		want := noAcceptanceBody + "\n\n## Acceptance check\n\ntask docs passes\n\n## Decisions\n\nD1: Acceptance check: task docs passes"
		if len(issues.edits) != 1 || issues.edits[0].body != want {
			t.Fatalf("edits = %#v; want body %q", issues.edits, want)
		}
	})

	t.Run("a race writes nothing", func(t *testing.T) {
		initial := needAnswerSnapshot()
		initial.Body = noAcceptanceBody
		changed := initial
		changed.Body += "\n\nConcurrent edit"
		issues := &fakeIssueEditor{snapshots: []IssueSnapshot{initial, changed}}
		rig := newNeedAnswerRig(t, issues, nil, acceptanceBrief())
		got := rig.relay.needAnswer(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "issue": 7, "room": "3buqdlot"}))
		if !strings.Contains(toolText(got), "issue_changed") {
			t.Fatalf("result = %v", got)
		}
		requireNoNeedAnswerWrite(t, rig)
	})

	t.Run("labels changing mid-answer writes nothing", func(t *testing.T) {
		initial := needAnswerSnapshot()
		initial.Body = noAcceptanceBody
		changed := initial
		changed.Labels = append([]string{"factory/ready"}, initial.Labels...)
		changed.UpdatedAt = changed.UpdatedAt.Add(time.Second)
		issues := &fakeIssueEditor{snapshots: []IssueSnapshot{initial, changed}}
		rig := newNeedAnswerRig(t, issues, nil, acceptanceBrief())
		got := rig.relay.needAnswer(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "issue": 7, "room": "3buqdlot"}))
		if !strings.Contains(toolText(got), "issue_changed") {
			t.Fatalf("result = %v", got)
		}
		requireNoNeedAnswerWrite(t, rig)
	})
}
