// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { classCard, profilesRow } from "../src/class-card";
import { evaluationWords } from "../src/settings";
import type { Catalogue, RepositoryPage } from "../src/settings";

// The catalogue shaped like ai-native-ref's live config: docs-links live, review off with a variant.
const catalogue = (): Catalogue => ({
  repository: "o/r", approveToMerge: true, maintainer: true, evaluation: { mode: "observe", controlPercent: 20 },
  classes: [
    { name: "docs-links", description: "Dead links in docs", mode: "live", ceilingUSD: 2, floor: "links-solo", allowed: [], escalation: null },
    { name: "review", description: "Anything else", mode: "off", ceilingUSD: 8, floor: "review-floor", allowed: ["review-frontier"],
      escalation: { outOfBudget: "review-frontier", notConverged: "review-frontier" } },
  ],
  profiles: {
    "links-solo": { template: "solo", roles: ["implementer"], tier: "light", checks: false, testFirst: false, loop: "", description: "One file of links", offered: true },
    "review-floor": { template: "", roles: null, tier: "", checks: true, testFirst: false, loop: "", description: "The default", offered: false, notOffered: "no_template" },
    "review-frontier": { template: "trio", roles: ["implementer", "tester", "reviewer"], tier: "frontier", checks: true, testFirst: true, loop: "", description: "Several files", offered: true },
  },
});
const page = (cat: Catalogue | null | undefined = catalogue()): RepositoryPage => ({
  report: { repository: "o/r", minimum: 20, readAt: "", factory: "aws-0", defaultBranch: "main", harnessProfiles: [], guardrails: {},
    classes: [{ class: "docs-links", source: "central", mode: "live" }, { class: "review", source: "central", mode: "off" }, { class: "revert", source: "central", mode: "live" }] },
  promotions: [], exceptions: [], you: { login: "me", maintainer: true, operator: false }, catalogue: cat,
});
const ctx = (p: RepositoryPage) => ({ page: p, get: fetch, say: document.createElement("p"), reload: () => {}, main: document.createElement("main") });
const card = (p: RepositoryPage, i: number) => classCard(p.report.classes[i], ctx(p));

describe("a class card's Profiles row", () => {
  it("shows the floor, the variants and the escalation targets, with each profile's roles and tier", () => {
    const row = card(page(), 1).querySelector(".class-profiles")!;
    expect(row.querySelector("h4")?.textContent).toBe("Profiles");
    expect([...row.querySelectorAll("li")].map((li) => li.textContent)).toEqual([
      "Floor review-floor: roles chosen at triage",
      "Variant review-frontier: roles implementer, tester, reviewer · tier frontier · tests first",
    ]);
    expect(row.querySelector(".class-escalation")?.textContent).toBe("Escalation: out of budget goes to review-frontier; not converged goes to review-frontier.");
  });
  it("says a floor with no template has its roles chosen at triage, and names one that has", () => {
    const live = card(page(), 0).querySelector(".class-profiles")!;
    expect([...live.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["Floor links-solo: roles implementer · tier light"]);
    expect(live.querySelector(".class-escalation")).toBeNull();
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
    cat.profiles["review-frontier"].tier = "<img src=x onerror=alert(1)>";
    const row = card(page(cat), 1).querySelector(".class-profiles")!;
    expect(row.querySelector("img")).toBeNull();
    expect(row.textContent).toContain("<img src=x onerror=alert(1)>");
  });
  it("says so when a class names a profile the factory's list lacks", () => {
    const cat = catalogue();
    delete cat.profiles["review-frontier"];
    expect(card(page(cat), 1).querySelector(".class-profiles")?.textContent).toContain("Variant review-frontier: not in the factory's list of profiles");
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
