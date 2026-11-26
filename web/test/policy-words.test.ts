// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { patternDetails, patternGloss } from "../src/policy-words";

describe("policy words", () => {
  it("turns common anchored patterns into a gloss and keeps the raw form on demand", () => {
    expect(patternGloss("^website/content/.+\\.mdx?$")).toBe("Markdown under website/content/");
    expect(patternGloss("^(docs|website/content)/.+\\.md$")).toBe("Markdown under docs/ or website/content/");
    expect(patternGloss("^README\\.md$")).toBe("README.md");
    expect(patternGloss("^.+\\.mdx?$")).toBe("Markdown files");
    const details = patternDetails(["^website/content/.+\\.mdx?$"]);
    expect(details.querySelector("summary")?.textContent).toBe("Show raw file patterns");
    expect(details.textContent).toContain("^website/content/.+\\.mdx?$");
  });
});
