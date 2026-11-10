// SPDX-License-Identifier: Apache-2.0

// A merge class in words: what the class card and the editor's live sentence say. Pure: one class
// (and the factory's guardrails, for the caps a class inherits) in, sentences out. A class never
// reads looser than it is: every key of every leaf and entry gets a phrase, and a key or value the
// page has no words for is named, never dropped. The editor feeds half-typed drafts, so every
// value is checked by type before it is read. Patterns and check names are the policy's own text,
// written as text by the caller.

import type { ClassPolicy } from "./policy-editor";

export interface ClassStory { lead: string; when: string[]; also: string[] }

export function joinOr(xs: string[], word: "or" | "and" = "or"): string {
  if (xs.length <= 1) return xs[0] ?? "";
  return `${xs.slice(0, -1).join(", ")} ${word} ${xs.at(-1)}`;
}

const quoted = (xs: string[]) => xs.map((x) => `“${x}”`);
const isObj = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const nonEmpty = (v: unknown): v is string => typeof v === "string" && v !== "";
const strs = (v: unknown): v is string[] => Array.isArray(v) && v.length > 0 && v.every(nonEmpty);
const pos = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v) && v > 0;
const posInt = (v: unknown): v is number => pos(v) && Number.isInteger(v);
const named = (k: string) => `a condition this page does not describe (${k})`;
const setting = (k: string) => `Also limited by a setting this page does not describe (${k}).`;

const shapes: Record<string, string> = {
  "links-only": "the diff only changes links",
  "semver-patch": "the diff is a patch version bump",
  "semver-minor": "the diff is a minor version bump",
  "added-entry": "the diff only adds an entry",
};

// leafPhrases gives one phrase per key of a leaf; a leaf with no key, or not an object, is named.
function leafPhrases(l: unknown): string[] {
  if (!isObj(l)) return [named("malformed")];
  const keys = Object.keys(l);
  if (keys.length === 0) return [named("empty")];
  return keys.map((k) => leafPhrase(k, l[k]));
}

function leafPhrase(k: string, v: unknown): string {
  switch (k) {
    case "allowedFiles": return strs(v) ? `it only changes files matching ${joinOr(v)}` : named(k);
    case "companionFiles": return strs(v) ? `its other files match ${joinOr(v)}` : named(k);
    case "maxPatchFiles": return pos(v) ? `it changes at most ${v} files` : named(k);
    case "maxModifiedLines": return pos(v) ? `it changes at most ${v} lines` : named(k);
    case "modifyOnly": return v === true ? "it only modifies files, adding or deleting none" : named(k);
    case "diffShape":
      if (typeof v !== "string") return named(k);
      return Object.hasOwn(shapes, v) ? shapes[v] : `the diff has the shape ${v}`;
    case "holdoutChecks": {
      if (!strs(v)) return named(k);
      return `the check${v.length > 1 ? "s" : ""} ${joinOr(quoted(v), "and")} pass${v.length > 1 ? "" : "es"}`;
    }
    case "releaseAgeCheck": return nonEmpty(v) ? `the status “${v}” is green (the release is old enough)` : named(k);
    case "requiredLabel": return nonEmpty(v) ? `it carries the label “${v}”` : named(k);
    case "oracle": return isObj(v) && nonEmpty(v.probe) ? `the ${v.probe} oracle has no finding open` : named(k);
  }
  return named(k);
}

// branchPhrase reads one branch of an any: its leaves joined with and; an empty or malformed branch is named.
const branchPhrase = (b: unknown) => (Array.isArray(b) && b.length ? joinOr(b.flatMap(leafPhrases), "and") : named("branch"));

function who(c: ClassPolicy): string {
  if (c.author !== "adopt") return "an agent's pull request";
  const logins = Array.isArray(c.adopt?.logins) ? c.adopt.logins : [];
  const branches = Array.isArray(c.adopt?.branches) ? c.adopt.branches : [];
  return `a pull request from ${joinOr(logins) || "an adopted bot"}${branches.length ? ` on a branch matching ${joinOr(branches)}` : ""}`;
}

// entryKeys names an entry's keys; an entry that is not an object, or has none, is named as such.
const entryKeys = (e: unknown): string[] => (isObj(e) ? Object.keys(e) : []);

