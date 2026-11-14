// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { evidenceAge, blockerActions, hardeningLine, explain, isSettings, LADDER_LEGEND, modeCounts, mountSettings, poll, settingsFrame, trackRecord, type Evidence, type RepositoryPage } from "../src/settings";
import { READING } from "../src/class-card";

const ev = (over: Partial<Evidence> = {}): Evidence => ({
  shadow: { mergedUnchanged: 20, changed: 1, reverted: 0, autoMerged: 0, drill: 1, reviewers: [{ model: "glm-5.3", mergedUnchanged: 20, changed: 1 }] },
  clean: 20, bad: 0, immature: 2, pending: 1, bound: 0.139108, budget: 0.1, widen: false,
  ladder: [{ merges: 29, maxBad: 0, reached: false }, { merges: 46, maxBad: 1, reached: false }, { merges: 61, maxBad: 2, reached: false }],
  drill: "passed", minimum: 20, eligible: true, ...over,
});

// page is cloud-native-ref bound to its Repository under central authority: its bindings editor
// below the cards.
const page = (over: Partial<RepositoryPage> = {}): RepositoryPage => ({
  report: { repository: "Smana/cloud-native-ref", minimum: 20, readAt: "2026-11-08T00:00:00Z", evidenceAt: "2026-11-08T00:00:00Z", factory: "aws-0", defaultBranch: "main",
    harnessProfiles: ["docs-standard"], guardrails: { maxPatchFiles: 20 }, file: "factory: aws-0\n", policy: { factory: "aws-0" },
    repositoryObject: "cloud-native-ref", repositoryManifest: "kind: Repository\n", repositoryManifestPath: "repository-cloud-native-ref.yaml",
    repositoryStatus: { namespace: "agent-system", generation: 3, status: { observedGeneration: 3, authority: "central" } },
    bindings: [{ name: "docs-links", mode: "shadow", policyGeneration: 1, parameters: { name: "docs-links", mode: "shadow" }, accepted: true },
      { name: "revert", mode: "live", policyGeneration: 1, parameters: { name: "revert", mode: "live" }, accepted: true }],
    classes: [
    { class: "docs-links", source: "central", mode: "shadow", evidence: ev(),
      proposal: { path: ".github/agent-factory.yaml", line: 6, file: "factory: aws-0\n", editURL: "https://github.com/Smana/cloud-native-ref/edit/main/.github/agent-factory.yaml" } },
    { class: "revert", source: "central", mode: "live", evidence: ev({ eligible: false, clean: 3, blockers: ["below_minimum"] }), unproposed: "not_shadow" },
  ] },
  promotions: [], exceptions: [], you: { login: "Smana", maintainer: true, operator: false }, ...over,
});

