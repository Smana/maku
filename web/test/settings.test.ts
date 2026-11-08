// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { evidenceLine, explain, isSettings, ladderLine, mountSettings, type Evidence, type RepositoryPage } from "../src/settings";
import { fileYAML, setLeaf, toYAML } from "../src/policy-editor";

const ev = (over: Partial<Evidence> = {}): Evidence => ({
  shadow: { mergedUnchanged: 20, changed: 1, reverted: 0, autoMerged: 0, drill: 1, reviewers: [{ model: "glm-5.3", mergedUnchanged: 20, changed: 1 }] },
  clean: 20, bad: 0, immature: 2, pending: 1, bound: 0.139108, budget: 0.1, widen: false,
  ladder: [{ merges: 29, maxBad: 0, reached: false }, { merges: 46, maxBad: 1, reached: false }, { merges: 61, maxBad: 2, reached: false }],
  drill: "passed", minimum: 20, eligible: true, ...over,
});

const page = (over: Partial<RepositoryPage> = {}): RepositoryPage => ({
  report: { repository: "Smana/cloud-native-ref", minimum: 20, readAt: "2026-11-08T00:00:00Z", factory: "aws-0", defaultBranch: "main",
    harnessProfiles: ["docs-standard"], guardrails: { maxPatchFiles: 20 }, file: "factory: aws-0\nclasses:\n  docs-links:\n    mode: shadow\n",
    policy: { factory: "aws-0", classes: { "docs-links": { mode: "shadow", author: "agents", harness: "docs-standard",
      conditions: { all: [{ allowedFiles: ["^docs/.+\\.md$"] }, { maxPatchFiles: 10 }] } } } }, classes: [
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
    for (const p of ["/settings", "/settings/merge-policies", "/settings/merge-policies/Smana/cloud-native-ref", "/settings/evidence",
      "/settings/merge-policies/Smana/cloud-native-ref/decisions/12"]) {
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
    expect(app.querySelectorAll("button.primary:not([disabled])")).toHaveLength(0);
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

describe("the policy editor", () => {
  it("writes the file as YAML: factory first, patterns double-quoted, scalar lists in flow", () => {
    expect(fileYAML({ factory: "aws-0", classes: { "docs-links": { mode: "off", author: "agents", harness: "p",
      conditions: { all: [{ allowedFiles: ["^docs/.+\\.md$"] }, { maxPatchFiles: 3 }], any: [[{ modifyOnly: true }]] } } } }))
      .toBe('factory: "aws-0"\nclasses:\n  docs-links:\n    mode: "off"\n    author: "agents"\n    harness: "p"\n    conditions:\n' +
        '      all:\n        - allowedFiles: ["^docs/.+\\\\.md$"]\n        - maxPatchFiles: 3\n      any:\n        - - modifyOnly: true\n');
    expect(toYAML({ a: {}, b: undefined, c: [] })).toBe("c: []");
  });
  it("sets a leaf in place, and an empty value removes it", () => {
    const all = [{ allowedFiles: ["^a$"] }, { maxPatchFiles: 3 }];
    expect(setLeaf(all, "maxPatchFiles", 5)).toEqual([{ allowedFiles: ["^a$"] }, { maxPatchFiles: 5 }]);
    expect(setLeaf(all, "allowedFiles", [])).toEqual([{ maxPatchFiles: 3 }]);
    expect(setLeaf(all, "modifyOnly", true)).toEqual([...all, { modifyOnly: true }]);
  });
  it("edits a class in the form, rewrites the expert view, and shows the factory's check", async () => {
    const calls: string[] = [];
    const preview = { valid: true, structural: [{ class: "docs-links", kind: "tightening" }],
      simulation: { enabled: true, counts: { unchanged: 15, newly_blocked: 1 }, rows: [{ name: "01-arms", class: "docs-links",
        central: { verdict: "arm" }, repository: { verdict: "human", reason: "patch_too_large" }, change: "newly_blocked", conforms: true }] },
      strictness: [{ class: "docs-links", refused: 3, refusedMergedUnchanged: 2 }],
      proposal: { path: ".github/agent-factory.yaml", file: "factory: aws-0\n", editURL: "https://github.com/Smana/cloud-native-ref/edit/main/.github/agent-factory.yaml" } };
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref",
      fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page(), "/api/policy/repositories/Smana/cloud-native-ref/preview": preview }, calls));
    const yaml = app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement;
    expect(yaml.value).toBe("factory: aws-0\nclasses:\n  docs-links:\n    mode: shadow\n"); // the file as it is, comments and all
    const cap = [...app.querySelectorAll(".editor-field")].find((l) => l.textContent?.startsWith("maxPatchFiles"))!.querySelector("input")!;
    cap.value = "5";
    cap.dispatchEvent(new Event("input"));
    expect(yaml.value).toContain("        - maxPatchFiles: 5\n");
    expect(yaml.value).toContain('factory: "aws-0"');
    const check = [...app.querySelectorAll("button")].find((b) => b.textContent === "Check, compare and simulate")!;
    check.click();
    await settle();
    expect(calls.at(-1)).toMatch(/^POST \/api\/policy\/repositories\/Smana\/cloud-native-ref\/preview /);
    expect(app.textContent).toContain("docs-links: tightening");
    expect(app.textContent).toContain("newly blocked: 1");
    expect(app.textContent).toContain("3 refused by a class row, 2 of them then merged unchanged");
    expect(app.querySelector(".editor-preview pre.proposal-file")?.textContent).toBe("factory: aws-0\n");
  });
  it("starts a new class from a template, on a central profile", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    const pick = app.querySelector('select[aria-label="class to edit"]') as HTMLSelectElement;
    pick.value = "template:deps-patch";
    pick.dispatchEvent(new Event("change"));
    const mode = app.querySelector(".editor-form select") as HTMLSelectElement;
    mode.value = "shadow";
    mode.dispatchEvent(new Event("change"));
    const yaml = (app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement).value;
    expect(yaml).toContain("  deps-patch:\n");
    expect(yaml).toContain('harness: "docs-standard"');
    expect(yaml).toContain('logins: ["renovate[bot]"]');
  });
  it("lets a reader see the editor but not check: the broker would refuse it", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref",
      fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page({ you: { login: "dev", maintainer: false, operator: false } }) }));
    const check = [...app.querySelectorAll("button")].find((b) => b.textContent === "Check, compare and simulate")!;
    expect(check.disabled).toBe(true);
  });
});

