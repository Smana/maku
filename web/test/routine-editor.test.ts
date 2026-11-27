// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { validatePromptText, mountRoutineEditor } from "../src/routine-editor";
import type { RoutineCatalogResponse } from "../src/routine-catalog";
import { mountRoutines, type RoutineDetail } from "../src/routines";

interface RecordedCall {
  method: string;
  path: string;
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

function detailFixture(over: Partial<RoutineDetail> = {}): RoutineDetail {
  return {
    repository: "Smana/cloud-native-ref",
    routine: "dead-links",
    description: "Fix dead external links",
    mode: "propose",
    workflow: "quick",
    dataClass: "public",
    probeKind: "dead-links",
    cron: "0 3 * * 1,4",
    paused: false,
    maxOpenPRs: 1,
    maxCostUSD: 2.0,
    cooldown: "24h0m0s",
    egress: ["npm"],
    repoPrompt: true,
    uiWrite: false,
    definedIn: "gitops",
    openCount: 0,
    nextRunAt: "2026-11-30T03:00:00Z",
    sparkline: [],
    rollingMerged: 2,
    rollingClosed: 0,
    rollingNoOp: 1,
    mergeRate: 1,
    usdPerMerged: 0.9,
    promptEditURL: "https://github.com/Smana/cloud-native-ref/edit/main/.github/agent-factory/upkeep/dead-links.md",
    breakerState: "ok",
    canWrite: true,
    defaultPrompt: "## Goal\nFix dead links.\n\n## Verify\n- Run link check\n",
    breaker: {
      window: 10,
      minSamples: 5,
      minMergeRate: 0.25,
      maxConsecutiveClosed: 3,
      maxConsecutiveFailures: 3,
    },
    oracleClass: "docs-links",
    promptPath: ".github/agent-factory/upkeep/dead-links.md",
    promptExists: true,
    repoPromptText: "## Goal\nCustom repo prompt.\n\n## Verify\n- make check-links\n",
    repoEgress: ["golang", "npm"],
    upkeepDailyUSD: 20,
    spec: {
      name: "dead-links",
      description: "Fix dead external links",
      catalog: true,
      workflow: "quick",
      dataClass: "public",
      defaultPrompt: "## Goal\nFix dead links.\n\n## Verify\n- Run link check\n",
      probeKind: "dead-links",
      requires: ["oracleClass"],
      maxOpenPRs: 2,
      maxCostUSD: 4.0,
      cooldown: "12h0m0s",
      breaker: {
        window: 10,
        minSamples: 5,
        minMergeRate: 0.25,
        maxConsecutiveClosed: 3,
        maxConsecutiveFailures: 3,
      },
      consumerCount: 1,
      consumers: [],
    },
    runs: [],
    skips: [],
    ...over,
  };
}

function catalogFixture(over: Partial<RoutineCatalogResponse> = {}): RoutineCatalogResponse {
  return {
    uiWrite: false,
    upkeepDailyUSD: 20,
    repositories: ["Smana/cloud-native-ref", "Smana/empty-repo"],
    capabilities: [
      { repository: "Smana/cloud-native-ref", canWrite: true, egress: ["golang", "npm"], boundRoutines: ["dead-links"] },
      { repository: "Smana/empty-repo", canWrite: true, egress: ["npm"], boundRoutines: [] },
    ],
    routines: [
      {
        name: "dead-links",
        description: "Fix dead external links",
        catalog: true,
        workflow: "quick",
        dataClass: "public",
        defaultPrompt: "## Goal\nFix dead links.\n\n## Verify\n- Run link check\n",
        probeKind: "dead-links",
        requires: ["oracleClass"],
        maxOpenPRs: 2,
        maxCostUSD: 4.0,
        breaker: { window: 10, minSamples: 5, minMergeRate: 0.25, maxConsecutiveClosed: 3, maxConsecutiveFailures: 3 },
        egress: ["npm"],
        consumerCount: 1,
        consumers: [],
      },
      {
        name: "renovate-red",
        description: "Fix failing Renovate PRs",
        catalog: true,
        workflow: "standard",
        dataClass: "internal",
        defaultPrompt: "## Goal\nFix red Renovate PR.\n\n## Verify\n- Tests pass\n",
        probeKind: "renovate-red",
        probeAuthor: "renovate[bot]",
        maxOpenPRs: 1,
        maxCostUSD: 2.0,
        breaker: { window: 10, minSamples: 5, minMergeRate: 0.25, maxConsecutiveClosed: 3, maxConsecutiveFailures: 3 },
        consumerCount: 0,
        consumers: [],
      },
    ],
    ...over,
  };
}

const settle = () => new Promise((r) => setTimeout(r, 0));

describe("validatePromptText", () => {
  it("accepts valid prompt with optional # title and ## Verify bullets (- or *)", () => {
    const res = validatePromptText("# Title\n\n## Goal\nDo work\n\n## Verify\n* Run test\n");
    expect(res.valid).toBe(true);
    expect(res.sections).toBe(2);
    expect(res.hasVerify).toBe(true);
    expect(res.verifyItems).toBe(1);
  });

  it("rejects missing ## Verify, numbered-only Verify, >5 sections, >4096 bytes, text before first ##, CR and Cf chars", () => {
    expect(validatePromptText("## Goal\nNo verify\n").valid).toBe(false);
    expect(validatePromptText("## Verify\n1. Numbered only\n").valid).toBe(false);
    expect(validatePromptText("## Verify\n- \n").valid).toBe(false);
    expect(validatePromptText("Stray text\n## Verify\n- ok\n").valid).toBe(false);
    expect(validatePromptText("## A\n## B\n## C\n## D\n## E\n## Verify\n- ok\n").valid).toBe(false);
    expect(validatePromptText("## Verify\r\n- ok\n").valid).toBe(false);
    expect(validatePromptText("## Verify\n- ok\u200B\n").valid).toBe(false);

    const big = "## Verify\n- " + "é".repeat(2100); // > 4096 UTF-8 bytes
    const bigRes = validatePromptText(big);
    expect(bigRes.valid).toBe(false);
    expect(bigRes.bytes).toBeGreaterThan(4096);
  });
});

describe("routine editor (/routines/<owner>/<name>/<routine>/edit)", () => {
  it("updates cron and next-3-runs preview when preset changes", async () => {
    const app = document.createElement("div");
    await mountRoutines(
      app,
      "/routines/Smana/cloud-native-ref/dead-links/edit",
      fakeFetch({
        "/api/routines/Smana/cloud-native-ref/dead-links": detailFixture(),
        "/api/routines/catalog": catalogFixture(),
      }),
    );

    const preset = app.querySelector<HTMLSelectElement>('select[name="cronPreset"]')!;
    const cronInput = app.querySelector<HTMLInputElement>('input[name="cron"]')!;
    expect(preset.value).toBe("mon_thu");
    expect(cronInput.value).toBe("0 3 * * 1,4");
    expect(app.querySelectorAll(".cron-next-runs li")).toHaveLength(3);

    preset.value = "weekly_mon";
    preset.dispatchEvent(new Event("change"));
    expect(cronInput.value).toBe("0 4 * * 1");
    expect(app.querySelector(".cron-next-runs")?.textContent).toContain("UTC");

    cronInput.value = "bad cron";
    cronInput.dispatchEvent(new Event("input"));
    expect(app.querySelector(".cron-preview-error")?.textContent).toBeTruthy();
  });

  it("handles unbound ?from=catalog (404 on detail) and omits spec-owned fill-once params and blank cooldown", async () => {
    const calls: RecordedCall[] = [];
    const app = document.createElement("div");
    await mountRoutines(
      app,
      "/routines/Smana/empty-repo/renovate-red/edit",
      fakeFetch(
        {
          "/api/routines/catalog": catalogFixture({ uiWrite: true }),
          "/api/routines/Smana/empty-repo/preview": {
            valid: true,
            yaml: "- name: renovate-red\n",
            manifest: "kind: Repository\n",
            manifestPath: "repository-empty-repo.yaml",
          },
        },
        calls,
      ),
      undefined,
      "?from=catalog",
    );

    // spec sets probeAuthor: "renovate[bot]" -> read-only, no editable input, omitted from POST /preview.
    expect(app.querySelector('input[name="probeAuthor"]')).toBeNull();
    expect(app.textContent).toContain("renovate[bot]");
    // spec dataClass is "internal" -> "public" option disabled.
    const dcSelect = app.querySelector<HTMLSelectElement>('select[name="dataClass"]')!;
    const pubOpt = [...dcSelect.options].find((o) => o.value === "public")!;
    expect(pubOpt.disabled).toBe(true);

    // GitHub deep-link points to /new/main?filename=... when unbound.
    const ghLink = app.querySelector<HTMLAnchorElement>(".routine-github-prompt-link")!;
    expect(ghLink.getAttribute("href")).toContain("/Smana/empty-repo/new/main?filename=");
    expect(ghLink.textContent).toContain("Create on GitHub");

    app.querySelector<HTMLButtonElement>('button[data-act="preview"]')!.click();
    await settle();

    const pvCall = calls.find((c) => c.path === "/api/routines/Smana/empty-repo/preview")!;
    const body = JSON.parse(pvCall.body!) as { binding: Record<string, unknown> };
    expect(body.binding.probeAuthor).toBeUndefined();
    expect(body.binding.cooldown).toBeUndefined();
    expect(body.binding.name).toBe("renovate-red");
    expect(app.querySelector(".routine-yaml-preview")?.textContent).toContain("kind: Repository");
  });

  it("posts preview with promptText, renders Copy/Download on valid or errors on invalid, and validates prompt live", async () => {
    const calls: RecordedCall[] = [];
    const routes: Record<string, Response | unknown> = {
      "/api/routines/Smana/cloud-native-ref/dead-links": detailFixture({ promptExists: false, repoPromptText: "" }),
      "/api/routines/catalog": catalogFixture(),
      "/api/routines/Smana/cloud-native-ref/preview": {
        valid: false,
        reason: "over_routine",
        errors: ["maxOpenPRs 5 is over Routine's 2"],
        promptValid: false,
        promptError: "upkeep: routine prompt invalid: no ## Verify section",
      },
    };
    const app = document.createElement("div");
    await mountRoutineEditor(app, "Smana", "cloud-native-ref", "dead-links", fakeFetch(routes, calls));

    const ghLink = app.querySelector<HTMLAnchorElement>(".routine-github-prompt-link")!;
    expect(ghLink.getAttribute("href")).toContain("/Smana/cloud-native-ref/new/main?filename=");

    const textarea = app.querySelector<HTMLTextAreaElement>('textarea[name="repoPromptText"]')!;
    textarea.value = "## Goal\nMissing verify";
    textarea.dispatchEvent(new Event("input"));
    expect(app.querySelector(".routine-prompt-status")?.textContent).toContain("## Verify");

    app.querySelector<HTMLButtonElement>('button[data-act="preview"]')!.click();
    await settle();
    expect(app.querySelector(".preview-errors")?.textContent).toContain("maxOpenPRs 5 is over Routine's 2");

    // Valid preview renders Copy + Download + manifest.
    routes["/api/routines/Smana/cloud-native-ref/preview"] = {
      valid: true,
      promptValid: true,
      yaml: "- name: dead-links\n",
      manifest: "apiVersion: factory.maku.ogenki.io/v1alpha1\nkind: Repository\n",
      manifestPath: "repository-cloud-native-ref.yaml",
    };
    textarea.value = "## Goal\nValid\n\n## Verify\n- check links\n";
    textarea.dispatchEvent(new Event("input"));
    app.querySelector<HTMLButtonElement>('button[data-act="preview"]')!.click();
    await settle();
    expect(app.querySelector(".file-actions .copy")?.textContent).toContain("Copy repository-cloud-native-ref.yaml");
    expect(app.querySelector(".file-actions .download")).not.toBeNull();
    expect(app.querySelector(".routine-yaml-preview")?.textContent).toContain("kind: Repository");
  });

  it("hides Save to cluster when uiWrite is false and calls PUT when uiWrite is true for writable non-gitops routines", async () => {
    const calls: RecordedCall[] = [];
    const hiddenApp = document.createElement("div");
    await mountRoutineEditor(
      hiddenApp,
      "Smana",
      "cloud-native-ref",
      "dead-links",
      fakeFetch({
        "/api/routines/Smana/cloud-native-ref/dead-links": detailFixture({ uiWrite: false }),
        "/api/routines/catalog": catalogFixture({ uiWrite: false }),
      }),
    );
    expect(hiddenApp.querySelector('button[data-act="save"]')).toBeNull();

    // When uiWrite is true but definedIn is "gitops", Save is shown but disabled.
    const gitopsApp = document.createElement("div");
    await mountRoutineEditor(
      gitopsApp,
      "Smana",
      "cloud-native-ref",
      "dead-links",
      fakeFetch({
        "/api/routines/Smana/cloud-native-ref/dead-links": detailFixture({ uiWrite: true, definedIn: "gitops" }),
        "/api/routines/catalog": catalogFixture({ uiWrite: true }),
      }),
    );
    const gitopsSave = gitopsApp.querySelector<HTMLButtonElement>('button[data-act="save"]')!;
    expect(gitopsSave).not.toBeNull();
    expect(gitopsSave.disabled).toBe(true);

    // When uiWrite is true and definedIn is "ui" (or unbound), Save calls PUT and surfaces 409 defined_twice cleanly.
    const putRoutes: Record<string, Response | unknown> = {
      "/api/routines/Smana/cloud-native-ref/dead-links": detailFixture({ uiWrite: true, definedIn: "ui" }),
      "/api/routines/catalog": catalogFixture({ uiWrite: true }),
    };
    const saveApp = document.createElement("div");
    const fetchMock = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      calls.push({ method, path, body: init?.body ? String(init.body) : undefined });
      if (method === "PUT") {
        return putRoutes.putResp instanceof Response
          ? putRoutes.putResp.clone()
          : new Response(JSON.stringify(putRoutes.putResp ?? { repository: "Smana/cloud-native-ref", routine: "dead-links", binding: {} }), { status: 200 });
      }
      return fakeFetch(putRoutes)(input, init);
    }) as typeof fetch;

    await mountRoutineEditor(saveApp, "Smana", "cloud-native-ref", "dead-links", fetchMock);
    const saveBtn = saveApp.querySelector<HTMLButtonElement>('button[data-act="save"]')!;
    expect(saveBtn.disabled).toBe(false);
    saveBtn.click();
    await settle();

    const putCall = calls.find((c) => c.method === "PUT");
    expect(putCall?.path).toBe("/api/routines/Smana/cloud-native-ref/dead-links");
    expect(saveApp.querySelector(".routine-notice")?.textContent).toContain("Saved");

    putRoutes.putResp = new Response(JSON.stringify({ error: "defined_twice" }), { status: 409 });
    saveBtn.click();
    await settle();
    expect(saveApp.querySelector(".routine-notice")?.textContent).toContain("Repository.spec.routines[]");

    putRoutes.putResp = new Response(JSON.stringify({ error: "over_routine" }), { status: 400 });
    saveBtn.click();
    await settle();
    expect(saveApp.querySelector(".routine-notice")?.textContent).toContain("ceilings");
  });

