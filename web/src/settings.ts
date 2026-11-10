// SPDX-License-Identifier: Apache-2.0

// The settings pages (merge policy stage 6, policyManagement.promotion): each repository's merge
// classes with their promotion evidence, the promotion request and its records, and operator
// exceptions. The broker decides every write (MP17): this page shows what it answers, and a
// button it hides is only a convenience. Every value is written as text.

import { SignInRequired } from "./api";
import { classCard, READING } from "./class-card";
import { centralManifest, conditionWords, evidenceLine, explain, fileLine, ladderLine, policyState, post, read } from "./settings-api";
import { classForm, cleanClass, exampleGallery, fileYAML, type PolicyFile, type Preview, previewView, profilesFor, templates, yamlEditor } from "./policy-editor";
import { comments, locate, parsePolicy } from "./policy-yaml";
import { describeGuardrails, modeMeaning } from "./policy-describe";
import { filterField } from "./filter";
import { mountBrand } from "./brand";
import { mountThemeButton, type ThemeController } from "./theme";

export interface Step { merges: number; maxBad: number; reached: boolean }
export interface Reviewer { model: string; mergedUnchanged: number; changed: number }
export interface Evidence {
  shadow: { mergedUnchanged: number; changed: number; reverted: number; autoMerged: number; drill: number; reviewers: Reviewer[] | null };
  clean: number; bad: number; immature: number; pending: number;
  bound: number; budget: number; widen: boolean; ladder: Step[];
  drill: string; minimum: number; eligible: boolean; blockers?: string[];
}
export interface Proposal { path: string; line: number; file: string; editURL: string }
// evidence is absent while the factory reads it: never read yet, or the class was added since.
export interface ClassReport { class: string; source: string; mode: string; evidence?: Evidence; proposal?: Proposal; unproposed?: string }
export interface Condition { type: string; status: "True" | "False" | "Unknown" | string; reason?: string; message?: string }
export interface ClassStatus { name: string; mode: string; lastDecision?: string; lastDecisionAt?: string }
// A repository's MergePolicy object as the factory reads it; absent when it has none.
export interface MergePolicyStatus {
  namespace: string; generation: number;
  status: { observedGeneration?: number; authority?: string; conditions?: Condition[]; classes?: ClassStatus[] };
}
export interface Report {
  repository: string; minimum: number; policyError?: string; classes: ClassReport[]; readAt: string;
  evidenceAt?: string; evidenceError?: string;
  file?: string; policy?: PolicyFile; factory: string; defaultBranch: string; harnessProfiles: string[] | null; adoptedProfiles?: string[] | null;
  guardrails: Record<string, unknown>;
  // Set when the repository is served from a MergePolicy object: its name, its manifest as the
  // factory renders it, the file name to save that under, and its classes in the file's shape.
  mergePolicy?: string; mergePolicyStatus?: MergePolicyStatus; manifest?: string; manifestPath?: string; served?: PolicyFile;
}

export interface Step2 { row: string; status: string; detail?: string }
export interface Decision {
  at: string; head: string; class: string; verdict: string; reason: string; steps: Step2[]; revisions: Step2[];
  commentURL: string; policyURL: string;
}
export interface Timeline { repository: string; pullRequest: number; decisions: Decision[] }
export interface Promotion {
  id: string; repository: string; class: string; threshold: number; exceptionId?: string;
  requestedBy: string; requestedAt: string; state: string; pullRequest?: number; closedBy?: string;
}
export interface Exception {
  id: string; repository: string; class: string; minimum: number; justification: string;
  requestedBy: string; requestedAt: string; state: string; decidedBy?: string; note?: string; expiresAt?: string;
}
export interface RepositoryPage {
  report: Report; promotions: Promotion[]; exceptions: Exception[];
  you: { login: string; maintainer: boolean; operator: boolean };
}

// The routes this module answers, as the broker serves them.
export function isSettings(path: string): boolean {
  return /^\/settings(\/merge-policies(\/[^/]+\/[^/]+(\/decisions\/[0-9]+)?)?|\/evidence)?$/.test(path);
}

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

function link(href: string, text: string): HTMLAnchorElement {
  const a = el("a", text);
  a.href = href;
  return a;
}