describe("the decision timeline", () => {
  it("renders each decision's rows, revisions and links", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref/decisions/24", fakeFetch({
      "/api/policy/repositories/Smana/cloud-native-ref/decisions/24": { repository: "Smana/cloud-native-ref", pullRequest: 24, decisions: [{
        at: "2026-10-01T00:00:00Z", head: "a24", class: "docs-links", verdict: "human", reason: "patch_too_large",
        steps: [{ row: "fixed_base", status: "passed" }, { row: "class", status: "refused", detail: "patch_too_large" }, { row: "grant", status: "reserved" }],
        revisions: [{ row: "repository policy", status: "recorded", detail: "sha256 0123456789ab, dual=agree" }],
        commentURL: "https://github.com/Smana/cloud-native-ref/pull/24#issuecomment-1", policyURL: "https://github.com/Smana/cloud-native-ref/commits/HEAD/.github/agent-factory.yaml" }] } }));
    expect(app.textContent).toContain("docs-links · human:patch_too_large");
    expect(app.textContent).toContain("refused");
    expect(app.textContent).toContain("sha256 0123456789ab");
    expect(app.querySelector('a[href="https://github.com/Smana/cloud-native-ref/pull/24#issuecomment-1"]')).not.toBeNull();
  });
  it("says so when the pull request has no decision", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref/decisions/9", fakeFetch({
      "/api/policy/repositories/Smana/cloud-native-ref/decisions/9": { repository: "Smana/cloud-native-ref", pullRequest: 9, decisions: [] } }));
    expect(app.textContent).toContain("recorded no decision");
  });
});
