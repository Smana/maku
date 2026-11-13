// SPDX-License-Identifier: Apache-2.0

// Approval memory's page (merge policy stage 5), /settings/approval-memory: the saved grants the
// viewer may read, searched by text and status, each with its exact predicate, its expiry and its
// last use, revoked, renewed or reconfirmed in place, and the whole list exportable as YAML. The
// broker decides who may do what (MP17): a refused action comes back as its bounded code, shown
// as is. Every string is a maintainer's or the broker's, so all of it enters the DOM as text.

import { api } from "./api";
import { button, el } from "./controls";

// Grant is one grant as GET /api/approval-memory/grants lists it.
export interface Grant {
  id: string;
  version: number;
  repository: string;
  class: string;
  files: string[];
  diffShape?: string;
  policyHash: string;
  originPR: number;
  createdBy: string;
  expiresAt: string;
  status: string;
  lastUsedAt?: string;
  lastUsedPR?: number;
}

export interface GrantList {
  grants: Grant[];
  unverified?: boolean;
}

// The statuses the filter offers, as the broker names them.
export const statuses = ["active", "suspended", "expired", "revoked", "disabled"];

// The actions a grant's status offers. Revoked and disabled are for good.
export function actionsFor(status: string): string[] {
  switch (status) {
    case "active":
      return ["revoke", "renew"];
    case "suspended":
      return ["reconfirm", "revoke"];
    case "expired":
      return ["renew", "revoke"];
  }
  return [];
}

const codeRE = /^[a-z][a-z_]{0,47}$/;

// fetchGrants lists the grants the viewer may read, narrowed by q (a search) and status.
export async function fetchGrants(q: string, status: string, get: typeof fetch = fetch): Promise<GrantList> {
  const p = new URLSearchParams();
  if (q) p.set("q", q);
  if (status) p.set("status", status);
  const qs = p.toString();
  const r = await api(`/api/approval-memory/grants${qs ? `?${qs}` : ""}`, {}, get);
  if (!r.ok) throw new Error(`The grants are unavailable: ${r.status}.`);
  const body = (await r.json()) as GrantList;
  return { grants: Array.isArray(body.grants) ? body.grants : [], unverified: body.unverified === true };
}

// moveGrant posts one action on a grant; a refusal's bounded code ("repository_permission",
// "grant_state", …) is the error's message.
export async function moveGrant(id: string, action: string, body: Record<string, unknown>, get: typeof fetch = fetch): Promise<Grant> {
  const r = await api(`/api/approval-memory/grants/${encodeURIComponent(id)}/${encodeURIComponent(action)}`,
    { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }, get);
  if (r.ok) return (await r.json()) as Grant;
  let code = `HTTP ${r.status}`;
  try {
    const b = (await r.json()) as { error?: unknown };
    if (typeof b.error === "string" && codeRE.test(b.error)) code = b.error;
  } catch { /* not JSON: the status says it */ }
  throw new Error(code);
}

// predicate is a grant's exact predicate in one line: what it matches, never more.
export function predicate(g: Grant): string {
  const shape = g.diffShape ? `, diff shape ${g.diffShape}` : "";
  return `${g.repository} · ${g.class} · every caught file matches ${g.files.join(" | ")}${shape} · policy ${g.policyHash}`;
}

// GrantEvent is one entry of a grant's audit, as GET /api/approval-memory/grants/{id} answers it.
export interface GrantEvent {
  event: string;
  version: number;
  actor: string;
  reason?: string;
  pr?: number;
  head?: string;
  at: string;
}

export interface GrantDetail {
  grant: Grant;
  events: GrantEvent[];
  outside: string[];
}

// Proposal is "merge and create rule" before (preview) and after (create) it is saved.
export interface Proposal {
  grant?: Grant;
  predicate: { repository: string; class: string; files: string[]; diffShape?: string; policyHash: string };
  matches: string;
  outside: string[];
  expiresAt: string;
  revoke: string;
  mergeOnce: string;
}

// RuleRequest is what the page posts for a preview or a create.
export interface RuleRequest {
  repository: string;
  class: string;
  caughtFiles: string[];
  diffShape?: string;
  policyHash: string;
  pullRequest: number;
  lifetimeDays?: number;
}

// The events that are the grant's use, and those that are its outcome.
export const usageEvents = ["used"];
export const outcomeEvents = ["suspended", "reconfirmed", "renewed", "expired", "revoked", "disabled"];

// fetchGrant reads one grant with its audit.
export async function fetchGrant(id: string, get: typeof fetch = fetch): Promise<GrantDetail> {
  const r = await api(`/api/approval-memory/grants/${encodeURIComponent(id)}`, {}, get);
  if (!r.ok) throw new Error(`The grant's history is unavailable: ${r.status}.`);
  const d = (await r.json()) as GrantDetail;
  return { grant: d.grant, events: Array.isArray(d.events) ? d.events : [], outside: Array.isArray(d.outside) ? d.outside : [] };
}