  it("neutralizes XSS payloads in editor fields via textContent only", async () => {
    const app = document.createElement("div");
    await mountRoutineEditor(
      app,
      "Smana",
      "cloud-native-ref",
      "dead-links",
      fakeFetch({
        "/api/routines/Smana/cloud-native-ref/dead-links": detailFixture({
          description: '<img src=x onerror="alert(1)">',
          defaultPrompt: '<script>alert(2)</script>',
          promptEditURL: "javascript:alert(3)",
        }),
        "/api/routines/catalog": catalogFixture(),
      }),
    );
    expect(app.querySelector("main")!.querySelectorAll("img, script")).toHaveLength(0);
    expect(app.textContent).toContain('<img src=x onerror="alert(1)">');
    const hrefs = [...app.querySelectorAll("a")].map((a) => a.getAttribute("href") ?? "");
    expect(hrefs.some((h) => h.startsWith("javascript:"))).toBe(false);
  });

  it("uses 16 as maxFindings ceiling when spec omits maxFindings and allows breaker window/minSamples up to 100", async () => {
    const app = document.createElement("div");
    await mountRoutineEditor(
      app,
      "Smana",
      "cloud-native-ref",
      "dead-links",
      fakeFetch({
        "/api/routines/Smana/cloud-native-ref/dead-links": detailFixture({ maxFindings: 1 }),
        "/api/routines/catalog": catalogFixture(),
      }),
    );

    const mfInput = app.querySelector<HTMLInputElement>('input[name="maxFindings"]')!;
    expect(mfInput.max).toBe("16");
    expect(mfInput.parentElement?.textContent).toContain("ceiling: 16");
    expect(mfInput.parentElement?.textContent).not.toContain("ceiling: 1)");

    const brWin = app.querySelector<HTMLInputElement>('input[name="breakerWindow"]')!;
    const brMin = app.querySelector<HTMLInputElement>('input[name="breakerMinSamples"]')!;
    expect(brWin.max).toBe("100");
    expect(brMin.max).toBe("100");

    // When spec explicitly caps maxFindings, that ceiling is shown and enforced on max.
    const cappedDetail = detailFixture({ maxFindings: 1 });
    cappedDetail.spec!.maxFindings = 5;
    const cappedApp = document.createElement("div");
    await mountRoutineEditor(
      cappedApp,
      "Smana",
      "cloud-native-ref",
      "dead-links",
      fakeFetch({
        "/api/routines/Smana/cloud-native-ref/dead-links": cappedDetail,
        "/api/routines/catalog": catalogFixture(),
      }),
    );
    const cappedMf = cappedApp.querySelector<HTMLInputElement>('input[name="maxFindings"]')!;
    expect(cappedMf.max).toBe("5");
    expect(cappedMf.parentElement?.textContent).toContain("ceiling: 5");
  });

