// SPDX-License-Identifier: Apache-2.0

package policyview

import (
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/evidence"
)

func TestRecentReadsEveryPolicyRow(t *testing.T) {
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	prs := []evidence.PR{{Number: 42, Comments: []evidence.Comment{{
		Author: evidence.Actor{Login: "ogenki-agent-factory[bot]"}, CreatedAt: at,
		Body: "<!-- agent-factory-decision head=abc class=docs-links decision=shadow:shadow_would_arm files=1/3 lines=2/40 shape=ok holdout=n/a oracle=n/a age=n/a evidence=ok paused=false capped=false considered=docs:patch_too_large reviewer=glm-5.3 -->\n" +
			"<!-- agent-factory-decision head=abc class=docs decision=human:patch_too_large files=2/5 lines=131/100 shape=n/a holdout=n/a oracle=n/a age=n/a evidence=ok paused=false capped=false reviewer=glm-5.3 -->",
	}}}}
	got := ReadRecent("Smana/a", "2026-11-01", "ogenki-agent-factory[bot]", prs)
	if len(got.Decisions) != 2 {
		t.Fatalf("%+v", got)
	}
	first := got.Decisions[0]
	if first.PullRequest != 42 || first.Policy != "docs-links" || first.Outcome != "shadow" || !first.Rows.Counted ||
		first.Rows.Files != 1 || first.Rows.MaxFiles != 3 || first.Rows.Lines != 2 || first.Rows.MaxLines != 40 ||
		first.Considered != "docs:patch_too_large" || first.CommentURL == "" {
		t.Fatalf("%+v", first)
	}
	second := got.Decisions[1]
	if second.Policy != "docs" || second.Reason != "patch_too_large" || second.Rows.Lines != 131 || second.Rows.MaxLines != 100 {
		t.Fatalf("%+v", second)
	}
}

func TestRecentIgnoresImpersonatorsAndUnreadCounts(t *testing.T) {
	prs := []evidence.PR{{Number: 1, Comments: []evidence.Comment{{Author: evidence.Actor{Login: "app/ogenki-agent-factory"},
		Body: "<!-- agent-factory-decision head=abc class=docs decision=shadow:shadow_would_arm files=-/3 lines=-/100 shape=ok holdout=n/a oracle=n/a age=n/a evidence=ok paused=false capped=false reviewer=none -->"}}}}
	got := ReadRecent("Smana/a", "2026-11-01", "ogenki-agent-factory[bot]", prs)
	if len(got.Decisions) != 0 {
		t.Fatalf("a slug-only impersonator counted: %+v", got)
	}
	prs[0].Comments[0].Author.Login = "ogenki-agent-factory[bot]"
	got = ReadRecent("Smana/a", "2026-11-01", "ogenki-agent-factory[bot]", prs)
	if len(got.Decisions) != 1 || got.Decisions[0].Rows.Counted {
		t.Fatalf("unread counts became measurements: %+v", got)
	}
}
