// SPDX-License-Identifier: Apache-2.0

import { afterEach, describe, expect, it } from "vitest";
import { evidenceAge, evidenceLine, explain, isSettings, ladderLine, mountSettings, PENDING, poll, settingsFrame, type Evidence, type RepositoryPage } from "../src/settings";
import { fileYAML, setLeaf, templates, toYAML } from "../src/policy-editor";

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
  it("gives a template a profile its author may take, and declares the required classes unarmed", async () => {
    const app = document.createElement("div");
    const r = page();
    r.report = { ...r.report, harnessProfiles: ["adopted-none", "checked", "plain"], adoptedProfiles: ["adopted-none"],
      file: "factory: aws-0\n", policy: { factory: "aws-0" } };
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": r }));
    const pick = app.querySelector('select[aria-label="class to edit"]') as HTMLSelectElement;
    const yamlOf = () => (app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement).value;
    const edit = () => {
      const mode = app.querySelector(".editor-form select") as HTMLSelectElement;
      mode.dispatchEvent(new Event("change"));
    };
    pick.value = "template:docs-broad";
    pick.dispatchEvent(new Event("change"));
    edit();
    expect(yamlOf()).toMatch(/  docs-broad:\n(    .*\n)*?    harness: "checked"\n/);
    expect(yamlOf()).toContain('  docs-links:\n    mode: "off"\n    author: "agents"\n    harness: "checked"\n');
    expect(yamlOf()).toContain('  revert:\n    mode: "off"\n');
    const harness = [...app.querySelectorAll(".editor-form select")][2] as HTMLSelectElement;
    expect([...harness.options].map((o) => o.value)).not.toContain("adopted-none");
    pick.value = "template:deps-patch";
    pick.dispatchEvent(new Event("change"));
    edit();
    expect(yamlOf()).toMatch(/  deps-patch:\n(    .*\n)*?    harness: "adopted-none"\n/);
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
    const field = (name: string) => [...app.querySelectorAll(".editor-field")].find((l) => l.textContent?.startsWith(name))!.querySelector("input, textarea, select") as HTMLInputElement;
    return { app, yaml, type, field };
  };
  it("reads the expert text back into the form, comments kept in the text", async () => {
    const { app, yaml, type, field } = await mount();
    type('# ours\nfactory: aws-0\nclasses:\n  docs-links:\n    mode: live # promoted\n    author: agents\n    harness: docs-standard\n' +
      "    conditions:\n      all:\n        - maxPatchFiles: 4\n  extra:\n    mode: off\n    author: agents\n    harness: docs-standard\n");
    expect(field("maxPatchFiles").value).toBe("4");
    expect((app.querySelector(".editor-form select") as HTMLSelectElement).value).toBe("live");
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
    field("maxPatchFiles").value = "5";
    field("maxPatchFiles").dispatchEvent(new Event("input"));
    expect(yaml.value).not.toContain("#");
    const undo = [...app.querySelectorAll(".yaml-status button")].find((b) => b.textContent === "Undo") as HTMLButtonElement;
    undo.click();
    expect(yaml.value).toBe(commented);
    expect(field("maxPatchFiles").value).toBe("");
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
    const rows = [...app.querySelectorAll(".policy-classes tbody tr")];
    expect(rows).toHaveLength(2);
    for (const r of rows) expect(r.textContent).toContain(PENDING);
    expect([...app.querySelectorAll("button")].some((b) => b.textContent === "Promote to live")).toBe(false);
    expect(app.querySelector(".policy-classes .evidence-age")?.textContent).toBe(`Evidence ${PENDING}.`);
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
    expect(evidenceAge({ ...r, evidenceAt: undefined, evidenceError: "deadline_exceeded" })).toBe(`${PENDING} (the last read failed: deadline exceeded)`);
  });

  it("writes a pending class on the evidence page", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/evidence", fakeFetch({ "/api/policy/repositories": { repositories: ["Smana/cloud-native-ref"] },
      "/api/policy/repositories/Smana/cloud-native-ref": pendingPage() }));
    expect(app.textContent).toContain(PENDING);
  });
});

describe("a repository served from a MergePolicy", () => {
  const manifest = "apiVersion: maku.ogenki.io/v1alpha1\nkind: MergePolicy\nmetadata:\n  name: cloud-native-ref\n  namespace: factory\nspec:\n  classes:\n    docs-links:\n      author: agents\n      harness: docs-standard\n      mode: shadow\n  merge:\n    mergerLogin: ogenki-agent-merger[bot]\n  repositoryRef: cloud-native-ref\n";
  const central = (): RepositoryPage => {
    const p = page();
    p.report = { ...p.report, mergePolicy: "cloud-native-ref", manifest, manifestPath: "mergepolicy-cloud-native-ref.yaml",
      classes: p.report.classes.map((c) => ({ ...c, source: "central", proposal: undefined, unproposed: "not_in_repository_policy" })) };
    return p;
  };
  const mountWith = async (p: RepositoryPage) => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": p }));
    return app;
  };

  it("offers the manifest, wrapped around what the editor holds, and no GitHub edit", async () => {
    const app = await mountWith(central());
    const copy = app.querySelector(".yaml-bar button.copy") as HTMLButtonElement;
    expect(copy.textContent).toBe("Copy MergePolicy manifest");
    const dl = app.querySelector(".yaml-bar a.download") as HTMLAnchorElement;
    expect(dl.getAttribute("download")).toBe("mergepolicy-cloud-native-ref.yaml");
    expect([...app.querySelectorAll("a")].some((a) => /github\.com.*edit|on GitHub/.test(`${a.href} ${a.textContent}`))).toBe(false);
    expect(app.textContent).toContain("GitOps pull request on the MergePolicy manifest");
    expect(app.textContent).toContain("which Flux applies");

    const wrote: string[] = [];
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async (t: string) => { wrote.push(t); } } });
    const yaml = app.querySelector("textarea.expert-yaml") as HTMLTextAreaElement;
    yaml.value = yaml.value.replace(`mode: "shadow"`, `mode: "live"`);
    yaml.dispatchEvent(new Event("input"));
    copy.click();
    await settle();
    expect(wrote[0]).toContain("kind: MergePolicy");
    expect(wrote[0]).toContain("name: cloud-native-ref");
    expect(wrote[0]).toContain("repositoryRef: cloud-native-ref");
    expect(wrote[0]).toContain("mode: live");
    expect(wrote[0]).not.toContain("factory:");
    expect(wrote[0]).toContain("merge:");
    expect(wrote[0]).toContain("mergerLogin: ogenki-agent-merger[bot]");
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

