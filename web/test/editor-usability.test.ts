// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { bindingEditor } from "../src/binding-editor";
import { classCard } from "../src/class-card";
import { MEANING_KEYS } from "../src/policy-fields";
import { boundPage } from "./bound-fixture";

const settle = () => new Promise((r) => setTimeout(r, 0));

describe("the decision-centered editors", () => {
  it("uses an Off, Shadow, Live radio ladder and writes disabled for Off", async () => {
    const calls: string[] = [];
    const get = (async (_input: RequestInfo | URL, init?: RequestInit) => {
      calls.push(String(init?.body));
      return new Response(JSON.stringify({ valid: true, manifest: "kind: Repository\n", bindings: [] }));
    }) as typeof fetch;
    const p = boundPage();
    const box = bindingEditor(p.report, {}, true, get);
    const group = box.querySelector<HTMLElement>('[role="radiogroup"][aria-label="docs-links mode"]')!;
    expect([...group.querySelectorAll("input")].map((x) => x.value)).toEqual(["disabled", "shadow", "live"]);
    expect([...group.querySelectorAll("label")].map((x) => x.textContent)).toEqual(["Off", "Shadow", "Live"]);
    expect(group.querySelector<HTMLInputElement>('input[value="shadow"]')?.checked).toBe(true);
    expect(box.querySelector('select[aria-label="docs-links mode"]')).toBeNull();
    const off = group.querySelector<HTMLInputElement>('input[value="disabled"]')!;
    off.click();
    box.querySelector<HTMLButtonElement>("button.primary")!.click();
    await settle();
    expect(calls[0]).toContain('"mode":"disabled"');
  });

  it("opens essentials and folds shape, checks and expert fields into valid groups", () => {
    const p = boundPage();
    const box = bindingEditor(p.report, {}, true, fetch);
    const allowed = box.querySelector('[data-key="allowedFiles"]')!;
    expect(allowed.closest("details")).toBeNull();
    expect(box.querySelector('[data-key="holdoutChecks"]')?.closest("details")?.querySelector("summary")?.textContent).toBe("Shape and checks");
    expect(box.querySelector('[data-key="protectedFiles"]')?.closest("details")?.querySelector("summary")?.textContent).toBe("Expert");
    expect(box.querySelector('[data-key="diffShape"]')).toBeNull();
    for (const removed of ["harness", "harnessAllowed", "escalation", "checks", "testFirst", "requires"]) expect(MEANING_KEYS).not.toContain(removed);
  });

  it("puts the detailed statistics under How sure is this?", () => {
    const p = boundPage();
    const card = classCard(p.report.classes[0], { page: p, get: fetch, say: document.createElement("p"), reload: () => {}, main: document.createElement("main") });
    const fold = [...card.querySelectorAll("details")].find((d) => d.querySelector("summary")?.textContent === "How sure is this?")!;
    expect(fold.querySelector(".evidence-ladder")).not.toBeNull();
    expect(fold.querySelector(".evidence-detail")).not.toBeNull();
    expect(card.querySelector(":scope > .evidence-ladder")).toBeNull();
    expect(card.querySelector(".policy-sentence")).not.toBeNull();
  });

  it("backtests the edited bindings against the repository and links the changed pull request", async () => {
    const calls: string[] = [];
    const get = (async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push(`${init?.method ?? "GET"} ${input} ${String(init?.body ?? "")}`);
      return new Response(JSON.stringify({ valid: true, repository: "Smana/ai-native-ref", since: "2026-11-01",
        counts: { considered: 1, changed: 1, unchanged: 0, unknown: 0, matched: 0 },
        rows: [{ pullRequest: 42, url: "https://github.com/Smana/ai-native-ref/pull/42", policy: "docs-links",
          before: { verdict: "shadow", reason: "shadow_would_arm" }, after: { verdict: "human", reason: "patch_too_large" }, change: "changed" }] }));
    }) as typeof fetch;
    const box = bindingEditor(boundPage().report, {}, true, get);
    [...box.querySelectorAll("button")].find((b) => b.textContent === "Backtest the last 30 days")!.click();
    await settle();
    expect(calls[0]).toContain("POST /api/policy/repositories/Smana/ai-native-ref/backtest");
    expect(calls[0]).toContain('"kind":"binding"');
    expect(calls[0]).toContain('"since":"30d"');
    expect(box.querySelector(".backtest-table a")?.getAttribute("href")).toBe("https://github.com/Smana/ai-native-ref/pull/42");
    expect(box.textContent).toContain("matched none");
  });
});
