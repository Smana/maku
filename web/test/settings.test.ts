// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { evidenceAge, blockerActions, hardeningLine, explain, isSettings, LADDER_LEGEND, modeCounts, mountSettings, poll, settingsFrame, trackRecord, type Evidence, type Report, type RepositoryPage } from "../src/settings";
import { READING } from "../src/class-card";
import { type ClassPolicy, classForm, fileYAML, type Preview, setLeaf, templates, toYAML } from "../src/policy-editor";
import { parsePolicy } from "../src/policy-yaml";

const ev = (over: Partial<Evidence> = {}): Evidence => ({
  shadow: { mergedUnchanged: 20, changed: 1, reverted: 0, autoMerged: 0, drill: 1, reviewers: [{ model: "glm-5.3", mergedUnchanged: 20, changed: 1 }] },
  clean: 20, bad: 0, immature: 2, pending: 1, bound: 0.139108, budget: 0.1, widen: false,
  ladder: [{ merges: 29, maxBad: 0, reached: false }, { merges: 46, maxBad: 1, reached: false }, { merges: 61, maxBad: 2, reached: false }],
  drill: "passed", minimum: 20, eligible: true, ...over,
});

const page = (over: Partial<RepositoryPage> = {}): RepositoryPage => ({
  report: { repository: "Smana/cloud-native-ref", minimum: 20, readAt: "2026-11-08T00:00:00Z", evidenceAt: "2026-11-08T00:00:00Z", factory: "aws-0", defaultBranch: "main",
    harnessProfiles: ["docs-standard"], guardrails: { maxPatchFiles: 20 }, mergePolicy: "cloud-native-ref", file: "factory: aws-0\nclasses:\n  docs-links:\n    mode: shadow\n",
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
  it("writes MP7's numbers as a track record in words, and blockers as actions", () => {
    expect(trackRecord(ev()).textContent).toBe("20 of 20 clean merges · 0 bad · 2 awaiting outcome · 1 awaiting a verdict · worst-case bad rate ≤ 14 % · rollback drill passed");
    expect(blockerActions(ev({ clean: 19, bad: 2, blockers: ["below_minimum", "bad_outcome", "no_drill"] })))
      .toEqual(["needs 1 more clean merge", "2 bad merges to explain", "run a rollback drill (factory/drill)"]);
    expect(blockerActions(ev({ blockers: [] }))).toEqual([]);
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
    expect(app.textContent).toContain("20 of 20 clean merges");
    expect(app.textContent).toContain("glm-5.3 20/21");
    expect(app.textContent).toContain("Live \u00b7 3 clean decisions recorded.");
    expect(app.textContent).not.toContain("not shadow");
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

  it("links every settings page from its nav, approval memory included", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref",
      fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    const nav = [...app.querySelectorAll("nav.settings-nav a")].map((a) => a.getAttribute("href"));
    expect(nav).toEqual(["/", "/settings", "/settings/merge-policies", "/settings/evidence", "/settings/approval-memory"]);
  });

  it("marks the page's section as the current tab, one tab at a time", async () => {
    const current = async (path: string) => {
      const app = document.createElement("div");
      await mountSettings(app, path, fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page(), "/api/policy/repositories": { repositories: [] } }));
      return [...app.querySelectorAll('nav.settings-nav a[aria-current="page"]')].map((a) => a.textContent);
    };
    expect(await current("/settings")).toEqual(["Settings"]);
    expect(await current("/settings/merge-policies")).toEqual(["Merge policies"]);
    expect(await current("/settings/merge-policies/Smana/cloud-native-ref")).toEqual(["Merge policies"]);
  });

  it("frames approval memory with the same header and tabs, its own tab current", () => {
    const app = document.createElement("div");
    const main = settingsFrame(app, "Approval memory", "/settings/approval-memory");
    expect(app.querySelector("header.page-header h1")?.textContent).toBe("Approval memory");
    expect(app.querySelector("nav.settings-nav")?.getAttribute("aria-label")).toBe("Sections");
    expect(app.querySelector('nav.settings-nav a[aria-current="page"]')?.getAttribute("href")).toBe("/settings/approval-memory");
    expect(main.parentElement).toBe(app);
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
    expect(app.textContent).toContain("Blocks promotion: needs 8 more clean merges.");
    const card = app.querySelector("#class-docs-links")!;
    expect([...card.querySelectorAll("button")].some((b) => b.textContent === "Ask an operator for an exception")).toBe(true);
    const approve = [...card.querySelectorAll("button")].find((b) => b.textContent === "Approve")!;
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
  const two = () => fakeFetch({ "/api/policy/repositories": { repositories: ["Smana/cloud-native-ref", "Smana/broken"] },
    "/api/policy/repositories/Smana/cloud-native-ref": page(), "/api/approval-memory": { grants: [{}, {}] } });

  it("opens settings on one card per section, each the link, with its counts", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings", two());
    const cards = [...app.querySelectorAll("a.settings-card")];
    expect(cards.map((a) => a.getAttribute("href"))).toEqual(["/settings/merge-policies", "/settings/evidence", "/settings/approval-memory"]);
    expect(cards[0].textContent).toContain("2 repositories");
    expect(cards[0].textContent).toContain("1 live · 1 shadow (1 unreadable)");
    expect(cards[1].textContent).toContain("1 class eligible to promote (1 unreadable)");
    expect(cards[2].textContent).toContain("2 saved grants");
    expect(app.querySelector("main ul")).toBeNull(); // no bullet list
  });

  it("says the counts are unavailable when the list, or every repository, cannot be read", async () => {
    for (const routes of [{}, { "/api/policy/repositories": { repositories: ["Smana/a", "Smana/b"] } }]) {
      const app = document.createElement("div");
      await mountSettings(app, "/settings", fakeFetch(routes));
      const cards = [...app.querySelectorAll("a.settings-card")];
      expect(cards[0].textContent).toContain("Counts unavailable right now.");
      expect(cards[1].textContent).toContain("Counts unavailable right now.");
      expect(cards[0].textContent).not.toContain("no class");
    }
  });

  it("counts an empty list as zero, not as unavailable", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings", fakeFetch({ "/api/policy/repositories": { repositories: [] } }));
    const cards = [...app.querySelectorAll("a.settings-card")];
    expect(cards[0].textContent).toContain("0 repositories");
    expect(cards[1].textContent).toContain("No class to promote yet.");
    expect(cards[1].textContent).not.toContain("unavailable");
  });

  it("lists each repository with its MergePolicy and its classes by mode", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", two());
    const rows = [...app.querySelectorAll("table.repo-table tbody tr")].map((tr) => [...tr.children].map((td) => td.textContent));
    expect(rows[0]).toEqual(["Smana/cloud-native-ref", "cloud-native-ref · status unknown", "1 live · 1 shadow", "docs-links 20/20"]);
    expect([...app.querySelectorAll("table.repo-table thead th")].map((th) => th.textContent)[1]).toBe("MergePolicy");
    expect(rows[1][0]).toBe("Smana/broken");
    expect(rows[1][1]).toMatch(/^Not readable: /);
    expect(app.querySelector('table.repo-table a[href="/settings/merge-policies/Smana/cloud-native-ref"]')).not.toBeNull();
  });

  it("names the MergePolicy with its Accepted and InSync words, or says the repository is not migrated", async () => {
    const synced = page();
    synced.report.mergePolicyStatus = { namespace: "agent-system", generation: 3, status: { authority: "central", conditions: [
      { type: "Accepted", status: "True" }, { type: "RepositoryFileValid", status: "True" },
      { type: "InSync", status: "False", message: "docs: only in the central config; tests: only in the central config" }] } };
    const legacy = page();
    delete legacy.report.mergePolicy;
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", fakeFetch({ "/api/policy/repositories": { repositories: ["Smana/synced", "Smana/legacy"] },
      "/api/policy/repositories/Smana/synced": synced, "/api/policy/repositories/Smana/legacy": legacy }));
    const from = [...app.querySelectorAll("table.repo-table tbody tr")].map((tr) => tr.children[1].textContent);
    expect(from).toEqual(["cloud-native-ref · Accepted · Not in sync: 2 differences", "Not migrated"]);
  });

  it("labels each cell with its column, for the stacked phone table, and scopes its styles to the settings header", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", two());
    expect([...app.querySelectorAll("table.repo-table tbody tr")[0].children].map((td) => td.getAttribute("data-label")))
      .toEqual(["Repository", "MergePolicy", "Classes", "Closest to promotion"]);
    const css = readFileSync(join(__dirname, "../src/app.css"), "utf8");
    expect(css).toMatch(/td:nth-child\(n \+ 3\)\[data-label\]::before \{ content: attr\(data-label\) ": "; \}/);
    // The room page's p.policy-conditions is not the header's chip row.
    expect(css).not.toMatch(/(^|\n)\.policy-conditions\s*\{/);
  });

  it("says a MergePolicy's status is unknown when it has no Accepted or InSync condition", async () => {
    const bare = page();
    bare.report.mergePolicyStatus = { namespace: "agent-system", generation: 1, status: { conditions: [{ type: "RepositoryFileValid", status: "True" }] } };
    const none = page();
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", fakeFetch({ "/api/policy/repositories": { repositories: ["Smana/bare", "Smana/none"] },
      "/api/policy/repositories/Smana/bare": bare, "/api/policy/repositories/Smana/none": none }));
    const from = [...app.querySelectorAll("table.repo-table tbody tr")].map((tr) => tr.children[1].textContent);
    expect(from).toEqual(["cloud-native-ref · status unknown", "cloud-native-ref · status unknown"]);
  });

  it("counts a report's classes by mode", () => {
    expect(modeCounts(page().report)).toEqual({ live: 1, shadow: 1 });
  });

  it("lists every class's evidence", async () => {
    const get = fakeFetch({ "/api/policy/repositories": { repositories: ["Smana/cloud-native-ref"] }, "/api/policy/repositories/Smana/cloud-native-ref": page() });
    const app = document.createElement("div");
    await mountSettings(app, "/settings/evidence", get);
    expect(app.textContent).toContain("docs-links");
    expect(app.textContent).toContain("eligible");
    expect(app.textContent).toContain(LADDER_LEGEND);
    expect([...app.querySelectorAll("thead th")].map((h) => h.textContent)).toEqual(["Repository", "Class", "Mode", "Track record", "Blocks promotion"]);
    expect(app.textContent).not.toMatch(/Widening|Eligibility|immature|bound /);
    expect(app.querySelectorAll(".ladder-legend")).toHaveLength(1);
  });
});

