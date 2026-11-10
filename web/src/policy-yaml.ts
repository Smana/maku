// SPDX-License-Identifier: Apache-2.0

// The expert YAML view's text side: the file's key order, reading the text back into the form,
// highlighting it, and finding the line an error from the factory's check is about. The factory's
// parser (mergepolicy.Parse) stays the judge of the file; this only keeps the form and the text
// in step and points at places.

import { CORE_SCHEMA, load, type YAMLException } from "js-yaml";
import type { PolicyFile } from "./policy-editor";

// A Shape is where a value sits in the file: a mapping's known fields in the Go types' order, a
// map's values (class names, sorted as Go writes them), or a list's items.
export interface Shape { fields?: Record<string, Shape | null>; values?: Shape; items?: Shape }

const within: Shape = { fields: { within: null } };
const leaf: Shape = { fields: { allowedFiles: null, modifyOnly: null, maxPatchFiles: null, maxModifiedLines: null, diffShape: null,
  holdoutChecks: null, releaseAgeCheck: null, oracle: { fields: { probe: null, workflow: null, maxFindings: null } }, companionFiles: null, requiredLabel: null } };
const classShape: Shape = { fields: {
  mode: null, author: null, adopt: { fields: { logins: null, branches: null } }, harness: null,
  harnessAllowed: null, escalation: { fields: { outOfBudget: null, notConverged: null } },
  conditions: { fields: { all: { items: leaf }, any: { items: { items: leaf } }, deny: { items: { fields: { protectedFiles: null } } } } },
  requiresApproval: { items: { fields: { changedFiles: null } } },
  limits: { fields: { maxPerDay: null } },
  monitor: { fields: { oracleRuns: null, flux: { fields: { source: null, within: null } }, verification: within, followUpWindow: null } },
} };
export const fileShape: Shape = { fields: { factory: null, classes: { values: classShape } } };

// canonical is v with its keys in the file's order, so the same file always writes the same text
// however it was built (a template, the broker's JSON, parsed text). A key the file does not know
// keeps its place after the known ones: the factory's check names it.
export function canonical<T>(v: T, s: Shape | null = fileShape): T {
  if (Array.isArray(v)) return v.map((x) => canonical(x, s?.items ?? null)) as T;
  if (!v || typeof v !== "object") return v;
  const o = v as Record<string, unknown>;
  const known = s?.fields ?? {};
  const keys = s?.values ? Object.keys(o).sort() : [...Object.keys(known).filter((k) => k in o), ...Object.keys(o).filter((k) => !(k in known))];
  return Object.fromEntries(keys.map((k) => [k, canonical(o[k], s?.values ?? known[k] ?? null)])) as T;
}

export type Parsed = { ok: true; file: PolicyFile } | { ok: false; line?: number; message: string };

// parsePolicy reads the expert text for the form. The core schema keeps a date or a yes a string,
// and a duplicated key is refused, as the factory's strict parser does.
export function parsePolicy(text: string): Parsed {
  let v: unknown;
  try {
    v = load(text, { schema: CORE_SCHEMA });
  } catch (e) {
    const y = e as YAMLException;
    return { ok: false, line: y.mark ? y.mark.line + 1 : undefined, message: y.reason ?? String(e) };
  }
  if (!v || typeof v !== "object" || Array.isArray(v)) return { ok: false, line: 1, message: "the file is a mapping: factory, then classes" };
  const f = v as PolicyFile;
  if (f.classes != null && (typeof f.classes !== "object" || Array.isArray(f.classes))) {
    return { ok: false, line: keyLine(text.split("\n"), "classes", 0), message: "classes is a mapping of class names" };
  }
  return { ok: true, file: f };
}

// --- Highlighting: line by line, no grammar; the file is flat enough to read at a glance. ---

export interface Tok { t: string; c: string }

function scalarClass(v: string): string {
  if (/^-?\d+(\.\d+)?$/.test(v)) return "y-num";
  if (/^(true|false|null|~)$/.test(v)) return "y-bool";
  return "y-str";
}

