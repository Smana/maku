// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { evidenceLine, explain, isSettings, ladderLine, mountSettings, type Evidence, type RepositoryPage } from "../src/settings";

const ev = (over: Partial<Evidence> = {}): Evidence => ({
  shadow: { mergedUnchanged: 20, changed: 1, reverted: 0, autoMerged: 0, drill: 1, reviewers: [{ model: "glm-5.3", mergedUnchanged: 20, changed: 1 }] },
  clean: 20, bad: 0, immature: 2, pending: 1, bound: 0.139108, budget: 0.1, widen: false,
  ladder: [{ merges: 29, maxBad: 0, reached: false }, { merges: 46, maxBad: 1, reached: false }, { merges: 61, maxBad: 2, reached: false }],
  drill: "passed", minimum: 20, eligible: true, ...over,
});

const page = (over: Partial<RepositoryPage> = {}): RepositoryPage => ({
  report: { repository: "Smana/cloud-native-ref", minimum: 20, readAt: "2026-11-08T00:00:00Z", classes: [
    { class: "docs-links", source: "repository", mode: "shadow", evidence: ev(),
      proposal: { path: ".github/agent-factory.yaml", line: 6, file: "factory: aws-0\n", editURL: "https://github.com/Smana/cloud-native-ref/edit/main/.github/agent-factory.yaml" } },
    { class: "revert", source: "repository", mode: "live", evidence: ev({ eligible: false, clean: 3, blockers: ["below_minimum"] }), unproposed: "not_shadow" },
  ] },
  promotions: [], exceptions: [], you: { login: "Smana", maintainer: true, operator: false }, ...over,
});

// fakeFetch answers by path and records each call.
function fakeFetch(routes: Record<string, unknown>, calls: string[] = []): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    calls.push(`${init?.method ?? "GET"} ${path}${init?.body ? " " + String(init.body) : ""}`);
    if (!(path in routes)) return new Response(JSON.stringify({ error: "not_found" }), { status: 404 });
    const v = routes[path];
    if (v instanceof Response) return v;
    return new Response(JSON.stringify(v), { status: 200 });
  }) as typeof fetch;
}

const settle = () => new Promise((r) => setTimeout(r, 0));

describe("settings routes", () => {
  it("answers the four settings paths and nothing else", () => {
    for (const p of ["/settings", "/settings/merge-policies", "/settings/merge-policies/Smana/cloud-native-ref", "/settings/evidence"]) {
      expect(isSettings(p)).toBe(true);
    }
    for (const p of ["/", "/r/abcdefgh", "/settings/approval-memory", "/settings/merge-policies/Smana", "/settings/x/y/z"]) {
      expect(isSettings(p)).toBe(false);
    }
  });
});

describe("evidence", () => {
  it("writes MP7's numbers in one line and the widening ladder", () => {
    expect(evidenceLine(ev())).toBe("20/20 clean · 0 bad · 2 immature · 1 pending · bound 0.139 · drill passed");
    expect(ladderLine(ev({ ladder: [{ merges: 29, maxBad: 0, reached: true }, { merges: 46, maxBad: 1, reached: false }, { merges: 61, maxBad: 2, reached: false }] })))
      .toBe("29 ✓ / 46 / 61");
  });
  it("explains a refusal by its code, and names an unknown one", () => {
    expect(explain("not_eligible")).toMatch(/minimum/);
    expect(explain("brand_new")).toBe("Refused: brand_new");
  });
});

describe("the repository page", () => {
  it("offers a maintainer the promotion of an eligible shadow class, and shows the proposal it gets", async () => {
    const calls: string[] = [];
    const proposal = { path: ".github/agent-factory.yaml", line: 6, file: "factory: aws-0\nclasses: {}\n", editURL: "https://github.com/Smana/cloud-native-ref/edit/main/.github/agent-factory.yaml" };
    const get = fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page(), "/api/policy/promotions": { promotion: { id: "aaaaaaaa" }, proposal } }, calls);
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", get);
    expect(app.textContent).toContain("20/20 clean");
    expect(app.textContent).toContain("glm-5.3 20/21");
    expect(app.textContent).toContain("not shadow");
    const buttons = [...app.querySelectorAll("button")].filter((b) => b.textContent === "Promote to live");
    expect(buttons).toHaveLength(1);
    buttons[0].click();
    await settle();
    expect(calls).toContain(`POST /api/policy/promotions {"repository":"Smana/cloud-native-ref","class":"docs-links"}`);
    const pre = app.querySelector("pre.proposal-file");
    expect(pre?.textContent).toBe(proposal.file);
    const edit = [...app.querySelectorAll("a")].find((a) => a.textContent === "edit the file on GitHub");
    expect(edit?.getAttribute("href")).toBe(proposal.editURL);
  });

  it("offers a reader no button: the broker would refuse it", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref",
      fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page({ you: { login: "dev", maintainer: false, operator: false } }) }));
    expect(app.querySelectorAll("button.primary")).toHaveLength(0);
  });

  it("offers a maintainer an exception below the minimum, and an operator its decision", async () => {
    const short = page();
    short.report.classes[0].evidence = ev({ clean: 12, eligible: false, blockers: ["below_minimum"] });
    short.exceptions = [{ id: "cccccccc", repository: "Smana/cloud-native-ref", class: "docs-links", minimum: 10,
      justification: "a narrow class: ten clean decisions bound it", requestedBy: "Smana", requestedAt: "2026-11-08T00:00:00Z", state: "requested" }];
    short.you = { login: "ops", maintainer: true, operator: true };
    const calls: string[] = [];
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref",
      fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": short, "/api/policy/exceptions/cccccccc": {} }, calls));
    expect(app.textContent).toContain("below_minimum");
    expect([...app.querySelectorAll("button")].some((b) => b.textContent === "Ask an operator for an exception")).toBe(true);
    const approve = [...app.querySelectorAll("button")].find((b) => b.textContent === "Approve")!;
    approve.click();
    await settle();
    expect(calls).toContain(`POST /api/policy/exceptions/cccccccc {"decision":"approved"}`);
  });

  it("says why a repository cannot be read, not a blank page", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/secret", fakeFetch({}));
    expect(app.textContent).toContain("Not found, or not readable");
  });
});

describe("the index and evidence pages", () => {
  it("lists the repositories the caller reads, and every class's evidence", async () => {
    const get = fakeFetch({ "/api/policy/repositories": { repositories: ["Smana/cloud-native-ref"] }, "/api/policy/repositories/Smana/cloud-native-ref": page() });
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", get);
    expect(app.querySelector('a[href="/settings/merge-policies/Smana/cloud-native-ref"]')).not.toBeNull();
    await mountSettings(app, "/settings/evidence", get);
    expect(app.textContent).toContain("docs-links");
    expect(app.textContent).toContain("eligible");
    expect(app.textContent).toContain("29 / 46 / 61");
  });
});
