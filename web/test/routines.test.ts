// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import {
  isRoutines,
  mountRoutines,
  sparklineOutcome,
  type RoutineDetail,
  type RoutineOverview,
  type RoutineSparklineDay,
} from "../src/routines";

function makeSparkline(overrides: Record<number, Partial<RoutineSparklineDay>> = {}): RoutineSparklineDay[] {
  return Array.from({ length: 14 }, (_, i) => ({
    date: `2026-11-${String(i + 13).padStart(2, "0")}`,
    merged: 0,
    closed: 0,
    noOp: 0,
    escalated: 0,
    ...overrides[i],
  }));
}

function overview(over: Partial<RoutineOverview> = {}): RoutineOverview {
  return {
    repository: "Smana/cloud-native-ref",
    routine: "dead-links",
    description: "Fix dead external links",
    mode: "pr",
    workflow: "quick",
    probeKind: "dead-links",
    cron: "0 7 * * *",
    paused: false,
    maxOpenPRs: 1,
    maxCostUSD: 2.0,
    openCount: 0,
    nextRunAt: "2026-11-27T07:00:00Z",
    sparkline: makeSparkline({
      10: { merged: 1 },
      11: { noOp: 2 },
      12: { closed: 1, merged: 1 },
      13: { escalated: 1, closed: 1 },
    }),
    rollingMerged: 6,
    rollingClosed: 4,
    rollingNoOp: 2,
    mergeRate: 0.6,
    usdPerMerged: 1.5,
    promptEditURL: "https://github.com/Smana/cloud-native-ref/edit/main/.github/agent-factory/upkeep/dead-links.md",
    promptSHA: "abcdef1234567890abcdef1234567890",
    breakerState: "ok",
    canWrite: true,
    ...over,
  };
}

function detail(over: Partial<RoutineDetail> = {}): RoutineDetail {
  return {
    ...overview(),
    defaultPrompt: "Fix the dead external links.",
    runs: [
      {
        name: "rt-2",
        createdAt: "2026-11-26T07:00:00Z",
        phase: "Done",
        outcome: "accepted",
        roomRef: "3kq7x2ma",
        prURL: "https://github.com/Smana/cloud-native-ref/pull/42",
        prNumber: 42,
        promptSHA: "22222222bbbbbbbb",
        requestedBy: "github:Smana",
        costUSD: 0.85,
      },
      {
        name: "rt-1",
        createdAt: "2026-11-25T07:00:00Z",
        phase: "NoOp",
        outcome: "no_op",
        roomRef: "9ab2cdef",
        promptSHA: "11111111aaaaaaaa",
        requestedBy: "system:scheduler",
        costUSD: 0.35,
      },
    ],
    skips: [
      { slotKey: "2026-11-24T07:00", at: "2026-11-24T07:00:00Z", reason: "open_cap" },
      { slotKey: "2026-11-23T07:00", at: "2026-11-23T07:00:00Z", reason: "upkeep_budget" },
      { slotKey: "2026-11-22T07:00", at: "2026-11-22T07:00:00Z", reason: "probe_refused" },
    ],
    ...over,
  };
}

interface RecordedCall {
  method: string;
  path: string;
  credentials?: RequestCredentials;
  headers?: Record<string, string>;
  body?: string;
}

function fakeFetch(routes: Record<string, Response | unknown>, calls: RecordedCall[] = []): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    const headers: Record<string, string> = {};
    if (init?.headers && !(init.headers instanceof Headers) && !Array.isArray(init.headers)) {
      for (const [k, v] of Object.entries(init.headers)) headers[k] = v;
    }
    calls.push({
      method: init?.method ?? "GET",
      path,
      credentials: init?.credentials,
      headers,
      body: init?.body ? String(init.body) : undefined,
    });
    if (!(path in routes)) {
      return new Response(JSON.stringify({ error: "not_found" }), { status: 404 });
    }
    const v = routes[path];
    if (v instanceof Response) return v.clone();
    return new Response(JSON.stringify(v), { status: 200 });
  }) as typeof fetch;
}

const settle = () => new Promise((r) => setTimeout(r, 0));