// field is the editor's field of key; startFrom begins a new class from an example in the gallery.
const field = (app: HTMLElement, key: string) => app.querySelector(`.editor-field[data-key="${key}"]`) as HTMLLabelElement;
const startFrom = (app: HTMLElement, name: string) => {
  (app.querySelector("button.example-open") as HTMLButtonElement).click();
  (app.querySelector(`.example-card[data-example="${name}"] button`) as HTMLButtonElement).click();
};

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
    const cap = field(app, "maxPatchFiles").querySelector("input")!;
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
    startFrom(app, "deps-patch");
    const mode = field(app, "mode").querySelector("select")!;
    mode.value = "shadow";
    mode.dispatchEvent(new Event("change"));
    const yaml = (app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement).value;
    expect(yaml).toContain("  deps-patch:\n");
    expect(yaml).toContain('harness: "docs-standard"');
    expect(yaml).toContain('logins: ["renovate[bot]"]');
  });
  it("gives a template a profile its author may take, and declares the required classes unarmed", async () => {
    const app = document.createElement("div");
    const r = page();
    r.report = { ...r.report, harnessProfiles: ["adopted-none", "checked", "plain"], adoptedProfiles: ["adopted-none"],
      file: "factory: aws-0\n", policy: { factory: "aws-0" } };
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": r }));
    const yamlOf = () => (app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement).value;
    const edit = () => field(app, "mode").querySelector("select")!.dispatchEvent(new Event("change"));
    startFrom(app, "docs-broad");
    edit();
    expect(yamlOf()).toMatch(/  docs-broad:\n(    .*\n)*?    harness: "checked"\n/);
    expect(yamlOf()).toContain('  docs-links:\n    mode: "off"\n    author: "agents"\n    harness: "checked"\n');
    expect(yamlOf()).toContain('  revert:\n    mode: "off"\n');
    const harness = field(app, "harness").querySelector("select")!;
    expect([...harness.options].map((o) => o.value)).not.toContain("adopted-none");
    startFrom(app, "deps-patch");
    edit();
    expect(yamlOf()).toMatch(/  deps-patch:\n(    .*\n)*?    harness: "adopted-none"\n/);
  });
  it("labels every field in words, with its key, one help line and a link to the guide", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    const f = field(app, "maxPatchFiles");
    expect(f.querySelector(".field-label")?.textContent).toBe("Most files changed");
    expect(f.querySelector("code.field-key")?.textContent).toBe("maxPatchFiles");
    expect(f.querySelector(".field-help")?.textContent).toMatch(/factory's cap/);
    expect(f.querySelector("a.field-docs")?.getAttribute("href")).toMatch(/merge-policies\.md#leaves$/);
    expect(app.querySelector(".editor-form legend")?.textContent).toBe("Editing docs-links");
  });
  it("keeps the help and the guide link out of the label, so clicking them never toggles a checkbox", async () => {
    const app = document.createElement("div");
    document.body.append(app);
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    const f = field(app, "modifyOnly");
    expect(f.tagName).toBe("DIV");
    const box = f.querySelector("input")!;
    const label = f.querySelector("label")!;
    expect(label.htmlFor).toBe(box.id);
    expect(label.contains(f.querySelector(".field-help"))).toBe(false);
    const before = box.checked;
    (f.querySelector(".field-help") as HTMLElement).click();
    expect(box.checked).toBe(before);
    label.click();
    expect(box.checked).toBe(!before);
    app.remove();
  });
  it("groups the fields by question under h3s, and folds a group with nothing set", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    expect([...app.querySelectorAll(".editor-group h3")].map((h) => h.textContent))
      .toEqual(["What it is for", "Who writes it", "What it may touch", "How big it may be", "What must pass", "When to ask a maintainer", "Limits and follow-up"]);
    const ask = app.querySelector('.editor-group[data-group="ask"]')!;
    const add = ask.querySelector("button.group-add") as HTMLButtonElement;
    expect(add.textContent).toBe("Add an approval rule");
    expect(ask.querySelector(".editor-field")).toBeNull();
    add.click();
    expect(ask.querySelector('.editor-field[data-key="changedFiles"]')).not.toBeNull();
  });
  it("edits the description first, capped at 280, and shows it in the live story and the YAML", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    const intent = app.querySelector('.editor-group[data-group="intent"]')!;
    expect(app.querySelector(".editor-group")).toBe(intent);
    (intent.querySelector("button.group-add") as HTMLButtonElement).click();
    const box = field(app, "description").querySelector("textarea")!;
    expect(box.maxLength).toBe(280);
    box.value = "Keeps the docs current.";
    box.dispatchEvent(new Event("input"));
    expect(app.querySelector(".class-story")?.textContent).toContain("Keeps the docs current.");
    expect((app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement).value).toContain(`description: "Keeps the docs current."`);
    expect(field(app, "description").textContent).toContain("23 of 280");
    box.value = "x".repeat(280);
    box.dispatchEvent(new Event("input"));
    expect(field(app, "description").textContent).toContain("280 of 280");
    box.value = "   ";
    box.dispatchEvent(new Event("input"));
    expect((app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement).value).not.toContain("description:");
  });
  it("selects a central class in the editor's picker from its card's Edit this class", async () => {
    const p = page();
    const docs: ClassPolicy = { mode: "shadow", author: "agents", harness: "docs-standard", conditions: { all: [{ maxPatchFiles: 7 }] } };
    p.report.served = { factory: "aws-0", classes: { "docs-links": p.report.policy!.classes!["docs-links"], docs } };
    p.report.manifest = "kind: MergePolicy\n";
    p.report.mergePolicyStatus = { namespace: "agent-system", generation: 2, status: { authority: "central" } };
    p.report.classes = [{ class: "docs", source: "central", mode: "shadow", unproposed: "not_in_repository_policy", evidence: ev() }];
    const app = document.createElement("div");
    document.body.append(app);
    try {
      await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": p }));
      const pick = app.querySelector<HTMLSelectElement>('.policy-editor select[aria-label="class to edit"]')!;
      expect(pick.value).toBe("docs-links");
      [...app.querySelectorAll<HTMLButtonElement>("#class-docs button")].find((b) => b.textContent === "Edit this class")!.click();
      expect(pick.value).toBe("docs");
      expect(document.activeElement).toBe(pick);
      expect(app.querySelector(".class-story")?.textContent).toContain("it changes at most 7 files");
    } finally { app.remove(); }
  });
  it("rewrites the class's sentence as the form changes", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    const cap = field(app, "maxPatchFiles").querySelector("input")!;
    cap.value = "4";
    cap.dispatchEvent(new Event("input"));
    expect(app.querySelector(".class-story")?.textContent).toContain("it changes at most 4 files");
  });
  it("lists only classes in the picker; examples come from the gallery", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    const pick = app.querySelector('select[aria-label="class to edit"]') as HTMLSelectElement;
    expect([...pick.options].map((o) => o.value)).toEqual(["docs-links"]);
  });
  it("keeps the expert YAML in a tab beside the form, the check below both", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    const tabs = [...app.querySelectorAll('.policy-editor [role="tab"]')] as HTMLButtonElement[];
    expect(tabs.map((t) => t.textContent)).toEqual(["Form", "YAML (expert)"]);
    expect(tabs[0].getAttribute("aria-selected")).toBe("true");
    expect((app.querySelector(".yaml-editor")!.closest('[role="tabpanel"]') as HTMLElement).hidden).toBe(true);
    tabs[1].click();
    expect((app.querySelector(".yaml-editor")!.closest('[role="tabpanel"]') as HTMLElement).hidden).toBe(false);
    expect(app.querySelector(".editor-actions button")?.textContent).toBe("Check, compare and simulate");
  });
  it("offers simple and advanced examples, each explained, and starts the form from one", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    (app.querySelector("button.example-open") as HTMLButtonElement).click();
    const g = app.querySelector("section.example-gallery") as HTMLElement;
    expect(g.hidden).toBe(false);
    expect([...g.querySelectorAll("h3")].map((h) => h.textContent)).toEqual(["Simple", "Advanced"]);
    const card = g.querySelector('.example-card[data-example="infra-with-approval"]')!;
    expect(card.querySelector(".example-why")?.textContent).toMatch(/network/);
    expect(card.textContent).toContain("A maintainer approves it first if it changes ^infrastructure/network/.+$.");
    (card.querySelector("button") as HTMLButtonElement).click();
    expect(app.querySelector(".editor-form legend")?.textContent).toBe("New class from infra-with-approval");
    expect((app.querySelector('input[aria-label="new class name"]') as HTMLInputElement).hidden).toBe(false);
  });
  it("offers harnessAllowed and escalation only to an off class, or the review class, and writes them", async () => {
    const app = document.createElement("div");
    const r = page();
    r.report = { ...r.report, file: "factory: aws-0\n", policy: { factory: "aws-0", classes: {
      "docs-links": { mode: "shadow", author: "agents", harness: "docs-standard" },
      review: { mode: "", author: "agents", harness: "docs-standard" } } } };
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": r }));
    expect(field(app, "harnessAllowed")).toBeNull();
    const mode = field(app, "mode").querySelector("select")!;
    mode.value = "off";
    mode.dispatchEvent(new Event("change"));
    const allowed = field(app, "harnessAllowed").querySelector("textarea")!;
    allowed.value = "checked\nplain";
    allowed.dispatchEvent(new Event("input"));
    const out = field(app, "escalation.outOfBudget").querySelector("input")!;
    out.value = "checked";
    out.dispatchEvent(new Event("input"));
    const yaml = (app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement).value;
    expect(yaml).toContain('    harnessAllowed: ["checked", "plain"]\n    escalation:\n      outOfBudget: "checked"\n');
    out.value = "";
    out.dispatchEvent(new Event("input"));
    expect((app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement).value).not.toContain("escalation");
    const pick = app.querySelector('select[aria-label="class to edit"]') as HTMLSelectElement;
    pick.value = "review";
    pick.dispatchEvent(new Event("change"));
    expect(field(app, "harnessAllowed")).not.toBeNull();
    expect(field(app, "escalation.notConverged")).not.toBeNull();
  });
  it("marks a pattern line that is not a regular expression", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    const files = field(app, "allowedFiles").querySelector("textarea")!;
    files.value = "^docs/.+$\n^docs/(.+$";
    files.dispatchEvent(new Event("input"));
    expect(files.getAttribute("aria-invalid")).toBe("true");
    const err = field(app, "allowedFiles").querySelector(".field-error") as HTMLElement;
    expect(err.hidden).toBe(false);
    expect(err.textContent).toBe("Line 2 is not a regular expression");
    files.value = "^docs/.+$";
    files.dispatchEvent(new Event("input"));
    expect(files.hasAttribute("aria-invalid")).toBe(false);
    expect(err.hidden).toBe(true);
  });
  it("says whether the gallery is open on the button that opens it", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    const open = app.querySelector("button.example-open") as HTMLButtonElement;
    expect(open.getAttribute("aria-expanded")).toBe("false");
    open.click();
    expect(open.getAttribute("aria-expanded")).toBe("true");
    startFrom(app, "deps-patch"); // the click closes it, then starting one closes it too
    expect(open.getAttribute("aria-expanded")).toBe("false");
    expect((app.querySelector("section.example-gallery") as HTMLElement).hidden).toBe(true);
  });
  it("shows the YAML tab when an error's line is chosen from the form", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page(),
      "/api/policy/repositories/Smana/cloud-native-ref/preview": { valid: false, simulation: { enabled: false },
        errors: ["class docs-links: guardrail: maxPatchFiles 30 is over the guardrail's 20"] } }));
    const cap = field(app, "maxPatchFiles").querySelector("input")!;
    cap.value = "30";
    cap.dispatchEvent(new Event("input"));
    (app.querySelector(".editor-actions button") as HTMLButtonElement).click();
    await settle();
    (app.querySelector(".editor-preview button.error-line") as HTMLButtonElement).click();
    const pane = app.querySelector(".yaml-editor")!.closest('[role="tabpanel"]') as HTMLElement;
    expect(pane.hidden).toBe(false);
    const yamlTab = [...app.querySelectorAll('[role="tab"]')].find((t) => t.textContent === "YAML (expert)")!;
    expect(yamlTab.getAttribute("aria-selected")).toBe("true");
  });
  it("goes back to a class of the file after starting from an example, and lists the new one once written", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    const pick = app.querySelector('select[aria-label="class to edit"]') as HTMLSelectElement;
    startFrom(app, "docs-broad");
    expect(pick.value).toBe("");
    expect(pick.selectedOptions[0].textContent).toBe("New class from docs-broad");
    pick.value = "docs-links";
    pick.dispatchEvent(new Event("change"));
    expect(app.querySelector(".editor-form legend")?.textContent).toBe("Editing docs-links");
    expect([...pick.options].map((o) => o.value)).toEqual(["docs-links"]);
    startFrom(app, "docs-broad");
    field(app, "mode").querySelector("select")!.dispatchEvent(new Event("change"));
    expect([...pick.options].map((o) => o.value)).toEqual(["docs-links", "revert", "docs-broad"]);
    expect(pick.value).toBe("docs-broad");
    const yaml = app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement;
    yaml.value = yaml.value.replace(/(  docs-broad:\n(?:    .*\n)*?)    mode: "shadow"/, '$1    mode: "live"');
    yaml.dispatchEvent(new Event("input"));
    expect(field(app, "mode").querySelector("select")!.value).toBe("live"); // the text refills the new class's form
  });
  it("moves focus to the new class's name when an example starts", async () => {
    const app = document.createElement("div");
    document.body.append(app);
    try {
      await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
      startFrom(app, "docs-broad");
      expect(document.activeElement).toBe(app.querySelector('input[aria-label="new class name"]'));
    } finally { app.remove(); }
  });

  it("never overwrites a class of the file with a new one of the same name", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    const pick = app.querySelector('select[aria-label="class to edit"]') as HTMLSelectElement;
    const name = app.querySelector('input[aria-label="new class name"]') as HTMLInputElement;
    const edit = () => field(app, "mode").querySelector("select")!.dispatchEvent(new Event("change"));
    startFrom(app, "docs-broad");
    edit();
    startFrom(app, "docs-broad");
    expect(name.value).toBe("docs-broad-2");
    expect(app.querySelector(".name-notice")?.textContent).toBe("A class named docs-broad exists: this one is named docs-broad-2.");
    edit();
    expect([...pick.options].map((o) => o.value)).toEqual(["docs-links", "revert", "docs-broad", "docs-broad-2"]);
    // A typed name that is taken is suffixed at the first write too.
    startFrom(app, "docs-broad");
    name.value = "docs-links";
    edit();
    expect([...pick.options].map((o) => o.value)).toContain("docs-links-2");
    const parsed = parsePolicy((app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement).value);
    expect(parsed.ok && parsed.file.classes?.["docs-links"].conditions?.all).toEqual([{ allowedFiles: ["^docs/.+\\.md$"] }, { maxPatchFiles: 10 }]);
  });

  it("writes an adopted bot's logins and branches even when the YAML has author: adopt and no adopt:", () => {
    const got: ClassPolicy[] = [];
    const form = classForm({ mode: "shadow", author: "adopt", harness: "none" }, ["none"], (c) => got.push(c));
    const at = (k: string) => form.querySelector(`.editor-field[data-key="${k}"] textarea`) as HTMLTextAreaElement;
    at("adopt.logins").value = "renovate[bot]";
    expect(() => at("adopt.logins").dispatchEvent(new Event("input"))).not.toThrow();
    at("adopt.branches").value = "^renovate/";
    at("adopt.branches").dispatchEvent(new Event("input"));
    expect(got.at(-1)?.adopt).toEqual({ logins: ["renovate[bot]"], branches: ["^renovate/"] });
  });

  it("holds every semver template to modifyOnly, as the factory's class check does", () => {
    for (const name of ["deps-patch", "deps-minor"]) {
      const all = templates[name]().conditions?.all ?? [];
      expect(all.some((l) => "diffShape" in l)).toBe(true);
      expect(all).toContainEqual({ modifyOnly: true });
    }
  });
  const repo = "/api/policy/repositories/Smana/cloud-native-ref";
  const mount = async (routes: Record<string, unknown> = {}) => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ [repo]: page(), ...routes }));
    const yaml = app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement;
    const type = (v: string) => { yaml.value = v; yaml.dispatchEvent(new Event("input")); };
    const input = (key: string) => field(app, key)?.querySelector("input, textarea, select") as HTMLInputElement;
    return { app, yaml, type, field: input };
  };
  it("reads the expert text back into the form, comments kept in the text", async () => {
    const { app, yaml, type, field } = await mount();
    type('# ours\nfactory: aws-0\nclasses:\n  docs-links:\n    mode: live # promoted\n    author: agents\n    harness: docs-standard\n' +
      "    conditions:\n      all:\n        - maxPatchFiles: 4\n  extra:\n    mode: off\n    author: agents\n    harness: docs-standard\n");
    expect(field("maxPatchFiles").value).toBe("4");
    expect(field("mode").value).toBe("live");
    expect([...(app.querySelector('select[aria-label="class to edit"]') as HTMLSelectElement).options].map((o) => o.value)).toContain("extra");
    expect(yaml.value).toContain("# promoted");
    field("maxPatchFiles").value = "5";
    field("maxPatchFiles").dispatchEvent(new Event("input"));
    expect(yaml.value).toContain('mode: "live"');
    expect(yaml.value).toContain("        - maxPatchFiles: 5\n");
  });
  it("pauses the form while the text does not parse, and says where", async () => {
    const { app, type } = await mount();
    type("factory: aws-0\nclasses:\n  docs-links:\n    mode: [\n");
    expect(app.querySelector(".yaml-status")?.textContent).toMatch(/^Line 5: /);
    expect((app.querySelector(".editor-form") as HTMLFieldSetElement).disabled).toBe(true);
    expect(app.querySelector(".yaml-gutter .y-error")?.textContent).toBe("5");
    type("factory: aws-0\nclasses:\n  docs-links:\n    mode: shadow\n");
    expect((app.querySelector(".editor-form") as HTMLFieldSetElement).disabled).toBe(false);
    expect(app.querySelector(".yaml-status")?.textContent).toBe("");
  });
  it("highlights the text it shows, line for line", async () => {
    const { app, type } = await mount();
    type("factory: aws-0\n");
    const hl = app.querySelector(".yaml-hl")!;
    expect(hl.textContent).toBe("factory: aws-0\n");
    expect(hl.querySelector(".y-key")?.textContent).toBe("factory");
  });
  it("points each of the factory's errors at its line, and a click goes there", async () => {
    const { app, yaml, type } = await mount({ [`${repo}/preview`]: { valid: false, simulation: { enabled: false },
      errors: ["class docs-links: guardrail: maxPatchFiles 30 is over the guardrail's 20", "class revert is required"] } });
    type("factory: aws-0\nclasses:\n  docs-links:\n    mode: shadow\n    conditions:\n      all:\n        - maxPatchFiles: 30\n");
    [...app.querySelectorAll("button")].find((b) => b.textContent === "Check, compare and simulate")!.click();
    await settle();
    const items = [...app.querySelectorAll(".editor-preview li")];
    expect(items.map((li) => li.querySelector("button")?.textContent ?? null)).toEqual(["Line 7", null]);
    expect(app.querySelector(".yaml-gutter .y-error")?.textContent).toBe("7");
    items[0].querySelector("button")!.click();
    expect(yaml.value.slice(yaml.selectionStart, yaml.selectionEnd)).toBe("        - maxPatchFiles: 30");
  });
  it("offers the keys a key error lists, and a pick rewrites that key on its line", async () => {
    const { app, yaml, type, field } = await mount({ [`${repo}/preview`]: { valid: false, simulation: { enabled: false },
      errors: ['mergepolicy: classes.docs-links.conditions.all[0]: key "maxFiles" is not a field (did you mean "maxPatchFiles"?); keys are matched exactly, in their documented case: one of allowedFiles, maxPatchFiles'] } });
    type("factory: aws-0\nclasses:\n  docs-links:\n    mode: shadow\n    author: agents\n    harness: docs-standard\n    conditions:\n      all:\n        - maxFiles: 4 # cap\n");
    [...app.querySelectorAll("button")].find((b) => b.textContent === "Check, compare and simulate")!.click();
    await settle();
    const li = app.querySelector(".editor-preview li")!;
    expect(li.querySelector("button.error-line")?.textContent).toBe("Line 9");
    const fixes = [...li.querySelectorAll("button.key-fix")];
    expect(fixes.map((b) => b.textContent)).toEqual(["maxPatchFiles", "allowedFiles"]);
    (fixes[0] as HTMLButtonElement).click();
    expect(yaml.value.split("\n")[8]).toBe("        - maxPatchFiles: 4 # cap");
    expect(field("maxPatchFiles").value).toBe("4");
    expect(app.querySelector(".yaml-gutter .y-error")).toBeNull();
  });
  it("warns before a form edit drops the text's comments, and can undo it", async () => {
    const { app, yaml, type, field } = await mount();
    const warn = () => app.querySelector(".comments-warning")?.textContent ?? "";
    expect(warn()).toBe("");
    const commented = "# ours\nfactory: aws-0\nclasses:\n  docs-links:\n    mode: shadow # for now\n    author: agents\n    harness: docs-standard\n";
    type(commented);
    expect(warn()).toMatch(/2 comments/);
    (app.querySelector('.editor-group[data-group="size"] button.group-add') as HTMLButtonElement).click();
    field("maxPatchFiles").value = "5";
    field("maxPatchFiles").dispatchEvent(new Event("input"));
    expect(yaml.value).not.toContain("#");
    const undo = [...app.querySelectorAll(".yaml-status button")].find((b) => b.textContent === "Undo") as HTMLButtonElement;
    undo.click();
    expect(yaml.value).toBe(commented);
    expect(field("maxPatchFiles")).toBeUndefined(); // nothing set again: the size group folds
    expect(app.querySelector('.editor-group[data-group="size"] button.group-add')).not.toBeNull();
  });
  it("copies and downloads the text as the file it replaces", async () => {
    const { app, yaml } = await mount();
    const wrote: string[] = [];
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async (t: string) => { wrote.push(t); } } });
    const copy = app.querySelector(".yaml-bar button.copy") as HTMLButtonElement;
    copy.click();
    await settle();
    expect(wrote).toEqual([yaml.value]);
    expect(copy.textContent).toBe("Copied");
    const dl = app.querySelector(".yaml-bar a.download") as HTMLAnchorElement;
    expect(dl.getAttribute("download")).toBe("agent-factory.yaml");
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
        steps: [{ row: "fixed_base", status: "passed" }, { row: "class", status: "refused", detail: "patch_too_large" }, { row: "grant", status: "recorded", detail: "no grant used" }],
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

