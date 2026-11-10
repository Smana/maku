// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { classCard, evidenceBar, READING } from "../src/class-card";
import type { Evidence, RepositoryPage } from "../src/settings";

const PROMOTE = "To promote, set live: true on this class in the MergePolicy and merge the change.";

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
  it("leads with the name, the mode word, the summary, the evidence bar, then the folded conditions", () => {
    const c = classCard(page().report.classes[0], ctx());
    expect(c.id).toBe("class-docs");
    expect(c.querySelector("h3")?.textContent).toBe("docs");
    const badge = c.querySelector(".mode-badge")!;
    expect(badge.textContent).toBe("Shadow");
    expect(badge.getAttribute("data-mode")).toBe("shadow");
    expect(badge.getAttribute("title")).toBe("records what it would do, merges nothing");
    expect(c.querySelector(".class-intent")).toBeNull();
    const order = [...c.children].map((e) => e.className || e.tagName.toLowerCase());
    expect(order.slice(0, 4)).toEqual(["class-card-head", "class-summary", "evidence-bar", "evidence-ladder"]);
    expect(c.querySelector(".class-summary")?.textContent).toBe("≤ 10 files");
    const more = c.querySelector("details.class-conditions")!;
    expect(more.querySelector("summary")?.textContent).toBe("Conditions");
    expect(more.textContent).toContain("it changes at most 10 files");
    expect(c.querySelector(".class-source")).toBeNull();
  });
  it("puts the description between the badge and the summary", () => {
    const p = page();
    p.report.policy!.classes!.docs.description = "Docs-only pull requests with links intact.";
    const c = classCard(p.report.classes[0], ctx(p));
    expect([...c.children].slice(0, 3).map((e) => e.className)).toEqual(["class-card-head", "class-intent", "class-summary"]);
    expect(c.querySelector(".class-intent")?.textContent).toBe("Docs-only pull requests with links intact.");
  });
  it("shows the badge as the bare word for each mode", () => {
    for (const [mode, word] of [["live", "Live"], ["shadow", "Shadow"], ["off", "Off"]]) {
      const p = page();
      p.report.classes[0].mode = mode;
      expect(classCard(p.report.classes[0], ctx(p)).querySelector(".mode-badge")?.textContent).toBe(word);
    }
  });
  it("says a class set by the factory has its conditions there, and never a per-card source", () => {
    const p = page();
    p.report.classes[0].source = "central";
    const c = classCard(p.report.classes[0], { ...ctx(p), policy: undefined });
    expect(c.querySelector(".class-summary")?.textContent).toBe("This class's conditions are set in the factory's config and are not shown here.");
    expect(c.querySelector(".class-source")).toBeNull();
    expect(c.querySelector("details.class-conditions")).toBeNull();
  });
  it("shows the evidence as progress toward the minimum, with the count in words", () => {
    const bar = evidenceBar(ev());
    expect(bar.getAttribute("role")).toBe("meter");
    expect(bar.getAttribute("aria-valuenow")).toBe("14");
    expect(bar.getAttribute("aria-valuemax")).toBe("20");
    expect(bar.textContent).toContain("14 of 20 clean decisions to promote");
  });
  it("counts the decisions recorded past the minimum, and never fills a blocked bar", () => {
    const past = evidenceBar(ev({ clean: 21, eligible: true, blockers: [] }));
    expect(past.querySelector(".evidence-count")?.textContent).toBe("20 of 20 clean decisions (21 recorded)");
    expect(past.hasAttribute("data-blocked")).toBe(false);
    expect((past.querySelector(".evidence-fill") as HTMLElement).style.width).toBe("100%");
    const blocked = evidenceBar(ev({ clean: 25, eligible: false, blockers: ["no_drill"] }));
    expect(blocked.getAttribute("data-blocked")).toBe("true");
    expect((blocked.querySelector(".evidence-fill") as HTMLElement).style.width).toBe("95%");
  });
  it("offers no promotion nor exception while the class has a promotion open", () => {
    for (const [state, eligible] of [["requested", true], ["pr_opened", false]] as const) {
      const p = page();
      p.report.classes[0] = { ...p.report.classes[0], evidence: ev(eligible ? { clean: 20, eligible: true, blockers: [] } : {}),
        proposal: { path: ".github/agent-factory.yaml", line: 3, file: "", editURL: "" } };
      p.promotions = [{ id: "p1", repository: "o/r", class: "docs", threshold: 20, requestedBy: "me", requestedAt: "2026-11-09", state }];
      const names = [...classCard(p.report.classes[0], ctx(p)).querySelectorAll("button")].map((b) => b.textContent);
      expect(names).not.toContain("Promote to live");
      expect(names).not.toContain("Ask an operator for an exception");
    }
  });
  it("leaves the page's one reading line alone, and offers no promotion meanwhile", () => {
    const p = page();
    delete p.report.classes[0].evidence;
    const c = classCard(p.report.classes[0], ctx(p));
    expect(c.textContent).not.toContain(READING);
    expect(c.querySelector(".evidence-reading")).toBeNull();
    expect([...c.querySelectorAll("button")].some((b) => b.textContent === "Promote to live")).toBe(false);
  });
  it("says a class added since the last read is still being read", () => {
    const p = page();
    p.report.evidenceAt = "2026-11-10T00:00:00Z";
    delete p.report.classes[0].evidence;
    const c = classCard(p.report.classes[0], ctx(p));
    expect(c.querySelector(".evidence-reading")?.textContent).toBe("Evidence for this class is still being read.");
    expect([...c.querySelectorAll("button")].some((b) => b.textContent === "Promote to live")).toBe(false);
  });
  it("says nothing of promotions or exceptions the class does not have", () => {
    const c = classCard(page().report.classes[0], ctx());
    expect(c.querySelector("table")).toBeNull();
    expect(c.querySelector(".class-promotions")).toBeNull();
    expect(c.querySelector(".class-exceptions")).toBeNull();
    expect(c.textContent).not.toContain("No promotion requested");
    expect(c.textContent).not.toContain("adds no condition of its own");
  });
  it("lists the class's own promotion and exception inside it", () => {
    const p = page();
    p.promotions = [{ id: "p1", repository: "o/r", class: "docs", threshold: 20, requestedBy: "me", requestedAt: "2026-11-09", state: "pr_opened", pullRequest: 7 }];
    p.exceptions = [{ id: "e1", repository: "o/r", class: "docs", minimum: 12, justification: "low blast radius, docs only", requestedBy: "me", requestedAt: "2026-11-08", state: "approved" }];
    const c = classCard(p.report.classes[0], ctx(p));
    expect(c.textContent).toContain("Promotion requested by me on 2026-11-09: pull request opened #7 (threshold 20)");
    p.promotions[0] = { ...p.promotions[0], threshold: 12, exceptionId: "e1" };
    expect(classCard(p.report.classes[0], ctx(p)).textContent).toContain("pull request opened #7 (under exception e1, threshold 12)");
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
    expect(c.textContent).toContain("Live \u00b7 40 clean decisions recorded.");
    expect(c.querySelector(".evidence-detail")).not.toBeNull();
    expect(c.textContent).not.toContain("Not promotable");
    expect(c.querySelector(".class-actions")?.textContent).toBe("");
  });
  it("shows an off class no bar: one line, and no word on promotion", () => {
    const p = page();
    p.report.classes[0] = { class: "docs", source: "repository", mode: "off", unproposed: "not_shadow", evidence: ev({ clean: 0 }) };
    const c = classCard(p.report.classes[0], ctx(p));
    expect(c.querySelector('[role="meter"]')).toBeNull();
    expect(c.textContent).toContain("Off: no decisions recorded.");
    expect(c.textContent).not.toContain("Not promotable");
    expect(c.querySelector(".class-actions")?.textContent).toBe("");
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
  it("says why a shadow class of a repository file cannot be proposed, in words", () => {
    const p = page();
    p.report.classes[0] = { class: "docs", source: "central", mode: "shadow", unproposed: "not_in_repository_policy", evidence: ev() };
    expect(classCard(p.report.classes[0], ctx(p)).querySelector(".class-actions")?.textContent)
      .toBe("Not promotable here: the class is not declared in the repository's policy file.");
  });
  it("tells a shadow class of a central MergePolicy to promote there, and offers to edit it", () => {
    const p = page();
    p.report.mergePolicy = "cnr";
    p.report.classes[0] = { class: "docs", source: "central", mode: "shadow", unproposed: "not_in_repository_policy", evidence: ev() };
    const actions = classCard(p.report.classes[0], ctx(p)).querySelector(".class-actions")!;
    expect(actions.textContent).toContain(PROMOTE);
    expect(actions.textContent).not.toContain("Not promotable");
    expect([...actions.querySelectorAll("button")].map((x) => x.textContent)).toContain("Edit this class"); // settings.test clicks it on a rendered page
  });
  it("shows a class's last decision from the MergePolicy's status", () => {
    const p = page();
    p.report.mergePolicyStatus = { namespace: "agent-system", generation: 3, status: { classes: [{ name: "docs", mode: "shadow", lastDecision: "would_merge:clean", lastDecisionAt: "2026-11-10T00:00:00Z" }] } };
    const now = () => Date.parse("2026-11-10T03:00:00Z");
    const last = classCard(p.report.classes[0], { ...ctx(p), now }).querySelector(".class-last")!;
    expect(last.textContent).toBe("Last decision: would merge (clean), 3 h ago");
    expect(last.querySelector("time")?.getAttribute("datetime")).toBe("2026-11-10T00:00:00Z");
    expect(last.querySelector("time")?.title).toBe(new Date("2026-11-10T00:00:00Z").toLocaleString());
    for (const [at, words] of [["2026-11-10T02:59:30Z", "just now"], ["2026-11-10T02:15:00Z", "45 min ago"], ["2026-11-09T03:00:00Z", "1 day ago"], ["2026-11-01T00:00:00Z", "9 days ago"], ["not a time", "not a time"]]) {
      p.report.mergePolicyStatus.status.classes![0].lastDecisionAt = at;
      expect(classCard(p.report.classes[0], { ...ctx(p), now }).querySelector(".class-last time")?.textContent).toBe(words);
    }
    expect(classCard(page().report.classes[0], ctx()).querySelector(".class-last")).toBeNull();
  });
  it("counts a class eligible only under an approved exception as eligible, its bar not blocked", () => {
    const p = page();
    p.report.classes[0] = { class: "docs", source: "repository", mode: "shadow", evidence: ev({ clean: 14, eligible: false, blockers: ["below_minimum"] }) };
    p.exceptions = [{ id: "e1", repository: "o/r", class: "docs", minimum: 12, justification: "x", requestedBy: "me", requestedAt: "", state: "approved", expiresAt: "2999-01-01T00:00:00Z" }];
    const c = classCard(p.report.classes[0], ctx(p));
    expect(c.querySelector(".evidence-bar")?.hasAttribute("data-blocked")).toBe(false);
    expect(c.querySelector(".evidence-eligibility")?.textContent).toContain("exception e1");
    p.exceptions = [];
    expect(classCard(p.report.classes[0], ctx(p)).querySelector(".evidence-bar")?.getAttribute("data-blocked")).toBe("true");
  });
});
