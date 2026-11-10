// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { centralManifest, conditionWords, fileLine, policyState } from "../src/settings-api";
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
  it("not migrated", () => {
    expect(fileLine(report())).toEqual({ text: "Classes come from .github/agent-factory.yaml.", prominent: false });
  });
});