describe("routine routes", () => {
  it("matches /routines, /routines/catalog, /routines/<owner>/<name>/<routine>, and /edit", () => {
    expect(isRoutines("/routines")).toBe(true);
    expect(isRoutines("/routines/Smana/cloud-native-ref/dead-links")).toBe(true);
    expect(isRoutines("/routines/catalog")).toBe(true);
    expect(isRoutines("/routines/Smana/cloud-native-ref/dead-links/edit")).toBe(true);
    expect(isRoutines("/settings")).toBe(false);
  });

  it("renders Browse catalog CTA both when 0 routines are configured and when populated", async () => {
    const emptyApp = document.createElement("div");
    await mountRoutines(emptyApp, "/routines", fakeFetch({ "/api/routines": { routines: [] } }));
    const emptyCTA = emptyApp.querySelector<HTMLAnchorElement>('a.routine-catalog-cta[href="/routines/catalog"]');
    expect(emptyCTA).not.toBeNull();
    expect(emptyCTA?.textContent).toContain("Browse catalog");

    const popApp = document.createElement("div");
    await mountRoutines(popApp, "/routines", fakeFetch({ "/api/routines": { routines: [overview()] } }));
    const popCTA = popApp.querySelector<HTMLAnchorElement>('a.routine-catalog-cta[href="/routines/catalog"]');
    expect(popCTA).not.toBeNull();
    expect(popCTA?.textContent).toContain("Browse catalog");
    const cfgLink = popApp.querySelector<HTMLAnchorElement>('a.routine-configure-link[href="/routines/Smana/cloud-native-ref/dead-links/edit"]');
    expect(cfgLink).not.toBeNull();
    expect(cfgLink?.textContent).toContain("Configure");
  });

  it("renders a safe error message on malformed percent-encoded detail URLs", async () => {
    const app = document.createElement("div");
    await expect(mountRoutines(app, "/routines/o/r/%ZZ", fakeFetch({}))).resolves.toBeUndefined();
    expect(app.querySelector(".notice")?.textContent).toContain("Invalid routine URL");
    expect(app.querySelector('a[href="/routines"]')).not.toBeNull();
  });
});

describe("sparkline outcome precedence", () => {
  it("prioritizes escalated > closed > merged > no-op > empty", () => {
    expect(sparklineOutcome({ date: "2026-11-26", merged: 1, closed: 1, noOp: 1, escalated: 1 })).toBe("escalated");
    expect(sparklineOutcome({ date: "2026-11-26", merged: 1, closed: 1, noOp: 1, escalated: 0 })).toBe("closed");
    expect(sparklineOutcome({ date: "2026-11-26", merged: 1, closed: 0, noOp: 1, escalated: 0 })).toBe("merged");
    expect(sparklineOutcome({ date: "2026-11-26", merged: 0, closed: 0, noOp: 1, escalated: 0 })).toBe("no-op");
    expect(sparklineOutcome({ date: "2026-11-26", merged: 0, closed: 0, noOp: 0, escalated: 0 })).toBe("empty");
  });
});