// The live bug (v0.40.1): the factory read the evidence on the page's request, and the page showed
// nothing. It now answers the classes at once, their evidence pending, and fills them in place.
describe("pending evidence", () => {
  const pendingPage = (): RepositoryPage => {
    const p = page();
    p.report.classes = p.report.classes.map(({ evidence: _, ...c }) => c);
    delete p.report.evidenceAt;
    return p;
  };
  const was = { ...poll };
  afterEach(() => Object.assign(poll, was));

  it("renders each class as computing, offers no promotion, and keeps the editor", async () => {
    poll.times = 0;
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": pendingPage() }));
    const cards = [...app.querySelectorAll(".policy-classes .class-card")];
    expect(cards).toHaveLength(2);
    expect(app.querySelectorAll(".policy-classes .evidence-reading")).toHaveLength(0);
    expect(app.querySelector(".policy-classes")?.textContent?.split(READING)).toHaveLength(2); // said once
    expect([...app.querySelectorAll("button")].some((b) => b.textContent === "Promote to live")).toBe(false);
    expect(app.querySelector(".policy-classes .evidence-age")?.textContent).toBe(READING);
    expect(app.querySelector("section.policy-editor")).not.toBeNull();
  });

  it("fills the classes in place once the evidence is read, leaving the editor as it was", async () => {
    poll.every = 1;
    poll.times = 5;
    const routes: Record<string, unknown> = { "/api/policy/repositories/Smana/cloud-native-ref": pendingPage() };
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch(routes));
    const editor = app.querySelector("section.policy-editor");
    const ready = page();
    ready.report.evidenceAt = "2026-11-08T00:05:00Z";
    routes["/api/policy/repositories/Smana/cloud-native-ref"] = ready;
    await expect.poll(() => app.querySelector(".policy-classes")?.textContent).toContain("20 of 20 clean merges");
    expect(app.querySelector(".policy-classes .evidence-age")?.textContent).toMatch(/^Evidence read .+\.$/);
    expect(app.querySelector(".policy-classes .evidence-age time")?.getAttribute("datetime")).toBe("2026-11-08T00:05:00Z");
    expect(app.querySelector("section.policy-editor")).toBe(editor);
    expect([...app.querySelectorAll("button")].filter((b) => b.textContent === "Promote to live")).toHaveLength(1);
  });

  it("writes the repository's hardening readiness, ready or not, never its gaps (H5)", async () => {
    expect(hardeningLine({ repository: "o/r", hardened: true })).toBe("Hardened: every readiness fact holds.");
    expect(hardeningLine({ repository: "o/r", hardened: false })).toBe("Not hardened: an operator can see which readiness facts are missing.");
    expect(hardeningLine({ repository: "o/r", hardened: false, missing: ["error"] })).toBe("Hardening readiness could not be read just now.");
    const p = page();
    p.report.hardening = { repository: p.report.repository, hardened: false };
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref",
      fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": p, "/api/policy/repositories": { repositories: [] } }));
    const row = app.querySelector(".hardening-readiness");
    expect(row?.textContent).toBe("Not hardened: an operator can see which readiness facts are missing.");
    expect((row as HTMLElement | null)?.dataset.hardened).toBe("false");
  });

  it("says why the evidence shown is older", () => {
    const r = page().report;
    const text = (xs: (string | Node)[]) => { const p = document.createElement("p"); p.append(...xs); return p.textContent; };
    const now = Date.parse("2026-11-08T02:00:00Z");
    expect(text(evidenceAge({ ...r, evidenceAt: "2026-11-08T00:00:00Z", evidenceError: "rate_limited" }, now)))
      .toBe("Evidence read 2 h ago (the newest read failed: rate limited).");
    expect(text(evidenceAge({ ...r, evidenceAt: undefined, evidenceError: "deadline_exceeded" }, now))).toBe(`${READING} (the last read failed: deadline exceeded)`);
  });

  it("writes a pending class on the evidence page", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/evidence", fakeFetch({ "/api/policy/repositories": { repositories: ["Smana/cloud-native-ref"] },
      "/api/policy/repositories/Smana/cloud-native-ref": pendingPage() }));
    expect(app.textContent?.split(READING)).toHaveLength(2); // once for the page
    const cells = [...app.querySelectorAll("tbody tr")].flatMap((tr) => [tr.children[3], tr.children[4]]);
    expect(cells.map((c) => c.textContent)).toEqual(Array(4).fill("reading GitHub\u2026"));
    expect(cells.every((c) => c.querySelector(".reading"))).toBe(true);
  });
});