// The settings' sections, as tabs: every settings page and approval memory carry the same row.
const SECTIONS: [href: string, label: string][] = [["/", "Rooms"], ["/settings", "Settings"], ["/settings/merge-policies", "Merge policies"],
  ["/settings/evidence", "Evidence"], ["/settings/approval-memory", "Approval memory"]];
export type Section = (typeof SECTIONS)[number][0];

// settingsNav is the tab row, the section the page belongs to marked aria-current="page": one
// state the screen reader and the stylesheet read alike.
export function settingsNav(current: Section): HTMLElement {
  const nav = el("nav", undefined, "settings-nav");
  nav.setAttribute("aria-label", "Sections");
  for (const [href, label] of SECTIONS) {
    const a = link(href, label);
    if (href === current) a.setAttribute("aria-current", "page");
    nav.append(a);
  }
  return nav;
}

// settingsFrame is a settings page's chrome: the header (the brand, the page's title, the theme), the
// tabs, then the page's own main, which it returns.
export function settingsFrame(app: HTMLElement, title: string, section: Section, theme?: ThemeController): HTMLElement {
  const header = el("header", undefined, "page-header");
  mountBrand(header);
  header.append(el("h1", title, "page-title"));
  if (theme) mountThemeButton(header, theme);
  const main = el("main", undefined, "settings");
  const nav = settingsNav(section);
  app.replaceChildren(header, nav, main);
  // A phone's row scrolls sideways: the current tab starts in view, never past the edge.
  const cur = nav.querySelector<HTMLElement>('[aria-current="page"]');
  if (cur && cur.offsetLeft + cur.offsetWidth > nav.clientWidth) nav.scrollLeft = cur.offsetLeft + cur.offsetWidth - nav.clientWidth + 24; // clear of the fade
  return main;
}

function table(head: string[], rows: (string | Node)[][], cls = ""): HTMLTableElement {
  const t = el("table", undefined, `rooms settings-table ${cls}`.trim());
  const tr = el("tr");
  for (const h of head) tr.append(el("th", h));
  t.append(el("thead"), el("tbody"));
  t.tHead!.append(tr);
  for (const r of rows) {
    const row = el("tr");
    for (const c of r) {
      const td = el("td");
      td.append(c);
      row.append(td);
    }
    t.tBodies[0].append(row);
  }
  return t;
}

// Re-exported where the page's tests and neighbours import them from.
export { centralManifest, evidenceLine, explain, ladderLine } from "./settings-api";
export { READING } from "./class-card";

// evidenceAge is when the shown evidence was read, and why it is older when the newest read failed.
export function evidenceAge(r: Report): string {
  if (!r.evidenceAt) return r.evidenceError ? `${READING} (the last read failed: ${r.evidenceError.replaceAll("_", " ")})` : READING;
  return `as of ${r.evidenceAt}` + (r.evidenceError ? ` (the newest read failed: ${r.evidenceError.replaceAll("_", " ")})` : "");
}

// poll is how often, and how many times, a page with pending evidence asks again; a test shortens it.
export const poll = { every: 5000, times: 60 };

function failed(main: HTMLElement, e: unknown) {
  main.replaceChildren(el("p", e instanceof SignInRequired ? "signing in again…" : explain(e instanceof Error ? e.message : String(e)), "notice"));
}

// mountSettings renders the settings page at path.
export async function mountSettings(app: HTMLElement, path: string, get: typeof fetch = fetch, theme?: ThemeController) {
  const dec = path.match(/^\/settings\/merge-policies\/([^/]+)\/([^/]+)\/decisions\/([0-9]+)$/);
  if (dec) return timelinePage(app, `${decodeURIComponent(dec[1])}/${decodeURIComponent(dec[2])}`, Number(dec[3]), get, theme);
  const repo = path.match(/^\/settings\/merge-policies\/([^/]+)\/([^/]+)$/);
  if (repo) return repositoryPage(app, `${decodeURIComponent(repo[1])}/${decodeURIComponent(repo[2])}`, get, theme);
  if (path === "/settings/merge-policies") return repositories(app, get, theme);
  if (path === "/settings/evidence") return evidencePage(app, get, theme);
  return home(app, get, theme);
}