async function postRule(path: string, req: RuleRequest, get: typeof fetch): Promise<Proposal> {
  const r = await api(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(req) }, get);
  if (r.ok) return (await r.json()) as Proposal;
  let code = `HTTP ${r.status}`;
  try {
    const b = (await r.json()) as { error?: unknown };
    if (typeof b.error === "string" && codeRE.test(b.error)) code = b.error;
  } catch { /* not JSON: the status says it */ }
  throw new Error(code);
}

// previewRule asks the broker what a rule would be, saving nothing; createRule saves it.
export const previewRule = (req: RuleRequest, get: typeof fetch = fetch) => postRule("/api/approval-memory/preview", req, get);
export const createRule = (req: RuleRequest, get: typeof fetch = fetch) => postRule("/api/approval-memory/grants", req, get);

// ruleFromQuery reads a rule request from the page's query, so a pull request's decision can link
// to "merge and create rule" prefilled: ?repository=&class=&pr=&resolved=&file=…&file=…. resolved= is
// the decision record's class definition hash (B13); policy=, its name before, is read for one release.
export function ruleFromQuery(search: string): Partial<RuleRequest> {
  const q = new URLSearchParams(search);
  const pr = Number(q.get("pr"));
  return { repository: q.get("repository") ?? "", class: q.get("class") ?? "", policyHash: q.get("resolved") ?? q.get("policy") ?? "",
    pullRequest: Number.isInteger(pr) && pr > 0 ? pr : 0, caughtFiles: q.getAll("file") };
}

function day(iso?: string): string {
  return iso ? iso.slice(0, 10) : "never";
}

function eventLine(e: GrantEvent): HTMLLIElement {
  const on = e.pr ? ` on #${e.pr}` : "";
  const head = e.head ? ` at ${e.head.slice(0, 7)}` : "";
  const why = e.reason ? `: ${e.reason}` : "";
  return el("li", { textContent: `${e.at.slice(0, 16).replace("T", " ")} ${e.event} (v${e.version}) by ${e.actor}${on}${head}${why}` });
}

// history renders a grant's usage (its merges) and outcome (every change of its status).
export function history(d: GrantDetail): HTMLElement {
  const box = el("div", { className: "grant-history" });
  const used = d.events.filter((e) => usageEvents.includes(e.event));
  const outcome = d.events.filter((e) => outcomeEvents.includes(e.event));
  const section = (title: string, evs: GrantEvent[], none: string) => {
    const ul = el("ul");
    ul.append(...(evs.length ? evs.map(eventLine) : [el("li", { textContent: none })]));
    return [el("h3", { textContent: title }), ul];
  };
  box.append(...section("Usage history", used, "Never used."), ...section("Outcome history", outcome, "No change since it was created."));
  return box;
}

