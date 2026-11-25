// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { classCard, profilesRow } from "../src/class-card";
import { evaluationWords } from "../src/settings";
import type { Catalogue, RepositoryPage } from "../src/settings";

// The catalogue shaped like ai-native-ref's live config: docs-links live, review off, and the
// workflows every class offers (WF4), review-floor the default.
const catalogue = (): Catalogue => ({
  repository: "o/r", approveToMerge: true, maintainer: true, evaluation: { mode: "observe", controlPercent: 20 },
  defaultWorkflow: "review-floor",
  classes: [
    { name: "docs-links", description: "Dead links in docs", mode: "live", ceilingUSD: 2 },
    { name: "review", description: "Anything else", mode: "off", ceilingUSD: 8 },
  ],
  profiles: {
    "links-solo": { template: "solo", roles: ["implementer"], tier: "light", checks: false, testFirst: false, loop: "", description: "One file of links", ceilingUSD: 5.10, offered: true },
    "review-floor": { template: "", roles: null, tier: "", checks: true, testFirst: false, loop: "", description: "The default", ceilingUSD: 5.10, offered: false, notOffered: "no_template",
      escalation: { outOfBudget: "review-frontier", notConverged: "review-frontier" } },
    "review-frontier": { template: "pair", roles: ["implementer", "reviewer"], tier: "frontier", checks: true, testFirst: true, loop: "", description: "Several files", ceilingUSD: 13.60, offered: true },
  },
});
const page = (cat: Catalogue | null | undefined = catalogue()): RepositoryPage => ({
  report: { repository: "o/r", minimum: 20, readAt: "", factory: "aws-0", defaultBranch: "main", harnessProfiles: [], guardrails: {},
    classes: [{ class: "docs-links", source: "central", mode: "live" }, { class: "review", source: "central", mode: "off" }, { class: "revert", source: "central", mode: "live" }] },
  promotions: [], exceptions: [], you: { login: "me", maintainer: true, operator: false }, catalogue: cat,
});
const ctx = (p: RepositoryPage) => ({ page: p, get: fetch, say: document.createElement("p"), reload: () => {}, main: document.createElement("main") });
const card = (p: RepositoryPage, i: number) => classCard(p.report.classes[i], ctx(p));

describe("a class card's Workflows row", () => {
  it("shows the repository's default, then every other workflow, with roles, description, ceiling and escalation", () => {
    const row = card(page(), 1).querySelector(".class-profiles")!;
    expect(row.querySelector("h4")?.textContent).toBe("Workflows");
    expect([...row.querySelectorAll("li")].map((li) => li.textContent)).toEqual([
      "Default review-floor: roles chosen at triage · The default · up to $5.10 · out of budget goes to review-frontier; not converged goes to review-frontier",
      "links-solo: roles implementer · One file of links · up to $5.10",
      "review-frontier: roles implementer → reviewer · Several files · up to $13.60 · tests first",
    ]);
    expect(row.textContent).not.toContain("tier ");
  });
  it("lists a list-form workflow's resolved stages, a scoper, a tester and a custom read role included", () => {
    const cat = catalogue();
    cat.profiles.thorough = { template: "pair", roles: ["implementer", "reviewer"], tier: "frontier", checks: true, testFirst: true, loop: "",
      description: "Tests first, then two reviews", ceilingUSD: 13.60, offered: true,
      path: ["scoper", "tester", "implementer", "reviewer", "security-reviewer", "CI", "merge gate"] };
    cat.profiles["links-solo"].path = ["scoper", "implementer", "CI", "merge gate"];
    const items = [...card(page(cat), 1).querySelectorAll(".class-profiles li")].map((li) => li.textContent);
    expect(items).toContain("links-solo: roles scoper → implementer · One file of links · up to $5.10");
    expect(items).toContain("thorough: roles scoper → tester → implementer → reviewer → security-reviewer · Tests first, then two reviews · up to $13.60 · tests first");
  });
  it("is the same on every class (WF4)", () => {
    const live = card(page(), 0).querySelector(".class-profiles")!;
    expect(live.querySelectorAll("li").length).toBe(3);
  });
  it("is absent for a class the catalogue does not offer, and for a page with no catalogue", () => {
    expect(card(page(), 2).querySelector(".class-profiles")).toBeNull(); // revert: the factory's own
    expect(card(page(null), 1).querySelector(".class-profiles")).toBeNull();
    const bare = page();
    delete bare.catalogue; // a page from a broker that predates the catalogue
    expect(card(bare, 1).querySelector(".class-profiles")).toBeNull();
    expect(profilesRow(null, "review")).toBeUndefined();
  });
  it("sets a maintainer's text as text, never as markup", () => {
    const cat = catalogue();
    cat.profiles["review-frontier"].description = "<img src=x onerror=alert(1)>";
    const row = card(page(cat), 1).querySelector(".class-profiles")!;
    expect(row.querySelector("img")).toBeNull();
    expect(row.textContent).toContain("<img src=x onerror=alert(1)>");
  });
  it("says so when the default is not in the factory's list", () => {
    const cat = catalogue();
    cat.defaultWorkflow = "gone";
    expect(card(page(cat), 1).querySelector(".class-profiles")?.textContent).toContain("Default gone: not in the factory's list of workflows");
  });
});

describe("the settings index", () => {
  it("shows the factory's evaluation mode and control share", () => {
    expect(evaluationWords([page()])).toBe(" · evaluation: observe · control 20 %");
    expect(evaluationWords([page(null), page()])).toBe(" · evaluation: observe · control 20 %");
    expect(evaluationWords([page(null)])).toBe("");
    expect(evaluationWords([])).toBe("");
  });
});
