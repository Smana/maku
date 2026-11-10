// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { describeClass, describeGuardrails, joinOr, modeMeaning } from "../src/policy-describe";
import { templates } from "../src/policy-editor";

describe("describeClass", () => {
  it("reads a docs class as its conditions, deny, approval and limit", () => {
    const s = describeClass({ mode: "shadow", author: "agents", harness: "plain",
      conditions: { all: [{ allowedFiles: ["^docs/.+\\.md$", "^README\\.md$"] }, { maxPatchFiles: 10 }, { maxModifiedLines: 200 },
        { holdoutChecks: ["Check the documentation links"] }], deny: [{ protectedFiles: ["^docs/security/"] }] },
      requiresApproval: [{ changedFiles: ["^docs/architecture/"] }], limits: { maxPerDay: 2 }, monitor: { followUpWindow: "168h" } });
    expect(s.lead).toBe("Would merge an agent's pull request on its own when all of these hold (shadow: it records the decision and merges nothing):");
    expect(s.when).toEqual([
      "it only changes files matching ^docs/.+\\.md$ or ^README\\.md$",
      "it changes at most 10 files",
      "it changes at most 200 lines",
      "the check “Check the documentation links” passes",
    ]);
    expect(s.also).toEqual([
      "Never if it touches ^docs/security/.",
      "A maintainer approves it first if it changes ^docs/architecture/.",
      "At most 2 a day.",
      "Watched for 168h after it merges.",
    ]);
  });

  it("names an adopted bot and its branches", () => {
    const s = describeClass({ mode: "live", author: "adopt", harness: "adopted-none", adopt: { logins: ["renovate[bot]"], branches: ["^renovate/"] },
      conditions: { all: [{ diffShape: "semver-patch" }, { modifyOnly: true }, { releaseAgeCheck: "renovate/stability-days" }] } });
    expect(s.lead).toBe("Merges a pull request from renovate[bot] on a branch matching ^renovate/ on its own when all of these hold:");
    expect(s.when).toEqual(["the diff is a patch version bump", "it only modifies files, adding or deleting none",
      "the status “renovate/stability-days” is green (the release is old enough)"]);
  });

  it("says an off class decides nothing, and what it would check", () => {
    expect(describeClass({ mode: "off", author: "agents", harness: "plain" }).lead)
      .toBe("Off: decides nothing. If turned on, it would merge an agent's pull request on its own when all of these hold:");
  });

  it("says when a class has no condition of its own", () => {
    expect(describeClass({ mode: "shadow", author: "agents", harness: "plain" }).when)
      .toEqual(["the factory's base checks pass (the class adds no condition of its own)"]);
  });

  it("falls back to the factory's caps when the class sets none", () => {
    const s = describeClass({ mode: "shadow", author: "agents", harness: "plain", conditions: { all: [{ allowedFiles: ["^a$"] }] } },
      { maxPatchFiles: 20, maxModifiedLines: 400, maxPerDay: 5 });
    expect(s.when).toContain("it changes at most 20 files (the factory's cap)");
    expect(s.when).toContain("it changes at most 400 lines (the factory's cap)");
    expect(s.also).toContain("At most 5 a day (the factory's cap).");
  });

  it("reads an any as one of its branches", () => {
    const s = describeClass({ mode: "shadow", author: "agents", harness: "plain",
      conditions: { any: [[{ allowedFiles: ["^docs/"] }], [{ allowedFiles: ["^test/"] }, { maxPatchFiles: 3 }]] } });
    expect(s.when).toEqual(["one of: (it only changes files matching ^docs/) or (it only changes files matching ^test/ and it changes at most 3 files)"]);
  });

  it("names a leaf it has no words for, never drops it", () => {
    expect(describeClass({ mode: "shadow", author: "agents", harness: "plain", conditions: { all: [{ brandNew: 1 }] } }).when)
      .toEqual(["a condition this page does not describe (brandNew)"]);
  });

  it("has words for every leaf of every template", () => {
    for (const [name, make] of Object.entries(templates)) {
      const s = describeClass(make());
      expect(s.when.join(" "), name).not.toContain("does not describe");
    }
  });
});

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
  });
  it("joins with or, and with and", () => {
    expect(joinOr(["a"])).toBe("a");
    expect(joinOr(["a", "b", "c"])).toBe("a, b or c");
    expect(joinOr(["a", "b"], "and")).toBe("a and b");
  });
});

