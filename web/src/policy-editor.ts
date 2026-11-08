// SPDX-License-Identifier: Apache-2.0

// The merge-policy editor (merge policy stage 6): a guided form over one class of the repository's
// .github/agent-factory.yaml and an expert YAML view of the whole file. Neither writes anything:
// "Check" sends the file to the broker, the factory validates it strictly against the central
// guardrails (mergepolicy.Parse, Validate, Resolve), compares it structurally with the current
// policy and simulates it, and the answer is the file to submit as the maintainer's own pull
// request. factory: is never in the form.

export type Leaf = Record<string, unknown>;
export interface ClassPolicy {
  mode: string; author: string; adopt?: { logins: string[]; branches: string[] }; harness: string;
  conditions?: { all?: Leaf[]; any?: Leaf[][]; deny?: { protectedFiles: string[] }[] };
  requiresApproval?: { changedFiles: string[] }[];
  limits?: { maxPerDay?: number };
  monitor?: Record<string, unknown>;
}
export interface PolicyFile { factory: string; classes?: Record<string, ClassPolicy> }

export interface Preview {
  valid: boolean; errors?: string[];
  structural?: { class: string; kind: string; why?: string[] }[];
  simulation: { enabled: boolean; rows?: { name: string; class: string; central: Outcome; repository: Outcome; change: string; conforms: boolean }[]; counts?: Record<string, number> };
  strictness?: { class: string; refused: number; refusedMergedUnchanged: number }[];
  proposal?: { path: string; file: string; editURL: string };
}
export interface Outcome { verdict: string; reason?: string }

// The spec's class templates, all in shadow: the editor's starting points.
export const templates: Record<string, () => ClassPolicy> = {
  "docs-broad": () => ({ mode: "shadow", author: "agents", harness: "", conditions: {
    all: [{ allowedFiles: ["^docs/.+\\.md$", "^README\\.md$"] }, { maxPatchFiles: 10 }, { maxModifiedLines: 200 },
      { holdoutChecks: ["Check the documentation links 🔗"] }],
    deny: [{ protectedFiles: ["^docs/superpowers/.+$", "^docs/specs/.+$", "^docs/security/.+$"] }] },
    requiresApproval: [{ changedFiles: ["^docs/architecture/.+$"] }], limits: { maxPerDay: 2 }, monitor: { followUpWindow: "168h" } }),
  "deps-patch": () => ({ mode: "shadow", author: "adopt", adopt: { logins: ["renovate[bot]"], branches: ["^renovate/"] }, harness: "",
    conditions: { all: [{ diffShape: "semver-patch" }, { maxPatchFiles: 3 }, { maxModifiedLines: 10 }, { releaseAgeCheck: "renovate/stability-days" }] },
    limits: { maxPerDay: 2 }, monitor: { followUpWindow: "168h" } }),
  "generated-files": () => ({ mode: "shadow", author: "agents", harness: "", conditions: {
    all: [{ allowedFiles: ["^gen/.+$"] }, { holdoutChecks: ["Regenerate and compare"] }, { modifyOnly: true }] } }),
};

// profilesFor are the central profiles a class of author may reference: an adopted class only a
// profile that runs nothing (template none), an agents class only one that runs. adopted unknown
// (an older broker), every profile.
export function profilesFor(author: string, profiles: string[], adopted?: string[] | null): string[] {
  if (!adopted) return profiles;
  return profiles.filter((p) => adopted.includes(p) === (author === "adopt"));
}

// The leaves the form edits, in the order it shows them. Any other leaf, and every any-branch, is
// kept as it is and edited in the expert view.
const listLeaves = ["allowedFiles", "holdoutChecks", "companionFiles"] as const;
const numberLeaves = ["maxPatchFiles", "maxModifiedLines"] as const;
const textLeaves = ["releaseAgeCheck", "requiredLabel"] as const;
export const diffShapes = ["", "links-only", "semver-patch", "semver-minor", "added-entry"];

