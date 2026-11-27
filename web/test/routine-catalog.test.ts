// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { mountRoutineCatalog, type RoutineCatalogResponse } from "../src/routine-catalog";
import { mountRoutines } from "../src/routines";

function catalogFixture(over: Partial<RoutineCatalogResponse> = {}): RoutineCatalogResponse {
  return {
    uiWrite: false,
    upkeepDailyUSD: 20.0,
    repositories: ["Smana/ai-native-ref", "Smana/cloud-native-ref", "Smana/empty-repo"],
    capabilities: [
      {
        repository: "Smana/ai-native-ref",
        canWrite: false,
        egress: ["pypi"],
        boundRoutines: [],
      },
      {
        repository: "Smana/cloud-native-ref",
        canWrite: true,
        egress: ["golang", "npm"],
        boundRoutines: ["dead-links"],
      },
      {
        repository: "Smana/empty-repo",
        canWrite: true,
        egress: [],
        boundRoutines: [],
      },
    ],
    routines: [
      {
        name: "dead-links",
        description: "Fix dead external links in docs",
        catalog: true,
        workflow: "quick",
        dataClass: "public",
        defaultPrompt: "## Goal\nFix dead links.\n\n## Verify\n- Run link check\n",
        probeKind: "dead-links",
        requires: ["oracleClass"],
        maxOpenPRs: 1,
        maxCostUSD: 2.0,
        cooldown: "24h0m0s",
        breaker: {
          window: 10,
          minSamples: 5,
          minMergeRate: 0.25,
          maxConsecutiveClosed: 3,
          maxConsecutiveFailures: 3,
        },
        egress: ["npm"],
        consumerCount: 1,
        consumers: [{ repository: "Smana/cloud-native-ref", mode: "propose", paused: false }],
      },
      {
        name: "renovate-red",
        description: "Investigate failing dependency update PRs",
        catalog: true,
        workflow: "standard",
        dataClass: "internal",
        defaultPrompt: "## Goal\nFix red Renovate PR.\n\n## Verify\n- Tests pass\n",
        probeKind: "renovate-red",
        maxOpenPRs: 2,
        maxCostUSD: 3.5,
        breaker: {
          window: 10,
          minSamples: 5,
          minMergeRate: 0.25,
          maxConsecutiveClosed: 3,
          maxConsecutiveFailures: 3,
        },
        probeAuthor: "renovate[bot]",
        consumerCount: 0,
        consumers: [],
      },
      {
        name: "custom-audit",
        description: "Platform custom audit",
        catalog: false,
        workflow: "quick",
        probeKind: "actions-findings",
        requires: ["probeWorkflow"],
        maxOpenPRs: 1,
        maxCostUSD: 1.5,
        breaker: {
          window: 8,
          minSamples: 4,
          minMergeRate: 0.5,
          maxConsecutiveClosed: 2,
          maxConsecutiveFailures: 2,
        },
        consumerCount: 0,
        consumers: [],
      },
    ],
    ...over,
  };
}

function fakeFetch(routes: Record<string, Response | unknown>, calls: { path: string; credentials?: RequestCredentials }[] = []): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    calls.push({ path, credentials: init?.credentials });
    if (!(path in routes)) {
      return new Response(JSON.stringify({ error: "not_found" }), { status: 404 });
    }
    const v = routes[path];
    if (v instanceof Response) return v.clone();
    return new Response(JSON.stringify(v), { status: 200 });
  }) as typeof fetch;
}

