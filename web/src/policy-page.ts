// SPDX-License-Identifier: Apache-2.0

// A merge policy's page (B16), /settings/merge-policies/policies/<name>: the class definition the
// factory serves, what each binding must supply, the consumers the reader reads, and an editor whose
// output is the policy's manifest and each consumer's change (B11). Read-only (D9): the operator
// commits the manifest; Maku never edits the cluster.

import { CORE_SCHEMA, load } from "js-yaml";
import { explain, post, read } from "./settings-api";
import { fileActions, toYAML } from "./policy-editor";
import { settingsFrame } from "./settings";
import type { ThemeController } from "./theme";

export interface ConsumerStatus { repository: string; mode?: string; servedGeneration?: number; accepted: boolean; reason?: string }
export interface PolicyReport {
  name: string; generation: number; spec: Record<string, unknown> & { description?: string; requires?: string[] };
  manifest: string; manifestPath: string;
  status: { observedGeneration?: number; consumerCount: number; currentCount: number; liveCount?: number; consumers?: ConsumerStatus[] };
}
export interface ConsumerImpact { repository: string; mode: string; change: string; why?: string[]; resolves: boolean; reason?: string }
export interface PolicyPreview { valid: boolean; errors?: string[]; consumers?: ConsumerImpact[]; manifest?: string; manifestPath?: string }

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

function table(head: string[], rows: (string | Node)[][], cls: string): HTMLTableElement {
  const t = el("table", undefined, `rooms settings-table ${cls}`);
  const tr = el("tr");
  for (const h of head) tr.append(el("th", h));
  t.append(el("thead"), el("tbody"));
  t.tHead!.append(tr);
  for (const r of rows) {
    const row = el("tr");
    for (const [i, c] of r.entries()) {
      const td = el("td");
      td.dataset.label = head[i] ?? "";
      td.append(c);
      row.append(td);
    }
    t.tBodies[0].append(row);
  }
  return t;
}

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

// countsWords is "2 consumers · 1 current": the consumers the reader sees, and those serving the
// policy's current generation.
export const countsWords = (p: PolicyReport) => `${plural(p.status.consumerCount, "consumer")} · ${p.status.currentCount} current`;

// policiesTable lists the policies on the merge policies page, each linked to its own.
export function policiesTable(ps: PolicyReport[]): HTMLTableElement {
  return table(["Policy", "Generation", "Consumers"], ps.map((p) => {
    const a = el("a", p.name);
    a.href = `/settings/merge-policies/policies/${encodeURIComponent(p.name)}`;
    return [a, String(p.generation), countsWords(p)];
  }), "policy-table");
}

// policyPage renders the policy name.
export async function policyPage(app: HTMLElement, name: string, get: typeof fetch, theme?: ThemeController) {
  const main = settingsFrame(app, name, "/settings/merge-policies", theme);
  let p: PolicyReport;
  try { p = await read<PolicyReport>(`/api/policy/policies/${encodeURIComponent(name)}`, get); }
  catch (e) { main.replaceChildren(el("p", explain(e instanceof Error ? e.message : String(e)), "notice")); return; }
  main.append(el("p", countsWords(p), "policy-counts"));
  if (p.spec.description) main.append(el("p", p.spec.description, "class-intent"));
  main.append(el("p", `Generation ${p.generation}.`, "policy-generation"));
  if (p.spec.requires?.length) main.append(el("p", `Each binding must supply: ${p.spec.requires.join(", ")}`, "policy-requires"));
  main.append(el("h2", "Definition"), el("pre", toYAML(p.spec), "policy-definition"));
  main.append(el("h2", "Consumers"));
  const cs = p.status.consumers ?? [];
  if (cs.length === 0) main.append(el("p", "No repository you read binds this policy."));
  else main.append(table(["Repository", "Mode", "Served generation", "Accepted", "Reason"],
    cs.map((c) => [c.repository, c.mode ?? "", c.servedGeneration ? String(c.servedGeneration) : "-", c.accepted ? "yes" : "no", c.reason ?? ""]), "consumers"));
  main.append(el("h2", "Edit this policy"), policyEditor(p, get));
}

// policyEditor edits the policy's spec as YAML; Check is the factory's: the manifest to commit, and
// each consumer's structural change, live consumers first (Q5).
export function policyEditor(p: PolicyReport, get: typeof fetch): HTMLElement {
  const box = el("section", undefined, "policy-editor");
  const text = el("textarea", undefined, "policy-spec");
  text.value = toYAML(p.spec) + "\n";
  text.rows = Math.min(30, text.value.split("\n").length + 2);
  text.setAttribute("aria-label", `${p.name} spec`);
  const check = el("button", "Check the change and its consumers", "primary");
  check.type = "button";
  const out = el("div", undefined, "policy-preview");
  check.onclick = async () => {
    let spec: unknown;
    try { spec = load(text.value, { schema: CORE_SCHEMA }) ?? {}; }
    catch (e) { out.replaceChildren(el("p", `The text does not parse: ${e instanceof Error ? e.message : String(e)}`, "notice")); return; }
    check.disabled = true;
    try {
      const pv = (await post(`/api/policy/policies/${encodeURIComponent(p.name)}/preview`, { spec }, get)) as PolicyPreview;
      out.replaceChildren(...previewOf(pv));
    } catch (e) { out.replaceChildren(el("p", explain(e instanceof Error ? e.message : String(e)), "notice")); }
    check.disabled = false;
  };
  box.append(el("p", "The policy's spec, as its manifest holds it. A binding's mode and parameters are its repository's, edited on the repository's page.", "editor-meta"),
    text, check, out);
  return box;
}

function previewOf(pv: PolicyPreview): Node[] {
  if (!pv.valid) {
    const ul = el("ul", undefined, "preview-errors");
    for (const e of pv.errors ?? []) ul.append(el("li", e));
    return [el("p", "The factory refuses this policy:", "notice"), ul];
  }
  const out: Node[] = [];
  const path = pv.manifestPath ?? "mergepolicy.yaml";
  const actions = el("p", undefined, "file-actions");
  actions.append(...fileActions(() => pv.manifest ?? "", path, `Copy ${path}`));
  out.push(actions);
  const cs = [...(pv.consumers ?? [])].sort((a, b) => Number(b.mode === "live") - Number(a.mode === "live"));
  if (cs.length === 0) out.push(el("p", "No consumer you read binds this policy."));
  else out.push(table(["Repository", "Mode", "Change", "Why"], cs.map((c) => [c.repository, c.mode === "live" ? "LIVE" : c.mode,
    c.resolves ? c.change : `does not resolve: ${c.reason ?? ""}`, (c.why ?? []).join("; ")]), "impact"));
  return out;
}
