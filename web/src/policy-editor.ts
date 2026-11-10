// SPDX-License-Identifier: Apache-2.0

// The merge-policy editor (merge policy stage 6): a guided form over one class of the repository's
// .github/agent-factory.yaml and an expert YAML view of the whole file. Neither writes anything:
// "Check" sends the file to the broker, the factory validates it strictly against the central
// guardrails (mergepolicy.Parse, Validate, Resolve), compares it structurally with the current
// policy and simulates it, and the answer is the file to submit as the maintainer's own pull
// request. factory: is never in the form.

import templateFile from "./policy-templates.json";
import { canonical, highlight, keyFix } from "./policy-yaml";
import { describeClass } from "./policy-describe";
import { EXAMPLES } from "./policy-examples";
import { docsLink, type Field, FIELDS, type Group, GROUPS } from "./policy-fields";

export type Leaf = Record<string, unknown>;
export interface ClassPolicy {
  mode: string; author: string; adopt?: { logins: string[]; branches: string[] }; harness: string;
  harnessAllowed?: string[]; escalation?: { outOfBudget?: string; notConverged?: string };
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
  // For a repository served from a MergePolicy: the factory's manifest of the checked text.
  manifest?: string; manifestPath?: string;
}
export interface Outcome { verdict: string; reason?: string }

// The spec's class templates, all in shadow: the editor's starting points. They live in a JSON file
// so the factory's own check runs on them (mergepolicy.TestEveryEditorTemplatePassesCheck): a semver
// shape needs modifyOnly, and semver-minor stays shadow only (M2).
export const templates: Record<string, () => ClassPolicy> = Object.fromEntries(
  Object.entries(templateFile as Record<string, ClassPolicy>).map(([k, v]) => [k, () => structuredClone(v)]));

// profilesFor are the central profiles a class of author may reference: an adopted class only a
// profile that runs nothing (template none), an agents class only one that runs. adopted unknown
// (an older broker), every profile.
export function profilesFor(author: string, profiles: string[], adopted?: string[] | null): string[] {
  if (!adopted) return profiles;
  return profiles.filter((p) => adopted.includes(p) === (author === "adopt"));
}

// The diff shapes the form offers. A leaf the field table has no field for, and every any-branch,
// is kept as it is and edited in the YAML view.
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