function row(g: Grant, act: (g: Grant, action: string) => void, get: typeof fetch): HTMLLIElement {
  const li = el("li", { className: "grant" });
  li.dataset.id = g.id;
  li.dataset.status = g.status;
  const head = el("div", { className: "grant-head" });
  head.append(el("code", { textContent: g.id.slice(0, 8), title: `${g.id} version ${g.version}` }), ` ${g.status} `);
  const facts = el("div", { className: "grant-facts", textContent:
    `from #${g.originPR} by ${g.createdBy} · expires ${day(g.expiresAt)} · last used ${day(g.lastUsedAt)}${g.lastUsedPR ? ` on #${g.lastUsedPR}` : ""}` });
  const actions = el("div", { className: "grant-actions" });
  for (const a of actionsFor(g.status)) actions.append(button(a, a, () => act(g, a)));
  const slot = el("div");
  actions.append(button("history", "history", async () => {
    if (slot.firstChild) { slot.replaceChildren(); return; }
    try {
      slot.replaceChildren(history(await fetchGrant(g.id, get)));
    } catch (e) {
      slot.replaceChildren(el("p", { className: "grants-note", textContent: (e as Error).message }));
    }
  }));
  li.append(head, el("p", { className: "grant-predicate", textContent: predicate(g) }), facts, actions, slot);
  return li;
}

function field(label: string, input: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement): HTMLLabelElement {
  const l = el("label", { className: "rule-field" });
  l.append(el("span", { textContent: label }), input);
  return l;
}

// proposalView is the preview a maintainer reads before saving: the exact predicate, the future
// pull requests it would match, what stays outside it, its expiry and how to revoke it, and what
// "merge once" does instead.
export function proposalView(p: Proposal): HTMLElement {
  const box = el("section", { className: "rule-preview" });
  const outside = el("ul");
  outside.append(...p.outside.map((o) => el("li", { textContent: o })));
  const pred = p.predicate;
  box.append(el("h3", { textContent: p.grant ? `Rule saved: ${p.grant.id.slice(0, 8)}` : "The rule, before it is saved" }),
    el("p", { className: "grant-predicate", textContent:
      `${pred.repository} · ${pred.class} · every caught file matches ${pred.files.join(" | ")}${pred.diffShape ? `, diff shape ${pred.diffShape}` : ""} · policy ${pred.policyHash}` }),
    el("h4", { textContent: "Future pull requests it would match" }), el("p", { textContent: p.matches }),
    el("h4", { textContent: "Still decided outside it" }), outside,
    el("p", { textContent: `Expires ${day(p.expiresAt)}. Revoke: ${p.revoke}.` }),
    el("p", { className: "grants-note", textContent: `Merge once instead: ${p.mergeOnce}.` }));
  return box;
}

// mountRule renders "merge and create rule" for a pull request in a live class's approval subset.
function mountRule(root: HTMLElement, get: typeof fetch, saved: () => Promise<void>) {
  const pre = ruleFromQuery(location.search);
  const repository = el("input", { name: "repository", placeholder: "owner/name", value: pre.repository ?? "" });
  const klass = el("input", { name: "class", placeholder: "docs-broad", value: pre.class ?? "" });
  const pr = el("input", { name: "pr", type: "number", min: "1", value: pre.pullRequest ? String(pre.pullRequest) : "" });
  const policy = el("input", { name: "policy", placeholder: "the decision record's resolved=", value: pre.policyHash ?? "" });
  const files = el("textarea", { name: "files", rows: 3, placeholder: "the files the approval subset caught, one a line", value: (pre.caughtFiles ?? []).join("\n") });
  const shape = el("select", { name: "diffShape" });
  shape.append(...["", "links-only", "semver-patch", "semver-minor"].map((v) => el("option", { value: v, textContent: v || "any diff shape" })));
  const days = el("input", { name: "lifetimeDays", type: "number", min: "1", max: "365", value: "90" });
  const out = el("div", { className: "rule-out" });
  const req = (): RuleRequest => ({ repository: repository.value.trim().toLowerCase(), class: klass.value.trim(), policyHash: policy.value.trim(),
    pullRequest: Number(pr.value), caughtFiles: files.value.split("\n").map((f) => f.trim()).filter(Boolean),
    diffShape: shape.value || undefined, lifetimeDays: Number(days.value) || undefined });
  const create = button("Merge and create rule", "create", async () => {
    try {
      out.replaceChildren(proposalView(await createRule(req(), get)));
      create.hidden = true;
      await saved();
    } catch (e) {
      out.append(el("p", { className: "grants-note", textContent: `Not saved: ${(e as Error).message}` }));
    }
  });
  create.hidden = true;
  const preview = button("Preview rule", "preview", async () => {
    try {
      out.replaceChildren(proposalView(await previewRule(req(), get)));
      create.hidden = false;
    } catch (e) {
      out.replaceChildren(el("p", { className: "grants-note", textContent: `No preview: ${(e as Error).message}` }));
      create.hidden = true;
    }
  });
  const form = el("div", { className: "rule-form" });
  form.append(field("Repository", repository), field("Class", klass), field("Pull request", pr), field("Policy", policy),
    field("Caught files", files), field("Diff shape", shape), field("Lifetime (days)", days));
  const actions = el("div", { className: "grant-actions" });
  actions.append(preview, create);
  root.append(el("h2", { textContent: "Merge and create rule" }),
    el("p", { textContent: "For a pull request a live class sent to approval: save its approval as a rule for the same files. Each file becomes an exact pattern; preview it before saving." }),
    form, actions, out);
}

// mountApprovalMemory renders the page into root and reads the list once; every action reads it again.
export function mountApprovalMemory(root: HTMLElement, get: typeof fetch = fetch): { refresh(): Promise<void> } {
  const search = el("input", { type: "search", placeholder: "Search grants", name: "q" });
  const status = el("select", { name: "status" });
  status.append(el("option", { value: "", textContent: "any status" }), ...statuses.map((s) => el("option", { value: s, textContent: s })));
  const exportLink = el("a", { href: "/api/approval-memory/export", textContent: "Export as YAML" });
  const note = el("p", { className: "grants-note" });
  const list = el("ul", { className: "grants" });
  const rule = el("section", { className: "rule" });
  root.classList.add("approval-memory");
  root.replaceChildren(el("h1", { textContent: "Approval memory" }),
    el("p", { textContent: "Saved grants let a live class merge inside its approval subset. Each matches its exact predicate only; the base gate, the class's conditions, its deny rules, the breaker and the caps still apply." }),
    el("div", { className: "grants-filter" }), note, list, rule);
  root.querySelector(".grants-filter")!.append(search, status, exportLink);

  const act = async (g: Grant, action: string) => {
    if (action === "revoke" && !confirm(`Revoke grant ${g.id.slice(0, 8)}? It stops matching at once, for good.`)) return;
    try {
      await moveGrant(g.id, action, { version: g.version }, get);
    } catch (e) {
      note.textContent = `${action} refused: ${(e as Error).message}`;
      return;
    }
    await refresh();
  };
  const refresh = async () => {
    try {
      const l = await fetchGrants(search.value.trim(), status.value, get);
      list.replaceChildren(...l.grants.map((g) => row(g, act, get)));
      note.textContent = l.unverified
        ? "Some repositories' access could not be verified: their grants are not listed. Retry."
        : l.grants.length ? "" : "No grant matches.";
    } catch (e) {
      note.textContent = (e as Error).message;
    }
  };
  search.oninput = () => void refresh();
  status.onchange = () => void refresh();
  mountRule(rule, get, refresh);
  void refresh();
  return { refresh };
}
