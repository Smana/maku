// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { KINDS, policyGuide } from "../src/policy-guide";

const catalog = [{ name: "docs", spec: { allowedFiles: ["^.+\\.mdx?$"], maxPatchFiles: 5, maxModifiedLines: 100, maxPerDay: 2, description: "served" } }];
const click = (root: HTMLElement, text: string) => [...root.querySelectorAll("button")].find((b) => b.textContent === text)!.click();

describe("the guided new-policy flow (correctness signals W7)", () => {
  it("reaches the editor for every kind that a policy can cover, with its signal or its shadow line", () => {
    for (const k of KINDS) {
      const used: Record<string, unknown>[] = [];
      const g = policyGuide(catalog, (spec) => used.push(spec));
      expect(g.querySelector("h3")?.textContent).toBe("What kind of change?");
      click(g, k.kind);
      expect(g.querySelector("h3")?.textContent).toBe("What can check its meaning?");
      expect(g.querySelector(".guide-says")?.textContent).toBe(k.says);
      const next = [...g.querySelectorAll("button")].find((b) => b.textContent === "Next: where, and how much");
      if (!k.preset) {
        expect(next, k.kind).toBeUndefined();
        continue;
      }
      next!.click();
      expect(g.querySelector("h3")?.textContent).toBe("Where, and how much?");
      for (const f of g.querySelectorAll(".editor-field")) expect(f.querySelector(".field-help")?.textContent).toMatch(/\S/);
      click(g, "Open in the editor");
      expect(used, k.kind).toHaveLength(1);
    }
  });
  it("starts from the served policy of the preset's name, else from the template, and edits it", () => {
    const used: Record<string, unknown>[] = [];
    const g = policyGuide(catalog, (spec) => used.push(spec));
    click(g, "Prose");
    click(g, "Next: where, and how much");
    const lines = g.querySelector<HTMLInputElement>('input[aria-label="guide maxModifiedLines"]')!;
    lines.value = "60";
    lines.dispatchEvent(new Event("input"));
    click(g, "Open in the editor");
    expect(used[0]).toMatchObject({ description: "served", maxModifiedLines: 60 });
    const g2 = policyGuide(catalog, (spec) => used.push(spec));
    click(g2, "Configuration (alerts, values, manifests)");
    expect(g2.querySelector(".guide-says")?.textContent).toContain("Needs a test runner");
    click(g2, "Next: where, and how much");
    click(g2, "Open in the editor");
    expect(used[1]).toMatchObject({ requires: ["plan", "tests"] });
  });
  it("promises no rule the factory does not apply yet", () => {
    for (const k of KINDS) expect(`${k.signal} ${k.says}`).not.toMatch(/one in five|factory\/wrong|never merges without you/i);
  });
});
