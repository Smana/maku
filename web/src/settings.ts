// SPDX-License-Identifier: Apache-2.0

// The settings pages (merge policy stage 6, policyManagement.promotion): each repository's merge
// classes with their promotion evidence, the promotion request and its records, and operator
// exceptions. The broker decides every write (MP17): this page shows what it answers, and a
// button it hides is only a convenience. Every value is written as text.

import { SignInRequired } from "./api";
import { classCard, READING } from "./class-card";
import { conditionWords, blockerActions, explain, fileLine, LADDER_LEGEND, policyState, post, read, servingStatus, timeText, trackRecord } from "./settings-api";
import { policyPage, policiesTable, type PolicyReport } from "./policy-page";
import { bindingEditor } from "./binding-editor";
import type { PolicyFile } from "./policy-editor";
import { describeGuardrails, modeMeaning } from "./policy-describe";
import { filterField } from "./filter";
import { mountBrand } from "./brand";
import { mountThemeButton, type ThemeController } from "./theme";

export interface Step { merges: number; maxBad: number; reached: boolean }
export interface Reviewer { model: string; mergedUnchanged: number; changed: number }
// GenerationCount is one policy generation's share of the shadow decisions, or, before, the share
// decided before the class was bound (Q6).
export interface GenerationCount { generation?: number; before?: boolean; mergedUnchanged: number; changed: number }
export interface Evidence {
  shadow: { mergedUnchanged: number; changed: number; reverted: number; autoMerged: number; drill: number; reviewers: Reviewer[] | null; byGeneration?: GenerationCount[] };
  clean: number; bad: number; immature: number; pending: number;
  bound: number; budget: number; widen: boolean; ladder: Step[];
  drill: string; minimum: number; eligible: boolean; blockers?: string[];
}
export interface Proposal { path: string; line: number; file: string; editURL: string }
// evidence is absent while the factory reads it: never read yet, or the class was added since.
export interface ClassReport { class: string; source: string; mode: string; evidence?: Evidence; proposal?: Proposal; unproposed?: string }
export interface Condition { type: string; status: "True" | "False" | "Unknown" | string; reason?: string; message?: string }
// A binding's parameters, as Repository.spec.mergePolicies holds them (B4).
export interface Binding {
  name: string; mode: string; allowedFiles?: string[]; companionFiles?: string[]; requiredLabel?: string; oracleWorkflow?: string;
  holdoutChecks?: string[]; protectedFiles?: string[]; requiresApproval?: { changedFiles: string[] }[];
  maxPatchFiles?: number; maxModifiedLines?: number; maxPerDay?: number;
}
// One binding of a bound repository, as the report gives it (B16).
export interface BindingView { name: string; mode: string; policyGeneration?: number; parameters: Binding; accepted: boolean; reason?: string }
export interface BindingStatus { name: string; mode?: string; policyGeneration?: number; accepted: boolean; reason?: string; message?: string; lastDecision?: string; lastDecisionAt?: string }
// A bound repository's Repository object as the factory reads it (B10).
export interface RepositoryStatus {
  namespace: string; generation: number;
  status: { observedGeneration?: number; authority?: string; conditions?: Condition[]; classes?: BindingStatus[];
    merge?: Record<string, unknown>; mergeDefaulted?: string[] };
}
export interface Report {
  repository: string; minimum: number; policyError?: string; classes: ClassReport[]; readAt: string;
  evidenceAt?: string; evidenceError?: string;
  // The repository's hardening readiness (H5), present at hardening mode audit or above.
  hardening?: Readiness;
  file?: string; policy?: PolicyFile; factory: string; defaultBranch: string; harnessProfiles: string[] | null; adoptedProfiles?: string[] | null;
  guardrails: Record<string, unknown>;
  // Set when the repository is served from its Repository's bindings (B3, B16): the object's name,
  // its manifest and the file name to save it under, its status, and each binding.
  repositoryObject?: string; repositoryManifest?: string; repositoryManifestPath?: string; repositoryStatus?: RepositoryStatus;
  bindings?: BindingView[];
}

// One repository's hardening readiness: hardened, and the facts that do not hold.
export interface Readiness { repository: string; hardened: boolean; missing?: string[]; drill?: string }