  it("clears stale preview on any edit, gates invalid cron before API calls, and warns when prompt edits are not saved to cluster", async () => {
    const calls: RecordedCall[] = [];
    const app = document.createElement("div");
    const fetchMock = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      calls.push({ method, path, body: init?.body ? String(init.body) : undefined });
      if (method === "POST" && path.endsWith("/preview")) {
        return new Response(
          JSON.stringify({ valid: true, manifest: "kind: Repository\n", manifestPath: "repository.yaml" }),
          { status: 200 },
        );
      }
      if (method === "PUT") {
        return new Response(JSON.stringify({ repository: "Smana/cloud-native-ref", routine: "dead-links", binding: {} }), { status: 200 });
      }
      return fakeFetch({
        "/api/routines/Smana/cloud-native-ref/dead-links": detailFixture({ uiWrite: true, definedIn: "ui", promptExists: false, repoPromptText: "" }),
        "/api/routines/catalog": catalogFixture({ uiWrite: true }),
      })(input, init);
    }) as typeof fetch;

    await mountRoutineEditor(app, "Smana", "cloud-native-ref", "dead-links", fetchMock);

    // 1. Preview renders YAML, then editing maxCostUSD clears stale YAML immediately.
    app.querySelector<HTMLButtonElement>('button[data-act="preview"]')!.click();
    await settle();
    expect(app.querySelector(".routine-yaml-preview")?.textContent).toContain("kind: Repository");
    const costInput = app.querySelector<HTMLInputElement>('input[name="maxCostUSD"]')!;
    costInput.value = "1.50";
    costInput.dispatchEvent(new Event("input", { bubbles: true }));
    expect(app.querySelector(".routine-yaml-preview")).toBeNull();