function leaf(all: Leaf[], key: string): unknown {
  return all.find((l) => key in l)?.[key];
}

// setLeaf sets (or, for an empty value, removes) the leaf of key in all, in place of the old one.
export function setLeaf(all: Leaf[], key: string, v: unknown): Leaf[] {
  const empty = v === undefined || v === "" || v === false || (Array.isArray(v) && v.length === 0) || (typeof v === "number" && isNaN(v));
  const i = all.findIndex((l) => key in l);
  const out = all.filter((_, j) => j !== i);
  if (empty) return out;
  const at = i < 0 ? out.length : i;
  out.splice(at, 0, { [key]: v });
  return out;
}

function scalar(v: unknown): string {
  return typeof v === "string" ? JSON.stringify(v) : String(v);
}

function isScalar(v: unknown): boolean {
  return v === null || typeof v !== "object";
}

// toYAML writes a policy file as YAML: block mappings, scalar lists in flow, every string
// double-quoted (JSON's escapes are YAML's), so a pattern is never read as anything else.
export function toYAML(v: unknown, indent = 0): string {
  const pad = " ".repeat(indent);
  const lines: string[] = [];
  if (Array.isArray(v)) {
    for (const item of v) {
      if (isScalar(item)) { lines.push(`${pad}- ${scalar(item)}`); continue; }
      const sub = toYAML(item, indent + 2).split("\n");
      sub[0] = `${pad}- ${sub[0].slice(indent + 2)}`;
      lines.push(...sub);
    }
    return lines.join("\n");
  }
  for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
    if (val === undefined || val === null) continue;
    if (isScalar(val)) lines.push(`${pad}${k}: ${scalar(val)}`);
    else if (Array.isArray(val) && val.every(isScalar)) lines.push(`${pad}${k}: [${val.map(scalar).join(", ")}]`);
    else if (!Array.isArray(val) && Object.keys(val as object).length === 0) continue;
    else lines.push(`${pad}${k}:`, toYAML(val, indent + 2));
  }
  return lines.join("\n");
}

// fileYAML is the whole file, factory first, as the form writes it.
export function fileYAML(f: PolicyFile): string {
  return toYAML({ factory: f.factory, classes: f.classes }) + "\n";
}

// normalize drops what the form must not write back as Go marshalled it: a duration's 0m0s.
function duration(s: unknown): string {
  return typeof s === "string" ? s.replace(/(\d+h)0m0s$/, "$1").replace(/(\d+m)0s$/, "$1") : "";
}

const lines = (s: string) => s.split("\n").map((x) => x.trim()).filter(Boolean);

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

function labelled(label: string, input: HTMLElement): HTMLLabelElement {
  const l = el("label", undefined, "editor-field");
  l.append(el("span", label), input);
  return l;
}