// hardeningLine is the repository's readiness as one sentence (H5). The page shows a reader ready,
// not ready or error only: which facts are missing stays on the operators' route.
export function hardeningLine(h: Readiness): string {
  if (h.missing?.includes("error")) return "Hardening readiness could not be read just now.";
  if (h.hardened) return "Hardened: every readiness fact holds.";
  return "Not hardened: an operator can see which readiness facts are missing.";
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
// The factory's catalogue of what a repository offers at intake (profile choice at intake): the
// classes it offers, each with its floor profile, variants and escalation, and the profiles by name.
// Null on a page when the factory could not be asked: the rest of the page still reads.
export interface CatalogueClass {
  name: string; description: string; mode: string; ceilingUSD: number; floor: string; allowed: string[];
  escalation: { outOfBudget?: string; notConverged?: string } | null;
}
export interface CatalogueProfile {
  template: string; roles: string[] | null; tier: string; checks: boolean; testFirst: boolean; loop: string;
  description: string; offered: boolean; notOffered?: string;
}
export interface Catalogue {
  repository: string; evaluation: { mode: string; controlPercent: number }; approveToMerge: boolean; maintainer: boolean;
  classes: CatalogueClass[]; profiles: Record<string, CatalogueProfile>;
}
export interface RepositoryPage {
  report: Report; promotions: Promotion[]; exceptions: Exception[];
  you: { login: string; maintainer: boolean; operator: boolean };
  catalogue?: Catalogue | null;
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
    for (const [i, c] of r.entries()) {
      const td = el("td");
      td.dataset.label = head[i] ?? ""; // a stacked phone table shows it
      td.append(c);
      row.append(td);
    }
    t.tBodies[0].append(row);
  }
  return t;
}

// Re-exported where the page's tests and neighbours import them from.
export { blockerActions, explain, LADDER_LEGEND, trackRecord, trackRecordParts } from "./settings-api";
export { READING } from "./class-card";

// evidenceAge is when the shown evidence was read, as a sentence, and why it is older when the
// newest read failed.
export function evidenceAge(r: Report, now: number = Date.now()): (string | Node)[] {
  if (!r.evidenceAt) return [r.evidenceError ? `${READING} (the last read failed: ${r.evidenceError.replaceAll("_", " ")})` : READING];
  return ["Evidence read ", timeText(r.evidenceAt, now), (r.evidenceError ? ` (the newest read failed: ${r.evidenceError.replaceAll("_", " ")})` : "") + "."];
}

// poll is how often, and how many times, a page with pending evidence asks again; a test shortens it.
export const poll = { every: 5000, times: 60 };

function failed(main: HTMLElement, e: unknown) {
  main.replaceChildren(el("p", e instanceof SignInRequired ? "signing in again…" : explain(e instanceof Error ? e.message : String(e)), "notice"));
}

// mountSettings renders the settings page at path.
export async function mountSettings(app: HTMLElement, path: string, get: typeof fetch = fetch, theme?: ThemeController) {
  // PR15: a policy's page first, so /policies/<name> is never read as a repository named so.
  const pol = path.match(/^\/settings\/merge-policies\/policies\/([^/]+)$/);
  if (pol) return policyPage(app, decodeURIComponent(pol[1]), get, theme);
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

// evaluationWords is " · evaluation: observe · control 20 %" from the first page whose catalogue the
// factory answered: the team evaluation is the factory's, one for every repository. Empty when none did.
export function evaluationWords(pages: RepositoryPage[]): string {
  const e = pages.find((p) => p.catalogue)?.catalogue?.evaluation;
  return e ? ` · evaluation: ${e.mode} · control ${e.controlPercent} %` : "";
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
      !known ? unknown : `${plural(repos!.length, "repository", "repositories")}${repos!.length ? ` · ${countsText(modes)}` : ""}${note}${evaluationWords(ok)}`),
    card("/settings/evidence", "Evidence", "How close each shadow class is to being promoted to live.",
      !known ? unknown : repos!.length === 0 ? "No class to promote yet." : `${plural(eligible, "class", "classes")} eligible to promote${note}`),
    card("/settings/approval-memory", "Approval memory", "Approvals you saved so a later, similar pull request can merge without asking again.",
      grants === undefined ? unknown : plural(grants, "saved grant")));
}

