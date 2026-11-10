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
});