// fileYAML is the whole file as the form writes it: keys in the file's order, so one file is one text.
export function fileYAML(f: PolicyFile): string {
  return toYAML(canonical({ factory: f.factory, classes: f.classes })) + "\n";
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

// hasValue reports whether c sets key, the field table's key (dotted for nested keys): a group
// with no key set is folded behind its add button.
function hasValue(c: ClassPolicy, key: string): boolean {
  const all = c.conditions?.all ?? [];
  switch (key) {
    case "adopt.logins": return !!c.adopt?.logins.length;
    case "adopt.branches": return !!c.adopt?.branches.length;
    case "harnessAllowed": return !!c.harnessAllowed?.length;
    case "escalation.outOfBudget": return !!c.escalation?.outOfBudget;
    case "escalation.notConverged": return !!c.escalation?.notConverged;
    case "protectedFiles": return !!c.conditions?.deny?.length;
    case "changedFiles": return !!c.requiresApproval?.length;
    case "maxPerDay": return !!c.limits?.maxPerDay;
    case "followUpWindow": return !!c.monitor?.followUpWindow;
    default: return all.some((l) => key in l);
  }
}

// The list fields whose lines are regular expressions: each line is checked as one as it is typed.
const patternKeys = new Set(["adopt.branches", "allowedFiles", "companionFiles", "protectedFiles", "changedFiles"]);
// A class named review has no mode: like an off class, it takes harnessAllowed and escalation.
const reviewOnly = (key: string) => key === "harnessAllowed" || key.startsWith("escalation.");
let fieldIds = 0;

// classForm renders the guided form for cls, calling change with the class as edited. Fields are
// grouped by the question each answers (policy-fields); a group with nothing set folds behind its
// add button. Above them, the class as a sentence, rewritten at each edit. name is the class's
// name (empty for a new one), from the example it starts from, if any.
export function classForm(cls: ClassPolicy, profiles: string[], change: (c: ClassPolicy) => void, adopted?: string[] | null,
  guardrails: Record<string, unknown> = {}, name = "", from = ""): HTMLElement {
  const c: ClassPolicy = structuredClone(cls);
  c.conditions ??= {};
  const form = el("fieldset", undefined, "editor-form");
  form.append(el("legend", from ? `New class from ${from}` : name ? `Editing ${name}` : "New class"));
  const story = el("div", undefined, "class-story");
  story.setAttribute("aria-live", "polite");
  const tell = () => {
    const s = describeClass(c, guardrails);
    const ul = el("ul");
    for (const w of s.when) ul.append(el("li", w));
    story.replaceChildren(el("p", s.lead), ul, ...s.also.map((a) => el("p", a)));
  };
  const emit = () => { tell(); change(structuredClone(c)); };
  form.append(story);

  // field wraps one control, its label, key, help and guide link in a div; data-key is the selector
  // tests use. Only the label (name and key) is a <label for>: the help and the guide link sit
  // beside it, so clicking them never toggles a checkbox. The control is named by the label's
  // name alone and described by the help line.
  const field = (key: string, label: string, help: string, anchor: string, input: HTMLElement, error?: HTMLElement) => {
    const id = `field-${++fieldIds}`;
    const l = el("div", undefined, "editor-field");
    l.dataset.key = key;
    input.id = `${id}-control`;
    const top = el("label", undefined, "field-top");
    top.htmlFor = input.id;
    const name = el("span", label, "field-label");
    name.id = `${id}-label`;
    top.append(name, el("code", key, "field-key"));
    const docs = el("a", "Learn more", "field-docs");
    docs.href = docsLink(anchor);
    docs.target = "_blank";
    docs.rel = "noopener";
    const h = el("p", help, "field-help");
    h.id = `${id}-help`;
    h.append(" ", docs);
    input.setAttribute("aria-labelledby", name.id);
    input.setAttribute("aria-describedby", error ? `${h.id} ${error.id = `${id}-error`}` : h.id);
    l.append(top, input, ...(error ? [error] : []), h);
    return l;
  };
  const selectOf = (options: string[], value: string, set: (v: string) => void) => {
    const s = el("select");
    for (const o of options) {
      const opt = el("option", o || "(none)");
      opt.value = o;
      s.append(opt);
    }
    s.value = value;
    s.onchange = () => { set(s.value); emit(); };
    return s;
  };
  const textOf = (value: string, set: (v: string) => void) => {
    const i = el("input");
    i.value = value;
    i.oninput = () => { set(i.value); emit(); };
    return i;
  };
  // listOf is a textarea of one value a line; a pattern list marks the lines that are not a
  // regular expression, the first one named.
  const listOf = (value: string[], set: (v: string[]) => void, patterns: boolean) => {
    const t = el("textarea");
    t.value = value.join("\n");
    const err = el("p", "", "field-error");
    err.hidden = true;
    const check = () => {
      if (!patterns) return;
      const bad = t.value.split("\n").findIndex((x) => { if (!x.trim()) return false; try { new RegExp(x.trim()); return false; } catch { return true; } });
      if (bad < 0) { t.removeAttribute("aria-invalid"); err.hidden = true; err.textContent = ""; return; }
      t.setAttribute("aria-invalid", "true");
      err.textContent = `Line ${bad + 1} is not a regular expression`;
      err.hidden = false;
    };
    t.oninput = () => { check(); set(lines(t.value)); emit(); };
    check();
    return { input: t, error: patterns ? err : undefined };
  };
  const numberOf = (value: unknown, max: unknown, set: (v: number) => void) => {
    const i = el("input");
    i.type = "number";
    i.min = "1";
    if (typeof max === "number" && max > 0) i.max = String(max);
    i.value = typeof value === "number" ? String(value) : "";
    i.oninput = () => { set(i.value === "" ? NaN : Number(i.value)); emit(); };
    return i;
  };

  const all = () => c.conditions!.all ?? [];
  const setAll = (k: string) => (v: unknown) => { c.conditions!.all = setLeaf(all(), k, v); };
  const escalate = (k: "outOfBudget" | "notConverged") => (v: string) => {
    const e = { ...c.escalation, [k]: v.trim() };
    if (!e[k]) delete e[k];
    if (Object.keys(e).length) c.escalation = e; else delete c.escalation;
  };
  // control builds the input of f, writing through the setter its key has always had.
  const control = (f: Field): { input: HTMLElement; error?: HTMLElement } => {
    const list = (v: string[], set: (v: string[]) => void) => listOf(v, set, patternKeys.has(f.key));
    switch (f.key) {
      case "adopt.logins": return list(c.adopt?.logins ?? [], (v) => { c.adopt!.logins = v; });
      case "adopt.branches": return list(c.adopt?.branches ?? [], (v) => { c.adopt!.branches = v; });
      case "harnessAllowed": return list(c.harnessAllowed ?? [], (v) => { if (v.length) c.harnessAllowed = v; else delete c.harnessAllowed; });
      case "escalation.outOfBudget": return { input: textOf(c.escalation?.outOfBudget ?? "", escalate("outOfBudget")) };
      case "escalation.notConverged": return { input: textOf(c.escalation?.notConverged ?? "", escalate("notConverged")) };
      case "protectedFiles": return list((c.conditions!.deny ?? []).flatMap((d) => d.protectedFiles), (p) => { c.conditions!.deny = p.length ? [{ protectedFiles: p }] : undefined; });
      case "changedFiles": return list((c.requiresApproval ?? []).flatMap((a) => a.changedFiles), (p) => { c.requiresApproval = p.length ? [{ changedFiles: p }] : undefined; });
      case "maxPerDay": return { input: numberOf(c.limits?.maxPerDay, guardrails.maxPerDay, (v) => { c.limits = isNaN(v) || v === 0 ? undefined : { maxPerDay: v }; }) };
      case "followUpWindow": return { input: textOf(duration(c.monitor?.followUpWindow), (v) => {
        if (v.trim() === "") delete c.monitor;
        else c.monitor = { ...(c.monitor ?? {}), followUpWindow: v.trim() };
      }) };
    }
    switch (f.kind) {
      case "list": return list((leaf(all(), f.key) as string[] | undefined) ?? [], setAll(f.key));
      case "number": return { input: numberOf(leaf(all(), f.key), guardrails[f.key], setAll(f.key)) };
      case "text": return { input: textOf((leaf(all(), f.key) as string) ?? "", setAll(f.key)) };
      case "bool": {
        const b = el("input");
        b.type = "checkbox";
        b.checked = leaf(all(), f.key) === true;
        b.onchange = () => { setAll(f.key)(b.checked); emit(); };
        return { input: b };
      }
      case "shape": {
        // An agents class takes links-only only; a shape the text already has stays selectable.
        const v = (leaf(all(), f.key) as string) ?? "";
        const fit = c.author === "adopt" ? diffShapes : ["", "links-only"];
        // A shape decides whether companionFiles is shown: the groups are drawn again.
        return { input: selectOf(fit.includes(v) ? fit : [...fit, v], v, (s) => { setAll(f.key)(s); redraw(); }) };
      }
    }
  };
  const put = (f: { key: string; label: string; help: string; anchor: string }, ctl: { input: HTMLElement; error?: HTMLElement }) =>
    field(f.key, f.label, f.help, f.anchor, ctl.input, ctl.error);

  // Mode stays above the groups; author and harness open "Who writes it", drawn with it, since the
  // author decides which profiles and which fields the class takes.
  form.append(put({ key: "mode", label: "Mode", anchor: "shadow-and-live",
    help: "Live merges, shadow records what it would do and merges nothing, off decides nothing." },
  { input: selectOf(["shadow", "live", "off"], c.mode, (v) => { c.mode = v; redraw(); }) }));
  const author = () => put({ key: "author", label: "Written by", anchor: "a-class",
    help: "Agents: the factory's own runs. Adopt: another App's pull requests, such as Renovate's." },
  { input: selectOf(["agents", "adopt"], c.author, (v) => {
    c.author = v;
    if (v === "adopt") c.adopt ??= { logins: [], branches: [] }; else delete c.adopt;
    if (!profilesFor(v, profiles, adopted).includes(c.harness)) c.harness = profilesFor(v, profiles, adopted)[0] ?? "";
    redraw();
  }) });
  const harness = () => {
    const fit = profilesFor(c.author, profiles, adopted);
    const options = fit.includes(c.harness) || c.harness === "" ? fit : [c.harness, ...fit];
    return put({ key: "harness", label: "Harness profile", anchor: "central-config",
      help: "How the factory runs this class's tasks, one of the profiles the factory defines." },
    { input: selectOf(["", ...options], c.harness, (v) => { c.harness = v; }) });
  };

  const groupsAt = el("div", undefined, "editor-groups");
  form.append(groupsAt);
  if ((c.conditions.any ?? []).length > 0 || leaf(all(), "oracle") !== undefined) {
    form.append(el("p", "This class also has any-branches or an oracle: they are kept as they are; edit them in the YAML view.", "notice"));
  }
  const shown = (f: Field) => f.shown(c) || (name === "review" && reviewOnly(f.key));
  // A group once open stays open, so emptying its last field does not fold it under the cursor.
  const open = new Set<Group>();
  // The sections stay; a redraw refills each one under its heading.
  const sections = GROUPS.map((g) => {
    const sec = el("section", undefined, "editor-group");
    sec.dataset.group = g.id;
    groupsAt.append(sec);
    return { g, sec, h: el("h3", g.title) };
  });
  const redraw = () => {
    // Redrawing replaces the controls: the one that had focus gets it back by its key.
    const had = (document.activeElement?.closest(".editor-field") as HTMLElement | null)?.dataset.key;
    for (const { g, sec, h } of sections) {
      const fs = FIELDS.filter((f) => f.group === g.id && shown(f));
      const top = g.id === "who" ? [author(), harness()] : [];
      if (g.add && !open.has(g.id) && !fs.some((f) => hasValue(c, f.key))) {
        const b = el("button", g.add, "group-add");
        b.type = "button";
        b.onclick = () => {
          open.add(g.id);
          redraw();
          (sec.querySelector(":is(input, textarea, select)") as HTMLElement | null)?.focus();
        };
        sec.replaceChildren(h, ...top, b);
        continue;
      }
      open.add(g.id);
      sec.replaceChildren(h, ...top, ...fs.map((f) => put(f, control(f))));
    }
    if (had) (groupsAt.querySelector(`[data-key="${had}"] :is(input, textarea, select)`) as HTMLElement | null)?.focus();
  };
  redraw();
  tell();
  return form;
}

// exampleGallery shows each template as an example: its level, why one would start from it, what
// it does in words and its YAML; start(name) begins a new class from it.
export function exampleGallery(start: (name: string) => void, guardrails: Record<string, unknown> = {}): HTMLElement {
  const box = el("section", undefined, "example-gallery");
  box.hidden = true;
  for (const level of ["simple", "advanced"] as const) {
    box.append(el("h3", level === "simple" ? "Simple" : "Advanced"));
    const row = el("div", undefined, "example-row");
    for (const ex of EXAMPLES.filter((e) => e.level === level)) {
      const t = templates[ex.name]();
      const s = describeClass(t, guardrails);
      const card = el("article", undefined, "example-card");
      card.dataset.example = ex.name;
      const ul = el("ul");
      for (const w of s.when) ul.append(el("li", w));
      const yaml = el("details");
      const pre = el("pre", undefined, "example-yaml");
      pre.append(highlight(toYAML({ [ex.name]: t })));
      yaml.append(el("summary", "YAML"), pre);
      const go = el("button", "Start from this");
      go.type = "button";
      go.onclick = () => start(ex.name);
      card.append(el("h4", ex.name), el("p", ex.why, "example-why"), el("p", s.lead), ul, ...s.also.map((a) => el("p", a)), yaml, go);
      row.append(card);
    }
    box.append(row);
  }
  return box;
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
// at is the line of the checked text an error is about, if any; go shows that line, and fix
// replaces a refused key on it with one the error lists.
export function previewView(p: Preview, at: (msg: string) => number | undefined = () => undefined, go: (line: number) => void = () => {},
  fix?: (line: number, from: string, to: string) => void): HTMLElement {
  const box = el("section", undefined, "editor-preview");
  if (!p.valid) {
    box.append(el("h3", "The file does not pass"));
    const ul = el("ul");
    for (const e of p.errors ?? []) {
      const li = el("li");
      const line = at(e);
      if (line !== undefined) {
        const b = el("button", `Line ${line}`, "error-line");
        b.type = "button";
        b.onclick = () => go(line);
        li.append(b, " ");
      }
      li.append(e);
      const k = keyFix(e);
      if (line !== undefined && fix && k?.keys.length) {
        const keys = el("span", " Replace with: ", "key-fixes");
        for (const to of k.keys) {
          const b = el("button", to, "key-fix");
          b.type = "button";
          b.onclick = () => fix(line, k.key, to);
          keys.append(b, " ");
        }
        li.append(keys);
      }
      ul.append(li);
    }
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
    const pre = el("pre", undefined, "proposal-file");
    pre.append(highlight(p.proposal.file));
    const file = p.proposal.file;
    box.append(lead, pre, ...fileActions(() => file, p.proposal.path));
  }
  return box;
}

// fileActions copies text or downloads it as the file at path. The label says it was copied:
// the change of word, not a motion, is the signal. text may be an answer still to come
// (a check the factory runs first): undefined is no text, and the copy says so.
export function fileActions(text: () => string | Promise<string | undefined>, path: string, label = "Copy"): HTMLElement[] {
  const copy = el("button", label, "copy");
  copy.type = "button";
  let reset: ReturnType<typeof setTimeout> | undefined;
  copy.onclick = async () => {
    try {
      const t = await text();
      if (t === undefined) copy.textContent = "No manifest: see the check below";
      else {
        await navigator.clipboard.writeText(t);
        copy.textContent = "Copied";
      }
    } catch {
      copy.textContent = "Copy failed: select the text";
    }
    clearTimeout(reset);
    reset = setTimeout(() => { copy.textContent = label; }, 1500);
  };
  const download = el("a", "Download", "download");
  download.setAttribute("download", path.split("/").at(-1)!);
  download.href = "#";
  const href = (t: string) => `data:application/yaml;charset=utf-8,${encodeURIComponent(t)}`;
  // The href is set at the click, so the download is the text as it is then. An answer still to
  // come holds the click, then clicks again once the href is the answer.
  let ready = false;
  download.onclick = (e) => {
    if (ready) { ready = false; return; }
    const t = text();
    if (typeof t === "string") { download.href = href(t); return; }
    e.preventDefault();
    void t.then((v) => {
      if (v === undefined) return;
      download.href = href(v);
      ready = true;
      download.click();
    }, () => undefined);
  };
  return [copy, download];
}

export interface YamlView {
  root: HTMLElement;
  text: HTMLTextAreaElement;
  set(v: string): void;
  mark(lines: Iterable<number>): void;
  say(...parts: (string | Node)[]): void;
  reveal(line: number): void;
  replaceKey(line: number, from: string, to: string): void;
}

// yamlEditor is the expert view: a textarea over its own highlighted copy, with line numbers, the
// lines an error is about marked, a status line, and copy and download. input is each edit.
// out, when given, is what copy and download give instead of the text as it is: the file the
// factory builds of the text (a MergePolicy manifest), named path, its copy button labelled label;
// undefined when the factory builds none.
export interface YamlOut { path: string; label: string; get: (text: string) => Promise<string | undefined> }
export function yamlEditor(initial: string, path: string, input: (text: string) => void, out?: YamlOut): YamlView {
  const text = el("textarea", undefined, "expert-yaml");
  text.setAttribute("aria-label", "expert YAML view");
  text.spellcheck = false;
  text.wrap = "off";
  const gutter = el("pre", undefined, "yaml-gutter");
  const hl = el("pre", undefined, "yaml-hl");
  gutter.setAttribute("aria-hidden", "true");
  hl.setAttribute("aria-hidden", "true");
  const status = el("p", "", "yaml-status");
  status.setAttribute("role", "status");
  let marks = new Set<number>();
  const paint = () => {
    hl.replaceChildren(highlight(text.value, marks));
    gutter.replaceChildren(...text.value.split("\n").flatMap((_, i) => [el("span", String(i + 1), marks.has(i + 1) ? "y-error" : undefined), "\n"]));
  };
  text.onscroll = () => {
    hl.scrollTop = gutter.scrollTop = text.scrollTop;
    hl.scrollLeft = text.scrollLeft;
  };
  text.oninput = () => {
    marks = new Set();
    input(text.value);
    paint();
  };
  const bar = el("div", undefined, "yaml-bar");
  bar.append(el("span", out?.path ?? path, "yaml-name"),
    ...(out ? fileActions(() => out.get(text.value), out.path, out.label) : fileActions(() => text.value, path)));
  const stack = el("div", undefined, "yaml-stack");
  stack.append(hl, text);
  const body = el("div", undefined, "yaml-body");
  body.append(gutter, stack);
  const root = el("div", undefined, "yaml-editor");
  root.append(bar, body, status);
  text.value = initial;
  paint();
  return {
    root, text,
    set(v) { text.value = v; marks = new Set(); paint(); },
    mark(lines) { marks = new Set(lines); paint(); },
    say(...parts) { status.replaceChildren(...parts); },
    reveal(line) {
      const ls = text.value.split("\n");
      const start = ls.slice(0, line - 1).reduce((n, l) => n + l.length + 1, 0);
      text.focus();
      text.setSelectionRange(start, start + (ls[line - 1]?.length ?? 0));
      const lh = parseFloat(getComputedStyle(text).lineHeight);
      if (lh) text.scrollTop = Math.max(0, (line - 3) * lh);
      text.onscroll?.(new Event("scroll"));
    },
    // replaceKey renames from to to on line, as an edit of the text: the form follows.
    replaceKey(line, from, to) {
      const ls = text.value.split("\n");
      const esc = from.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
      ls[line - 1] = ls[line - 1].replace(new RegExp(`^(\\s*(?:-\\s+)*["']?)${esc}(["']?\\s*:)`), (_, a, b) => a + to + b);
      text.value = ls.join("\n");
      text.dispatchEvent(new Event("input"));
    },
  };
}