describe("routines overview", () => {
  it("renders 14-day sparklines, rolling merge rate, $/merged PR, open PRs, nextRunAt, breaker badge, and filters", async () => {
    const r1 = overview();
    const r2 = overview({
      repository: "Smana/ai-native-ref",
      routine: "doc-drift",
      mode: "disabled",
      paused: true,
      pausedReason: "manual",
      openCount: 0,
      maxOpenPRs: 2,
      mergeRate: null,
      usdPerMerged: null,
      nextRunAt: null,
      breakerState: "half_open",
      canWrite: false,
    });
    const calls: RecordedCall[] = [];
    const app = document.createElement("div");
    await mountRoutines(app, "/routines", fakeFetch({ "/api/routines": { routines: [r1, r2] } }, calls));

    expect(calls[0].credentials).toBe("same-origin");
    expect(app.querySelector('nav.settings-nav a[aria-current="page"]')?.getAttribute("href")).toBe("/routines");

    const cards = [...app.querySelectorAll<HTMLElement>(".routine-card")];
    expect(cards).toHaveLength(2);
    // Attention-first sorting places paused/breaker-attention cards before active cards.
    const docCard = cards[0];
    const deadCard = cards[1];
    expect(docCard.dataset.routine).toBe("doc-drift");
    expect(deadCard.dataset.routine).toBe("dead-links");
    expect(deadCard.querySelector("h2 a.routine-title")?.textContent).toBe("dead-links");
    expect(app.querySelector(".routine-spark-legend")).not.toBeNull();

    expect(deadCard.querySelectorAll(".spark-day")).toHaveLength(14);
    expect(deadCard.querySelectorAll('.spark-day[data-outcome="escalated"]')).toHaveLength(1);
    expect(deadCard.querySelectorAll('.spark-day[data-outcome="closed"]')).toHaveLength(1);
    expect(deadCard.querySelectorAll('.spark-day[data-outcome="merged"]')).toHaveLength(1);
    expect(deadCard.querySelectorAll('.spark-day[data-outcome="no-op"]')).toHaveLength(1);
    expect(deadCard.querySelector(".spark-day")?.getAttribute("aria-label")).toContain("2026-11-");
    expect(deadCard.querySelector(".routine-spark-summary")?.textContent).toContain("merged");

    expect(deadCard.textContent).toContain("60% · 6/10");
    expect(deadCard.textContent).toContain("$1.50");
    expect(deadCard.textContent).toContain("0/1");
    expect(deadCard.querySelector("time")?.getAttribute("datetime")).toBe("2026-11-27T07:00:00Z");
    expect(deadCard.querySelector(".routine-breaker")?.textContent).toContain("ok");

    // Non-maintainer / disabled controls.
    expect(docCard.querySelector<HTMLButtonElement>('button[data-act="run-now"]')?.disabled).toBe(true);
    expect(docCard.querySelector<HTMLButtonElement>('button[data-act="resume"]')?.disabled).toBe(true);
    expect(docCard.textContent).toContain("—");

    // Filters by repository, state, mode, and search + Clear filters.
    const repoSelect = app.querySelector<HTMLSelectElement>('select[name="repository"]')!;
    repoSelect.value = "Smana/ai-native-ref";
    repoSelect.dispatchEvent(new Event("change"));
    expect(cards.filter((c) => !c.hidden)).toHaveLength(1);
    expect(cards.find((c) => !c.hidden)?.textContent).toContain("doc-drift");

    repoSelect.value = "";
    repoSelect.dispatchEvent(new Event("change"));
    const stateSelect = app.querySelector<HTMLSelectElement>('select[name="state"]')!;
    stateSelect.value = "active";
    stateSelect.dispatchEvent(new Event("change"));
    expect(cards.filter((c) => !c.hidden)).toHaveLength(1);
    expect(cards.find((c) => !c.hidden)?.textContent).toContain("dead-links");

    stateSelect.value = "";
    stateSelect.dispatchEvent(new Event("change"));
    const modeSelect = app.querySelector<HTMLSelectElement>('select[name="mode"]')!;
    modeSelect.value = "disabled";
    modeSelect.dispatchEvent(new Event("change"));
    expect(cards.filter((c) => !c.hidden)).toHaveLength(1);
    expect(cards.find((c) => !c.hidden)?.textContent).toContain("doc-drift");

    const searchInput = app.querySelector<HTMLInputElement>('input[name="search"]')!;
    searchInput.value = "no-such-routine";
    searchInput.dispatchEvent(new Event("input"));
    expect(cards.filter((c) => !c.hidden)).toHaveLength(0);
    app.querySelector<HTMLButtonElement>('button[data-act="clear-filters"]')!.click();
    expect(cards.filter((c) => !c.hidden)).toHaveLength(2);
  });

  it("gates Run now and Pause/Resume with inline confirmation, pause reason, declarative pause guard, and non-widening modes", async () => {
    const calls: RecordedCall[] = [];
    const routes: Record<string, Response | unknown> = {
      "/api/routines": { routines: [overview({ canWrite: true, paused: false })] },
      "/api/routines/Smana/cloud-native-ref/dead-links/runs": new Response(
        JSON.stringify({ task: "rt-1", room: "3kq7x2ma", repository: "Smana/cloud-native-ref", routine: "dead-links", mode: "pr", requestedBy: "github:Smana" }),
        { status: 201 },
      ),
      "/api/routines/Smana/cloud-native-ref/dead-links/pause": {
        repository: "Smana/cloud-native-ref",
        routine: "dead-links",
        paused: true,
        declarativePaused: false,
        pausedReason: "release freeze",
        pausedBy: "github:smana",
      },
      "/api/routines/Smana/cloud-native-ref/dead-links/resume": {
        repository: "Smana/cloud-native-ref",
        routine: "dead-links",
        paused: false,
        declarativePaused: false,
      },
    };

    const app = document.createElement("div");
    await mountRoutines(app, "/routines", fakeFetch(routes, calls));

    // Trigger Run now -> inline confirm -> POST /runs.
    app.querySelector<HTMLButtonElement>('button[data-act="run-now"]')!.click();
    expect(calls.some((c) => c.path.endsWith("/runs"))).toBe(false);
    app.querySelector<HTMLButtonElement>('button[data-act="confirm-run"]')!.click();
    await settle();
    const runCall = calls.find((c) => c.path.endsWith("/runs"));
    expect(runCall?.method).toBe("POST");
    expect(runCall?.credentials).toBe("same-origin");
    expect(runCall?.headers?.["Content-Type"]).toBe("application/json");
    const runBody = JSON.parse(runCall!.body!) as { idempotencyKey?: string };
    expect(runBody.idempotencyKey).toBeTruthy();
    expect(app.querySelector('a[href="/r/3kq7x2ma"]')).not.toBeNull();

    // Select propose mode -> surfaces mode_requires_phase2 cleanly.
    routes["/api/routines/Smana/cloud-native-ref/dead-links/runs"] = new Response(
      JSON.stringify({ error: "mode_requires_phase2" }),
      { status: 400 },
    );
    const modeSel = app.querySelector<HTMLSelectElement>(".routine-actions select.routine-mode-select")!;
    modeSel.value = "propose";
    app.querySelector<HTMLButtonElement>('button[data-act="run-now"]')!.click();
    app.querySelector<HTMLButtonElement>('button[data-act="confirm-run"]')!.click();
    await settle();
    expect(app.querySelector(".routine-notice")?.textContent).toMatch(/mode_requires_phase2/i);

    // Pause -> inline reason input -> sends reason JSON.
    routes["/api/routines"] = { routines: [overview({ canWrite: true, paused: true, pausedReason: "release freeze" })] };
    app.querySelector<HTMLButtonElement>('button[data-act="pause"]')!.click();
    const reasonInput = app.querySelector<HTMLInputElement>('input[name="pauseReason"]')!;
    reasonInput.value = "release freeze";
    app.querySelector<HTMLButtonElement>('button[data-act="confirm-pause"]')!.click();
    await settle();
    const pauseCall = calls.find((c) => c.path.endsWith("/pause"));
    expect(pauseCall?.body).toBe(JSON.stringify({ reason: "release freeze" }));
    expect(pauseCall?.headers?.["Content-Type"]).toBe("application/json");

    // Resume sends {} JSON object.
    routes["/api/routines"] = { routines: [overview({ canWrite: true, paused: false })] };
    app.querySelector<HTMLButtonElement>('button[data-act="resume"]')!.click();
    await settle();
    const resumeCall = calls.find((c) => c.path.endsWith("/resume"));
    expect(resumeCall?.body).toBe("{}");
    expect(resumeCall?.headers?.["Content-Type"]).toBe("application/json");

    // Declaratively paused routine disables Resume and prevents widening mode selection on propose.
    const declApp = document.createElement("div");
    await mountRoutines(
      declApp,
      "/routines",
      fakeFetch({
        "/api/routines": {
          routines: [overview({ canWrite: true, mode: "propose", paused: true, declarativePaused: true, pausedReason: "declarative" })],
        },
      }),
    );
    expect(declApp.querySelector<HTMLButtonElement>('button[data-act="resume"]')?.disabled).toBe(true);
    expect(declApp.textContent).toContain("Paused in config");
    const propSel = declApp.querySelector<HTMLSelectElement>("select.routine-mode-select")!;
    expect([...propSel.options].find((o) => o.value === "pr")?.disabled).toBe(true);
    expect([...propSel.options].find((o) => o.value === "draft")?.disabled).toBe(true);
  });

  it("renders Retry button on overview fetch error and reloads cleanly", async () => {
    let failedOnce = false;
    const get = (async (input: RequestInfo | URL) => {
      if (!failedOnce && String(input) === "/api/routines") {
        failedOnce = true;
        return new Response(JSON.stringify({ error: "routines_unavailable" }), { status: 503 });
      }
      return new Response(JSON.stringify({ routines: [overview()] }), { status: 200 });
    }) as typeof fetch;
    const app = document.createElement("div");
    await mountRoutines(app, "/routines", get);
    const retry = app.querySelector<HTMLButtonElement>('button[data-act="retry"]')!;
    expect(retry).not.toBeNull();
    retry.click();
    await settle();
    expect(app.querySelectorAll(".routine-card")).toHaveLength(1);
  });
});