describe("routine catalog (/routines/catalog)", () => {
  it("renders catalog and custom routine cards with specs, ceilings, consumers, upkeep budget, and uiWrite status", async () => {
    const calls: { path: string; credentials?: RequestCredentials }[] = [];
    const app = document.createElement("div");
    await mountRoutines(app, "/routines/catalog", fakeFetch({ "/api/routines/catalog": catalogFixture({ uiWrite: true }) }, calls));

    expect(calls[0]).toEqual({ path: "/api/routines/catalog", credentials: "same-origin" });
    expect(app.querySelector('nav.settings-nav a[aria-current="page"]')?.getAttribute("href")).toBe("/routines");
    expect(app.querySelector('a[href="/routines"]')).not.toBeNull();

    expect(app.textContent).toContain("$20.00");
    expect(app.textContent).toContain("uiWrite");

    const cards = [...app.querySelectorAll<HTMLElement>(".routine-catalog-card")];
    expect(cards).toHaveLength(3);

    expect(cards[0].textContent).toContain("dead-links");
    expect(cards[0].textContent).toContain("catalog");
    expect(cards[0].textContent).toContain("dead-links");
    expect(cards[0].textContent).toContain("oracleClass");
    expect(cards[0].textContent).toContain("$2.00");
    expect(cards[0].textContent).toContain("24h0m0s");
    expect(cards[0].textContent).toContain("Smana/cloud-native-ref");

    expect(cards[1].textContent).toContain("renovate-red");
    expect(cards[1].textContent).toContain("$3.50");
    expect(cards[1].textContent).toContain("renovate[bot]");

    expect(cards[2].textContent).toContain("custom-audit");
    expect(cards[2].textContent).toContain("custom");
    expect(cards[2].textContent).toContain("probeWorkflow");
  });

  it("populates target repository picker including repos with 0 routines and updates Enable/Configure links", async () => {
    const app = document.createElement("div");
    await mountRoutineCatalog(app, fakeFetch({ "/api/routines/catalog": catalogFixture() }));

    const select = app.querySelector<HTMLSelectElement>('select[name="targetRepository"]')!;
    expect(select).not.toBeNull();
    expect([...select.options].map((o) => o.value)).toEqual([
      "Smana/ai-native-ref",
      "Smana/cloud-native-ref",
      "Smana/empty-repo",
    ]);

    // Default first repo: Smana/ai-native-ref (0 bound routines, read-only -> secondary "View configuration").
    const links = () => [...app.querySelectorAll<HTMLAnchorElement>(".routine-catalog-enable")];
    expect(links()[0].getAttribute("href")).toBe("/routines/Smana/ai-native-ref/dead-links/edit?from=catalog");
    expect(links()[0].textContent).toContain("View configuration");
    expect(links()[0].dataset.readonly).toBe("true");
    expect(app.textContent).toContain("Read-only");

    // Switch to Smana/cloud-native-ref (dead-links already bound, writable).
    select.value = "Smana/cloud-native-ref";
    select.dispatchEvent(new Event("change"));
    expect(links()[0].getAttribute("href")).toBe("/routines/Smana/cloud-native-ref/dead-links/edit?from=catalog");
    expect(links()[0].textContent).toContain("Configure on repository");
    expect(links()[0].dataset.readonly).toBe("false");
    expect(links()[1].getAttribute("href")).toBe("/routines/Smana/cloud-native-ref/renovate-red/edit?from=catalog");
    expect(links()[1].textContent).toContain("Enable on repository");

    // Switch to Smana/empty-repo (0 bound routines, writable).
    select.value = "Smana/empty-repo";
    select.dispatchEvent(new Event("change"));
    expect(links()[0].getAttribute("href")).toBe("/routines/Smana/empty-repo/dead-links/edit?from=catalog");
    expect(links()[0].textContent).toContain("Enable on repository");

    // Catalog search filters cards and restores cleanly.
    const search = app.querySelector<HTMLInputElement>('input[name="catalogSearch"]')!;
    search.value = "renovate";
    search.dispatchEvent(new Event("input"));
    const cards = [...app.querySelectorAll<HTMLElement>(".routine-catalog-card")];
    expect(cards.filter((c) => !c.hidden)).toHaveLength(1);
    search.value = "";
    search.dispatchEvent(new Event("input"));
    expect(cards.filter((c) => !c.hidden)).toHaveLength(3);
  });

  it("neutralizes untrusted HTML/script payloads via textContent (zero innerHTML)", async () => {
    const xss = catalogFixture({
      repositories: ['<img src=x onerror="alert(1)">/repo'],
      capabilities: [
        {
          repository: '<img src=x onerror="alert(1)">/repo',
          canWrite: true,
          boundRoutines: [],
        },
      ],
      routines: [
        {
          name: "dead-links",
          description: '<script>alert("desc")</script>',
          catalog: true,
          workflow: "quick",
          defaultPrompt: '<img src=x onerror="alert(2)">',
          probeKind: "actions-findings",
          requires: ['<svg onload="alert(3)">'],
          maxOpenPRs: 1,
          maxCostUSD: 2.0,
          breaker: { window: 10, minSamples: 5, minMergeRate: 0.25, maxConsecutiveClosed: 3, maxConsecutiveFailures: 3 },
          probeWorkflow: '<img src=x onerror="alert(4)">',
          consumerCount: 1,
          consumers: [{ repository: '<img src=x onerror="alert(5)">/repo', mode: "propose", paused: false }],
        },
      ],
    });

    const app = document.createElement("div");
    await mountRoutineCatalog(app, fakeFetch({ "/api/routines/catalog": xss }));

    expect(app.querySelector("main")!.querySelectorAll("img, script, svg")).toHaveLength(0);
    expect(app.textContent).toContain('<script>alert("desc")</script>');
    expect(app.textContent).toContain('<img src=x onerror="alert(2)">');
    expect(app.textContent).toContain('<svg onload="alert(3)">');
  });

  it("handles empty repositories and server errors cleanly", async () => {
    const emptyApp = document.createElement("div");
    await mountRoutineCatalog(
      emptyApp,
      fakeFetch({
        "/api/routines/catalog": catalogFixture({ repositories: [], capabilities: [] }),
      }),
    );
    expect(emptyApp.textContent).toContain("No readable repositories");
    expect(emptyApp.querySelectorAll<HTMLAnchorElement>(".routine-catalog-enable")).toHaveLength(0);

    const errApp = document.createElement("div");
    await mountRoutineCatalog(
      errApp,
      fakeFetch({
        "/api/routines/catalog": new Response(JSON.stringify({ error: "routine_state_unavailable" }), { status: 503 }),
      }),
    );
    expect(errApp.textContent).toContain("Routine state store is unavailable");
  });
});