// classForm renders the guided form for cls, calling change with the class as edited.
export function classForm(cls: ClassPolicy, profiles: string[], change: (c: ClassPolicy) => void, adopted?: string[] | null): HTMLElement {
  const c: ClassPolicy = structuredClone(cls);
  c.conditions ??= {};
  const form = el("fieldset", undefined, "editor-form");
  form.append(el("legend", "Class"));
  const emit = () => change(structuredClone(c));

  const select = (label: string, value: string, options: string[], set: (v: string) => void) => {
    const s = el("select");
    for (const o of options) {
      const opt = el("option", o || "(none)");
      opt.value = o;
      s.append(opt);
    }
    s.value = value;
    s.onchange = () => { set(s.value); emit(); };
    form.append(labelled(label, s));
    return s;
  };
  const text = (label: string, value: string, set: (v: string) => void, area = false) => {
    const i = area ? el("textarea") : el("input");
    i.value = value;
    i.oninput = () => { set(i.value); emit(); };
    form.append(labelled(label, i));
    return i;
  };
  const number = (label: string, value: unknown, set: (v: number) => void) => {
    const i = el("input");
    i.type = "number";
    i.min = "0";
    i.value = typeof value === "number" ? String(value) : "";
    i.oninput = () => { set(i.value === "" ? NaN : Number(i.value)); emit(); };
    form.append(labelled(label, i));
  };

  select("mode", c.mode, ["shadow", "live", "off"], (v) => { c.mode = v; });
  let harness: HTMLSelectElement | undefined;
  const fill = () => {
    const fit = profilesFor(c.author, profiles, adopted);
    const options = fit.includes(c.harness) || c.harness === "" ? fit : [c.harness, ...fit];
    harness!.replaceChildren(...["", ...options].map((o) => { const opt = el("option", o || "(none)"); opt.value = o; return opt; }));
    harness!.value = c.harness;
  };
  select("author", c.author, ["agents", "adopt"], (v) => {
    c.author = v;
    if (v === "adopt") c.adopt ??= { logins: [], branches: [] }; else delete c.adopt;
    if (!profilesFor(v, profiles, adopted).includes(c.harness)) c.harness = profilesFor(v, profiles, adopted)[0] ?? "";
    fill();
  });
  if (c.adopt) {
    text("adopted logins (one a line)", c.adopt.logins.join("\n"), (v) => { c.adopt!.logins = lines(v); }, true);
    text("adopted branches (one a line)", c.adopt.branches.join("\n"), (v) => { c.adopt!.branches = lines(v); }, true);
  }
  harness = select("harness profile (the central list)", c.harness, [], (v) => { c.harness = v; });
  fill();

  form.append(el("h4", "Conditions (all must hold)"));
  const all = () => c.conditions!.all ?? [];
  for (const k of listLeaves) {
    text(`${k} (one a line)`, ((leaf(all(), k) as string[] | undefined) ?? []).join("\n"), (v) => { c.conditions!.all = setLeaf(all(), k, lines(v)); }, true);
  }
  for (const k of numberLeaves) number(k, leaf(all(), k), (v) => { c.conditions!.all = setLeaf(all(), k, v); });
  const mo = el("input");
  mo.type = "checkbox";
  mo.checked = leaf(all(), "modifyOnly") === true;
  mo.onchange = () => { c.conditions!.all = setLeaf(all(), "modifyOnly", mo.checked); emit(); };
  form.append(labelled("modifyOnly", mo));
  select("diffShape", (leaf(all(), "diffShape") as string) ?? "", diffShapes, (v) => { c.conditions!.all = setLeaf(all(), "diffShape", v); });
  for (const k of textLeaves) text(k, (leaf(all(), k) as string) ?? "", (v) => { c.conditions!.all = setLeaf(all(), k, v); });
  if ((c.conditions.any ?? []).length > 0 || leaf(all(), "oracle") !== undefined) {
    form.append(el("p", "This class also has any-branches or an oracle: they are kept as they are; edit them in the expert view.", "notice"));
  }

  form.append(el("h4", "Deny (any match refuses)"));
  text("protectedFiles (one a line)", (c.conditions.deny ?? []).flatMap((d) => d.protectedFiles).join("\n"), (v) => {
    const p = lines(v);
    c.conditions!.deny = p.length ? [{ protectedFiles: p }] : undefined;
  }, true);
  form.append(el("h4", "Requires approval (asks a human even when live)"));
  text("changedFiles (one a line)", (c.requiresApproval ?? []).flatMap((a) => a.changedFiles).join("\n"), (v) => {
    const p = lines(v);
    c.requiresApproval = p.length ? [{ changedFiles: p }] : undefined;
  }, true);
  form.append(el("h4", "Limits and monitor"));
  number("limits.maxPerDay", c.limits?.maxPerDay, (v) => { c.limits = isNaN(v) || v === 0 ? undefined : { maxPerDay: v }; });
  text("monitor.followUpWindow (e.g. 168h; empty: no monitor)", duration(c.monitor?.followUpWindow), (v) => {
    if (v.trim() === "") delete c.monitor;
    else c.monitor = { ...(c.monitor ?? {}), followUpWindow: v.trim() };
  });
  return form;
}