// Round 1 review: a class never reads looser than it is. Every key of a leaf, every entry of a
// deny, approval, limit or monitor block is described or named; a malformed value is named too.
describe("a leaf with several keys, and malformed values", () => {
  const when = (l: unknown) => describeClass({ mode: "shadow", author: "agents", harness: "plain", conditions: { all: [l as never] } }).when;
  const named = (k: string) => `a condition this page does not describe (${k})`;

  it("describes every key of a leaf, one phrase each", () => {
    expect(when({ maxPatchFiles: 3, brandNew: 1 })).toEqual(["it changes at most 3 files", named("brandNew")]);
  });
  it("names a leaf with no keys", () => {
    expect(when({})).toEqual([named("empty")]);
  });
  it("describes modifyOnly only when it is true", () => {
    expect(when({ modifyOnly: true })).toEqual(["it only modifies files, adding or deleting none"]);
    expect(when({ modifyOnly: false })).toEqual([named("modifyOnly")]);
  });
  it("describes a file or check list only when it is a non-empty array of strings", () => {
    expect(when({ allowedFiles: [] })).toEqual([named("allowedFiles")]);
    expect(when({ allowedFiles: "^a$" })).toEqual([named("allowedFiles")]);
    expect(when({ companionFiles: [1] })).toEqual([named("companionFiles")]);
    expect(when({ holdoutChecks: [] })).toEqual([named("holdoutChecks")]);
    expect(when({ holdoutChecks: "lint" })).toEqual([named("holdoutChecks")]);
  });
  it("describes a size cap only when it is a positive number", () => {
    expect(when({ maxPatchFiles: 0 })).toEqual([named("maxPatchFiles")]);
    expect(when({ maxPatchFiles: "3" })).toEqual([named("maxPatchFiles")]);
    expect(when({ maxModifiedLines: -1 })).toEqual([named("maxModifiedLines")]);
  });
  it("describes a check name or label only when it is a non-empty string", () => {
    expect(when({ releaseAgeCheck: "" })).toEqual([named("releaseAgeCheck")]);
    expect(when({ requiredLabel: 3 })).toEqual([named("requiredLabel")]);
    expect(when({ requiredLabel: "docs" })).toEqual(["it carries the label “docs”"]);
  });
  it("describes diffShape only as a string, and keeps an unknown shape's name", () => {
    expect(when({ diffShape: 3 })).toEqual([named("diffShape")]);
    expect(when({ diffShape: "weird" })).toEqual(["the diff has the shape weird"]);
  });
  it("describes an oracle only with a string probe", () => {
    expect(when({ oracle: { probe: "dead-links" } })).toEqual(["the dead-links oracle has no finding open"]);
    expect(when({ oracle: {} })).toEqual([named("oracle")]);
    expect(when({ oracle: { probe: 2 } })).toEqual([named("oracle")]);
  });
  it("names a malformed condition and a non-array all", () => {
    expect(when(null)).toEqual([named("malformed")]);
    expect(describeClass({ mode: "shadow", author: "agents", harness: "plain", conditions: { all: "x" as never } }).when)
      .toEqual([named("all")]);
  });
  it("does not read a prototype name as a shape or a mode", () => {
    expect(when({ diffShape: "constructor" })).toEqual(["the diff has the shape constructor"]);
    expect(modeMeaning("constructor")).toEqual({ label: "constructor", meaning: "a mode this page does not know" });
  });
});

describe("deny, approval, limits and monitor entries", () => {
  const cls = (over: Record<string, unknown>, g: Record<string, unknown> = {}) =>
    describeClass({ mode: "shadow", author: "agents", harness: "plain", ...over } as never, g);

  it("names an unknown key on a deny entry and keeps the known files", () => {
    expect(cls({ conditions: { deny: [{ protectedFiles: ["^x$"], brandNew: 1 }] } }).also).toEqual([
      "Never if it touches ^x$.",
      "Never if it matches a rule this page does not describe (brandNew).",
    ]);
  });
  it("names a deny entry whose protectedFiles is empty", () => {
    expect(cls({ conditions: { deny: [{ protectedFiles: [] }] } }).also)
      .toEqual(["Never if it matches a rule this page does not describe (protectedFiles)."]);
  });
  it("names an unknown key on a requiresApproval entry", () => {
    expect(cls({ requiresApproval: [{ changedFiles: ["^x$"], other: 1 }] }).also).toEqual([
      "A maintainer approves it first if it changes ^x$.",
      "A maintainer approves it first under a rule this page does not describe (other).",
    ]);
  });
  it("names an unknown limits key, and a bad maxPerDay without the factory's fallback", () => {
    expect(cls({ limits: { maxPerDay: 2, weekly: 9 } }).also).toEqual([
      "At most 2 a day.",
      "Also limited by a setting this page does not describe (weekly).",
    ]);
    expect(cls({ limits: { maxPerDay: 0 } }, { maxPerDay: 5 }).also)
      .toEqual(["Also limited by a setting this page does not describe (maxPerDay)."]);
  });
  it("describes the three known monitor keys, and names the rest", () => {
    expect(cls({ monitor: { followUpWindow: "168h", oracleRuns: 3, flux: true, verification: true } }).also).toEqual([
      "Watched for 168h after it merges.",
      "Re-runs the oracle 3 times after merge.",
      "Waits for Flux to apply it.",
      "Verifies the deployment after merge.",
    ]);
    expect(cls({ monitor: { mystery: 1, followUpWindow: 12 } }).also).toEqual([
      "Also limited by a setting this page does not describe (mystery).",
      "Also limited by a setting this page does not describe (followUpWindow).",
    ]);
  });
});
