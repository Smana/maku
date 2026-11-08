// SPDX-License-Identifier: Apache-2.0

// The settings pages (merge policy stage 6, policyManagement.promotion): each repository's merge
// classes with their promotion evidence, the promotion request and its records, and operator
// exceptions. The broker decides every write (MP17): this page shows what it answers, and a
// button it hides is only a convenience. Every value is written as text.

import { api, SignInRequired } from "./api";
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
export interface ClassReport { class: string; source: string; mode: string; evidence: Evidence; proposal?: Proposal; unproposed?: string }
export interface Report { repository: string; minimum: number; policyError?: string; classes: ClassReport[]; readAt: string }
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
  return /^\/settings(\/merge-policies(\/[^/]+\/[^/]+)?|\/evidence)?$/.test(path);
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

// A refusal's bounded code, as the broker writes it, else the status.
async function refusal(r: Response): Promise<string> {
  try {
    const body = (await r.json()) as { error?: unknown };
    if (typeof body.error === "string" && /^[a-z][a-z_]{0,47}$/.test(body.error)) return body.error;
  } catch { /* not JSON */ }
  return `HTTP ${r.status}`;
}

const reasons: Record<string, string> = {
  not_a_maintainer: "Only a maintainer of the repository (write on GitHub) may do this.",
  not_an_operator: "Only a platform operator may decide an exception.",
  not_eligible: "The evidence does not meet the minimum, has a bad outcome, or has no passed drill.",
  not_promotable: "The class is not a shadow class of the repository's own policy file.",
  promotion_open: "The class already has an open promotion request.",
  exception_unusable: "The exception is not an approved, unexpired one of this class.",
  minimum_not_below: "An exception's minimum is below the configured one, and at least 1.",
  justification_length: "A justification is 20 to 2,000 characters.",
  already_moved: "That record has already moved.",
  evidence_unavailable: "The evidence could not be read from GitHub. Retry.",
  not_found: "Not found, or not readable with your GitHub access.",
};

export function explain(code: string): string {
  return reasons[code] ?? `Refused: ${code}`;
}

async function post(path: string, body: unknown, get: typeof fetch): Promise<unknown> {
  const r = await api(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }, get);
  if (!r.ok) throw new Error(await refusal(r));
  return r.json();
}

async function read<T>(path: string, get: typeof fetch): Promise<T> {
  const r = await api(path, {}, get);
  if (!r.ok) throw new Error(await refusal(r));
  return (await r.json()) as T;
}

function frame(app: HTMLElement, title: string, theme?: ThemeController): HTMLElement {
  const header = el("header", undefined, "page-header");
  mountBrand(header);
  header.append(el("h1", title, "page-title"));
  if (theme) mountThemeButton(header, theme);
  const crumbs = el("nav", undefined, "settings-nav");
  crumbs.append(link("/", "Rooms"), link("/settings", "Settings"), link("/settings/merge-policies", "Merge policies"), link("/settings/evidence", "Evidence"));
  const main = el("main", undefined, "settings");
  app.replaceChildren(header, crumbs, main);
  return main;
}

