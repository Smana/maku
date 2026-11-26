// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { MEANING_KEYS } from "../src/policy-fields";
import { durationWords, FIELD_HELP, readOnlyWords } from "../src/policy-help";
import { sentenceNode } from "../src/sentence";

describe("help lines (correctness signals W3)", () => {
  it("has one for every field a policy can hold", () => {
    for (const key of [...MEANING_KEYS, "requires"]) expect(FIELD_HELP[key], key).toMatch(/\S/);
    expect(FIELD_HELP.maxModifiedLines).toContain("`patch_too_large`");
    expect(FIELD_HELP.holdoutChecks).toContain("not that the change is right");
  });
  it("reads the fields the form does not edit", () => {
    expect(readOnlyWords("requires", ["plan", "tests"])).toBe("Tasks that touch these files run: a plan you approve and tests written before the change");
    expect(readOnlyWords("evidence", "low")).toBe("5 agreed decisions before promotion");
    expect(readOnlyWords("author", { adopt: { logins: ["renovate[bot]"], branches: ["^renovate/"] } })).toBe("Pull requests of renovate[bot] from branches starting renovate/");
    expect(readOnlyWords("oracle", { probe: "dead-links", maxFindings: 8 })).toBe("Detector `dead-links`, at most 8 findings a task, reported by the binding's workflow");
  });
  it("reads a Go duration", () => {
    expect([durationWords("30m0s"), durationWords("2h0m0s"), durationWords("168h0m0s"), durationWords("72h0m0s")]).toEqual(["30 min", "2 h", "7 days", "72 h"]);
  });
  it("renders the sentence without parsing HTML", () => {
    const n = sentenceNode("**Merges** `<b>x</b>` now");
    expect(n.querySelector("strong")!.textContent).toBe("Merges");
    expect(n.querySelector("code")!.textContent).toBe("<b>x</b>");
    expect(n.querySelector("b")).toBeNull();
  });
});