describe("a repository served from a MergePolicy", () => {
  // The factory's own output (internal/factory/policyview/manifest_test.go writes it): its report
  // of a repository served from a MergePolicy, and its preview of docs-links promoted to live.
  const golden = JSON.parse(readFileSync(join(__dirname, "../../internal/factory/policyview/testdata/ui/merge-policy.json"), "utf8")) as
    { report: Report; edited: string; preview: Preview };
  const was = { ...poll };
  afterEach(() => Object.assign(poll, was));
  const central = (): RepositoryPage => ({ ...page(), report: structuredClone(golden.report) });
  const preview = "/api/policy/repositories/Smana/cloud-native-ref/preview";
  const mountWith = async (p: RepositoryPage, calls: string[] = []) => {
    poll.times = 0;
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": p, [preview]: golden.preview }, calls));
    return app;
  };
  const clipboard = () => {
    const wrote: string[] = [];
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async (t: string) => { wrote.push(t); } } });
    return wrote;
  };

  it("edits the served classes in the file's shape, never spec.classes, and offers no GitHub edit", async () => {
    const app = await mountWith(central());
    const copy = app.querySelector(".yaml-bar button.copy") as HTMLButtonElement;
    expect(copy.textContent).toBe("Copy MergePolicy manifest");
    const dl = app.querySelector(".yaml-bar a.download") as HTMLAnchorElement;
    expect(dl.getAttribute("download")).toBe("mergepolicy-cloud-native-ref.yaml");
    expect([...app.querySelectorAll("a")].some((a) => /github\.com.*edit|on GitHub/.test(`${a.href} ${a.textContent}`))).toBe(false);
    expect(app.textContent).toContain("Copy or download the manifest and commit it by pull request; Flux applies it. Maku never edits the cluster or the repository.");
    expect(app.textContent?.split("Flux applies it")).toHaveLength(2); // said once, in the editor footer
    expect(app.querySelector(".policy-editor .editor-footer")).not.toBeNull();
    expect(app.textContent).toContain("Built from the version the factory serves; a change the factory refused is not in it.");
    const yaml = (app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement).value;
    expect(yaml).toMatch(/harness: "?plain/);
    expect(yaml).not.toMatch(/\b(live|shadow): true/);
  });

  it("copies the report's manifest while the text is as served, with no check", async () => {
    const calls: string[] = [];
    const app = await mountWith(central(), calls);
    const wrote = clipboard();
    (app.querySelector(".yaml-bar button.copy") as HTMLButtonElement).click();
    await settle();
    expect(wrote).toEqual([golden.report.manifest]);
    expect(calls.some((c) => c.startsWith("POST"))).toBe(false);
  });

  it("checks an edited text first, then copies the factory's manifest of it", async () => {
    const calls: string[] = [];
    const app = await mountWith(central(), calls);
    const wrote = clipboard();
    const yaml = app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement;
    yaml.value = golden.edited;
    yaml.dispatchEvent(new Event("input"));
    (app.querySelector(".yaml-bar button.copy") as HTMLButtonElement).click();
    await settle(); await settle(); await settle();
    expect(calls.filter((c) => c.startsWith(`POST ${preview}`))).toEqual([`POST ${preview} ${JSON.stringify({ file: golden.edited })}`]);
    expect(wrote).toEqual([golden.preview.manifest]);
    expect(wrote[0]).toContain("live: true");
    expect(app.textContent).not.toContain("edit the file on GitHub");

    // The same text again: the latest check's manifest, no second check.
    (app.querySelector(".yaml-bar button.copy") as HTMLButtonElement).click();
    await settle(); await settle();
    expect(calls.filter((c) => c.startsWith("POST"))).toHaveLength(1);
    expect(wrote).toEqual([golden.preview.manifest, golden.preview.manifest]);
  });

  it("copies nothing for a text the factory refused", async () => {
    const app = document.createElement("div");
    poll.times = 0;
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": central(),
      [preview]: { valid: false, errors: ["class docs-links: mode is not live, shadow or off"], simulation: { enabled: false } } }));
    const wrote = clipboard();
    const yaml = app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement;
    yaml.value = golden.edited.replace("mode: live", "mode: armed");
    yaml.dispatchEvent(new Event("input"));
    const copy = app.querySelector(".yaml-bar button.copy") as HTMLButtonElement;
    copy.click();
    await settle(); await settle(); await settle();
    expect(wrote).toEqual([]);
    expect(copy.textContent).toBe("No manifest: see the check below");
    expect(app.textContent).toContain("mode is not live, shadow or off");
  });

  it("is today's page without the manifest", async () => {
    const app = await mountWith(page());
    expect(app.querySelector(".yaml-bar button.copy")?.textContent).toBe("Copy");
    expect(app.querySelector(".yaml-bar a.download")?.getAttribute("download")).toBe("agent-factory.yaml");
    expect(app.textContent).not.toContain("MergePolicy manifest");
  });

  it("keeps the repository file path when the repository's own file is the authority, and says the file decides", async () => {
    const p = central();
    p.report.classes[0].source = "repository";
    p.report.mergePolicyStatus!.status.authority = "repository";
    p.report.mergePolicyStatus!.status.conditions = [{ type: "RepositoryFileValid", status: "False", message: "line 3" }];
    const app = await mountWith(p);
    expect(app.querySelector(".yaml-bar button.copy")?.textContent).toBe("Copy");
    expect(app.querySelector(".policy-header")?.textContent).toContain("repository");
    expect(app.querySelector(".policy-authority")?.textContent).toContain(".github/agent-factory.yaml decides");
    expect(app.querySelector(".policy-editor .editor-footer")?.textContent).toContain("a pull request you open and a human merges");
    const fl = app.querySelector(".file-line")!;
    expect(fl.getAttribute("data-prominent")).toBe("true");
    expect(fl.textContent).toContain("auto-merge is paused");
  });

  it("heads the page with the MergePolicy once: name, namespace, authority, generation, conditions, and the mode legend", async () => {
    const app = await mountWith(central());
    const headers = app.querySelectorAll("header.policy-header");
    expect(headers).toHaveLength(1);
    const h = headers[0];
    const dl = [...h.querySelectorAll("dt")].map((dt) => [dt.textContent, dt.nextElementSibling?.textContent]);
    expect(dl).toEqual([["MergePolicy", "cloud-native-ref"], ["Namespace", "agent-system"], ["Authority", "central"], ["Generation", "3"]]);
    const chips = [...h.querySelectorAll(".policy-condition")].map((c) => [c.textContent, c.getAttribute("data-tone")]);
    expect(chips).toEqual([["Accepted", "ok"], ["Repository file valid", "ok"], ["Not in sync: 2 differences", "warn"]]);
    expect([...app.querySelectorAll(".mode-legend .mode-badge")].map((b) => b.textContent)).toEqual(["Live", "Shadow", "Off"]);
    expect(app.querySelector(".mode-legend")?.textContent).toContain("records what it would do, merges nothing");
    expect(app.querySelector(".class-source")).toBeNull();
    expect(app.querySelector(".file-line")?.getAttribute("data-prominent")).toBe("false");
    expect(app.querySelector(".file-line")?.textContent).toContain("differs from the MergePolicy in 2 places");
  });

  it("shows each condition's message as text: the differences listed under the chips, another message as a line", async () => {
    const p = central();
    p.report.mergePolicyStatus!.status.conditions = [{ type: "Accepted", status: "False", reason: "Invalid", message: "spec.classes.docs: unknown key" },
      { type: "InSync", status: "False", reason: "Diverges", message: "docs: only in the central config; tests: only in the central config" }];
    const h = (await mountWith(p)).querySelector("header.policy-header")!;
    const d = h.querySelector("details.policy-differences")!;
    expect(d.querySelector("summary")?.textContent).toBe("What differs");
    expect([...d.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["docs: only in the central config", "tests: only in the central config"]);
    expect(h.querySelector(".policy-condition-message")?.textContent).toBe("Refused: spec.classes.docs: unknown key");
  });

  it("says the status's generation only when it lags the spec, and when the policy was read as a <time>", async () => {
    const p = central();
    const st = p.report.mergePolicyStatus!;
    const gen = async () => {
      const h = (await mountWith(p)).querySelector("header.policy-header")!;
      return [...h.querySelectorAll("dt")].find((dt) => dt.textContent === "Generation")?.nextElementSibling?.textContent;
    };
    st.generation = 4;
    st.status.observedGeneration = 3;
    expect(await gen()).toBe("4 (status from generation 3)");
    st.status.observedGeneration = 4;
    expect(await gen()).toBe("4");
    const read = (await mountWith(p)).querySelector(".policy-read time");
    expect(read?.getAttribute("datetime")).toBe(p.report.readAt);
  });

  it("folds a policy file error into the file line: one line, never a second notice", async () => {
    const p = central();
    p.report.policyError = "invalid";
    const app = await mountWith(p);
    expect(app.querySelector(".file-line")?.textContent).toBe(".github/agent-factory.yaml is invalid; the MergePolicy serves the classes.");
    expect(app.textContent).not.toContain("the central classes are shown");
    const legacy = page();
    delete legacy.report.mergePolicy;
    legacy.report.policyError = "invalid";
    const l = await mountWith(legacy);
    expect([...l.querySelectorAll("p")].filter((x) => x.textContent?.includes("policy file is invalid")).map((x) => x.className)).toEqual(["file-line"]);
  });

  it("renders a repository with no MergePolicy read-only: the notice, the cards, no editor", async () => {
    const p = page();
    delete p.report.mergePolicy;
    const app = await mountWith(p);
    expect(app.querySelector(".not-migrated")?.textContent).toBe("Not migrated yet: maku config migrate prints its MergePolicy.");
    expect(app.querySelector(".policy-header")).toBeNull();
    expect(app.querySelector(".policy-editor")).toBeNull();
    expect([...app.querySelectorAll("h2")].some((h) => h.textContent === "Policy editor")).toBe(false);
    expect(app.querySelectorAll(".class-card")).toHaveLength(2);
  });
});

