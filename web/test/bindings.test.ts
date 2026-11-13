// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { mountSettings } from "../src/settings";
import { boundPage, ev } from "./bound-fixture";
import { generationLine } from "../src/class-card";

function fakeFetch(routes: Record<string, unknown>, calls: string[] = []): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    calls.push(`${init?.method ?? "GET"} ${path}${init?.body ? " " + String(init.body) : ""}`);
    if (!(path in routes)) return new Response(JSON.stringify({ error: "not_found" }), { status: 404 });
    return new Response(JSON.stringify(routes[path]), { status: 200 });
  }) as typeof fetch;
}

const card = (app: HTMLElement, name: string) => app.querySelector<HTMLElement>(`#class-${name}`)!;

describe("a bound repository's page (B16)", () => {
  it("shows one card per binding, each with its policy, generation, parameters and verdict", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/ai-native-ref", fakeFetch({ "/api/policy/repositories/Smana/ai-native-ref": boundPage() }));
    expect(app.querySelectorAll("article.class-card")).toHaveLength(2);
    const dead = card(app, "dead-links");
    expect(dead.textContent).toContain("Refused: MissingParameter: monitor.flux needs Repository.spec.merge.fluxSource");
    expect(dead.textContent).toContain("Serving generation 3");
    expect(dead.querySelector('a[href="/settings/merge-policies/policies/dead-links"]')).not.toBeNull();
    expect(dead.textContent).toContain("allowedFiles: ^docs/");
    const docs = card(app, "docs-links");
    expect(docs.textContent).toContain("Policy generation 1");
    expect(docs.textContent).not.toContain("Refused");
  });

  it("splits the evidence by policy generation, and says where a promotion is made", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/ai-native-ref", fakeFetch({ "/api/policy/repositories/Smana/ai-native-ref": boundPage() }));
    expect(card(app, "dead-links").textContent).toContain("gen 4: 3 clean · gen 3: 17 clean · before the binding: 12");
    expect(card(app, "docs-links").textContent).toContain("To promote, set mode: live on the binding in repository-ai-native-ref.yaml and merge the change.");
    const cut = boundPage();
    cut.report.repositoryStatus!.status.authority = "repository";
    const app2 = document.createElement("div");
    await mountSettings(app2, "/settings/merge-policies/Smana/ai-native-ref", fakeFetch({ "/api/policy/repositories/Smana/ai-native-ref": cut }));
    expect(card(app2, "docs-links").textContent).toContain("To promote, set mode: live on the binding in .github/agent-factory.yaml and merge the change.");
  });

  it("heads the page with the Repository, its conditions and its merge settings, the Factory's marked", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/ai-native-ref", fakeFetch({ "/api/policy/repositories/Smana/ai-native-ref": boundPage() }));
    const head = app.querySelector(".policy-header")!;
    const rows = [...head.querySelectorAll("dt")].map((d) => d.textContent);
    expect(rows).toContain("Repository");
    expect(rows).not.toContain("MergePolicy");
    expect(head.textContent).toContain("Bindings refused: refused: dead-links (MissingParameter)");
    const merge = app.querySelector(".repository-merge")!.textContent;
    expect(merge).toContain("autoMergesPerDay: 2 (from the Factory)");
    expect(merge).toContain("mergerLogin: ogenki-agent-merger[bot]");
    expect(merge).not.toContain("mergerLogin: ogenki-agent-merger[bot] (from the Factory)");
  });

  it("names the Repository in the list", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", fakeFetch({ "/api/policy/repositories": { repositories: ["Smana/ai-native-ref"] },
      "/api/policy/repositories/Smana/ai-native-ref": boundPage(), "/api/policy/policies": { policies: [] } }));
    const row = [...app.querySelectorAll("table.repo-table tbody tr")][0];
    expect(row.children[1].textContent).toBe("Repository ai-native-ref · Accepted · Bindings refused");
  });

  it("writes each generation's share in words", () => {
    expect(generationLine(ev())).toBe("gen 4: 3 clean · gen 3: 17 clean · before the binding: 12");
    expect(generationLine(ev({ shadow: { ...ev().shadow, byGeneration: undefined } }))).toBe("");
  });
});

