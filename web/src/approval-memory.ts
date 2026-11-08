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

function day(iso?: string): string {
  return iso ? iso.slice(0, 10) : "never";
}

function row(g: Grant, act: (g: Grant, action: string) => void): HTMLLIElement {
  const li = el("li", { className: "grant" });
  li.dataset.id = g.id;
  li.dataset.status = g.status;
  const head = el("div", { className: "grant-head" });
  head.append(el("code", { textContent: g.id.slice(0, 8), title: `${g.id} version ${g.version}` }), ` ${g.status} `);
  const facts = el("div", { className: "grant-facts", textContent:
    `from #${g.originPR} by ${g.createdBy} · expires ${day(g.expiresAt)} · last used ${day(g.lastUsedAt)}${g.lastUsedPR ? ` on #${g.lastUsedPR}` : ""}` });
  const actions = el("div", { className: "grant-actions" });
  for (const a of actionsFor(g.status)) actions.append(button(a, a, () => act(g, a)));
  li.append(head, el("p", { className: "grant-predicate", textContent: predicate(g) }), facts, actions);
  return li;
}

// mountApprovalMemory renders the page into root and reads the list once; every action reads it again.
export function mountApprovalMemory(root: HTMLElement, get: typeof fetch = fetch): { refresh(): Promise<void> } {
  const search = el("input", { type: "search", placeholder: "Search grants", name: "q" });
  const status = el("select", { name: "status" });
  status.append(el("option", { value: "", textContent: "any status" }), ...statuses.map((s) => el("option", { value: s, textContent: s })));
  const exportLink = el("a", { href: "/api/approval-memory/export", textContent: "Export as YAML" });
  const note = el("p", { className: "grants-note" });
  const list = el("ul", { className: "grants" });
  root.replaceChildren(el("h1", { textContent: "Approval memory" }),
    el("p", { textContent: "Saved grants let a live class merge inside its approval subset. Each matches its exact predicate only; the base gate, the class's conditions, its deny rules, the breaker and the caps still apply." }),
    el("div", { className: "grants-filter" }), note, list);
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
      list.replaceChildren(...l.grants.map((g) => row(g, act)));
      note.textContent = l.unverified
        ? "Some repositories' access could not be verified: their grants are not listed. Retry."
        : l.grants.length ? "" : "No grant matches.";
    } catch (e) {
      note.textContent = (e as Error).message;
    }
  };
  search.oninput = () => void refresh();
  status.onchange = () => void refresh();
  void refresh();
  return { refresh };
}