describe("routine detail and security", () => {
  it("renders runs with Room/PR links, prompt SHA change badge, skips, and neutralizes XSS + non-https URLs", async () => {
    const xssDetail = detail({
      description: `<img src=x onerror="alert(1)">`,
      paused: true,
      pausedReason: `<script>alert(2)</script>`,
      defaultPrompt: `<img src=x onerror="alert(3)">`,
      promptEditURL: "javascript:alert(4)",
      runs: [
        {
          name: "rt-2",
          createdAt: "2026-11-26T07:00:00Z",
          phase: "Done",
          outcome: "accepted",
          reason: `<img src=x onerror="alert(5)">`,
          roomRef: "3kq7x2ma",
          prURL: "https://github.com/Smana/cloud-native-ref/pull/42",
          prNumber: 42,
          promptSHA: "22222222bbbbbbbb",
          requestedBy: "github:Smana",
          costUSD: 0.85,
        },
        {
          name: "rt-1",
          createdAt: "2026-11-25T07:00:00Z",
          phase: "Closed",
          outcome: "closed",
          roomRef: "9ab2cdef",
          prURL: "javascript:alert(6)",
          promptSHA: "11111111aaaaaaaa",
          requestedBy: "system:scheduler",
          costUSD: 0.35,
        },
      ],
    });

    const app = document.createElement("div");
    await mountRoutines(
      app,
      "/routines/Smana/cloud-native-ref/dead-links",
      fakeFetch({ "/api/routines/Smana/cloud-native-ref/dead-links": xssDetail }),
    );

    // Zero injected HTML elements (header.brand owns the 2 static brand mark <img>s).
    expect(app.querySelector("main")!.querySelectorAll("img, script")).toHaveLength(0);
    expect(app.querySelectorAll('img[src="x"]')).toHaveLength(0);
    expect(app.textContent).toContain(`<img src=x onerror="alert(1)">`);
    expect(app.textContent).toContain(`<script>alert(2)</script>`);

    // Non-https URLs dropped from anchors.
    const hrefs = [...app.querySelectorAll("a")].map((a) => a.getAttribute("href") ?? "");
    expect(hrefs.some((h) => h.startsWith("javascript:"))).toBe(false);
    expect(hrefs).toContain("/routines");
    expect(hrefs).toContain("/routines/Smana/cloud-native-ref/dead-links/edit");
    expect(hrefs).toContain("/r/3kq7x2ma");
    expect(hrefs).toContain("https://github.com/Smana/cloud-native-ref/pull/42");

    // Prompt SHA change marker between rt-2 and rt-1.
    const changed = app.querySelectorAll('[data-prompt-changed="true"]');
    expect(changed).toHaveLength(1);
    expect(app.textContent).toContain("prompt changed");

    // Skip diagnostics.
    expect(app.textContent).toContain("open_cap");
    expect(app.textContent).toContain("upkeep_budget");
    expect(app.textContent).toContain("probe_refused");
  });
});
