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
// The factory's sentence for docs in shadow (correctness signals CS7): the card prints it as sent.
const docsMeaning = { signal: "none", prose: true, lead: "Would merge without you when", checkedBy: "nothing: this is prose",
  text: "**Would merge without you when** an agent changes Markdown files and a reviewer approves. **Meaning checked by** nothing: this is prose." };
const DOCS_WORDS = "Would merge without you when an agent changes Markdown files and a reviewer approves. Meaning checked by nothing: this is prose.";
const page = (): RepositoryPage => ({
  report: { repository: "o/r", minimum: 20, readAt: "", factory: "aws-0", defaultBranch: "main", harnessProfiles: ["plain"], guardrails: {},
    policy: { factory: "aws-0" },
    classes: [{ class: "docs", source: "repository", mode: "shadow", evidence: ev(), meaning: docsMeaning }] },
  promotions: [], exceptions: [], you: { login: "me", maintainer: true, operator: false },
});
const ctx = (p = page()) => ({ page: p, get: fetch, say: document.createElement("p"), reload: () => {}, main: document.createElement("main") });

describe("a class card", () => {
  it("leads with the name, the mode word, where its conditions are, then the evidence bar", () => {
    const c = classCard(page().report.classes[0], ctx());
    expect(c.id).toBe("class-docs");
    expect(c.querySelector("h3")?.textContent).toBe("docs");
    const badge = c.querySelector(".mode-badge")!;
    expect(badge.textContent).toBe("Shadow");
    expect(badge.getAttribute("data-mode")).toBe("shadow");
    expect(badge.getAttribute("title")).toBe("records what it would do, merges nothing");
    expect(c.querySelector(".class-intent")).toBeNull();
    const order = [...c.children].map((e) => e.className || e.tagName.toLowerCase());
    expect(order.slice(0, 5)).toEqual(["class-card-head", "policy-sentence", "evidence-bar", "evidence-progress", "policy-certainty"]);
    expect(c.querySelector("details.class-conditions")).toBeNull();
    expect(c.querySelector(".class-source")).toBeNull();
  });
  it("leads with the policy's sentence and a link to its definition", () => {
    const p = page();
    p.report.repositoryObject = "r";
    const card = classCard(p.report.classes[0], ctx(p));
    expect(card.querySelector(".policy-sentence")?.textContent).toBe(DOCS_WORDS);
    expect(card.querySelector(".class-summary")).toBeNull();
  });
  it("links a bound class's pill to its row in the Bindings editor (gcp-0, 2026-11-25)", () => {
    const p = page();
    p.report.repositoryObject = "r";
    p.report.bindings = [{ name: "docs", parameters: { name: "docs", mode: "shadow" } }] as typeof p.report.bindings;
    const badge = classCard(p.report.classes[0], ctx(p)).querySelector(".mode-badge")!;
    expect(badge.tagName).toBe("A");
    expect(badge.getAttribute("href")).toBe("#binding-docs");
    expect(badge.textContent).toBe("Shadow");
    // Bound in the repository's own file, or not migrated: no editor row to go to.
    p.report.repositoryStatus = { status: { authority: "repository" } } as typeof p.report.repositoryStatus;
    expect(classCard(p.report.classes[0], ctx(p)).querySelector(".mode-badge")?.tagName).toBe("SPAN");
    expect(classCard(page().report.classes[0], ctx()).querySelector(".mode-badge")?.tagName).toBe("SPAN");
  });
  it("shows the badge as the bare word for each mode", () => {
    for (const [mode, word] of [["live", "Live"], ["shadow", "Shadow"], ["off", "Off"]]) {
      const p = page();
      p.report.classes[0].mode = mode;
      expect(classCard(p.report.classes[0], ctx(p)).querySelector(".mode-badge")?.textContent).toBe(word);
    }
  });
  it("uses the same plain sentence for a factory-defined policy and no per-card source", () => {
    const p = page();
    p.report.classes[0].source = "central";
    const c = classCard(p.report.classes[0], ctx(p));
    expect(c.querySelector(".policy-sentence")?.textContent).toBe(DOCS_WORDS);
    expect(c.querySelector(".class-source")).toBeNull();
    expect(c.querySelector("details.class-conditions")).toBeNull();
  });
  it("shows the evidence as progress toward the minimum, with the count in words", () => {
    const bar = evidenceBar(ev());
    expect(bar.getAttribute("role")).toBe("meter");
    expect(bar.getAttribute("aria-valuenow")).toBe("14");
    expect(bar.getAttribute("aria-valuemax")).toBe("20");
    expect(bar.textContent).toContain("14 of 20 agreed to promote");
  });
  it("counts the decisions recorded past the minimum, and never fills a blocked bar", () => {
    const past = evidenceBar(ev({ clean: 21, eligible: true, blockers: [] }));
    expect(past.querySelector(".evidence-count")?.textContent).toBe("20 of 20 agreed (21 recorded)");
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
    p.exceptions[0].expiresAt = "2026-12-08T09:00:00Z";
    const now = () => Date.parse("2026-11-10T00:00:00Z");
    const c = classCard(p.report.classes[0], { ...ctx(p), now });
    expect(c.textContent).toContain("Promotion requested by me 1 d ago: pull request opened #7 (threshold 20)");
    expect(c.querySelector(".class-promotions time")?.getAttribute("datetime")).toBe("2026-11-09");
    const until = c.querySelector(".class-exceptions time")!;
    expect(c.querySelector(".class-exceptions")?.textContent).toContain(` until ${until.textContent}.`);
    expect(until.textContent).toBe(new Date("2026-12-08T09:00:00Z").toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" }));
    expect(classCard(p.report.classes[0], { ...ctx(p), now: () => Date.parse("2027-01-01T00:00:00Z") }).querySelector(".class-exceptions")?.textContent)
      .toContain(", expired 23 d ago.");
    p.promotions[0] = { ...p.promotions[0], threshold: 12, exceptionId: "e1" };
    expect(classCard(p.report.classes[0], ctx(p)).textContent).toContain("pull request opened #7 (under exception e1, threshold 12)");
    expect(c.querySelector('a[href="https://github.com/o/r/pull/7"]')).not.toBeNull();
    expect(c.textContent).toContain("Exception: 12 agreed decisions instead of 20, approved");
  });
  it("shows a live class no bar and no promotion, its statistics folded", () => {
    const p = page();
    p.report.classes[0] = { class: "docs", source: "repository", mode: "live", unproposed: "not_shadow", evidence: ev({ clean: 40, eligible: true, blockers: [] }) };
    const c = classCard(p.report.classes[0], ctx(p));
    expect(c.querySelector('[role="meter"]')).toBeNull();
    expect(c.querySelector(":scope > .evidence-ladder")).toBeNull();
    expect(c.querySelector("details.policy-certainty .evidence-ladder")).not.toBeNull();
    expect([...c.querySelectorAll("button")].some((b) => b.textContent === "Promote to live")).toBe(false);
    expect(c.textContent).toContain("Live. 0 merged alone. 0 quiet. 0 wrong calls.");
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
    expect(say({ eligible: true, blockers: [] })).toBe("Eligible to promote: enough agreed decisions, no wrong call, rollback drill done.");
    expect(say({ clean: 4, bad: 1, blockers: ["below_minimum", "bad_outcome", "no_drill", "odd_one"] })).toBe("Blocks promotion:");
    const exceptions = [{ id: "e1", repository: "o/r", class: "docs", minimum: 12, justification: "x", requestedBy: "me", requestedAt: "", state: "approved", expiresAt: "2999-01-01T00:00:00Z" }] as RepositoryPage["exceptions"];
    expect(say({ clean: 14 }, { exceptions })).toBe("Eligible to promote under exception e1 (12 agreed decisions required).");
  });
  it("writes the track record in words, each part with its hover explanation", () => {
    const p = page();
    p.report.classes[0] = { class: "docs", source: "repository", mode: "shadow", evidence: ev({ clean: 4, bad: 1, immature: 2, bound: 0.751, drill: "none" }) };
    const d = classCard(p.report.classes[0], ctx(p)).querySelector(".evidence-detail")!;
    expect(d.textContent).toBe("4 of 20 agreed \u00b7 1 wrong call \u00b7 2 awaiting outcome \u00b7 worst-case wrong-call rate \u2264 75 % \u00b7 no rollback drill yet");
    const titles = [...d.querySelectorAll("span[title]")].map((s) => s.getAttribute("title"));
    expect(titles.join("|")).toContain("inside the monitor window");
    expect(titles.join("|")).toContain("Worst-case wrong-call rate \u2264 75 % at 95 % confidence");
    expect(d.textContent).not.toMatch(/bound|immature|widening/i);
  });
  it("says why a shadow class of a repository file cannot be proposed, in words", () => {
    const p = page();
    p.report.classes[0] = { class: "docs", source: "central", mode: "shadow", unproposed: "not_in_repository_policy", evidence: ev() };
    expect(classCard(p.report.classes[0], ctx(p)).querySelector(".class-actions")?.textContent)
      .toBe("Not promotable here: the class is not declared in the repository's policy file.");
  });
  it("shows a class's last decision from the Repository's status", () => {
    const p = page();
    p.report.repositoryObject = "r";
    p.report.repositoryStatus = { namespace: "agent-system", generation: 3, status: { classes: [{ name: "docs", mode: "shadow", accepted: true, lastDecision: "would_merge:clean", lastDecisionAt: "2026-11-10T00:00:00Z" }] } };
    const now = () => Date.parse("2026-11-10T03:00:00Z");
    const last = classCard(p.report.classes[0], { ...ctx(p), now }).querySelector(".class-last")!;
    expect(last.textContent).toBe("Last decision: would merge (clean), 3 h ago");
    expect(last.querySelector("time")?.getAttribute("datetime")).toBe("2026-11-10T00:00:00Z");
    expect(last.querySelector("time")?.title).toBe(new Date("2026-11-10T00:00:00Z").toLocaleString());
    for (const [at, words] of [["2026-11-10T02:59:30Z", "just now"], ["2026-11-10T02:15:00Z", "45 min ago"], ["2026-11-09T03:00:00Z", "1 d ago"], ["2026-11-01T00:00:00Z", "9 d ago"], ["not a time", "not a time"]]) {
      p.report.repositoryStatus.status.classes![0].lastDecisionAt = at;
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

  // Correctness signals W5: progress names the judge of every number, live and in shadow.
  it("counts agreed, quiet and wrong calls, each with its judge", () => {
    const p = page();
    const c = classCard(p.report.classes[0], ctx(p));
    expect(c.querySelector(".evidence-progress")?.textContent).toBe("14 of 20 agreed: you merged 14 unchanged, changed 1. 0 wrong calls. Rollback drill: done.");
    p.report.classes[0] = { class: "docs-links", source: "central", mode: "live", unproposed: "not_shadow",
      evidence: ev({ clean: 5, minimum: 5, eligible: true, blockers: [], outcomes: { merges: 34, conclusive: 34, bad: 0 } }),
      meaning: { signal: "links-only", prose: false, lead: "", checkedBy: "", text: "x" } };
    expect(classCard(p.report.classes[0], ctx(p)).querySelector(".evidence-live")?.textContent)
      .toBe("Live. 34 merged alone, all signal-checked by links-only. 34 quiet. 0 wrong calls.");
    p.report.classes[0].meaning = { signal: "none", prose: true, lead: "", checkedBy: "", text: "x" };
    p.report.classes[0].evidence = ev({ outcomes: { merges: 20, conclusive: 19, bad: 1 }, bad: 1 });
    expect(classCard(p.report.classes[0], ctx(p)).querySelector(".evidence-live")?.textContent)
      .toBe("Live. 20 merged alone, unchecked: nothing checks the meaning of prose. 18 quiet. 1 wrong call.");
  });

  // W6: a maintainer sees Promote disabled, one line per blocker with its next step.
  it("keeps Promote visible and disabled, and says why with the next step", () => {
    const p = page();
    p.report.classes[0] = { ...p.report.classes[0], proposal: { path: "f.yaml", line: 3, file: "", editURL: "" },
      evidence: ev({ clean: 7, bad: 1, drill: "none", blockers: ["below_minimum", "bad_outcome", "no_drill", "Unchecked"] }) };
    const c = classCard(p.report.classes[0], ctx(p));
    const promote = [...c.querySelectorAll("button")].find((b) => b.textContent === "Promote to live")!;
    expect(promote.disabled).toBe(true);
    const lines = [...c.querySelectorAll(".promote-blockers li")].map((li) => li.textContent);
    expect(lines).toEqual([
      "Needs 13 more decisions you agree with. Merge unchanged the pull requests it would have merged.",
      "1 wrong call among its recent merges. Read each one in the decisions list: a wrong call blocks promotion.",
      "No rollback drill on this repository yet. Run `maku policy drill owner/name#PR` on a merged pull request, then merge the revert it opens.",
      "Unchecked.",
    ]);
    expect([...c.querySelectorAll("button")].some((b) => b.textContent === "Ask an operator for an exception")).toBe(true);
  });
});