describe("the filter", () => {
  const was = { ...poll };
  afterEach(() => { Object.assign(poll, was); history.replaceState(null, "", "/"); });
  const repos = ["Smana/a", "Smana/b", "Smana/cloud-native-ref", "Smana/d"];
  const manyClasses = (): RepositoryPage => {
    const p = page();
    p.report.classes = ["alpha", "beta", "gamma", "delta"].map((class_, i) => ({ class: class_, source: "repository", mode: i === 3 ? "live" : "shadow", evidence: ev() }));
    p.report.policy!.classes = { alpha: { mode: "shadow", description: "Fixes typos" } } as never;
    return p;
  };

  it("filters the repository table rows by repository and MergePolicy name", async () => {
    const routes: Record<string, unknown> = { "/api/policy/repositories": { repositories: repos } };
    for (const r of repos) routes[`/api/policy/repositories/${r}`] = page();
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", fakeFetch(routes));
    const input = app.querySelector<HTMLInputElement>(".filter input")!;
    input.value = "cloud-native";
    input.dispatchEvent(new Event("input"));
    expect([...app.querySelectorAll<HTMLElement>("table.repo-table tbody tr")].filter((tr) => !tr.hidden)).toHaveLength(4); // every row's MergePolicy is cloud-native-ref
    input.value = "smana/d";
    input.dispatchEvent(new Event("input"));
    expect([...app.querySelectorAll<HTMLElement>("table.repo-table tbody tr")].filter((tr) => !tr.hidden).map((tr) => tr.children[0].textContent)).toEqual(["Smana/d"]);
  });

  it("matches the table on the repository and MergePolicy names only, never the counts or the closest class", async () => {
    const routes: Record<string, unknown> = { "/api/policy/repositories": { repositories: repos } };
    for (const r of repos) routes[`/api/policy/repositories/${r}`] = page();
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", fakeFetch(routes));
    const input = app.querySelector<HTMLInputElement>(".filter input")!;
    const shown = () => [...app.querySelectorAll<HTMLElement>("table.repo-table tbody tr")].filter((tr) => !tr.hidden).length;
    for (const q of ["shadow", "docs-links", "unknown"]) {
      input.value = q;
      input.dispatchEvent(new Event("input"));
      expect(shown(), q).toBe(0);
    }
  });

  it("offers no field over a short table", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", fakeFetch({ "/api/policy/repositories": { repositories: ["Smana/cloud-native-ref"] }, "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    expect(app.querySelector(".filter input")).toBeNull();
  });

  it("filters the class cards by name, mode and description, and keeps the filter when the poll redraws", async () => {
    poll.every = 1;
    poll.times = 5;
    const pending = manyClasses();
    pending.report.classes = pending.report.classes.map(({ evidence: _, ...c }) => c);
    delete pending.report.evidenceAt;
    const routes: Record<string, unknown> = { "/api/policy/repositories/Smana/cloud-native-ref": pending };
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch(routes));
    const input = app.querySelector<HTMLInputElement>(".filter input")!;
    const visible = () => [...app.querySelectorAll<HTMLElement>("article.class-card")].filter((c) => !c.hidden).map((c) => c.id);
    input.value = "typos";
    input.dispatchEvent(new Event("input"));
    expect(visible()).toEqual(["class-alpha"]);
    input.value = "LIVE";
    input.dispatchEvent(new Event("input"));
    expect(visible()).toEqual(["class-delta"]); // by the mode badge
    input.value = "beta";
    input.dispatchEvent(new Event("input"));
    expect(visible()).toEqual(["class-beta"]);
    const ready = manyClasses();
    ready.report.evidenceAt = "2026-11-08T00:05:00Z";
    routes["/api/policy/repositories/Smana/cloud-native-ref"] = ready;
    await expect.poll(() => app.querySelector(".policy-classes")?.textContent).toContain("20 of 20 clean merges");
    expect(visible()).toEqual(["class-beta"]);
    expect(app.querySelector(".filter-count")?.textContent).toBe("1 of 4 classes");
  });
});