// modeCounts is a report's classes by mode, the words the list and the home card show.
export function modeCounts(r: Report): Record<string, number> {
  const out: Record<string, number> = {};
  for (const c of r.classes) out[c.mode] = (out[c.mode] ?? 0) + 1;
  return out;
}

const countsText = (n: Record<string, number>) =>
  ["live", "shadow", "off"].filter((m) => n[m]).map((m) => `${n[m]} ${m}`).join(" · ") || "no class";

// readAll reads each repository's page, at most limit at a time; a failed read is its Error, in place.
export async function readAll(repos: string[], get: typeof fetch, limit = 4): Promise<(RepositoryPage | Error)[]> {
  const out: (RepositoryPage | Error)[] = new Array(repos.length);
  let next = 0;
  const worker = async () => {
    while (next < repos.length) {
      const i = next++;
      try { out[i] = await read<RepositoryPage>(`/api/policy/repositories/${repos[i]}`, get); }
      catch (e) { out[i] = e instanceof Error ? e : new Error(String(e)); }
    }
  };
  await Promise.all(Array.from({ length: Math.min(limit, repos.length) }, worker));
  return out;
}

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

function card(href: string, title: string, purpose: string, counts: string): HTMLAnchorElement {
  const a = link(href, "");
  a.className = "settings-card";
  a.append(el("h2", title), el("p", purpose), el("p", counts, "settings-card-counts"));
  return a;
}

async function home(app: HTMLElement, get: typeof fetch, theme?: ThemeController) {
  const main = settingsFrame(app, "Settings", "/settings", theme);
  const box = el("div", undefined, "settings-cards");
  main.append(box);
  let repos: string[] | undefined;
  let pages: (RepositoryPage | Error)[] = [];
  let grants: number | undefined;
  try { ({ repositories: repos } = await read<{ repositories: string[] }>("/api/policy/repositories", get)); pages = await readAll(repos, get); }
  catch (e) {
    if (e instanceof SignInRequired) { failed(main, e); return; }
    repos = undefined; // the card says the counts are unavailable
  }
  try { grants = (await read<{ grants: unknown[] }>("/api/approval-memory", get)).grants.length; } catch { /* counts unknown */ }
  const ok = pages.filter((p): p is RepositoryPage => !(p instanceof Error));
  const modes: Record<string, number> = {};
  for (const p of ok) for (const [m, n] of Object.entries(modeCounts(p.report))) modes[m] = (modes[m] ?? 0) + n;
  const eligible = ok.flatMap((p) => p.report.classes).filter((c) => c.mode === "shadow" && c.evidence?.eligible).length;
  const unknown = "Counts unavailable right now.";
  const bad = pages.length - ok.length;
  const note = bad ? ` (${bad} unreadable)` : "";
  const known = repos !== undefined && (repos.length === 0 || ok.length > 0);
  box.append(
    card("/settings/merge-policies", "Merge policies", "Which agent pull requests may merge on their own, per repository.",
      !known ? unknown : `${plural(repos!.length, "repository", "repositories")}${repos!.length ? ` · ${countsText(modes)}` : ""}${note}`),
    card("/settings/evidence", "Evidence", "How close each shadow class is to being promoted to live.",
      !known ? unknown : repos!.length === 0 ? "No class to promote yet." : `${plural(eligible, "class", "classes")} eligible to promote${note}`),
    card("/settings/approval-memory", "Approval memory", "Approvals you saved so a later, similar pull request can merge without asking again.",
      grants === undefined ? unknown : plural(grants, "saved grant")));
}

// mergePolicyCell is a repository's MergePolicy in the list: its name and whether it was accepted
// and is in sync, or that the repository is not migrated.
function mergePolicyCell(r: Report): string {
  if (policyState(r) === "not-migrated") return "Not migrated";
  const st = r.mergePolicyStatus;
  const kept = st && { ...st, status: { ...st.status, conditions: st.status.conditions?.filter((c) => c.type === "Accepted" || c.type === "InSync") } };
  return [r.mergePolicy!, ...conditionWords(kept).map((c) => c.word)].join(" · ");
}

