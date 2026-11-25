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

describe("a policy's page", () => {
  it("leads with what it allows and puts definition and repository health after it", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/policies/dead-links", fakeFetch({ "/api/policy/policies/dead-links": deadLinks }));
    expect(app.querySelector(".page-title")!.textContent).toBe("dead-links");
    expect(app.querySelector(".policy-counts")!.textContent).toBe("2 repositories");
    expect(app.querySelector(".policy-sentence")!.textContent).toContain("link targets only");
    expect(app.textContent).toContain("Each binding must supply: allowedFiles, holdoutChecks, oracleWorkflow");
    expect(app.querySelector(".policy-definition")!.textContent).toContain("Retargets dead external links.");
    const rows = [...app.querySelectorAll("table.consumers tbody tr")].map((tr) => [...tr.children].map((td) => td.textContent));
    expect(rows).toEqual([["ai-native-ref", "shadow", "refused", "MissingParameter"], ["cloud-native-ref", "shadow", "accepted", ""]]);
    expect(app.textContent).not.toContain("Generation 4");
  });

  it("offers a shipped preset with YAML, essentials open and valid optional fields folded", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/policies/dead-links", fakeFetch({ "/api/policy/policies/dead-links": deadLinks, "/api/policy/policies": { policies: [deadLinks] } }));
    expect(app.querySelector(".policy-presets")?.textContent).toContain("Start from a shipped policy");
    expect(app.querySelector(".preset-yaml summary")?.textContent).toBe("Show the YAML");
    expect(app.querySelector('[data-key="allowedFiles"]')?.closest("details")).toBeNull();
    expect(app.querySelector('[data-key="maxModifiedLines"]')?.closest("details")?.querySelector("summary")?.textContent).toBe("Expert");
    expect(app.querySelector('[data-key="evidence"]')?.closest("details")?.querySelector("summary")?.textContent).toBe("Expert");
    expect(app.querySelector('[data-key="companionFiles"]')).toBeNull();
    expect(app.querySelector('[data-key="harness"]')).toBeNull();
  });

  it("previews an edit: the manifest to submit, and each repository's change, live first", async () => {
    const calls: string[] = [];
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/policies/dead-links", fakeFetch({ "/api/policy/policies/dead-links": deadLinks,
      "/api/policy/policies/dead-links/preview": { valid: true, manifestPath: "mergepolicy-dead-links.yaml", manifest: deadLinks.manifest, consumers: [
        { repository: "ai-native-ref", mode: "shadow", change: "relaxation", resolves: true },
        { repository: "cloud-native-ref", mode: "live", change: "relaxation", why: ["maxModifiedLines 20 -> 40"], resolves: true }] } }, calls));
    const input = app.querySelector<HTMLInputElement>('input[aria-label="policy maxModifiedLines"]')!;
    input.value = "40";
    input.dispatchEvent(new Event("input"));
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

  it("backtests the edited policy against the selected repository", async () => {
    const calls: string[] = [];
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/policies/dead-links", fakeFetch({
      "/api/policy/policies/dead-links": deadLinks,
      "/api/policy/repositories": { repositories: ["Smana/ai-native-ref"] },
      "/api/policy/repositories/Smana/ai-native-ref/backtest": { valid: true, repository: "Smana/ai-native-ref", since: "2026-11-01",
        counts: { considered: 1, changed: 1, unchanged: 0, unknown: 0, matched: 0 },
        rows: [{ pullRequest: 42, url: "https://github.com/Smana/ai-native-ref/pull/42", policy: "dead-links",
          before: { verdict: "shadow", reason: "shadow_would_arm" }, after: { verdict: "human", reason: "patch_too_large" }, change: "changed" }] },
    }, calls));
    const input = app.querySelector<HTMLInputElement>('input[aria-label="policy maxModifiedLines"]')!;
    input.value = "10";
    input.dispatchEvent(new Event("input"));
    [...app.querySelectorAll("button")].find((b) => b.textContent === "Backtest the last 30 days")!.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(calls.at(-1)).toContain('POST /api/policy/repositories/Smana/ai-native-ref/backtest {"kind":"policy","policy":"dead-links"');
    expect(calls.at(-1)).toContain('"maxModifiedLines":10');
    expect(app.querySelector(".backtest-table a")?.getAttribute("href")).toBe("https://github.com/Smana/ai-native-ref/pull/42");
  });

  it("lists the policies by what they allow on the merge policies page", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", fakeFetch({ "/api/policy/repositories": { repositories: [] }, "/api/policy/policies": { policies: [deadLinks] } }));
    const rows = [...app.querySelectorAll("table.policy-table tbody tr")].map((tr) => [...tr.children].map((td) => td.textContent));
    expect(rows).toEqual([["dead-links", "link targets only, at most 20 lines", "2 repositories"]]);
    expect(app.querySelector('table.policy-table a[href="/settings/merge-policies/policies/dead-links"]')).not.toBeNull();
  });
});
