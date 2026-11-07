import { describe, expect, it } from "vitest";
import { renderPolicy } from "../src/policy";
import type { Summary } from "../src/summary";

type Policy = NonNullable<Summary["status"]["policy"]>;
const HOW = "The factory merges it once the class's conditions hold; otherwise a maintainer merges it. A pull request that touches a gate path always needs a human merge.";
const summary = (policy?: Partial<Policy>): Summary => ({
  apiVersion: "summary/v1", room: "3kq7x2ma", url: "https://rooms.example/r/3kq7x2ma",
  status: { phase: "AwaitingHuman", run: null, budget: null, pr: null, issue: null, lastVerdict: null,
    ...(policy ? { policy: { mode: "live", how: HOW, ...policy } as Policy } : {}) },
  needsYou: [], actions: [], notes: { untrusted: true, items: [] }, cursor: "seq:1",
});
const render = (s: Summary) => { const root = document.createElement("div"); renderPolicy(root, s); return root; };

describe("the Merge policy fold", () => {
  it("is absent without a policy", () => {
    expect(render(summary()).childElementCount).toBe(0);
  });
  it("is a closed fold named by its class and mode", () => {
    const d = render(summary({ class: "docs-links" })).querySelector("details")!;
    expect(d.open).toBe(false);
    expect(d.querySelector("summary")!.textContent).toBe("Merge policy · docs-links · live");
  });
  it("names the gate off, and a task without a class", () => {
    expect(render(summary({ gateOff: true, mode: "unarmed" })).querySelector("summary")!.textContent).toBe("Merge policy · gate off");
    expect(render(summary({ mode: "unarmed" })).querySelector("summary")!.textContent).toBe("Merge policy · no class · unarmed");
  });
  it("says what it takes to merge, and the gate's decision", () => {
    const root = render(summary({ class: "fix", decision: "it would auto-merge, and the merge gate is in shadow" }));
    expect(root.querySelector(".policy-how")!.textContent).toBe(`To merge: ${HOW}`);
    expect(root.querySelector(".policy-decision")!.textContent).toBe("Decision: it would auto-merge, and the merge gate is in shadow");
    expect(render(summary({ class: "fix" })).querySelector(".policy-decision")).toBeNull();
  });
  it("lists the class's conditions", () => {
    const root = render(summary({ class: "fix", approveToMerge: true, conditions: { diffShape: "links-only", maxPatchFiles: 5,
      maxModifiedLines: 40, modifyOnly: true, allowedFiles: ["^docs/", "^README\\.md$"], protectedFiles: ["^\\.github/"],
      holdoutChecks: ["e2e"], maxPerDay: 3, adoptLogins: ["renovate[bot]"], requiredLabel: "ready", oracle: true } }));
    expect(root.querySelector(".policy-conditions")!.textContent).toBe(
      "Conditions: diff shape links-only · at most 5 files · at most 40 changed lines · modifies only · allowed files ^docs/, ^README\\.md$ · " +
      "protected files ^\\.github/ · holdout checks e2e · at most 3 merges a day · adopts renovate[bot] · needs label ready · oracle-backed · approve to merge");
  });
  it("says a class declares no condition", () => {
    expect(render(summary({ class: "docs", mode: "unarmed" })).querySelector(".policy-conditions")!.textContent).toBe("Conditions: none declared");
  });
  it("lists the repository's classes, and says when the list was cut", () => {
    const root = render(summary({ class: "a", truncated: true, classes: [{ name: "a", mode: "live" }, { name: "deps", mode: "shadow", adopted: true }] }));
    expect([...root.querySelectorAll(".policy-classes li")].map((li) => li.textContent)).toEqual(["a · live", "deps · shadow · adopted"]);
    expect(root.querySelector(".policy-truncated")!.textContent).toBe("Some entries were left out: the list is longer than the room carries.");
  });
  it("stays open across a refresh once opened", () => {
    const root = render(summary({ class: "fix" }));
    root.querySelector("details")!.open = true;
    renderPolicy(root, summary({ class: "fix", decision: "it would auto-merge, and the merge gate is in shadow" }));
    expect(root.querySelector("details")!.open).toBe(true);
  });
  it("sets every string as text", () => {
    const root = render(summary({ class: "x", classes: [{ name: "<img src=x>", mode: "live" }], conditions: { allowedFiles: ["<b>a</b>"] } }));
    expect(root.querySelector("img, b")).toBeNull();
    expect(root.textContent).toContain("<img src=x>");
  });
});
