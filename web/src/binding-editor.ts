// SPDX-License-Identifier: Apache-2.0

// The bindings editor of a bound repository (B4, B16): each binding's mode and the parameters this
// repository supplies, the policy's meaning beside them read-only. Check is the factory's: each
// binding's resolution and the Repository manifest to commit ("Copy repository-<name>.yaml"). A
// policy is edited on its own page, with its own reviewers.

import { BINDING_FIELDS, MEANING_KEYS, bindingFieldValid, type BindingField, type BindingGroup } from "./policy-fields";
import { fileActions } from "./policy-editor";
import { backtestView, type Backtest } from "./backtest";
import { explain, post } from "./settings-api";
import type { Binding, Report } from "./settings";

export interface BindingResult { name: string; mode: string; resolves: boolean; reason?: string; message?: string; change?: string; why?: string[] }
export interface BindingPreview { valid: boolean; errors?: string[]; bindings?: BindingResult[]; manifest?: string; manifestPath?: string }

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

const lines = (v: string) => v.split("\n").map((x) => x.trim()).filter(Boolean);

// set writes value at key of b, or removes key when value is empty: a binding writes only what it
// supplies, in the order it already has.
function set(b: Binding, key: string, value: unknown) {
  const empty = value === undefined || value === "" || (Array.isArray(value) && value.length === 0) || Number.isNaN(value);
  const r = b as unknown as Record<string, unknown>;
  if (empty) delete r[key];
  else r[key] = value;
}

function parameterField(b: Binding, f: BindingField, change: () => void): HTMLLabelElement {
  const l = el("label", undefined, "editor-field");
  l.dataset.key = f.key;
  const cur = (b as unknown as Record<string, unknown>)[f.key];
  let input: HTMLInputElement | HTMLTextAreaElement;
  if (f.kind === "list" || f.kind === "approval") {
    input = el("textarea");
    input.rows = 2;
    input.value = f.kind === "approval"
      ? ((cur as { changedFiles: string[] }[] | undefined) ?? []).map((a) => a.changedFiles.join("\n")).join("\n\n")
      : ((cur as string[] | undefined) ?? []).join("\n");
    input.oninput = () => {
      set(b, f.key, f.kind === "approval"
        ? input.value.split(/\n\s*\n/).map(lines).filter((v) => v.length).map((v) => ({ changedFiles: v }))
        : lines(input.value));
      change();
    };
  } else {
    input = el("input");
    input.type = f.kind === "number" ? "number" : "text";
    if (f.kind === "number") input.min = "0";
    input.value = cur === undefined ? "" : String(cur);
    input.oninput = () => {
      set(b, f.key, f.kind === "number" ? (input.value === "" ? undefined : Number(input.value)) : input.value.trim());
      change();
    };
  }
  input.setAttribute("aria-label", `${b.name} ${f.key}`);
  l.append(el("span", f.label), input);
  return l;
}

function modeLadder(b: Binding, change: () => void): HTMLElement {
  const group = el("div", undefined, "mode-ladder");
  group.setAttribute("role", "radiogroup");
  group.setAttribute("aria-label", `${b.name} mode`);
  const current = b.mode === "off" ? "disabled" : b.mode;
  for (const [value, label] of [["disabled", "Off"], ["shadow", "Shadow"], ["live", "Live"]] as const) {
    const item = el("label", undefined, "mode-choice");
    const input = el("input");
    input.type = "radio";
    input.name = `mode-${b.name}`;
    input.value = value;
    input.checked = current === value;
    input.onclick = () => { b.mode = value; change(); };
    item.append(input, el("span", label));
    group.append(item);
  }
  return group;
}

