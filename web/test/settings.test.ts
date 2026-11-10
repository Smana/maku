// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { evidenceAge, evidenceLine, explain, isSettings, ladderLine, modeCounts, mountSettings, poll, settingsFrame, type Evidence, type Report, type RepositoryPage } from "../src/settings";
import { READING } from "../src/class-card";
import { fileYAML, type Preview, setLeaf, templates, toYAML } from "../src/policy-editor";

const ev = (over: Partial<Evidence> = {}): Evidence => ({
  shadow: { mergedUnchanged: 20, changed: 1, reverted: 0, autoMerged: 0, drill: 1, reviewers: [{ model: "glm-5.3", mergedUnchanged: 20, changed: 1 }] },
  clean: 20, bad: 0, immature: 2, pending: 1, bound: 0.139108, budget: 0.1, widen: false,
  ladder: [{ merges: 29, maxBad: 0, reached: false }, { merges: 46, maxBad: 1, reached: false }, { merges: 61, maxBad: 2, reached: false }],
  drill: "passed", minimum: 20, eligible: true, ...over,
});

const page = (over: Partial<RepositoryPage> = {}): RepositoryPage => ({
  report: { repository: "Smana/cloud-native-ref", minimum: 20, readAt: "2026-11-08T00:00:00Z", evidenceAt: "2026-11-08T00:00:00Z", factory: "aws-0", defaultBranch: "main",
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
    expect(app.textContent).toContain("below_minimum");
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

  it("lists each repository with where its classes come from and its classes by mode", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", two());
    const rows = [...app.querySelectorAll("table.repo-table tbody tr")].map((tr) => [...tr.children].map((td) => td.textContent));
    expect(rows[0]).toEqual(["Smana/cloud-native-ref", "its own .github/agent-factory.yaml", "1 live · 1 shadow", "docs-links 20/20"]);
    expect(rows[1][0]).toBe("Smana/broken");
    expect(rows[1][1]).toMatch(/^Not readable: /);
    expect(app.querySelector('table.repo-table a[href="/settings/merge-policies/Smana/cloud-native-ref"]')).not.toBeNull();
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
    expect(app.textContent).toContain("29 / 46 / 61");
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
  it("groups the fields by question under h3s, and folds a group with nothing set", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
    expect([...app.querySelectorAll(".editor-group h3")].map((h) => h.textContent))
      .toEqual(["Who writes it", "What it may touch", "How big it may be", "What must pass", "When to ask a maintainer", "Limits and follow-up"]);
    const ask = app.querySelector('.editor-group[data-group="ask"]')!;
    const add = ask.querySelector("button.group-add") as HTMLButtonElement;
    expect(add.textContent).toBe("Add an approval rule");
    expect(ask.querySelector(".editor-field")).toBeNull();
    add.click();
    expect(ask.querySelector('.editor-field[data-key="changedFiles"]')).not.toBeNull();
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
    expect(app.querySelectorAll(".policy-classes .evidence-reading")).toHaveLength(2);
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
    await expect.poll(() => app.querySelector(".policy-classes")?.textContent).toContain("20/20 clean");
    expect(app.querySelector(".policy-classes .evidence-age")?.textContent).toBe("Evidence as of 2026-11-08T00:05:00Z.");
    expect(app.querySelector("section.policy-editor")).toBe(editor);
    expect([...app.querySelectorAll("button")].filter((b) => b.textContent === "Promote to live")).toHaveLength(1);
  });

  it("says why the evidence shown is older", () => {
    const r = page().report;
    expect(evidenceAge({ ...r, evidenceAt: "2026-11-08T00:00:00Z", evidenceError: "rate_limited" }))
      .toBe("as of 2026-11-08T00:00:00Z (the newest read failed: rate limited)");
    expect(evidenceAge({ ...r, evidenceAt: undefined, evidenceError: "deadline_exceeded" })).toBe(`${READING} (the last read failed: deadline exceeded)`);
  });

  it("writes a pending class on the evidence page", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/evidence", fakeFetch({ "/api/policy/repositories": { repositories: ["Smana/cloud-native-ref"] },
      "/api/policy/repositories/Smana/cloud-native-ref": pendingPage() }));
    expect(app.textContent).toContain(READING);
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
    expect(app.textContent).toContain("GitOps pull request on the MergePolicy manifest");
    expect(app.textContent).toContain("which Flux applies");
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

  it("keeps the repository file when the repository's own file is the authority", async () => {
    const p = central();
    p.report.classes[0].source = "repository";
    const app = await mountWith(p);
    expect(app.querySelector(".yaml-bar button.copy")?.textContent).toBe("Copy");
  });
});
