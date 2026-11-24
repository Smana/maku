// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { backtestView } from "../src/backtest";

describe("the backtest table", () => {
  it("links real pull requests and shows before and after", () => {
    const nodes = backtestView({ valid: true, repository: "Smana/docs", since: "2026-11-01",
      counts: { considered: 2, changed: 1, unchanged: 0, unknown: 1, matched: 1 },
      rows: [
        { pullRequest: 42, url: "https://github.com/Smana/docs/pull/42", policy: "docs", before: { verdict: "shadow", reason: "shadow_would_arm" }, after: { verdict: "human", reason: "patch_too_large" }, change: "changed" },
        { pullRequest: 43, url: "https://github.com/Smana/docs/pull/43", policy: "docs", before: { verdict: "shadow", reason: "shadow_would_arm" }, after: { verdict: "unknown", reason: "diff_unrecorded" }, change: "unknown" },
      ] });
    const box = document.createElement("div");
    box.append(...nodes);
    expect(box.querySelector("a")?.getAttribute("href")).toBe("https://github.com/Smana/docs/pull/42");
    expect(box.textContent).toContain("shadow: it would merge, the gate is in shadow");
    expect(box.textContent).toContain("human: exceeds the file or line limit");
    expect(box.textContent).toContain("unknown");
    expect(box.textContent).not.toContain("Warning: the edited rule matched none");
  });

  it("warns when the rule matches none and never calls an unknown unchanged", () => {
    const nodes = backtestView({ valid: true, repository: "o/r", since: "2026-11-01",
      counts: { considered: 1, changed: 0, unchanged: 0, unknown: 1, matched: 0 }, rows: [] });
    const box = document.createElement("div");
    box.append(...nodes);
    expect(box.textContent).toContain("matched none");
    expect(box.textContent).toContain("1 unknown");
    expect(box.textContent).toContain("0 unchanged");
  });
});