// mergePolicyCell is what serves a repository in the list: its Repository object and whether it
// was accepted, its bindings valid and in sync, or that the repository is not migrated.
function mergePolicyCell(r: Report): string {
  if (r.repositoryObject) {
    const st = r.repositoryStatus;
    const kept = st && { ...st, status: { ...st.status, conditions: st.status.conditions?.filter((c) => ["Accepted", "BindingsValid", "InSync"].includes(c.type)) } };
    const words = conditionWords(kept).map((c) => c.word);
    return [`Repository ${r.repositoryObject}`, ...(words.length ? words : ["status unknown"])].join(" · ");
  }
  return "Not migrated";
}

async function repositories(app: HTMLElement, get: typeof fetch, theme?: ThemeController) {
  const main = settingsFrame(app, "Merge policies", "/settings/merge-policies", theme);
  try {
    const { repositories } = await read<{ repositories: string[] }>("/api/policy/repositories", get);
    // The policies the reader sees a consumer of (B16), below the repositories; none when unread.
    const policies = await read<{ policies: PolicyReport[] }>("/api/policy/policies", get).then((x) => x.policies, () => undefined);
    const tail = () => { if (policies?.length) main.append(el("h2", "Policies"), policiesTable(policies)); };
    if (repositories.length === 0) {
      main.append(el("p", "No repository you can read is configured."));
      tail();
      return;
    }
    const pages = await readAll(repositories, get);
    const repoTable = table(["Repository", "Served by", "Classes", "Closest to promotion"], repositories.map((repo, i) => {
      const p = pages[i];
      if (p instanceof Error) return [repo, `Not readable: ${explain(p.message)}`, "-", "-"];
      const r = p.report;
      const best = r.classes.filter((c) => c.mode === "shadow" && c.evidence).sort((x, y) => y.evidence!.clean - x.evidence!.clean)[0];
      return [link(`/settings/merge-policies/${repo}`, repo), mergePolicyCell(r), countsText(modeCounts(r)), best ? `${best.class} ${best.evidence!.clean}/${best.evidence!.minimum}` : "-"];
    }), "repo-table");
    const { box } = filterField({ label: "Filter repositories", noun: ["repository", "repositories"],
      items: () => [...repoTable.tBodies[0].rows], text: (tr) => `${tr.cells[0].textContent} ${(tr.cells[1].textContent ?? "").split(" · ")[0]}` }); // the two names, not the counts
    main.append(box, repoTable);
    tail();
  } catch (e) { failed(main, e); }
}

// loadingCell is what a cell of a row whose evidence the factory is still reading holds.
function loadingCell(): HTMLElement {
  const s = el("span", "reading GitHub\u2026", "reading");
  s.title = "The factory is reading this class's pull requests from GitHub; the row fills in on its own.";
  return s;
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
        rows.push([link(`/settings/merge-policies/${r}`, r), "-", "-", "Not readable", explain(page.message)]);
        continue;
      }
      for (const c of page.report.classes) {
        const e = c.evidence;
        reading ||= !e;
        // A row still being read says so in its own cells, never in the words of data.
        const blocks = !e ? loadingCell() : e.eligible ? "eligible" : blockerActions(e).join(", ") || "no reason given";
        rows.push([link(`/settings/merge-policies/${r}`, r), c.class, c.mode, e ? trackRecord(e) : loadingCell(), blocks]);
      }
    }
    // A row being read says so in its cells; the page says once that the evidence is being read.
    if (reading) main.append(el("p", READING, "evidence-age"));
    main.append(el("p", LADDER_LEGEND, "ladder-legend"), table(["Repository", "Class", "Mode", "Track record", "Blocks promotion"], rows));
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

  if (r.hardening) {
    const h = el("p", hardeningLine(r.hardening), "hardening-readiness");
    h.dataset.hardened = String(r.hardening.hardened);
    main.append(h);
  }
  main.append(el("h2", "Classes"));
  const guard = el("ul", undefined, "guardrails");
  for (const g of describeGuardrails(r.guardrails)) guard.append(el("li", g));
  main.append(el("p", "The factory's guardrails, which every class inherits:", "guardrails-lead"), guard);
  const classes = el("section", undefined, "policy-classes");
  main.append(classes);
  // A binding's conditions are its policy's, on the policy's page: the card links there.
  const age = (p: RepositoryPage) => { const e = el("p", undefined, "evidence-age"); e.append(...evidenceAge(p.report)); return e; };
  const draw = (p: RepositoryPage) => classes.replaceChildren(age(p),
    ...p.report.classes.map((c) => classCard(c, { page: p, get, say, reload, main })));
  draw(page);
  const filter = filterField({ label: "Filter classes", noun: ["class", "classes"],
    items: () => [...classes.querySelectorAll<HTMLElement>("article.class-card")],
    text: (c) => ["h3", ".mode-badge"].map((q) => c.querySelector(q)?.textContent ?? "").join(" ") });
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
  if (state === "repository") {
    // Cut over (stage 4): the file holds the bindings, so no Repository manifest edits them.
    main.append(el("h2", "Bindings"), el("p", "The bindings are in .github/agent-factory.yaml, which decides the classes: edit them there, by pull request.", "editor-footer"));
    return;
  }
  // B16: a bound repository edits its bindings; a policy is edited on its own page.
  const meaning = await read<{ policies: PolicyReport[] }>("/api/policy/policies", get)
    .then((x) => Object.fromEntries(x.policies.map((p) => [p.name, p.spec])), () => ({}));
  main.append(el("h2", "Bindings editor"), bindingEditor(r, meaning, page.you.maintainer, get));
}

