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

describe("a bound repository's page", () => {
  it("shows one card per binding and reports a refused binding once, with its field and fix", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/ai-native-ref", fakeFetch({ "/api/policy/repositories/Smana/ai-native-ref": boundPage() }));
    expect(app.querySelectorAll("article.class-card")).toHaveLength(2);
    const dead = card(app, "dead-links");
    expect(dead.textContent).not.toContain("Refused");
    expect(dead.textContent).toContain("files under docs/");
    expect(dead.querySelector('a[href="/settings/merge-policies/policies/dead-links"]')).not.toBeNull();
    const health = app.querySelectorAll(".policy-header .repository-health");
    expect(health).toHaveLength(1);
    expect(health[0].textContent).toContain("monitor.flux needs Repository.spec.merge.fluxSource");
    expect(health[0].textContent).toContain("Fix the named field");
    expect(card(app, "docs-links").textContent).not.toContain("Refused");
  });

  it("keeps generation statistics under the evidence fold, and says where a promotion is made", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/ai-native-ref", fakeFetch({ "/api/policy/repositories/Smana/ai-native-ref": boundPage() }));
    expect(card(app, "dead-links").querySelector("details.policy-certainty")?.textContent).toContain("gen 4: 3 clean · gen 3: 17 clean, 1 changed · before the binding: 12 clean");
    expect(card(app, "docs-links").textContent).toContain("To promote, set mode: live on the binding in repository-ai-native-ref.yaml and merge the change.");
    const cut = boundPage();
    cut.report.repositoryStatus!.status.authority = "repository";
    const app2 = document.createElement("div");
    await mountSettings(app2, "/settings/merge-policies/Smana/ai-native-ref", fakeFetch({ "/api/policy/repositories/Smana/ai-native-ref": cut }));
    expect(card(app2, "docs-links").textContent).toContain("To promote, set mode: live on the binding in .github/agent-factory.yaml and merge the change.");
  });

  it("shows no healthy status chips and keeps the merge settings available", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/ai-native-ref", fakeFetch({ "/api/policy/repositories/Smana/ai-native-ref": boundPage() }));
    const head = app.querySelector(".policy-header")!;
    expect([...head.querySelectorAll("dt")].map((d) => d.textContent)).toContain("Repository");
    expect(head.querySelectorAll(".policy-condition")).toHaveLength(0);
    const merge = app.querySelector(".repository-merge")!.textContent;
    expect(merge).toContain("autoMergesPerDay: 2 (from the Factory)");
    expect(merge).toContain("mergerLogin: ogenki-agent-merger[bot]");
    expect(merge).not.toContain("mergerLogin: ogenki-agent-merger[bot] (from the Factory)");
  });

  it("puts the health note beside the repository in the decision summary", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", fakeFetch({ "/api/policy/repositories": { repositories: ["Smana/ai-native-ref"] },
      "/api/policy/repositories/Smana/ai-native-ref": boundPage(), "/api/policy/policies": { policies: [] } }));
    const row = [...app.querySelectorAll("table.repo-table tbody tr")][0];
    expect(row.children[0].textContent).toContain("Refused binding dead-links");
    expect(row.children[1].textContent).toBe("-");
  });

  it("writes each generation's share in words", () => {
    expect(generationLine(ev())).toBe("gen 4: 3 clean · gen 3: 17 clean, 1 changed · before the binding: 12 clean");
    expect(generationLine(ev({ shadow: { ...ev().shadow, byGeneration: undefined } }))).toBe("");
  });
});

describe("a bound repository under repository authority", () => {
  const cutOver = () => {
    const p = boundPage();
    p.report.repositoryStatus!.status.authority = "repository";
    p.report.bindings = [];
    p.report.policy = { factory: "aws-0", mergePolicies: [{ name: "dead-links", mode: "shadow", allowedFiles: ["^docs/"], oracleWorkflow: "dead-links.yaml" },
      { name: "docs-links", mode: "shadow", holdoutChecks: ["lint"] }] };
    return p;
  };
  it("links a file binding's reusable definition from its card", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/ai-native-ref", fakeFetch({ "/api/policy/repositories/Smana/ai-native-ref": cutOver() }));
    expect(card(app, "dead-links").querySelector('a[href="/settings/merge-policies/policies/dead-links"]')).not.toBeNull();
  });
  it("offers no Repository manifest editor: the bindings are the file's", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/ai-native-ref", fakeFetch({ "/api/policy/repositories/Smana/ai-native-ref": cutOver(), "/api/policy/policies": { policies: [] } }));
    expect(app.querySelector(".binding-editor")).toBeNull();
    expect(app.textContent).not.toContain("repository-ai-native-ref.yaml");
    expect(app.textContent).toContain("The bindings are in .github/agent-factory.yaml, which decides the policies: edit them there");
  });
});