// notMigrated is page() with no Repository: the factory's config file serves its classes.
const notMigrated = (): RepositoryPage => {
  const p = page();
  for (const k of ["repositoryObject", "repositoryManifest", "repositoryManifestPath", "repositoryStatus", "bindings"] as const) delete p.report[k];
  return p;
};

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

  it("lists each repository with what serves it and its classes by mode", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", two());
    const rows = [...app.querySelectorAll("table.repo-table tbody tr")].map((tr) => [...tr.children].map((td) => td.textContent));
    expect(rows[0]).toEqual(["Smana/cloud-native-ref", "Repository cloud-native-ref · status unknown", "1 live · 1 shadow", "docs-links 20/20"]);
    expect([...app.querySelectorAll("table.repo-table thead th")].map((th) => th.textContent)[1]).toBe("Served by");
    expect(rows[1][0]).toBe("Smana/broken");
    expect(rows[1][1]).toMatch(/^Not readable: /);
    expect(app.querySelector('table.repo-table a[href="/settings/merge-policies/Smana/cloud-native-ref"]')).not.toBeNull();
  });

  it("names the Repository with its Accepted, BindingsValid and InSync words, or says the repository is not migrated", async () => {
    const synced = page();
    synced.report.repositoryStatus = { namespace: "agent-system", generation: 3, status: { authority: "central", conditions: [
      { type: "Accepted", status: "True" }, { type: "RepositoryFileValid", status: "True" },
      { type: "InSync", status: "False", message: "docs: only in the central config; tests: only in the central config" }] } };
    const legacy = notMigrated();
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", fakeFetch({ "/api/policy/repositories": { repositories: ["Smana/synced", "Smana/legacy"] },
      "/api/policy/repositories/Smana/synced": synced, "/api/policy/repositories/Smana/legacy": legacy }));
    const from = [...app.querySelectorAll("table.repo-table tbody tr")].map((tr) => tr.children[1].textContent);
    expect(from).toEqual(["Repository cloud-native-ref · Accepted · Not in sync: 2 differences", "Not migrated"]);
  });

  it("labels each cell with its column, for the stacked phone table, and scopes its styles to the settings header", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", two());
    expect([...app.querySelectorAll("table.repo-table tbody tr")[0].children].map((td) => td.getAttribute("data-label")))
      .toEqual(["Repository", "Served by", "Classes", "Closest to promotion"]);
    const css = readFileSync(join(__dirname, "../src/app.css"), "utf8");
    expect(css).toMatch(/td:nth-child\(n \+ 3\)\[data-label\]::before \{ content: attr\(data-label\) ": "; \}/);
    // The room page's p.policy-conditions is not the header's chip row.
    expect(css).not.toMatch(/(^|\n)\.policy-conditions\s*\{/);
  });

  it("says a Repository's status is unknown when it has no Accepted, BindingsValid or InSync condition", async () => {
    const bare = page();
    bare.report.repositoryStatus = { namespace: "agent-system", generation: 1, status: { conditions: [{ type: "RepositoryFileValid", status: "True" }] } };
    const none = page();
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", fakeFetch({ "/api/policy/repositories": { repositories: ["Smana/bare", "Smana/none"] },
      "/api/policy/repositories/Smana/bare": bare, "/api/policy/repositories/Smana/none": none }));
    const from = [...app.querySelectorAll("table.repo-table tbody tr")].map((tr) => tr.children[1].textContent);
    expect(from).toEqual(["Repository cloud-native-ref · status unknown", "Repository cloud-native-ref · status unknown"]);
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

  it("renders each class as computing, offers no promotion, and keeps the bindings editor", async () => {
    poll.times = 0;
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": pendingPage() }));
    const cards = [...app.querySelectorAll(".policy-classes .class-card")];
    expect(cards).toHaveLength(2);
    expect(app.querySelectorAll(".policy-classes .evidence-reading")).toHaveLength(0);
    expect(app.querySelector(".policy-classes")?.textContent?.split(READING)).toHaveLength(2); // said once
    expect([...app.querySelectorAll("button")].some((b) => b.textContent === "Promote to live")).toBe(false);
    expect(app.querySelector(".policy-classes .evidence-age")?.textContent).toBe(READING);
    expect(app.querySelector("section.binding-editor")).not.toBeNull();
  });

  it("fills the classes in place once the evidence is read, leaving the bindings editor as it was", async () => {
    poll.every = 1;
    poll.times = 5;
    const routes: Record<string, unknown> = { "/api/policy/repositories/Smana/cloud-native-ref": pendingPage() };
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch(routes));
    const editor = app.querySelector("section.binding-editor");
    const ready = page();
    ready.report.evidenceAt = "2026-11-08T00:05:00Z";
    routes["/api/policy/repositories/Smana/cloud-native-ref"] = ready;
    await expect.poll(() => app.querySelector(".policy-classes")?.textContent).toContain("20 of 20 clean merges");
    expect(app.querySelector(".policy-classes .evidence-age")?.textContent).toMatch(/^Evidence read .+\.$/);
    expect(app.querySelector(".policy-classes .evidence-age time")?.getAttribute("datetime")).toBe("2026-11-08T00:05:00Z");
    expect(app.querySelector("section.binding-editor")).toBe(editor);
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

describe("a bound repository's header, and a repository not migrated", () => {
  const was = { ...poll };
  afterEach(() => Object.assign(poll, was));
  const mountWith = async (p: RepositoryPage) => {
    poll.times = 0;
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": p }));
    return app;
  };
  // differs is page() whose Repository is accepted, its file valid and two differences out of sync.
  const differs = (): RepositoryPage => {
    const p = page();
    p.report.repositoryStatus!.status.conditions = [{ type: "Accepted", status: "True" }, { type: "RepositoryFileValid", status: "True" },
      { type: "InSync", status: "False", reason: "Diverges", message: "docs: only in the Repository; tests: only in the Repository" }];
    return p;
  };

  it("edits the bindings under central authority, and says the Repository manifest is committed by pull request", async () => {
    const app = await mountWith(page());
    expect([...app.querySelectorAll("h2")].map((h) => h.textContent)).toContain("Bindings editor");
    expect(app.querySelector(".binding-editor .editor-footer")?.textContent).toContain("Copy or download the Repository manifest");
    expect(app.textContent).not.toContain("Policy editor");
  });

  it("points to the file under repository authority, with no editor, and says the file decides", async () => {
    const p = page();
    p.report.repositoryStatus!.status.authority = "repository";
    p.report.repositoryStatus!.status.conditions = [{ type: "RepositoryFileValid", status: "False", message: "line 3" }];
    const app = await mountWith(p);
    expect(app.querySelector(".policy-editor")).toBeNull();
    expect(app.textContent).toContain("The bindings are in .github/agent-factory.yaml, which decides the classes: edit them there, by pull request.");
    expect(app.querySelector(".policy-header")?.textContent).toContain("repository");
    expect(app.querySelector(".policy-authority")?.textContent).toBe("The repository's .github/agent-factory.yaml decides the classes; the Repository defers to it.");
    const fl = app.querySelector(".file-line")!;
    expect(fl.getAttribute("data-prominent")).toBe("true");
    expect(fl.textContent).toContain("auto-merge is paused");
  });

  it("heads the page with the Repository once: name, namespace, authority, generation, conditions, and the mode legend", async () => {
    const app = await mountWith(differs());
    const headers = app.querySelectorAll("header.policy-header");
    expect(headers).toHaveLength(1);
    const h = headers[0];
    const dl = [...h.querySelectorAll("dt")].map((dt) => [dt.textContent, dt.nextElementSibling?.textContent]);
    expect(dl).toEqual([["Repository", "cloud-native-ref"], ["Namespace", "agent-system"], ["Authority", "central"], ["Generation", "3"]]);
    const chips = [...h.querySelectorAll(".policy-condition")].map((c) => [c.textContent, c.getAttribute("data-tone")]);
    expect(chips).toEqual([["Accepted", "ok"], ["Repository file valid", "ok"], ["Not in sync: 2 differences", "warn"]]);
    expect([...app.querySelectorAll(".mode-legend .mode-badge")].map((b) => b.textContent)).toEqual(["Live", "Shadow", "Off"]);
    expect(app.querySelector(".mode-legend")?.textContent).toContain("records what it would do, merges nothing");
    expect(app.querySelector(".class-source")).toBeNull();
    expect(app.querySelector(".file-line")?.getAttribute("data-prominent")).toBe("false");
    expect(app.querySelector(".file-line")?.textContent).toContain("differs from the Repository in 2 places");
  });

  it("shows each condition's message as text: the differences listed under the chips, another message as a line", async () => {
    const p = page();
    p.report.repositoryStatus!.status.conditions = [{ type: "Accepted", status: "False", reason: "Invalid", message: "spec.mergePolicies[0]: unknown key" },
      { type: "InSync", status: "False", reason: "Diverges", message: "docs: only in the Repository; tests: only in the Repository" }];
    const h = (await mountWith(p)).querySelector("header.policy-header")!;
    const d = h.querySelector("details.policy-differences")!;
    expect(d.querySelector("summary")?.textContent).toBe("What differs");
    expect([...d.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["docs: only in the Repository", "tests: only in the Repository"]);
    expect(h.querySelector(".policy-condition-message")?.textContent).toBe("Refused: spec.mergePolicies[0]: unknown key");
  });

  it("says the status's generation only when it lags the spec, and when the policy was read as a <time>", async () => {
    const p = page();
    const st = p.report.repositoryStatus!;
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
    const p = page();
    p.report.policyError = "invalid";
    const app = await mountWith(p);
    expect(app.querySelector(".file-line")?.textContent).toBe(".github/agent-factory.yaml is invalid; the Repository serves the classes.");
    expect(app.textContent).not.toContain("the central classes are shown");
    const legacy = notMigrated();
    legacy.report.policyError = "invalid";
    const l = await mountWith(legacy);
    expect([...l.querySelectorAll("p")].filter((x) => x.textContent?.includes("policy file is invalid")).map((x) => x.className)).toEqual(["file-line"]);
  });

  it("renders a repository with no Repository read-only: the notice, the cards, no editor", async () => {
    const app = await mountWith(notMigrated());
    expect(app.querySelector(".not-migrated")?.textContent).toBe("Not migrated yet: maku config migrate prints its Repository.");
    expect(app.querySelector(".policy-header")).toBeNull();
    expect(app.querySelector(".policy-editor")).toBeNull();
    expect(app.querySelectorAll(".class-card")).toHaveLength(2);
    expect(app.querySelector(".class-summary")?.textContent).toBe("This class's conditions are set in the factory's config and are not shown here.");
  });
});

describe("the filter", () => {
  const was = { ...poll };
  afterEach(() => { Object.assign(poll, was); history.replaceState(null, "", "/"); });
  const repos = ["Smana/a", "Smana/b", "Smana/cloud-native-ref", "Smana/d"];
  const manyClasses = (): RepositoryPage => {
    const p = page();
    p.report.classes = ["alpha", "beta", "gamma", "delta"].map((class_, i) => ({ class: class_, source: "repository", mode: i === 3 ? "live" : "shadow", evidence: ev() }));
    return p;
  };

  it("filters the repository table rows by repository and Repository object name", async () => {
    const routes: Record<string, unknown> = { "/api/policy/repositories": { repositories: repos } };
    for (const r of repos) routes[`/api/policy/repositories/${r}`] = page();
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", fakeFetch(routes));
    const input = app.querySelector<HTMLInputElement>(".filter input")!;
    input.value = "cloud-native";
    input.dispatchEvent(new Event("input"));
    expect([...app.querySelectorAll<HTMLElement>("table.repo-table tbody tr")].filter((tr) => !tr.hidden)).toHaveLength(4); // every row's Repository is cloud-native-ref
    input.value = "smana/d";
    input.dispatchEvent(new Event("input"));
    expect([...app.querySelectorAll<HTMLElement>("table.repo-table tbody tr")].filter((tr) => !tr.hidden).map((tr) => tr.children[0].textContent)).toEqual(["Smana/d"]);
  });

  it("matches the table on the repository and Repository object names only, never the counts or the closest class", async () => {
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

  it("filters the class cards by name and mode, and keeps the filter when the poll redraws", async () => {
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
    input.value = "alpha";
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
