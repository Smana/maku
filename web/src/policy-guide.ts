// SPDX-License-Identifier: Apache-2.0

// A guided start for a new policy (correctness signals, guidance surface 2): what kind of change,
// what can check its meaning, where and how much. It writes nothing: it ends in the editor,
// pre-filled, where Check and the backtest are. The sentence comes from the factory on Check.

import { FIELD_HELP } from "./policy-help";
import { sentenceNode } from "./sentence";

type Spec = Record<string, unknown>;
export interface Kind { kind: string; signal: string; preset?: string; says: string; template?: Spec }

const renovate = { adopt: { logins: ["renovate[bot]"], branches: ["^renovate/"] } };

// KINDS is the closed list of the guided flow. A served policy of the preset's name is used as it
// is; a preset the factory does not serve starts from the template.
export const KINDS: Kind[] = [
  { kind: "Link targets in docs", signal: "Meaning checked by the `links-only` shape.", preset: "docs-links",
    says: "Can go live after 5 agreed decisions.", template: { diffShape: "links-only", modifyOnly: true, maxModifiedLines: 20, evidence: "low" } },
  { kind: "Dead links a scan finds", signal: "Meaning checked by a detector and the `links-only` shape.", preset: "dead-links",
    says: "Needs a detector workflow in the repository.",
    template: { diffShape: "links-only", modifyOnly: true, maxModifiedLines: 20, oracle: { probe: "dead-links", maxFindings: 10 }, evidence: "low" } },
  { kind: "Dependency patch updates", signal: "Meaning checked by the `semver-patch` shape.", preset: "deps-patch",
    says: "Checks the bump, not the release.",
    template: { author: renovate, diffShape: "semver-patch", modifyOnly: true, maxPatchFiles: 3, maxModifiedLines: 10, releaseAgeCheck: "renovate/stability-days", evidence: "low" } },
  { kind: "Dependency minor updates", signal: "Meaning checked by the `semver-minor` shape, in shadow only.", preset: "deps-minor",
    says: "Stays in shadow until a rendered-manifest proof exists.", template: { author: renovate, diffShape: "semver-minor", modifyOnly: true } },
  { kind: "Prose", signal: "Meaning checked by nothing: this is prose.", preset: "docs",
    says: "Can go live after 20 agreed decisions.", template: { allowedFiles: ["^.+\\.mdx?$"], maxPatchFiles: 5, maxModifiedLines: 100, maxPerDay: 2 } },
  { kind: "Entries a bot adds", signal: "Meaning checked by nothing: `added-entry` bounds the structure, the entry is prose.", preset: "kb-entries",
    says: "As prose: 20 agreed decisions before it can go live.", template: { diffShape: "added-entry", evidence: "standard" } },
  { kind: "Configuration (alerts, values, manifests)", signal: "Meaning checked by the test contract: require a plan and tests.", preset: "alert-rules",
    says: "Needs a test runner for this file type in the repository. Without it, nothing checks the meaning.",
    template: { requires: ["plan", "tests"], evidence: "standard" } },
  { kind: "Generated or formatted files", signal: "An equivalence proof would check the meaning.", says: "Available when proofs ship." },
  { kind: "Application code", signal: "Nothing can check it here.", says: "No policy covers it. A maintainer merges." },
];

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

// specFor is the kind's starting spec: the served policy of its preset's name, else its template.
export function specFor(k: Kind, catalog: { name: string; spec: Spec }[]): Spec | undefined {
  const served = catalog.find((p) => p.name === k.preset);
  if (served) return structuredClone(served.spec);
  return k.template ? structuredClone(k.template) : undefined;
}

const STEP3: [string, string, "list" | "number"][] = [
  ["allowedFiles", "Files it may change", "list"], ["maxPatchFiles", "Most files changed", "number"],
  ["maxModifiedLines", "Most lines changed", "number"], ["maxPerDay", "Most merges a day", "number"],
];

// policyGuide is the three steps; use opens the editor with the spec. start 2 opens at a kind's
// signal: where a disabled Promote's "Add a signal" lands.
export function policyGuide(catalog: { name: string; spec: Spec }[], use: (spec: Spec) => void, start: 1 | 2 = 1, kind?: Kind): HTMLElement {
  const box = el("section", undefined, "policy-guide");
  const step1 = () => {
    const list = el("ul", undefined, "guide-kinds");
    for (const k of KINDS) {
      const b = el("button", k.kind);
      b.type = "button";
      b.onclick = () => step2(k);
      const li = el("li");
      li.append(b);
      list.append(li);
    }
    box.replaceChildren(el("h3", "What kind of change?"), list);
  };
  const step2 = (k: Kind) => {
    const spec = specFor(k, catalog);
    box.replaceChildren(el("h3", "What can check its meaning?"), sentenceNode(k.signal, "guide-signal"), el("p", k.says, "guide-says"));
    const back = el("button", "Back");
    back.type = "button";
    back.onclick = step1;
    box.append(back);
    if (!spec) return;
    const next = el("button", "Next: where, and how much", "primary");
    next.type = "button";
    next.onclick = () => step3(k, spec);
    box.append(next);
  };
  const step3 = (k: Kind, spec: Spec) => {
    const form = el("div", undefined, "guide-fields");
    for (const [key, label, type] of STEP3) {
      const l = el("label", undefined, "editor-field");
      l.dataset.key = key;
      const input = type === "list" ? el("textarea") : el("input");
      if (input instanceof HTMLInputElement) { input.type = "number"; input.min = "0"; }
      const v = spec[key];
      input.value = Array.isArray(v) ? v.join("\n") : v === undefined ? "" : String(v);
      input.setAttribute("aria-label", `guide ${key}`);
      input.oninput = () => {
        const raw = input.value.trim();
        if (!raw) delete spec[key];
        else spec[key] = type === "list" ? raw.split("\n").map((x) => x.trim()).filter(Boolean) : Number(raw);
      };
      l.append(el("span", label), input, sentenceNode(FIELD_HELP[key], "field-help", "span"));
      form.append(l);
    }
    const open = el("button", "Open in the editor", "primary");
    open.type = "button";
    open.onclick = () => use(spec);
    box.replaceChildren(el("h3", "Where, and how much?"), form, el("p", `${k.kind}: ${k.says} Check the change in the editor to see the factory's sentence.`, "guide-says"), open);
  };
  if (start === 2 && kind) step2(kind);
  else step1();
  return box;
}