export function describeClass(c: ClassPolicy, guardrails: Record<string, unknown> = {}): ClassStory {
  const cond = isObj(c.conditions) ? c.conditions : {};
  const when: string[] = [];
  const allRaw: unknown = cond.all;
  const all: unknown[] = Array.isArray(allRaw) ? allRaw : [];
  if (allRaw !== undefined && !Array.isArray(allRaw)) when.push(named("all"));
  when.push(...all.flatMap(leafPhrases));
  const has = (k: string) => all.some((l) => isObj(l) && k in l);

  const tail = "on its own when all of these hold";
  const lead = c.mode === "live" ? `Merges ${who(c)} ${tail}:`
    : c.mode === "shadow" ? `Would merge ${who(c)} ${tail} (shadow: it records the decision and merges nothing):`
    : c.mode === "off" ? `Off: decides nothing. If turned on, it would merge ${who(c)} ${tail}:`
    : `Mode "${String(c.mode)}" (a mode this page does not know): it would merge ${who(c)} ${tail}:`;
  for (const [k, unit] of [["maxPatchFiles", "files"], ["maxModifiedLines", "lines"]] as const) {
    if (!has(k) && pos(guardrails[k])) when.push(`it changes at most ${guardrails[k]} ${unit} (the factory's cap)`);
  }
  const anyRaw: unknown = cond.any;
  if (anyRaw !== undefined && !Array.isArray(anyRaw)) when.push(named("any"));
  else if (Array.isArray(anyRaw) && anyRaw.length) when.push(`one of: ${anyRaw.map((b) => `(${branchPhrase(b)})`).join(" or ")}`);
  if (when.length === 0) when.push("the factory's base checks pass (the class adds no condition of its own)");

  const also: string[] = [];
  // deny: the valid protectedFiles read as one sentence; any other key or entry is named.
  const denyRaw: unknown = cond.deny;
  const denyFiles: string[] = [];
  const denyBad: string[] = [];
  if (denyRaw !== undefined && !Array.isArray(denyRaw)) denyBad.push(`Never if it matches a rule this page does not describe (deny).`);
  for (const d of Array.isArray(denyRaw) ? denyRaw : []) {
    const keys = entryKeys(d);
    if (keys.length === 0) denyBad.push(`Never if it matches a rule this page does not describe (${isObj(d) ? "empty" : "malformed"}).`);
    for (const k of keys) {
      if (k === "protectedFiles" && isObj(d) && strs(d.protectedFiles)) denyFiles.push(...d.protectedFiles);
      else denyBad.push(`Never if it matches a rule this page does not describe (${k}).`);
    }
  }
  if (denyFiles.length) also.push(`Never if it touches ${joinOr(denyFiles)}.`);
  also.push(...denyBad);

  // requiresApproval reads the same way: valid changedFiles as one sentence, the rest named.
  const askRaw: unknown = c.requiresApproval;
  const askFiles: string[] = [];
  const askBad: string[] = [];
  if (askRaw !== undefined && !Array.isArray(askRaw)) askBad.push(`A maintainer approves it first under a rule this page does not describe (requiresApproval).`);
  for (const a of Array.isArray(askRaw) ? askRaw : []) {
    const keys = entryKeys(a);
    if (keys.length === 0) askBad.push(`A maintainer approves it first under a rule this page does not describe (${isObj(a) ? "empty" : "malformed"}).`);
    for (const k of keys) {
      if (k === "changedFiles" && isObj(a) && strs(a.changedFiles)) askFiles.push(...a.changedFiles);
      else askBad.push(`A maintainer approves it first under a rule this page does not describe (${k}).`);
    }
  }
  if (askFiles.length) also.push(`A maintainer approves it first if it changes ${joinOr(askFiles)}.`);
  also.push(...askBad);

  // limits: maxPerDay is read; the factory's cap stands in only when the class sets no maxPerDay key.
  const lim: unknown = c.limits;
  if (lim !== undefined && !isObj(lim)) also.push(setting("limits"));
  if (isObj(lim)) {
    for (const k of Object.keys(lim)) {
      if (k === "maxPerDay" && pos(lim.maxPerDay)) also.push(`At most ${lim.maxPerDay} a day.`);
      else also.push(setting(k));
    }
  }
  if (!(isObj(lim) && "maxPerDay" in lim) && pos(guardrails.maxPerDay)) also.push(`At most ${guardrails.maxPerDay} a day (the factory's cap).`);

  // monitor: the three known keys are read; any other key, or a value of the wrong type, is named.
  const mon: unknown = c.monitor;
  if (mon !== undefined && !isObj(mon)) also.push(setting("monitor"));
  if (isObj(mon)) {
    for (const k of Object.keys(mon)) {
      const v = mon[k];
      if (k === "followUpWindow" && nonEmpty(v)) also.push(`Watched for ${v} after it merges.`);
      else if (k === "oracleRuns" && posInt(v)) also.push(`Re-runs the oracle ${v} times after merge.`);
      else if (k === "flux" && v === true) also.push("Waits for Flux to apply it.");
      else if (k === "verification" && v === true) also.push("Verifies the deployment after merge.");
      else also.push(setting(k));
    }
  }
  return { lead, when, also };
}

export function describeGuardrails(g: Record<string, unknown>): string[] {
  const out: string[] = [];
  const list = (k: string) => (Array.isArray(g[k]) ? (g[k] as string[]) : []);
  if (list("protectedFiles").length) out.push(`Every class refuses a pull request that touches ${joinOr(list("protectedFiles"))}.`);
  if (list("mandatoryChecks").length) out.push(`Every class also needs the check${list("mandatoryChecks").length > 1 ? "s" : ""} ${joinOr(quoted(list("mandatoryChecks")), "and")}.`);
  const caps = ([["maxPatchFiles", "files"], ["maxModifiedLines", "lines"], ["maxPerDay", "merges a day"]] as const)
    .filter(([k]) => pos(g[k])).map(([k, unit]) => `${g[k]} ${unit}`);
  if (caps.length) out.push(`No class may allow more than ${joinOr(caps)}.`);
  return out.length ? out : ["The factory sets no guardrail beyond its base checks."];
}

const modes: Record<string, { label: string; meaning: string }> = {
  live: { label: "Live", meaning: "merges on its own" },
  shadow: { label: "Shadow", meaning: "records what it would do, merges nothing" },
  off: { label: "Off", meaning: "decides nothing" },
};

export function modeMeaning(mode: string): { label: string; meaning: string } {
  return Object.hasOwn(modes, mode) ? modes[mode] : { label: mode, meaning: "a mode this page does not know" };
}
