// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { isSettings, mountSettings } from "../src/settings";
import { bindingEditor } from "../src/binding-editor";
import { boundPage, MANIFEST } from "./bound-fixture";

function fakeFetch(routes: Record<string, unknown>, calls: string[] = []): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    calls.push(`${init?.method ?? "GET"} ${path}${init?.body ? " " + String(init.body) : ""}`);
    if (!(path in routes)) return new Response(JSON.stringify({ error: "not_found" }), { status: 404 });
    return new Response(JSON.stringify(routes[path]), { status: 200 });
  }) as typeof fetch;
}

describe("the binding editor (B4, B16)", () => {
  it("edits a binding's parameters, never the policy's meaning, and copies the Repository manifest", async () => {
    const calls: string[] = [];
    const get = fakeFetch({ "/api/policy/repositories/Smana/ai-native-ref/preview": { valid: true, manifest: MANIFEST, manifestPath: "repository-ai-native-ref.yaml",
      bindings: [{ name: "docs-links", mode: "live", resolves: true, change: "unchanged" }, { name: "dead-links", mode: "shadow", resolves: false, reason: "MissingParameter", message: "monitor.flux needs a source" }] } }, calls);
    const p = boundPage();
    const box = bindingEditor(p.report, { "dead-links": { diffShape: "links-only", requires: ["allowedFiles"] } }, true, get);
    // The policy's diffShape is shown, and no field edits it.
    expect(box.textContent).toContain("diffShape: links-only (the policy's)");
    expect(box.querySelector('[data-key="diffShape"]')).toBeNull();
    expect(box.querySelector('[data-key="allowedFiles"]')).not.toBeNull();
    const mode = box.querySelector<HTMLSelectElement>('select[aria-label="docs-links mode"]')!;
    mode.value = "live";
    mode.dispatchEvent(new Event("change"));
    box.querySelector<HTMLButtonElement>("button.primary")!.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(calls.at(-1)).toContain(`"kind":"binding"`);
    expect(calls.at(-1)).toContain(`{"name":"docs-links","mode":"live","holdoutChecks":["lint"]}`);
    expect(box.textContent).toContain("Copy repository-ai-native-ref.yaml");
    expect(box.textContent).toContain("dead-links: does not resolve: MissingParameter: monitor.flux needs a source");
  });
});

describe("routing (PR15)", () => {
  it("routes a policy's page before a repository's", async () => {
    expect(isSettings("/settings/merge-policies/policies/x")).toBe(true);
    const calls: string[] = [];
    await mountSettings(document.createElement("div"), "/settings/merge-policies/policies/x", fakeFetch({}, calls));
    expect(calls).toEqual(["GET /api/policy/policies/x"]);
    calls.length = 0;
    await mountSettings(document.createElement("div"), "/settings/merge-policies/Smana/runlore", fakeFetch({}, calls));
    expect(calls).toEqual(["GET /api/policy/repositories/Smana/runlore"]);
  });
});
