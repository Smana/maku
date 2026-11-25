// SPDX-License-Identifier: Apache-2.0

// A reusable policy, the repositories that bind it, and an editor built from presets and valid fields.
// The factory validates every preview; committing the resulting manifest remains a maintainer's job.
import { explain, post, read } from "./settings-api";
import { fileActions, toYAML } from "./policy-editor";
import { backtestView, type Backtest } from "./backtest";
import { patternDetails, policyWords } from "./policy-words";
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
type Spec = PolicyReport["spec"];

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

function table(head: string[], rows: (string | Node)[][], cls: string): HTMLTableElement {
  // A Why column with nothing in it would take half the width from the repository's name.
  const why = head.indexOf("Why");
  if (why >= 0 && rows.every((r) => !(r[why] instanceof Node) && !String(r[why] ?? "").trim())) {
    head = head.filter((_, i) => i !== why);
    rows = rows.map((r) => r.filter((_, i) => i !== why));
  }
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
export const countsWords = (p: PolicyReport) => plural(p.status.consumerCount, "repository", "repositories");

export function policiesTable(ps: PolicyReport[]): HTMLTableElement {
  return table(["Policy", "What it allows", "Repositories using it"], ps.map((p) => {
    const a = el("a", p.name);
    a.href = `/settings/merge-policies/policies/${encodeURIComponent(p.name)}`;
    return [a, policyWords(p.spec), countsWords(p)];
  }), "policy-table");
}

export async function policyPage(app: HTMLElement, name: string, get: typeof fetch, theme?: ThemeController) {
  const main = settingsFrame(app, name, "/settings/merge-policies", theme);
  let p: PolicyReport;
  try { p = await read<PolicyReport>(`/api/policy/policies/${encodeURIComponent(name)}`, get); }
  catch (e) { main.replaceChildren(el("p", explain(e instanceof Error ? e.message : String(e)), "notice")); return; }
  const catalog = await read<{ policies: PolicyReport[] }>("/api/policy/policies", get).then((x) => x.policies, () => [p]);
  const repositories = await read<{ repositories: string[] }>("/api/policy/repositories", get).then((x) => x.repositories, () => []);
  main.append(el("p", policyWords(p.spec), "policy-sentence"));
  const patterns = Array.isArray(p.spec.allowedFiles) ? p.spec.allowedFiles as string[] : [];
  if (patterns.length) main.append(patternDetails(patterns));
  const certainty = el("details", undefined, "policy-certainty");
  certainty.append(el("summary", "How sure is this?"), el("p", countsWords(p), "policy-counts"));
  if (p.spec.requires?.length) certainty.append(el("p", `Each binding must supply: ${p.spec.requires.join(", ")}`, "policy-requires"));
  main.append(certainty);
  const definition = el("details", undefined, "policy-source");
  definition.append(el("summary", "Show the YAML"), el("pre", toYAML(p.spec), "policy-definition"));
  main.append(definition, el("h2", "Repositories using it"));
  const cs = p.status.consumers ?? [];
  if (cs.length === 0) main.append(el("p", "No repository you read binds this policy."));
  else main.append(table(["Repository", "Mode", "Status", "Why"],
    cs.map((c) => [c.repository, c.mode ?? "", c.accepted ? "accepted" : "refused", c.reason ?? ""]), "consumers"));
  main.append(el("h2", "Edit this policy"), policyEditor(p, get, catalog, repositories));
}

function set(spec: Spec, key: string, value: unknown) {
  const empty = value === undefined || value === "" || value === false || (Array.isArray(value) && value.length === 0) || Number.isNaN(value);
  if (empty) delete spec[key];
  else spec[key] = value;
}
const lines = (v: string) => v.split("\n").map((x) => x.trim()).filter(Boolean);

type FieldKind = "text" | "list" | "number" | "boolean" | "approval" | "shape" | "evidence";
function field(spec: Spec, key: string, label: string, kind: FieldKind, changed: () => void): HTMLLabelElement {
  const l = el("label", undefined, "editor-field");
  l.dataset.key = key;
  const value = spec[key];
  let input: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement;
  if (kind === "shape" || kind === "evidence") {
    input = el("select");
    const choices = kind === "shape" ? ["", "links-only", "semver-patch", "semver-minor", "added-entry"] : ["standard", "low"];
    for (const choice of choices) {
      const option = el("option", choice || "No required diff shape");
      option.value = choice;
      input.append(option);
    }
    input.value = String(value ?? (kind === "evidence" ? "standard" : ""));
    input.onchange = () => { set(spec, key, input.value); changed(); };
  } else if (kind === "boolean") {
    const checkbox = el("input");
    input = checkbox;
    checkbox.type = "checkbox";
    checkbox.checked = value === true;
    checkbox.onchange = () => { set(spec, key, checkbox.checked); changed(); };
  } else if (kind === "list" || kind === "approval") {
    input = el("textarea");
    input.rows = 2;
    input.value = kind === "approval"
      ? ((value as { changedFiles: string[] }[] | undefined) ?? []).map((a) => a.changedFiles.join("\n")).join("\n\n")
      : ((value as string[] | undefined) ?? []).join("\n");
    input.oninput = () => {
      set(spec, key, kind === "approval" ? input.value.split(/\n\s*\n/).map(lines).filter((v) => v.length).map((v) => ({ changedFiles: v })) : lines(input.value));
      changed();
    };
  } else {
    input = el("input");
    input.type = kind === "number" ? "number" : "text";
    if (kind === "number") input.min = "0";
    input.value = value === undefined ? "" : String(value);
    input.oninput = () => { set(spec, key, kind === "number" && input.value !== "" ? Number(input.value) : input.value); changed(); };
  }
  input.setAttribute("aria-label", `policy ${key}`);
  l.append(el("span", label), input);
  return l;
}

function presets(catalog: PolicyReport[], use: (p: PolicyReport) => void): HTMLElement {
  const box = el("section", undefined, "policy-presets");
  box.append(el("h3", "Start from a shipped policy"));
  for (const p of catalog) {
    const item = el("details", undefined, "policy-preset");
    item.append(el("summary", p.name), el("p", policyWords(p.spec)));
    const yaml = el("details", undefined, "preset-yaml");
    yaml.append(el("summary", "Show the YAML"), el("pre", toYAML(p.spec)));
    const button = el("button", `Use ${p.name}`);
    button.type = "button";
    button.onclick = () => use(p);
    item.append(yaml, button);
    box.append(item);
  }
  return box;
}

// policyEditor edits real fields, never a mode or a repository-only field. Every preview is checked.
export function policyEditor(p: PolicyReport, get: typeof fetch, catalog: PolicyReport[] = [p], repositories: string[] = []): HTMLElement {
  const box = el("section", undefined, "policy-editor");
  let spec: Spec = structuredClone(p.spec);
  const out = el("div", undefined, "policy-preview");
  const draw = () => {
    const changed = () => out.replaceChildren();
    const form = el("div", undefined, "policy-fields");
    for (const [key, label, kind] of [["description", "Purpose", "text"], ["allowedFiles", "Files it may change", "list"], ["maxPerDay", "Most merges a day", "number"]] as [string, string, FieldKind][]) {
      form.append(field(spec, key, label, kind, changed));
    }
    const shape = el("details", undefined, "policy-shape");
    shape.append(el("summary", "Shape and checks"));
    for (const [key, label, kind] of [["diffShape", "Required diff shape", "shape"], ["modifyOnly", "Edit files only", "boolean"], ["holdoutChecks", "Checks that must pass", "list"]] as [string, string, FieldKind][]) {
      shape.append(field(spec, key, label, kind, changed));
    }
    if (spec.diffShape === "added-entry") shape.append(field(spec, "companionFiles", "Files that may grow beside the entry", "list", changed));
    if (spec.diffShape === "added-entry" || spec.author) shape.append(field(spec, "requiredLabel", "Required label", "text", changed));
    form.append(shape);
    const expert = el("details", undefined, "policy-expert");
    expert.append(el("summary", "Expert"));
    for (const [key, label, kind] of [["maxPatchFiles", "Most files changed", "number"], ["maxModifiedLines", "Most lines changed", "number"],
      ["protectedFiles", "Files it must never touch", "list"], ["requiresApproval", "Changes that need approval", "approval"]] as [string, string, FieldKind][]) {
      expert.append(field(spec, key, label, kind, changed));
    }
    if (spec.diffShape || spec.oracle) expert.append(field(spec, "evidence", "Evidence required before promotion", "evidence", changed));
    if (spec.author) expert.append(field(spec, "releaseAgeCheck", "Release-age check", "text", changed));
    form.append(expert);
    const check = el("button", "Check the change", "primary");
    check.type = "button";
    check.onclick = async () => {
      check.disabled = true;
      try {
        const pv = (await post(`/api/policy/policies/${encodeURIComponent(p.name)}/preview`, { spec }, get)) as PolicyPreview;
        out.replaceChildren(...previewOf(pv));
      } catch (e) { out.replaceChildren(el("p", explain(e instanceof Error ? e.message : String(e)), "notice")); }
      check.disabled = false;
    };
    const actions = el("div", undefined, "editor-actions");
    actions.append(check);
    if (repositories.length) {
      const repository = el("select");
      repository.setAttribute("aria-label", "Backtest repository");
      for (const name of repositories) {
        const option = el("option", name);
        option.value = name;
        repository.append(option);
      }
      const label = el("label", undefined, "backtest-repository");
      label.append(el("span", "Backtest against"), repository);
      const backtest = el("button", "Backtest the last 30 days");
      backtest.type = "button";
      backtest.onclick = async () => {
        backtest.disabled = true;
        try {
          const bt = (await post(`/api/policy/repositories/${repository.value}/backtest`,
            { kind: "policy", policy: p.name, spec, since: "30d" }, get)) as Backtest;
          out.replaceChildren(...backtestView(bt));
        } catch (e) { out.replaceChildren(el("p", explain(e instanceof Error ? e.message : String(e)), "notice")); }
        backtest.disabled = false;
      };
      actions.append(label, backtest);
    }
    box.replaceChildren(el("p", "Choose a preset or edit the fields. Repository modes and parameters stay on each repository's page.", "editor-meta"),
      presets(catalog, (next) => { spec = structuredClone(next.spec); out.replaceChildren(); draw(); }),
      el("p", policyWords(spec), "policy-sentence"), form, actions, out);
  };
  draw();
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
  if (cs.length) out.push(table(["Repository", "Mode", "Change", "Why"], cs.map((c) => [c.repository, c.mode === "live" ? "LIVE" : c.mode,
    c.resolves ? c.change : `does not resolve: ${c.reason ?? ""}`, (c.why ?? []).join("; ")]), "impact"));
  return out;
}
