// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { classCard, evidenceBar, READING } from "../src/class-card";
import type { Evidence, RepositoryPage } from "../src/settings";

const ev = (over: Partial<Evidence> = {}): Evidence => ({
  shadow: { mergedUnchanged: 14, changed: 1, reverted: 0, autoMerged: 0, drill: 1, reviewers: [] },
  clean: 14, bad: 0, immature: 0, pending: 0, bound: 0.1, budget: 0.2, widen: false,
  ladder: [{ merges: 29, maxBad: 0, reached: false }, { merges: 46, maxBad: 1, reached: false }, { merges: 61, maxBad: 2, reached: false }],
  drill: "passed", minimum: 20, eligible: false, blockers: ["below_minimum"], ...over,
});
const page = (): RepositoryPage => ({
  report: { repository: "o/r", minimum: 20, readAt: "", factory: "aws-0", defaultBranch: "main", harnessProfiles: ["plain"], guardrails: {},
    policy: { factory: "aws-0", classes: { docs: { mode: "shadow", author: "agents", harness: "plain", conditions: { all: [{ maxPatchFiles: 10 }] } } } },
    classes: [{ class: "docs", source: "repository", mode: "shadow", evidence: ev() }] },
  promotions: [], exceptions: [], you: { login: "me", maintainer: true, operator: false },
});
const ctx = (p = page()) => ({ page: p, get: fetch, say: document.createElement("p"), reload: () => {}, main: document.createElement("main"), policy: p.report.policy });

describe("a class card", () => {
  it("names the class, its mode and what the mode means, then the class as sentences", () => {
    const c = classCard(page().report.classes[0], ctx());
    expect(c.id).toBe("class-docs");
    expect(c.querySelector("h3")?.textContent).toBe("docs");
    expect(c.querySelector(".mode-badge")?.textContent).toBe("Shadow · records what it would do, merges nothing");
    expect(c.querySelector(".mode-badge")?.getAttribute("data-mode")).toBe("shadow");
    expect(c.textContent).toContain("it changes at most 10 files");
  });
  it("says a class set by the factory is set there, not a bare (central)", () => {
    const p = page();
    p.report.classes[0].source = "central";
    expect(classCard(p.report.classes[0], ctx(p)).textContent).toContain("Set by the factory's central config");
  });
  it("shows the evidence as progress toward the minimum, with the count in words", () => {
    const bar = evidenceBar(ev());
    expect(bar.getAttribute("role")).toBe("meter");
    expect(bar.getAttribute("aria-valuenow")).toBe("14");
    expect(bar.getAttribute("aria-valuemax")).toBe("20");
    expect(bar.textContent).toContain("14 of 20 clean decisions to promote");
  });
  it("says once that evidence is being read, and offers no promotion meanwhile", () => {
    const p = page();
    delete p.report.classes[0].evidence;
    const c = classCard(p.report.classes[0], ctx(p));
    expect(c.textContent).toContain(READING);
    expect([...c.querySelectorAll("button")].some((b) => b.textContent === "Promote to live")).toBe(false);
  });
  it("says there is no promotion request in one line, not an empty table", () => {
    const c = classCard(page().report.classes[0], ctx());
    expect(c.querySelector("table")).toBeNull();
    expect(c.textContent).toContain("No promotion requested.");
  });
  it("lists the class's own promotion and exception inside it", () => {
    const p = page();
    p.promotions = [{ id: "p1", repository: "o/r", class: "docs", threshold: 20, requestedBy: "me", requestedAt: "2026-11-09", state: "pr_opened", pullRequest: 7 }];
    p.exceptions = [{ id: "e1", repository: "o/r", class: "docs", minimum: 12, justification: "low blast radius, docs only", requestedBy: "me", requestedAt: "2026-11-08", state: "approved" }];
    const c = classCard(p.report.classes[0], ctx(p));
    expect(c.textContent).toContain("Promotion requested by me on 2026-11-09: pull request opened");
    expect(c.querySelector('a[href="https://github.com/o/r/pull/7"]')).not.toBeNull();
    expect(c.textContent).toContain("Exception: 12 clean decisions instead of 20, approved");
  });
  it("shows a live class no bar, no ladder and no promotion, one line of what it has done", () => {
    const p = page();
    p.report.classes[0] = { class: "docs", source: "repository", mode: "live", unproposed: "not_shadow", evidence: ev({ clean: 40, eligible: true, blockers: [] }) };
    const c = classCard(p.report.classes[0], ctx(p));
    expect(c.querySelector('[role="meter"]')).toBeNull();
    expect(c.querySelector(".evidence-ladder")).toBeNull();
    expect([...c.querySelectorAll("button")].some((b) => b.textContent === "Promote to live")).toBe(false);
    expect(c.textContent).toContain("Live since promotion \u00b7 40 clean decisions recorded.");
    expect(c.querySelector(".evidence-detail")).not.toBeNull();
    expect(c.textContent).not.toContain("Not promotable");
  });
  it("shows an off class no bar: one line, and why it cannot be promoted", () => {
    const p = page();
    p.report.classes[0] = { class: "docs", source: "repository", mode: "off", unproposed: "not_shadow", evidence: ev({ clean: 0 }) };
    const c = classCard(p.report.classes[0], ctx(p));
    expect(c.querySelector('[role="meter"]')).toBeNull();
    expect(c.textContent).toContain("Off: no decisions recorded.");
    expect(c.textContent).toContain("Not promotable here: the class is not in shadow.");
  });
  it("writes eligibility as sentences, never bare codes", () => {
    const say = (e: Partial<Evidence>, over: Partial<RepositoryPage> = {}) => {
      const p = { ...page(), ...over };
      p.report.classes[0] = { class: "docs", source: "repository", mode: "shadow", evidence: ev(e) };
      return classCard(p.report.classes[0], ctx(p)).querySelector(".evidence-eligibility")?.textContent;
    };
    expect(say({ eligible: true, blockers: [] })).toBe("Eligible to promote: enough clean decisions, no bad outcome, drill passed.");
    expect(say({ blockers: ["below_minimum", "bad_outcome", "no_drill", "odd_one"] }))
      .toBe("Not yet eligible: fewer clean decisions than the minimum, a bad outcome was recorded, the revert drill has not passed, odd one.");
    const exceptions = [{ id: "e1", repository: "o/r", class: "docs", minimum: 12, justification: "x", requestedBy: "me", requestedAt: "", state: "approved", expiresAt: "2999-01-01T00:00:00Z" }] as RepositoryPage["exceptions"];
    expect(say({ clean: 14 }, { exceptions })).toBe("Eligible to promote under exception e1 (12 clean decisions required).");
  });
  it("says why a shadow class cannot be proposed in words", () => {
    const p = page();
    p.report.classes[0] = { class: "docs", source: "central", mode: "shadow", unproposed: "not_in_repository_policy", evidence: ev() };
    expect(classCard(p.report.classes[0], ctx(p)).querySelector(".class-actions")?.textContent)
      .toBe("Not promotable here: the class is not declared in the repository's policy file.");
  });
});