async function repositories(app: HTMLElement, get: typeof fetch, theme?: ThemeController) {
  const main = settingsFrame(app, "Merge policies", "/settings/merge-policies", theme);
  try {
    const { repositories } = await read<{ repositories: string[] }>("/api/policy/repositories", get);
    if (repositories.length === 0) {
      main.append(el("p", "No repository you can read is configured."));
      return;
    }
    const pages = await readAll(repositories, get);
    const repoTable = table(["Repository", "MergePolicy", "Classes", "Closest to promotion"], repositories.map((repo, i) => {
      const p = pages[i];
      if (p instanceof Error) return [repo, `Not readable: ${explain(p.message)}`, "-", "-"];
      const r = p.report;
      const best = r.classes.filter((c) => c.mode === "shadow" && c.evidence).sort((x, y) => y.evidence!.clean - x.evidence!.clean)[0];
      return [link(`/settings/merge-policies/${repo}`, repo), mergePolicyCell(r), countsText(modeCounts(r)), best ? `${best.class} ${best.evidence!.clean}/${best.evidence!.minimum}` : "-"];
    }), "repo-table");
    const { box } = filterField({ label: "Filter repositories", noun: ["repository", "repositories"],
      items: () => [...repoTable.tBodies[0].rows], text: (tr) => tr.textContent ?? "" });
    main.append(box, repoTable);
  } catch (e) { failed(main, e); }
}

async function evidencePage(app: HTMLElement, get: typeof fetch, theme?: ThemeController) {
  const main = settingsFrame(app, "Evidence", "/settings/evidence", theme);
  try {
    const { repositories } = await read<{ repositories: string[] }>("/api/policy/repositories", get);
    const rows: (string | Node)[][] = [];
    let reading = false;
    const pages = await readAll(repositories, get);
    for (const [i, r] of repositories.entries()) {
      const page = pages[i];
      if (page instanceof Error) {
        rows.push([link(`/settings/merge-policies/${r}`, r), "-", "-", "Not readable", "-", explain(page.message)]);
        continue;
      }
      for (const c of page.report.classes) {
        const e = c.evidence;
        reading ||= !e;
        rows.push([link(`/settings/merge-policies/${r}`, r), c.class, c.mode, e ? evidenceLine(e) : "pending", e ? ladderLine(e) : "-",
          !e ? "pending" : e.eligible ? "eligible" : (e.blockers ?? []).join(", ")]);
      }
    }
    // Pending rows say so in a word; the page says once that the evidence is being read.
    if (reading) main.append(el("p", READING, "evidence-age"));
    main.append(table(["Repository", "Class", "Mode", "Evidence", "Widening 29/46/61", "Eligibility"], rows));
  } catch (e) { failed(main, e); }
}

