// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { centralManifest, conditionWords, differences, fileLine, policyState, timeText } from "../src/settings-api";
import type { Condition, Report } from "../src/settings";

const report = (over: Partial<Report> = {}, conds?: Condition[], authority = "central"): Report => ({
  repository: "o/r", minimum: 20, readAt: "", factory: "f", defaultBranch: "main", harnessProfiles: null, guardrails: {},
  classes: [{ class: "docs", source: "central", mode: "live" }],
  ...(conds ? { mergePolicy: "mp", mergePolicyStatus: { namespace: "ns", generation: 1, status: { authority, conditions: conds } } } : {}),
  ...over,
});
const inSync = (status: string, reason?: string, message?: string): Condition => ({ type: "InSync", status, reason, message });

describe("policyState", () => {
  const cases: [string, Report, string][] = [
    ["no mergePolicy", report(), "not-migrated"],
    ["authority central", report({}, [], "central"), "central"],
    ["status absent, classes central", report({ mergePolicy: "mp" }), "central"],
    ["authority repository", report({}, [], "repository"), "repository"],
    ["a class from the repository", report({ mergePolicy: "mp", classes: [{ class: "d", source: "repository", mode: "live" }] }), "repository"],
  ];
  for (const [name, r, want] of cases) it(name, () => expect(policyState(r)).toBe(want));
  it("centralManifest is the manifest only while central", () => {
    expect(centralManifest(report({ mergePolicy: "mp", manifest: "m" }))).toBe("m");
    expect(centralManifest(report({ mergePolicy: "mp", manifest: "m", classes: [{ class: "d", source: "repository", mode: "live" }] }))).toBeUndefined();
    expect(centralManifest(report({ manifest: "m" }))).toBeUndefined();
  });
});

describe("conditionWords", () => {
  const st = (c: Condition) => report({}, [c]).mergePolicyStatus;
  const cases: [string, Condition, object][] = [
    ["accepted", { type: "Accepted", status: "True" }, { word: "Accepted", tone: "ok" }],
    ["refused", { type: "Accepted", status: "False", message: "bad" }, { word: "Refused", tone: "bad", message: "bad" }],
    ["in sync", inSync("True"), { word: "In sync with .github/agent-factory.yaml", tone: "ok" }],
    ["diverges", inSync("False", "Diverges", "a; b"), { word: "Not in sync: 2 differences", tone: "warn", message: "a; b" }],
    ["read off", inSync("Unknown", "PolicyReadOff"), { word: "File not read", tone: "unknown" }],
    ["file invalid", inSync("False", "FileInvalid", "the repository's file is not valid"), { word: "Not in sync: file invalid", tone: "bad" }],
    ["more than five", inSync("False", "Diverges", "a; b; c; d; e; and 3 more"), { word: "Not in sync: 8 differences", tone: "warn" }],
    ["not read yet", inSync("Unknown", "NotRead"), { word: "Not compared yet", tone: "unknown" }],
    ["unknown type", { type: "Weird", status: "Maybe" }, { word: "Weird: Maybe", tone: "unknown" }],
  ];
  for (const [name, c, want] of cases) it(name, () => expect(conditionWords(st(c))).toMatchObject([want]));
  it("is empty without a status", () => expect(conditionWords(undefined)).toEqual([]));
});

describe("fileLine", () => {
  it("central in sync", () => {
    expect(fileLine(report({}, [inSync("True")]))).toEqual({ text: ".github/agent-factory.yaml matches the MergePolicy.", prominent: false });
  });
  it("central diverging names the count", () => {
    const l = fileLine(report({}, [inSync("False", "Diverges", "a; b")]));
    expect(l.text).toContain("2 places");
    expect(l.prominent).toBe(false);
  });
  it("repository authority with an invalid file is prominent", () => {
    const l = fileLine(report({}, [{ type: "RepositoryFileValid", status: "False" }], "repository"));
    expect(l.text).toContain("auto-merge is paused");
    expect(l.prominent).toBe(true);
  });
  // Each case is a status statuswriter writes; "matches" only when InSync is True.
  const central: [string, Condition[] | undefined, Partial<Report>, string][] = [
    ["no status", undefined, { mergePolicy: "mp" }, ".github/agent-factory.yaml has not been compared with the MergePolicy yet (the MergePolicy has no status yet)."],
    ["not read", [inSync("Unknown", "NotRead")], {}, ".github/agent-factory.yaml has not been compared with the MergePolicy yet (the file has not been read yet)."],
    ["not served", [inSync("Unknown", "NotServed")], {}, ".github/agent-factory.yaml has not been compared with the MergePolicy yet (the MergePolicy is not served yet)."],
    ["read off", [inSync("Unknown", "PolicyReadOff")], {}, ".github/agent-factory.yaml has not been compared with the MergePolicy yet (the factory does not read repository files)."],
    ["file invalid", [{ type: "RepositoryFileValid", status: "False", reason: "Invalid" }, inSync("False", "FileInvalid", "the repository's file is not valid")], {},
      ".github/agent-factory.yaml is invalid; the MergePolicy serves the classes."],
    ["policy error", [inSync("True")], { policyError: "invalid" }, ".github/agent-factory.yaml is invalid; the MergePolicy serves the classes."],
    ["eight differences", [inSync("False", "Diverges", "a; b; c; d; e; and 3 more")], {}, ".github/agent-factory.yaml differs from the MergePolicy in 8 places; the MergePolicy wins."],
  ];
  for (const [name, conds, over, want] of central) it(`central, ${name}`, () => expect(fileLine(report(over, conds))).toEqual({ text: want, prominent: false }));
  it("not migrated with a refused file says so, prominent", () => {
    expect(fileLine(report({ policyError: "invalid" }))).toEqual({ text: "The repository's policy file is invalid: the central classes are shown.", prominent: true });
  });
  it("not migrated", () => {
    expect(fileLine(report())).toEqual({ text: "Classes come from .github/agent-factory.yaml.", prominent: false });
  });
});

describe("differences", () => {
  const cases: [string | undefined, number][] = [[undefined, 0], ["", 0], ["a", 1], ["a; b", 2], ["a; b; c; d; e; and 3 more", 8], ["a; and more", 2]];
  for (const [m, n] of cases) it(`${m} -> ${n}`, () => expect(differences(m)).toBe(n));
});

describe("timeText", () => {
  const now = Date.parse("2026-11-10T12:00:00Z");
  const cases: [string, string][] = [
    ["2026-11-10T11:59:30Z", "just now"], ["2026-11-10T11:15:00Z", "45 min ago"], ["2026-11-10T09:00:00Z", "3 h ago"],
    ["2026-11-09T12:00:00Z", "1 d ago"], ["2026-11-01T12:00:00Z", "9 d ago"], ["soon", "soon"],
    ["2026-12-08T09:00:00Z", new Date("2026-12-08T09:00:00Z").toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" })],
  ];
  for (const [iso, want] of cases) it(`${iso} reads ${want}`, () => {
    const t = timeText(iso, now);
    expect([t.tagName, t.dateTime, t.textContent]).toEqual(["TIME", iso, want]);
  });
});