// commentAt is where a # comment starts in v, outside quotes; -1 when none.
function commentAt(v: string): number {
  let q = "";
  for (let i = 0; i < v.length; i++) {
    const ch = v[i];
    if (q) {
      if (ch === "\\" && q === '"') i++;
      else if (ch === q) q = "";
    } else if (ch === '"' || ch === "'") q = ch;
    else if (ch === "#" && (i === 0 || /\s/.test(v[i - 1]))) return i;
  }
  return -1;
}

function value(out: Tok[], text: string) {
  const lead = /^\s*/.exec(text)![0];
  if (lead) out.push({ t: lead, c: "" });
  let v = text.slice(lead.length);
  if (!v) return;
  const at = commentAt(v);
  const comment = at < 0 ? "" : v.slice(at);
  v = at < 0 ? v : v.slice(0, at);
  const body = v.trimEnd();
  if (body) out.push({ t: body, c: scalarClass(body) });
  if (comment || body !== v) out.push({ t: v.slice(body.length) + comment, c: "y-comment" });
}

// tokenize splits one line into classed pieces whose text is the line.
export function tokenize(line: string): Tok[] {
  const indent = /^\s*/.exec(line)![0];
  let body = line.slice(indent.length);
  if (body.startsWith("#")) return [{ t: line, c: "y-comment" }];
  const out: Tok[] = indent ? [{ t: indent, c: "" }] : [];
  const dash = /^(?:-(?:\s+|$))+/.exec(body);
  if (dash) {
    out.push({ t: dash[0], c: "y-punct" });
    body = body.slice(dash[0].length);
  }
  const kv = /^("(?:[^"\\]|\\.)*"|'[^']*'|[^:\s#'"][^:]*?):(\s|$)/.exec(body);
  if (kv) {
    out.push({ t: kv[1], c: "y-key" }, { t: ":", c: "y-punct" });
    body = body.slice(kv[1].length + 1);
  }
  value(out, body);
  return out;
}

// highlight is text as a <code> of one span a line, the lines in marked (1-based) flagged.
export function highlight(text: string, marked: Set<number> = new Set()): HTMLElement {
  const code = document.createElement("code");
  text.replace(/\n$/, "").split("\n").forEach((line, i) => {
    const l = document.createElement("span");
    l.className = marked.has(i + 1) ? "y-line y-error" : "y-line";
    for (const tok of tokenize(line)) {
      const s = document.createElement("span");
      if (tok.c) s.className = tok.c;
      s.textContent = tok.t;
      l.append(s);
    }
    l.append("\n");
    code.append(l);
  });
  return code;
}

// --- Locating an error: the factory's messages name a line, a class, a key, or nothing. ---

function allKeys(s: Shape | null | undefined, out = new Set<string>()): Set<string> {
  if (!s) return out;
  for (const [k, sub] of Object.entries(s?.fields ?? {})) { out.add(k); allKeys(sub, out); }
  allKeys(s?.values, out);
  allKeys(s?.items, out);
  return out;
}
const known = allKeys(fileShape);

const esc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
const indentOf = (l: string) => /^\s*/.exec(l)![0].length;
const blank = (l: string) => /^\s*(#.*)?$/.test(l);

// keyLine is the 1-based line of the first `key:` in lines[from:to), list dashes and quotes allowed.
function keyLine(lines: string[], key: string, from: number, to = lines.length, indent?: number): number | undefined {
  const re = new RegExp(`^(\\s*)(?:-\\s+)*["']?${esc(key)}["']?\\s*:(\\s|$)`);
  for (let i = from; i < to; i++) {
    const m = re.exec(lines[i]);
    if (m && (indent === undefined || m[1].length === indent)) return i + 1;
  }
  return undefined;
}

// classLine is the line declaring class name under classes:, and where its block ends.
function classBlock(lines: string[], name: string): { at: number; end: number } | undefined {
  const top = keyLine(lines, "classes", 0, lines.length, 0);
  if (top === undefined) return undefined;
  const first = lines.findIndex((l, i) => i >= top && !blank(l));
  if (first < 0) return undefined;
  const at = keyLine(lines, name, top, lines.length, indentOf(lines[first]));
  if (at === undefined) return undefined;
  let end = at;
  while (end < lines.length && (blank(lines[end]) || indentOf(lines[end]) > indentOf(lines[at - 1]))) end++;
  return { at, end };
}

// keyIn is the key a message is about: a quoted one, else the first word that is a file key
// (limits.maxPerDay reads maxPerDay).
function keyIn(msg: string): string | undefined {
  const q = /^(?:key )?"([^"]+)"/.exec(msg);
  if (q) return q[1];
  return msg.split(/[^A-Za-z.]+/).map((w) => w.split(".").at(-1)!).find((w) => known.has(w));
}