async function repositoryPage(app: HTMLElement, repository: string, get: typeof fetch, theme?: ThemeController) {
  const main = settingsFrame(app, repository, "/settings/merge-policies", theme);
  let page: RepositoryPage;
  try {
    page = await read<RepositoryPage>(`/api/policy/repositories/${repository}`, get);
  } catch (e) { failed(main, e); return; }
  const reload = () => void repositoryPage(app, repository, get, theme);
  const say = el("p", "", "notice");
  const r = page.report;
  const state = policyState(r);
  main.append(policyHeader(r), modeLegend(), say);
  const file = fileLine(r);
  const fl = el("p", file.text, "file-line");
  fl.dataset.prominent = String(file.prominent);
  main.append(fl);
  if (r.policyError) main.append(el("p", `The repository's policy file is ${r.policyError}: the central classes are shown.`, "notice"));

  main.append(el("h2", "Classes"));
  const guard = el("ul", undefined, "guardrails");
  for (const g of describeGuardrails(r.guardrails)) guard.append(el("li", g));
  main.append(el("p", "The factory's guardrails, which every class inherits:", "guardrails-lead"), guard);
  const classes = el("section", undefined, "policy-classes");
  main.append(classes);
  const policyOf = (p: RepositoryPage) => (centralManifest(p.report) ? p.report.served : p.report.policy);
  const draw = (p: RepositoryPage) => classes.replaceChildren(
    el("p", p.report.evidenceAt ? `Evidence ${evidenceAge(p.report)}.` : evidenceAge(p.report), "evidence-age"),
    ...p.report.classes.map((c) => classCard(c, { page: p, get, say, reload, main, policy: policyOf(p) })));
  draw(page);
  const filter = filterField({ label: "Filter classes", noun: ["class", "classes"],
    items: () => [...classes.querySelectorAll<HTMLElement>("article.class-card")],
    text: (c) => ["h3", ".mode-badge", ".class-intent"].map((q) => c.querySelector(q)?.textContent ?? "").join(" ") });
  classes.before(filter.box);
  // Pending evidence fills in place: the editor below is never rebuilt, so no edit is lost.
  void (async () => {
    let cur = page;
    for (let n = 0; n < poll.times && pending(cur.report); n++) {
      await new Promise((res) => setTimeout(res, poll.every));
      if (!app.contains(classes)) return; // the page moved on
      try { cur = await read<RepositoryPage>(`/api/policy/repositories/${repository}`, get); } catch { continue; }
      if (app.contains(classes)) { draw(cur); filter.apply(); }
    }
  })();

  main.append(el("h2", "Decision timeline"));
  const tl = el("form", undefined, "timeline-form");
  const pr = el("input");
  pr.type = "number";
  pr.min = "1";
  pr.placeholder = "PR #";
  pr.setAttribute("aria-label", "pull request number");
  const go = el("button", "Open the timeline");
  go.type = "submit";
  tl.append(pr, go);
  tl.onsubmit = (ev) => {
    ev.preventDefault();
    if (Number(pr.value) > 0) location.assign(`/settings/merge-policies/${r.repository}/decisions/${Number(pr.value)}`);
  };
  main.append(tl);

  if (state === "not-migrated") return;
  main.append(el("h2", "Policy editor"));
  main.append(editor(r, page.you.maintainer, get));
}

// policyHeader says once what serves the page's classes: the MergePolicy, its authority and
// generation, and each of its conditions as words with a tone. Nothing below repeats it.
function policyHeader(r: Report): HTMLElement {
  if (policyState(r) === "not-migrated") {
    const box = el("p", "Not migrated yet: ", "not-migrated");
    box.append(el("code", "maku config migrate"), " prints its MergePolicy.");
    return box;
  }
  const state = policyState(r);
  const st = r.mergePolicyStatus;
  const head = el("header", undefined, "policy-header");
  const dl = el("dl");
  const row = (k: string, v: string) => dl.append(el("dt", k), el("dd", v));
  row("MergePolicy", r.mergePolicy!);
  if (st) row("Namespace", st.namespace);
  row("Authority", st?.status.authority ?? state);
  if (st) row("Generation", String(st.generation));
  head.append(dl);
  const chips = el("ul", undefined, "policy-conditions");
  for (const c of conditionWords(st)) {
    const li = el("li", c.word, "policy-condition");
    li.dataset.tone = c.tone;
    if (c.message) li.title = c.message;
    chips.append(li);
  }
  head.append(chips);
  if (state === "repository") head.append(el("p", "The repository's .github/agent-factory.yaml decides the classes; the MergePolicy defers to it.", "policy-authority"));
  head.append(el("p", `Policy read ${r.readAt}.`, "policy-read"));
  return head;
}

// modeLegend says once what the three modes mean; a card's badge is only the word.
function modeLegend(): HTMLElement {
  const ul = el("ul", undefined, "mode-legend");
  for (const m of ["live", "shadow", "off"]) {
    const li = el("li");
    const b = el("span", modeMeaning(m).label, "mode-badge");
    b.dataset.mode = m;
    li.append(b, ` ${modeMeaning(m).meaning}`);
    ul.append(li);
  }
  return ul;
}

function pending(r: Report): boolean {
  return r.classes.some((c) => !c.evidence);
}

