// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { patternDetails, patternGloss, policyWords } from "../src/policy-words";

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

  it("builds one sentence from the policy and its repository binding", () => {
    const words = policyWords({
      allowedFiles: ["^website/content/.+\\.mdx?$"], diffShape: "links-only", maxPatchFiles: 5,
      maxModifiedLines: 60, maxPerDay: 2, requiresApproval: [{ changedFiles: ["^security/"] }],
    });
    expect(words).toBe("Markdown under website/content/, link targets only, at most 5 files and 60 lines, 2 a day; a maintainer must approve matching changes");
  });

  it("lets a binding narrow the policy and names checks and the other shapes", () => {
    expect(policyWords({ allowedFiles: ["^docs/"], holdoutChecks: ["lint", "links"] }, { allowedFiles: ["^docs/guide/"] }))
      .toBe("files under docs/guide/; lint and links must pass");
    expect(policyWords({ diffShape: "semver-patch", modifyOnly: true })).toBe("patch dependency updates, edits only");
    expect(policyWords({ diffShape: "added-entry", companionFiles: ["^index.md$"] })).toContain("one added entry");
    expect(policyWords({})).toBe("No extra policy restriction");
  });
});
