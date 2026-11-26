// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { HOW_POLICIES_DECIDE } from "../src/generated/how-policies-decide";
import { DOCS_SECTION, explainerSource, policyExplainer } from "../src/policy-explainer";

describe("How merge policies decide (correctness signals W9)", () => {
  it("is the docs section, not a second copy", () => {
    const doc = readFileSync(join(__dirname, "..", "..", "docs", "merge-policies.md"), "utf8");
    const heading = "## Holds, agreed, signal-checked\n";
    const rest = doc.slice(doc.indexOf(heading) + heading.length);
    const end = rest.indexOf("\n## ");
    expect(HOW_POLICIES_DECIDE, "run hack/docs.sh to regenerate").toBe(rest.slice(0, end < 0 ? undefined : end).trim());
  });
  it("renders the vocabulary, links the docs, and draws no diagram of its own", () => {
    const box = policyExplainer();
    expect(box.querySelector("summary")?.textContent).toBe("How merge policies decide");
    for (const word of ["holds", "correctness signal", "signal-checked", "unchecked", "agreed", "quiet", "regression", "wrong call"]) {
      expect(box.textContent).toContain(word);
    }
    expect(box.querySelector(`a[href="${DOCS_SECTION}"]`)).not.toBeNull();
    expect(box.textContent).not.toContain("flowchart");
    for (const a of box.querySelectorAll("a")) expect(a.getAttribute("href") ?? "").toMatch(/^https:\/\//);
  });
  it("points the docs' relative links at the docs", () => {
    expect(explainerSource("see [the stage](factory.md#the-tester-stage) and [here](#x)"))
      .toBe("see [the stage](https://github.com/Smana/maku/blob/main/docs/factory.md#the-tester-stage) and [here](https://github.com/Smana/maku/blob/main/docs/merge-policies.md#x)");
  });
});