// editor is the guided form and the expert YAML view over the repository's policy file. The form
// starts from the file on the default branch (or the central classes as exported when it has
// none); the expert view is the text checked. Each follows the other: a form edit rewrites the
// text, and text that parses refills the form. Check is the factory's, for a maintainer.
export function editor(r: Report, maintainer: boolean, get: typeof fetch): HTMLElement {
  const box = el("section", undefined, "policy-editor");
  const manifest = centralManifest(r);
  // Served from a MergePolicy: the editor holds the served classes in the file's shape, as the
  // factory exports them; the factory builds spec.classes of them, never the page.
  const file: PolicyFile = structuredClone((manifest ? r.served : r.policy) ?? { factory: r.factory, classes: {} });
  file.factory = r.factory; // read-only: Maku never edits factory:
  file.classes ??= {};
  const profiles = r.harnessProfiles ?? [];
  box.append(el("p", `This file belongs to the factory ${r.factory} (read-only). Harness profiles: ${profiles.join(", ") || "none"}.`, "editor-meta"));
  const pick = el("select");
  pick.setAttribute("aria-label", "class to edit");
  // source is what the form edits: a class of the file, or a new class from an example.
  let source: { kind: "class" | "template"; name: string } | undefined;
  // While a new class is not in the file yet, the picker shows it as a placeholder, so choosing
  // any class of the file, the one edited before included, is a change.
  const fillPick = () => {
    const was = pick.value;
    pick.replaceChildren();
    if (source?.kind === "template") { const o = el("option", `New class from ${source.name}`); o.value = ""; pick.append(o); }
    for (const n of Object.keys(file.classes!)) { const o = el("option", n); o.value = n; pick.append(o); }
    pick.value = source?.kind === "template" ? "" : [...pick.options].some((o) => o.value === was) ? was : pick.options[0]?.value ?? "";
  };
  fillPick();
  if (pick.value) source = { kind: "class", name: pick.value };
  const name = el("input");
  name.placeholder = "new class name";
  name.setAttribute("aria-label", "new class name");
  name.hidden = true;
  const nameNote = el("p", "", "notice name-notice");
  // free is base, or base-2, base-3… when the file already has a class of that name, said so.
  const free = (base: string): string => {
    let k = base;
    for (let n = 2; Object.hasOwn(file.classes!, k); n++) k = `${base}-${n}`;
    nameNote.textContent = k === base ? "" : `A class named ${base} exists: this one is named ${k}.`;
    return k;
  };
  const formAt = el("div");
  const gallery = exampleGallery((t) => {
    source = { kind: "template", name: t };
    name.value = free(t);
    name.hidden = false;
    gallery.hidden = true;
    opener.setAttribute("aria-expanded", "false");
    fillPick();
    show();
    name.focus(); // the gallery that held focus is hidden
  }, r.guardrails);
  gallery.id = `example-gallery-${r.repository.replace(/[^A-Za-z0-9_-]/g, "-")}`;
  const opener = el("button", "New class from an example", "example-open");
  opener.type = "button";
  opener.setAttribute("aria-expanded", "false");
  opener.setAttribute("aria-controls", gallery.id);
  opener.onclick = () => {
    gallery.hidden = !gallery.hidden;
    opener.setAttribute("aria-expanded", String(!gallery.hidden));
  };
  const out = el("div");
  // The form writes the file without comments: say so before an edit there drops them.
  const warning = el("p", "", "notice comments-warning");
  const note = (text: string) => {
    const n = comments(text);
    warning.textContent = n ? `The file has ${n} comment${n > 1 ? "s" : ""}. A form edit rewrites it without them: edit in the expert view to keep them.` : "";
  };
  // The manifest copied is the factory's: the report's while the text is as served, else the
  // latest valid check's of this very text, checked first when it changed since.
  const seed = manifest ? fileYAML(file) : r.file ?? fileYAML(file);
  let last: { text: string; manifest: string } | undefined;
  const built = async (text: string): Promise<string | undefined> => {
    if (text === seed) return manifest;
    if (last?.text === text) return last.manifest;
    const p = await run(text);
    return p?.valid ? p.manifest : undefined;
  };
  const view = yamlEditor(seed, ".github/agent-factory.yaml", (text) => {
    const p = parsePolicy(text);
    const form = formAt.querySelector("fieldset");
    if (form) form.disabled = !p.ok;
    if (!p.ok) {
      view.say(`${p.line ? `Line ${p.line}: ` : ""}${p.message}. The form waits until the text parses.`);
      if (p.line) view.mark([p.line]);
      return;
    }
    view.say("");
    note(text);
    file.classes = p.file.classes ?? {};
    fillPick();
    if (source?.kind !== "template") {
      source = pick.value ? { kind: "class", name: pick.value } : undefined;
      show();
    }
  }, manifest ? { path: r.manifestPath ?? "mergepolicy.yaml", label: "Copy MergePolicy manifest", get: (t) => built(t) } : undefined);
  const show = () => {
    const src = source;
    if (!src) {
      formAt.replaceChildren(el("p", "The file has no class yet: start one from an example.", "notice"));
      return;
    }
    const tpl = src.kind === "template";
    let cls = tpl ? templates[src.name]() : file.classes![src.name];
    if (tpl && !cls.harness) cls = { ...cls, harness: profilesFor(cls.author, profiles, r.adoptedProfiles)[0] ?? "" };
    // The new class's name is fixed by the first edit that writes it into the file.
    let written: string | undefined;
    const key = () => written ?? (tpl ? (name.value.trim() || src.name) : src.name);
    name.hidden = !tpl;
    try {
      formAt.replaceChildren(classForm(cls, profiles, (c) => {
        // The first write fixes the new class's name, never on a class the file has already.
        const first = tpl && !written;
        if (first) name.value = written = free(key());
        // A file that declares classes declares docs-links and revert (OD-8): a first class brings
        // them, unarmed, so the first check does not fail on what the maintainer never wrote.
        for (const req of ["docs-links", "revert"]) {
          file.classes![req] ??= { mode: "off", author: "agents", harness: profilesFor("agents", profiles, r.adoptedProfiles)[0] ?? "" };
        }
        file.classes![key()] = cleanClass(c);
        if (first) {
          // From now on it is a class of the file: the picker lists it and the text refills it.
          source = { kind: "class", name: key() };
          name.hidden = true;
          fillPick();
          pick.value = key();
        }
        const before = view.text.value;
        const dropped = comments(before);
        view.set(fileYAML(file));
        note(view.text.value);
        if (!dropped) return view.say();
        const undo = el("button", "Undo");
        undo.type = "button";
        undo.onclick = () => {
          view.text.value = before;
          view.text.dispatchEvent(new Event("input"));
        };
        view.say(`The form edit dropped ${dropped} comment${dropped > 1 ? "s" : ""}. `, undo);
      }, r.adoptedProfiles, r.guardrails, key(), tpl ? src.name : ""));
    } catch {
      formAt.replaceChildren(el("p", "The form cannot show this class as the text has it: edit it in the expert view.", "notice"));
    }
  };
  pick.onchange = () => {
    if (!pick.value) return; // the placeholder of the new class being started
    source = { kind: "class", name: pick.value };
    nameNote.textContent = "";
    fillPick();
    show();
  };
  // The form and the YAML are two tabs over one file; the check, below both, reads the YAML.
  const tabs = el("div", undefined, "editor-tabs");
  tabs.setAttribute("role", "tablist");
  tabs.setAttribute("aria-label", "Edit the file as");
  const panes: HTMLElement[] = [];
  // tab adds a tab over pane and returns what selects it.
  const tab = (label: string, pane: HTMLElement, on: boolean): (() => void) => {
    const b = el("button", label);
    b.type = "button";
    b.id = `${gallery.id}-tab-${panes.length}`;
    b.setAttribute("role", "tab");
    b.setAttribute("aria-selected", String(on));
    b.tabIndex = on ? 0 : -1;
    pane.setAttribute("role", "tabpanel");
    pane.setAttribute("aria-labelledby", b.id);
    pane.hidden = !on;
    const select = () => {
      for (const t of tabs.children) {
        t.setAttribute("aria-selected", String(t === b));
        (t as HTMLButtonElement).tabIndex = t === b ? 0 : -1;
      }
      for (const p of panes) p.hidden = p !== pane;
    };
    b.onclick = select;
    // Arrow keys move between the tabs, as a tablist's do.
    b.onkeydown = (e) => {
      if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
      const all = [...tabs.children] as HTMLButtonElement[];
      const next = all[(all.indexOf(b) + (e.key === "ArrowRight" ? 1 : all.length - 1)) % all.length];
      next.focus();
      next.click();
    };
    panes.push(pane);
    tabs.append(b);
    return select;
  };
  const picker = el("div", undefined, "editor-picker");
  picker.append(labelled2("Class", pick), opener, name, nameNote);
  const formPane = el("div", undefined, "editor-pane");
  formPane.append(picker, gallery, warning, formAt);
  const yamlPane = el("div", undefined, "editor-pane");
  yamlPane.append(el("p", "The form rewrites the whole file without comments; edits here are checked exactly as written."), view.root);
  if (manifest) yamlPane.append(el("p", "Built from the version the factory serves; a change the factory refused is not in it.", "notice"));
  tab("Form", formPane, true);
  const toYaml = tab("YAML (expert)", yamlPane, false);
  box.append(tabs, formPane, yamlPane);
  const check = el("button", "Check, compare and simulate", "primary");
  check.type = "button";
  check.disabled = !maintainer;
  const actions = el("div", undefined, "editor-actions");
  actions.append(check);
  if (!maintainer) actions.append(el("p", "Only a maintainer of the repository may check a proposal.", "notice"));
  // run is the factory's check of text, shown below; a valid one's manifest is kept with its text.
  const run = async (text: string): Promise<Preview | undefined> => {
    try {
      const p = (await post(`/api/policy/repositories/${r.repository}/preview`, { file: text }, get)) as Preview;
      const at = (msg: string) => locate(text, msg);
      view.mark((p.errors ?? []).map(at).filter((n) => n !== undefined));
      // Served from a MergePolicy, the change is the manifest's: no file to submit on GitHub.
      // An error's line is in the YAML: its tab is shown first, from the form too.
      out.replaceChildren(previewView(manifest ? { ...p, proposal: undefined } : p, at,
        (line) => { toYaml(); view.reveal(line); }, (line, from, to) => { toYaml(); view.replaceKey(line, from, to); }));
      if (p.valid && p.manifest) last = { text, manifest: p.manifest };
      return p;
    } catch (e) {
      out.replaceChildren(el("p", explain(e instanceof Error ? e.message : String(e)), "notice"));
      return undefined;
    }
  };
  check.onclick = async () => {
    check.disabled = true;
    await run(view.text.value);
    check.disabled = false;
  };
  box.append(out, actions);
  // Said once, here: the page's header says who holds the classes; this says how a change reaches them.
  box.append(el("p", manifest
    ? "Copy or download the manifest and commit it by pull request; Flux applies it. Maku never edits the cluster or the repository."
    : "A promotion changes the class's mode in the repository's .github/agent-factory.yaml, through a pull request you open and a human merges. Maku records the request and never edits the file.", "editor-footer"));
  note(view.text.value);
  show();
  return box;
}