// cleanClass is a class as the form edited it, with the empty sections dropped.
export function cleanClass(c: ClassPolicy): ClassPolicy {
  const out = structuredClone(c);
  if (out.conditions) {
    if (!out.conditions.all?.length) delete out.conditions.all;
    if (!out.conditions.deny?.length) delete out.conditions.deny;
    if (!out.conditions.any?.length) delete out.conditions.any;
    if (Object.keys(out.conditions).length === 0) delete out.conditions;
  }
  if (out.monitor && typeof out.monitor.followUpWindow === "string") out.monitor.followUpWindow = duration(out.monitor.followUpWindow);
  return out;
}

// previewView renders the factory's answer: errors, or the structural change, the simulation, the
// strictness counterfactual and the file to submit.
export function previewView(p: Preview): HTMLElement {
  const box = el("section", undefined, "editor-preview");
  if (!p.valid) {
    box.append(el("h3", "The file does not pass"));
    const ul = el("ul");
    for (const e of p.errors ?? []) ul.append(el("li", e));
    box.append(ul);
    return box;
  }
  box.append(el("h3", "Structural change"));
  const sul = el("ul");
  for (const c of p.structural ?? []) sul.append(el("li", `${c.class}: ${c.kind}${c.why?.length ? ` (${c.why.join("; ")})` : ""}`, `structural-${c.kind}`));
  box.append(sul);
  if ((p.structural ?? []).some((c) => c.kind === "relaxation")) {
    box.append(el("p", "A relaxation returns the class to shadow until it earns its evidence again (MP8).", "notice"));
  }
  box.append(el("h3", "Simulation"));
  if (!p.simulation.enabled) {
    box.append(el("p", "Simulation is off (policyManagement.simulation): no impact preview.", "notice"));
  } else {
    const counts = p.simulation.counts ?? {};
    box.append(el("p", ["newly_auto_merged", "newly_blocked", "newly_approval_required", "unchanged", "changed"]
      .map((k) => `${k.replaceAll("_", " ")}: ${counts[k] ?? 0}`).join(" · "), "simulation-counts"));
    const t = el("table", undefined, "rooms settings-table");
    const head = el("tr");
    for (const h of ["Fixture", "Class", "Central", "Repository", "Change"]) head.append(el("th", h));
    t.append(head);
    for (const r of p.simulation.rows ?? []) {
      const tr = el("tr");
      for (const v of [r.name, r.class, `${r.central.verdict}:${r.central.reason ?? "armed"}`, `${r.repository.verdict}:${r.repository.reason ?? "armed"}`,
        r.change + (r.conforms ? "" : " (fixture recorded another central decision)")]) tr.append(el("td", v));
      t.append(tr);
    }
    box.append(t);
  }
  box.append(el("h3", "Strictness counterfactual"));
  const cul = el("ul");
  for (const s of p.strictness ?? []) cul.append(el("li", `${s.class}: ${s.refused} refused by a class row, ${s.refusedMergedUnchanged} of them then merged unchanged by a human`));
  box.append(cul);
  if (p.proposal) {
    box.append(el("h3", "Submit it as your pull request"));
    const lead = el("p", `Replace ${p.proposal.path} with the file below, then propose the change: `);
    const a = el("a", "edit the file on GitHub");
    a.href = p.proposal.editURL;
    lead.append(a);
    box.append(lead, el("pre", p.proposal.file, "proposal-file"));
    const copy = el("button", "Copy the file");
    copy.type = "button";
    copy.onclick = () => void navigator.clipboard?.writeText(p.proposal!.file);
    box.append(copy);
  }
  return box;
}