// bindingForm keeps the fields a binding needs in view and the valid optional fields in two folds.
function bindingForm(b: Binding, meaning: Record<string, unknown> | undefined, change: () => void): HTMLFieldSetElement {
  const fs = el("fieldset", undefined, "binding-form");
  fs.dataset.binding = b.name;
  fs.append(el("legend", b.name), modeLadder(b, change));
  if (b.name !== "review") {
    const fields = BINDING_FIELDS.filter((f) => bindingFieldValid(f.key, meaning));
    fs.append(...fields.filter((f) => f.group === "essential").map((f) => parameterField(b, f, change)));
    for (const [group, title] of [["shape", "Shape and checks"], ["expert", "Expert"]] as [BindingGroup, string][]) {
      const folded = fields.filter((f) => f.group === group);
      if (!folded.length) continue;
      const details = el("details", undefined, `binding-${group}`);
      details.append(el("summary", title), ...folded.map((f) => parameterField(b, f, change)));
      fs.append(details);
    }
  }
  const fixed = MEANING_KEYS.filter((k) => meaning?.[k] !== undefined && meaning?.[k] !== "" && meaning?.[k] !== false);
  if (fixed.length) {
    const definition = el("details", undefined, "binding-definition");
    definition.append(el("summary", "Policy definition"));
    const ul = el("ul", undefined, "binding-meaning");
    for (const k of fixed) {
      const v = meaning![k];
      ul.append(el("li", `${k}: ${typeof v === "object" ? JSON.stringify(v) : String(v)}`));
    }
    definition.append(ul);
    fs.append(definition);
  }
  return fs;
}

// bindingEditor edits r's spec.mergePolicies; meaning is each policy's spec, by name, for its
// read-only rows (none when unread).
export function bindingEditor(r: Report, meaning: Record<string, Record<string, unknown>>, maintainer: boolean, get: typeof fetch): HTMLElement {
  const box = el("section", undefined, "policy-editor binding-editor");
  const bindings: Binding[] = structuredClone((r.bindings ?? []).map((b) => b.parameters));
  const out = el("div", undefined, "binding-preview");
  const changed = () => out.replaceChildren(); // an answer about other bindings is not this text's
  box.append(el("p", `The bindings of ${r.repositoryObject ?? r.repository}: each policy's mode here, and the parameters this repository supplies.`, "editor-meta"));
  for (const b of bindings) box.append(bindingForm(b, meaning[b.name], changed));
  const check = el("button", "Check the bindings", "primary");
  check.type = "button";
  check.disabled = !maintainer;
  check.onclick = async () => {
    check.disabled = true;
    try {
      const pv = (await post(`/api/policy/repositories/${r.repository}/preview`, { kind: "binding", bindings }, get)) as BindingPreview;
      out.replaceChildren(...previewOf(pv));
    } catch (e) { out.replaceChildren(el("p", explain(e instanceof Error ? e.message : String(e)), "notice")); }
    check.disabled = !maintainer;
  };
  const backtest = el("button", "Backtest the last 30 days");
  backtest.type = "button";
  backtest.disabled = !maintainer;
  backtest.onclick = async () => {
    backtest.disabled = true;
    try {
      const bt = (await post(`/api/policy/repositories/${r.repository}/backtest`, { kind: "binding", bindings, since: "30d" }, get)) as Backtest;
      out.replaceChildren(...backtestView(bt));
    } catch (e) { out.replaceChildren(el("p", explain(e instanceof Error ? e.message : String(e)), "notice")); }
    backtest.disabled = !maintainer;
  };
  const actions = el("div", undefined, "editor-actions");
  actions.append(check, backtest);
  box.append(actions);
  if (!maintainer) box.append(el("p", "Only a maintainer of the repository may check its bindings.", "notice"));
  box.append(out, el("p", "Copy or download the Repository manifest and commit it by pull request; Flux applies it. Maku never edits the cluster or the repository.", "editor-footer"));
  return box;
}

function previewOf(pv: BindingPreview): Node[] {
  if (!pv.valid) {
    const ul = el("ul", undefined, "preview-errors");
    for (const e of pv.errors ?? []) ul.append(el("li", e));
    return [el("p", "The factory refuses these bindings:", "notice"), ul];
  }
  const ul = el("ul", undefined, "binding-results");
  for (const b of pv.bindings ?? []) {
    ul.append(el("li", b.resolves
      ? `${b.name}: ${b.mode}, ${b.change ?? "resolves"}${b.why?.length ? ` (${b.why.join("; ")})` : ""}`
      : `${b.name}: does not resolve: ${[b.reason, b.message].filter(Boolean).join(": ")}`));
  }
  const path = pv.manifestPath ?? "repository.yaml";
  const actions = el("p", undefined, "file-actions");
  actions.append(...fileActions(() => pv.manifest ?? "", path, `Copy ${path}`));
  // The manifest itself, as the factory built it: what the pull request commits.
  return [ul, actions, el("pre", pv.manifest ?? "", "binding-manifest")];
}
