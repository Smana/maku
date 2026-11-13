// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { mountSettings } from "../src/settings";

const deadLinks = {
  name: "dead-links", generation: 4, manifestPath: "mergepolicy-dead-links.yaml",
  manifest: "apiVersion: maku.ogenki.io/v1alpha1\nkind: MergePolicy\nmetadata:\n  name: dead-links\nspec:\n  maxModifiedLines: 20\n",
  spec: { description: "Retargets dead external links.", requires: ["allowedFiles", "holdoutChecks", "oracleWorkflow"], maxModifiedLines: 20, diffShape: "links-only" },
  status: { observedGeneration: 4, consumerCount: 2, currentCount: 1, liveCount: 0, consumers: [
    { repository: "ai-native-ref", mode: "shadow", servedGeneration: 3, accepted: false, reason: "MissingParameter" },
    { repository: "cloud-native-ref", mode: "shadow", servedGeneration: 4, accepted: true }] },
};

function fakeFetch(routes: Record<string, unknown>, calls: string[] = []): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    calls.push(`${init?.method ?? "GET"} ${path}${init?.body ? " " + String(init.body) : ""}`);
    if (!(path in routes)) return new Response(JSON.stringify({ error: "not_found" }), { status: 404 });
    return new Response(JSON.stringify(routes[path]), { status: 200 });
  }) as typeof fetch;
}

describe("a policy's page (B16)", () => {
  it("shows the definition, what each binding must supply, and the consumers", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/policies/dead-links", fakeFetch({ "/api/policy/policies/dead-links": deadLinks }));
    expect(app.querySelector(".page-title")!.textContent).toBe("dead-links");
    expect(app.querySelector(".policy-counts")!.textContent).toBe("2 consumers · 1 current");
    expect(app.textContent).toContain("Retargets dead external links.");
    expect(app.textContent).toContain("Each binding must supply: allowedFiles, holdoutChecks, oracleWorkflow");
    expect(app.querySelector(".policy-definition")!.textContent).toContain("maxModifiedLines: 20");
    const rows = [...app.querySelectorAll("table.consumers tbody tr")].map((tr) => [...tr.children].map((td) => td.textContent));
    expect(rows).toEqual([["ai-native-ref", "shadow", "3", "no", "MissingParameter"], ["cloud-native-ref", "shadow", "4", "yes", ""]]);
  });

  it("previews an edit: the manifest to submit, and each consumer's change, live first", async () => {
    const calls: string[] = [];
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/policies/dead-links", fakeFetch({ "/api/policy/policies/dead-links": deadLinks,
      "/api/policy/policies/dead-links/preview": { valid: true, manifestPath: "mergepolicy-dead-links.yaml", manifest: deadLinks.manifest, consumers: [
        { repository: "ai-native-ref", mode: "shadow", change: "relaxation", resolves: true },
        { repository: "cloud-native-ref", mode: "live", change: "relaxation", why: ["maxModifiedLines 20 -> 40"], resolves: true }] } }, calls));
    const text = app.querySelector<HTMLTextAreaElement>("textarea.policy-spec")!;
    text.value = text.value.replace("maxModifiedLines: 20", "maxModifiedLines: 40");
    app.querySelector<HTMLButtonElement>(".policy-editor button.primary")!.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(calls.at(-1)).toContain(`POST /api/policy/policies/dead-links/preview`);
    expect(calls.at(-1)).toContain(`"maxModifiedLines":40`);
    expect(app.textContent).toContain("Copy mergepolicy-dead-links.yaml");
    const impact = [...app.querySelectorAll("table.impact tbody tr")].map((tr) => [...tr.children].map((td) => td.textContent));
    expect(impact[0]).toEqual(["cloud-native-ref", "LIVE", "relaxation", "maxModifiedLines 20 -> 40"]);
    expect(impact[1][0]).toBe("ai-native-ref");
  });

  it("says an edit the factory refuses, and offers nothing to copy", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/policies/dead-links", fakeFetch({ "/api/policy/policies/dead-links": deadLinks,
      "/api/policy/policies/dead-links/preview": { valid: false, errors: ["class dead-links: mode is the binding's"] } }));
    app.querySelector<HTMLButtonElement>(".policy-editor button.primary")!.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(app.textContent).toContain("class dead-links: mode is the binding's");
    expect(app.textContent).not.toContain("Copy mergepolicy-dead-links.yaml");
  });

  it("lists the policies the reader sees on the merge policies page", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", fakeFetch({ "/api/policy/repositories": { repositories: [] }, "/api/policy/policies": { policies: [deadLinks] } }));
    const rows = [...app.querySelectorAll("table.policy-table tbody tr")].map((tr) => [...tr.children].map((td) => td.textContent));
    expect(rows).toEqual([["dead-links", "4", "2 consumers · 1 current"]]);
    expect(app.querySelector('table.policy-table a[href="/settings/merge-policies/policies/dead-links"]')).not.toBeNull();
  });
});