    // 2. Invalid cron blocks Preview and Save without calling backend.
    const cronInput = app.querySelector<HTMLInputElement>('input[name="cron"]')!;
    cronInput.value = "not valid";
    cronInput.dispatchEvent(new Event("input", { bubbles: true }));
    const callsBefore = calls.length;
    app.querySelector<HTMLButtonElement>('button[data-act="preview"]')!.click();
    app.querySelector<HTMLButtonElement>('button[data-act="save"]')!.click();
    await settle();
    expect(calls.length).toBe(callsBefore);
    expect(cronInput.getAttribute("aria-invalid")).toBe("true");

    // 3. Valid cron + edited prompt -> GitHub Create URL carries value & Save warns prompt isn't written to GitHub.
    cronInput.value = "0 3 * * 1,4";
    cronInput.dispatchEvent(new Event("input", { bubbles: true }));
    const textarea = app.querySelector<HTMLTextAreaElement>('textarea[name="repoPromptText"]')!;
    textarea.value = "## Goal\nTighten check\n\n## Verify\n- run tests\n";
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
    expect(app.querySelector<HTMLAnchorElement>(".routine-github-prompt-link")?.href).toContain("value=");
    expect(app.querySelector('button[data-act="copy-prompt"]')).not.toBeNull();

    app.querySelector<HTMLButtonElement>('button[data-act="save"]')!.click();
    await settle();
    expect(app.querySelector(".routine-notice")?.textContent).toContain("not written to GitHub");
  });
});
