// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { decisionCounts, decisionCriterion, outcomeWords, recentDecisions, type Recent, type RecentDecision } from "../src/decisions";

const decision = (over: Partial<RecentDecision> = {}): RecentDecision => ({
  at: "2026-11-20T12:00:00Z", pullRequest: 42, head: "abc123", outcome: "human", policy: "docs", reason: "patch_too_large",
  rows: { counted: true, files: 2, maxFiles: 5, lines: 131, maxLines: 100 },
  commentURL: "https://github.com/Smana/docs/pull/42#issuecomment-1", ...over,
});
const recent = (decisions: RecentDecision[]): Recent => ({ repository: "Smana/docs", since: "2026-11-01", decisions });

describe("recent decisions", () => {
  it("puts the measured criterion and its limits in the decision row", () => {
    const table = recentDecisions(recent([decision()]), Date.parse("2026-11-21T12:00:00Z"));
    expect([...table.querySelectorAll("th")].map((x) => x.textContent)).toEqual(["Pull request", "Outcome", "Policy", "Deciding criterion", "When"]);
    expect(table.textContent).toContain("131 lines changed, limit 100");
    expect(table.textContent).toContain("2 files changed, limit 5");
    expect(table.textContent).toContain("Waited for a maintainer");
    expect(table.querySelector("a")?.getAttribute("href")).toBe("https://github.com/Smana/docs/pull/42#issuecomment-1");
    expect(table.querySelector("time")?.textContent).toBe("1 d ago");
  });

  it("says when a diff was not measured, and considers the other policy refusals", () => {
    const d = decision({ rows: { counted: false, files: 0, maxFiles: 0, lines: 0, maxLines: 0 }, reason: "diff_unreadable", considered: "docs-links:class_mismatch,deps:holdout_red" });
    expect(decisionCriterion(d)).toBe("a changed file could not be read, so the gate cannot prove the diff");
    expect(decisionCriterion({ ...d, reason: "no_policy_matched" })).toContain("docs-links: the diff is not the class the triage predicted");
    expect(recentDecisions(recent([d])).textContent).toContain("Waited for a maintainer");
  });

  it("uses the shared reason table for a shadow decision", () => {
    const d = decision({ outcome: "shadow", reason: "shadow_would_arm" });
    expect(outcomeWords(d)).toBe("Would merge");
    expect(decisionCriterion(d)).toBe("2 files and 131 lines changed; it would auto-merge, and the merge gate is in shadow");
  });

  it("counts each pull request once and limits the waiting count to seven days", () => {
    const now = Date.parse("2026-11-21T12:00:00Z");
    const counts = decisionCounts(recent([
      decision({ pullRequest: 1, outcome: "shadow", reason: "shadow_would_arm", at: "2026-11-20T12:00:00Z" }),
      decision({ pullRequest: 1, outcome: "human", reason: "patch_too_large", policy: "other", at: "2026-11-20T12:00:00Z" }),
      decision({ pullRequest: 2, outcome: "arm", reason: "clean", at: "2026-11-19T12:00:00Z" }),
      decision({ pullRequest: 3, outcome: "human", reason: "holdout_red", at: "2026-11-10T12:00:00Z" }),
      decision({ pullRequest: 4, outcome: "human", reason: "holdout_red", at: "2026-11-20T12:00:00Z" }),
    ]), now);
    expect(counts).toEqual({ automatic: 1, wouldMerge: 1, waited: 1 });
  });
});
