// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { describeGuardrails, joinOr, modeMeaning } from "../src/policy-describe";

describe("the guardrails and modes", () => {
  it("writes the guardrails as sentences, never as JSON", () => {
    expect(describeGuardrails({ protectedFiles: ["^\\.github/"], mandatoryChecks: ["lint"], maxPatchFiles: 20, maxModifiedLines: 400, maxPerDay: 5 })).toEqual([
      "Every class refuses a pull request that touches ^\\.github/.",
      "Every class also needs the check “lint”.",
      "No class may allow more than 20 files, 400 lines or 5 merges a day.",
    ]);
    expect(describeGuardrails({})).toEqual(["The factory sets no guardrail beyond its base checks."]);
  });
  it("gives every mode its meaning", () => {
    expect(modeMeaning("live")).toEqual({ label: "Live", meaning: "merges on its own" });
    expect(modeMeaning("shadow")).toEqual({ label: "Shadow", meaning: "records what it would do, merges nothing" });
    expect(modeMeaning("off")).toEqual({ label: "Off", meaning: "decides nothing" });
    expect(modeMeaning("weird")).toEqual({ label: "weird", meaning: "a mode this page does not know" });
    expect(modeMeaning("constructor")).toEqual({ label: "constructor", meaning: "a mode this page does not know" });
  });
  it("joins with or, and with and", () => {
    expect(joinOr(["a"])).toBe("a");
    expect(joinOr(["a", "b", "c"])).toBe("a, b or c");
    expect(joinOr(["a", "b"], "and")).toBe("a and b");
  });
});