function table(head: string[], rows: (string | Node)[][]): HTMLTableElement {
  const t = el("table", undefined, "rooms settings-table");
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

// evidenceLine is a class's evidence in one line, the numbers MP7 decides on.
export function evidenceLine(e: Evidence): string {
  return `${e.clean}/${e.minimum} clean · ${e.bad} bad · ${e.immature} immature · ${e.pending} pending · bound ${e.bound.toFixed(3)} · drill ${e.drill}`;
}

export function ladderLine(e: Evidence): string {
  return e.ladder.map((s) => `${s.merges}${s.reached ? " ✓" : ""}`).join(" / ");
}

function failed(main: HTMLElement, e: unknown) {
  main.replaceChildren(el("p", e instanceof SignInRequired ? "signing in again…" : explain(e instanceof Error ? e.message : String(e)), "notice"));
}

// mountSettings renders the settings page at path.
export async function mountSettings(app: HTMLElement, path: string, get: typeof fetch = fetch, theme?: ThemeController) {
  const repo = path.match(/^\/settings\/merge-policies\/([^/]+)\/([^/]+)$/);
  if (repo) return repositoryPage(app, `${decodeURIComponent(repo[1])}/${decodeURIComponent(repo[2])}`, get, theme);
  if (path === "/settings/merge-policies") return repositories(app, get, theme);
  if (path === "/settings/evidence") return evidencePage(app, get, theme);
  const main = frame(app, "Settings", theme);
  const ul = el("ul");
  for (const [href, text] of [["/settings/merge-policies", "Merge policies: each repository's classes, their evidence and promotion"],
    ["/settings/evidence", "Evidence: every class's promotion evidence"]]) {
    const li = el("li");
    li.append(link(href, text));
    ul.append(li);
  }
  main.append(ul);
}

async function repositories(app: HTMLElement, get: typeof fetch, theme?: ThemeController) {
  const main = frame(app, "Merge policies", theme);
  try {
    const { repositories } = await read<{ repositories: string[] }>("/api/policy/repositories", get);
    if (repositories.length === 0) {
      main.append(el("p", "No repository you can read is configured."));
      return;
    }
    const ul = el("ul");
    for (const r of repositories) {
      const li = el("li");
      li.append(link(`/settings/merge-policies/${r}`, r));
      ul.append(li);
    }
    main.append(ul);
  } catch (e) { failed(main, e); }
}

async function evidencePage(app: HTMLElement, get: typeof fetch, theme?: ThemeController) {
  const main = frame(app, "Evidence", theme);
  try {
    const { repositories } = await read<{ repositories: string[] }>("/api/policy/repositories", get);
    const rows: (string | Node)[][] = [];
    for (const r of repositories) {
      const page = await read<RepositoryPage>(`/api/policy/repositories/${r}`, get);
      for (const c of page.report.classes) {
        rows.push([link(`/settings/merge-policies/${r}`, r), c.class, c.mode, evidenceLine(c.evidence), ladderLine(c.evidence),
          c.evidence.eligible ? "eligible" : (c.evidence.blockers ?? []).join(", ")]);
      }
    }
    main.append(table(["Repository", "Class", "Mode", "Evidence", "Widening 29/46/61", "Eligibility"], rows));
  } catch (e) { failed(main, e); }
}

async function repositoryPage(app: HTMLElement, repository: string, get: typeof fetch, theme?: ThemeController) {
  const main = frame(app, repository, theme);
  let page: RepositoryPage;
  try {
    page = await read<RepositoryPage>(`/api/policy/repositories/${repository}`, get);
  } catch (e) { failed(main, e); return; }
  const reload = () => void repositoryPage(app, repository, get, theme);
  const say = el("p", "", "notice");
  const r = page.report;
  main.append(el("p", `Read ${r.readAt}. A promotion changes the class's mode in ${repository}'s own .github/agent-factory.yaml, ` +
    "through a pull request you open and a human merges: Maku records the request and never edits the file. " +
    "Until the repository is cut over to its own policy, the central config stays the merge authority."), say);
  if (r.policyError) main.append(el("p", `The repository's policy file is ${r.policyError}: the central classes are shown.`, "notice"));

  const approved = (cls: string) => page.exceptions.find((x) => x.class === cls && x.state === "approved" && Date.parse(x.expiresAt ?? "") > Date.now());
  main.append(el("h2", "Classes"));
  main.append(table(["Class", "Mode", "Evidence", "Reviewers", "Widening 29/46/61", "Eligibility", ""], r.classes.map((c) => {
    const e = c.evidence;
    const reviewers = (e.shadow.reviewers ?? []).map((m) => `${m.model} ${m.mergedUnchanged}/${m.mergedUnchanged + m.changed}`).join(", ") || "-";
    const action = el("span");
    const exc = approved(c.class);
    const eligible = e.eligible || (!!exc && e.clean >= exc.minimum && e.bad === 0 && e.drill === "passed");
    if (c.proposal && page.you.maintainer && eligible) {
      const b = el("button", "Promote to live", "primary");
      b.type = "button";
      b.onclick = async () => {
        b.disabled = true;
        try {
          const out = (await post("/api/policy/promotions", { repository: r.repository, class: c.class, ...(exc && !e.eligible ? { exceptionId: exc.id } : {}) }, get)) as { proposal: Proposal };
          showProposal(main, out.proposal);
        } catch (err) { say.textContent = explain(err instanceof Error ? err.message : String(err)); b.disabled = false; }
      };
      action.append(b);
    } else if (c.proposal && page.you.maintainer && !eligible) {
      const b = el("button", "Ask an operator for an exception");
      b.type = "button";
      b.onclick = () => exceptionForm(action, r.repository, c.class, r.minimum, get, say, reload);
      action.append(b);
    } else action.textContent = c.unproposed ? c.unproposed.replaceAll("_", " ") : "";
    return [c.class, `${c.mode} (${c.source})`, evidenceLine(e), reviewers, ladderLine(e),
      eligible ? (e.eligible ? "eligible" : `eligible under exception ${exc!.id}`) : (e.blockers ?? []).join(", "), action];
  })));

  main.append(el("h2", "Promotion requests"));
  main.append(table(["Class", "Requested", "Threshold", "State", "Pull request", ""], page.promotions.map((p) => {
    const act = el("span");
    if (page.you.maintainer && (p.state === "requested" || p.state === "pr_opened")) promotionActions(act, p, get, say, reload);
    return [p.class, `${p.requestedBy} ${p.requestedAt}`, String(p.threshold) + (p.exceptionId ? ` (exception ${p.exceptionId})` : ""),
      p.state, p.pullRequest ? link(`https://github.com/${p.repository}/pull/${p.pullRequest}`, `#${p.pullRequest}`) : "-", act];
  })));

  main.append(el("h2", "Exceptions"));
  main.append(table(["Class", "Minimum", "Justification", "Requested", "State", ""], page.exceptions.map((x) => {
    const act = el("span");
    if (page.you.operator && x.state === "requested") {
      for (const d of ["approved", "refused"]) {
        const b = el("button", d === "approved" ? "Approve" : "Refuse");
        b.type = "button";
        b.onclick = async () => {
          try { await post(`/api/policy/exceptions/${x.id}`, { decision: d }, get); reload(); } catch (err) { say.textContent = explain(err instanceof Error ? err.message : String(err)); }
        };
        act.append(b);
      }
    }
    return [x.class, String(x.minimum), x.justification, `${x.requestedBy} ${x.requestedAt}`,
      x.state + (x.decidedBy ? ` by ${x.decidedBy}` : "") + (x.expiresAt ? ` until ${x.expiresAt}` : ""), act];
  })));
}

// showProposal shows the one-line change and where to submit it: GitHub's edit page takes no
// prefilled content for an existing file, so the file is shown whole, its changed line named.
export function showProposal(main: HTMLElement, p: Proposal) {
  const box = el("section", undefined, "proposal");
  box.append(el("h2", "Your promotion pull request"));
  const lead = el("p", `Change line ${p.line} of ${p.path} from mode: shadow to mode: live, as below, then propose it as a pull request: `);
  lead.append(link(p.editURL, "edit the file on GitHub"));
  box.append(lead, el("pre", p.file, "proposal-file"));
  const copy = el("button", "Copy the file");
  copy.type = "button";
  copy.onclick = () => void navigator.clipboard?.writeText(p.file);
  box.append(copy);
  main.prepend(box);
}

function promotionActions(act: HTMLElement, p: Promotion, get: typeof fetch, say: HTMLElement, reload: () => void) {
  const send = async (body: unknown) => {
    try { await post(`/api/policy/promotions/${p.id}`, body, get); reload(); } catch (err) { say.textContent = explain(err instanceof Error ? err.message : String(err)); }
  };
  if (p.state === "requested") {
    const n = el("input");
    n.type = "number";
    n.min = "1";
    n.placeholder = "PR #";
    n.setAttribute("aria-label", "pull request number");
    const b = el("button", "Record the pull request");
    b.type = "button";
    b.onclick = () => void send({ state: "pr_opened", pullRequest: Number(n.value) });
    act.append(n, b);
  } else {
    const m = el("button", "Merged");
    m.type = "button";
    m.onclick = () => void send({ state: "merged" });
    act.append(m);
  }
  const a = el("button", "Abandon");
  a.type = "button";
  a.onclick = () => void send({ state: "abandoned" });
  act.append(a);
}

function exceptionForm(at: HTMLElement, repository: string, cls: string, minimum: number, get: typeof fetch, say: HTMLElement, reload: () => void) {
  const min = el("input");
  min.type = "number";
  min.min = "1";
  min.max = String(minimum - 1);
  min.setAttribute("aria-label", "minimum clean decisions");
  const why = el("textarea");
  why.maxLength = 2000;
  why.setAttribute("aria-label", "justification");
  why.placeholder = "Why this class may go live on fewer clean decisions (20 to 2,000 characters)";
  const b = el("button", "Ask");
  b.type = "button";
  b.onclick = async () => {
    try {
      await post("/api/policy/exceptions", { repository, class: cls, minimum: Number(min.value), justification: why.value }, get);
      reload();
    } catch (err) { say.textContent = explain(err instanceof Error ? err.message : String(err)); }
  };
  at.replaceChildren(min, why, b);
}