// keyCol is the column a line's key starts at, after any list dashes.
const keyCol = (l: string) => /^\s*(?:-\s+)*/.exec(l)![0].length;

// pathBlock walks a path such as classes.a.conditions.all[1] through the text's indentation: the
// 1-based line of the deepest step the text has, and where that step's block ends. A list item's
// first key sits on the item's own line, after its dash.
function pathBlock(lines: string[], path: string): { at: number; end: number } | undefined {
  let from = 0, end = lines.length, found: { at: number; end: number } | undefined;
  for (const seg of path.match(/[^.[\]]+|\[\d+\]/g) ?? []) {
    const first = lines.findIndex((l, i) => i >= from && i < end && !blank(l));
    if (first < 0) break;
    let hit = -1, col = 0;
    if (seg.startsWith("[")) {
      col = indentOf(lines[first]);
      let n = Number(seg.slice(1, -1));
      for (let i = first; i < end && hit < 0; i++) {
        if (!blank(lines[i]) && indentOf(lines[i]) === col && /^\s*-/.test(lines[i]) && n-- === 0) hit = i;
      }
    } else {
      col = keyCol(lines[first]);
      hit = (keyLine(lines, seg, first, end) ?? 0) - 1;
      while (hit >= 0 && keyCol(lines[hit]) !== col) hit = (keyLine(lines, seg, hit + 1, end) ?? 0) - 1;
    }
    if (hit < 0) break;
    let e = hit + 1;
    while (e < end && (blank(lines[e]) || indentOf(lines[e]) > col)) e++;
    found = { at: hit + 1, end: e };
    [from, end] = [seg.startsWith("[") ? hit : hit + 1, e];
  }
  return found;
}

// keyFix reads a key error: the key refused and the keys it may be, the one meant first.
export function keyFix(msg: string): { key: string; keys: string[] } | undefined {
  const m = /key "([^"]+)" is not a field(?: \(did you mean "([^"]+)"\?\))?/.exec(msg);
  if (!m) return undefined;
  const list = /one of (.+)$/.exec(msg)?.[1].split(", ").map((k) => k.trim()) ?? [];
  return { key: m[1], keys: [...new Set([...(m[2] ? [m[2]] : []), ...list])] };
}

// comments is how many lines of text carry a comment: what a rewrite from the form drops.
export function comments(text: string): number {
  return text.split("\n").filter((l) => tokenize(l).some((t) => t.c === "y-comment")).length;
}

// locate is the 1-based line of text an error message is about, or undefined when the text holds
// no such place (the error is about the file as a whole, or a class it lacks).
export function locate(text: string, msg: string): number | undefined {
  const n = /\bline (\d+)\b/.exec(msg);
  if (n) return Number(n[1]);
  const lines = text.split("\n");
  let body = msg.replace(/^mergepolicy: /, "");
  let from = 0, to = lines.length;
  const k = /^([\w-]+(?:\.[\w-]+|\[\d+\])*): (key ".*)$/.exec(body);
  if (k) {
    const b = pathBlock(lines, k[1]);
    if (b) return keyLine(lines, keyIn(k[2])!, b.at - 1, b.end) ?? b.at;
    body = k[2];
  }
  const c = /^class ([a-z0-9][-a-z0-9]*)(?::\s*|\s+)(.*)$/.exec(body);
  if (c) {
    const b = classBlock(lines, c[1]);
    if (!b) return undefined;
    [from, to, body] = [b.at - 1, b.end, c[2]];
  }
  const key = keyIn(body);
  const hit = key === undefined ? undefined : keyLine(lines, key, from, to);
  return hit ?? (c ? from + 1 : undefined);
}