// policyHeader says once what serves the page's classes: the Repository, its authority and
// generation, and each of its conditions as words with a tone. Nothing below repeats it.
function policyHeader(r: Report): HTMLElement {
  if (policyState(r) === "not-migrated") {
    const box = el("p", "Not migrated yet: ", "not-migrated");
    box.append(el("code", "maku config migrate"), " prints its Repository.");
    return box;
  }
  const state = policyState(r);
  const st = servingStatus(r);
  const head = el("header", undefined, "policy-header");
  const dl = el("dl");
  const row = (k: string, v: string) => dl.append(el("dt", k), el("dd", v));
  row("Repository", r.repositoryObject!);
  if (st) row("Namespace", st.namespace);
  row("Authority", st?.status.authority ?? state);
  // A status that lags the spec describes an older generation: say which, only then.
  const seen = st?.status.observedGeneration;
  if (st) row("Generation", String(st.generation) + (seen !== undefined && seen < st.generation ? ` (status from generation ${seen})` : ""));
  head.append(dl);
  const chips = el("ul", undefined, "policy-conditions");
  for (const c of conditionWords(st)) {
    const li = el("li", c.word, "policy-condition");
    li.dataset.tone = c.tone;
    if (c.message) li.title = c.message;
    chips.append(li);
  }
  head.append(chips);
  // Each condition's message, visible: a title alone never shows on touch or to the keyboard.
  for (const c of conditionWords(st)) {
    if (!c.message) continue;
    const parts = c.message.split(";").map((x) => x.trim()).filter(Boolean);
    if (c.word.startsWith("Not in sync:") && parts.length > 1) {
      const d = el("details", undefined, "policy-differences");
      const ul = el("ul");
      for (const x of parts) ul.append(el("li", x));
      d.append(el("summary", "What differs"), ul);
      head.append(d);
    } else head.append(el("p", `${c.word}: ${c.message}`, "policy-condition-message"));
  }
  if (state === "repository") head.append(el("p", `The repository's .github/agent-factory.yaml decides the classes; the Repository defers to it.`, "policy-authority"));
  const merge = mergeView(r);
  if (merge) head.append(merge);
  const read = el("p", "Policy read ", "policy-read");
  read.append(timeText(r.readAt), ".");
  head.append(read);
  return head;
}

// mergeView is a bound repository's merge settings as resolved (Q8), each key the Factory's
// mergeDefaults filled marked so; undefined without them.
function mergeView(r: Report): HTMLElement | undefined {
  const m = r.repositoryStatus?.status.merge;
  if (!m) return undefined;
  const from = new Set(r.repositoryStatus?.status.mergeDefaulted ?? []);
  const d = el("details", undefined, "repository-merge");
  d.append(el("summary", "Merge settings"));
  const ul = el("ul");
  for (const k of Object.keys(m).sort()) {
    const v = m[k];
    ul.append(el("li", `${k}: ${typeof v === "object" && v !== null ? JSON.stringify(v) : String(v)}${from.has(k) ? " (from the Factory)" : ""}`));
  }
  d.append(ul);
  return d;
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