function labelled2(label: string, input: HTMLElement): HTMLLabelElement {
  const l = el("label", undefined, "editor-field");
  l.append(el("span", label), input);
  return l;
}

async function timelinePage(app: HTMLElement, repository: string, pr: number, get: typeof fetch, theme?: ThemeController) {
  const main = settingsFrame(app, `${repository} #${pr}: decisions`, "/settings/merge-policies", theme);
  try {
    const t = await read<Timeline>(`/api/policy/repositories/${repository}/decisions/${pr}`, get);
    main.append(link(`/settings/merge-policies/${repository}`, `Back to ${repository}`));
    if (t.decisions.length === 0) main.append(el("p", "The factory recorded no decision on this pull request."));
    for (const d of t.decisions) {
      const sec = el("section", undefined, "timeline");
      sec.append(el("h2", `${d.at} · ${d.class} · ${d.verdict}:${d.reason} · head ${d.head}`));
      const links = el("p");
      links.append(link(d.commentURL, "the decision's PR comment"), " · ", link(d.policyURL, "the policy file's commits"));
      sec.append(links, table(["Row", "Status", "Detail"], d.steps.map((s) => [s.row, s.status, s.detail ?? ""])),
        el("h3", "Bound to"), table(["Input", "Status", "Revision"], d.revisions.map((s) => [s.row, s.status, s.detail ?? ""])));
      main.append(sec);
    }
  } catch (e) { failed(main, e); }
}
