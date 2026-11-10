// SPDX-License-Identifier: Apache-2.0

// A merge class in words: what the class card and the editor's live sentence say. Pure: one class
// (and the factory's guardrails, for the caps a class inherits) in, sentences out. Every leaf of
// the closed vocabulary has a phrase; one it does not know is named, never dropped, so the page
// never reads a class as looser than it is. Patterns and check names are the policy's own text,
// written as text by the caller.

import type { ClassPolicy, Leaf } from "./policy-editor";

export interface ClassStory { lead: string; when: string[]; also: string[] }

export function joinOr(xs: string[], word: "or" | "and" = "or"): string {
  if (xs.length <= 1) return xs[0] ?? "";
  return `${xs.slice(0, -1).join(", ")} ${word} ${xs.at(-1)}`;
}

const quoted = (xs: string[]) => xs.map((x) => `“${x}”`);

const shapes: Record<string, string> = {
  "links-only": "the diff only changes links",
  "semver-patch": "the diff is a patch version bump",
  "semver-minor": "the diff is a minor version bump",
  "added-entry": "the diff only adds an entry",
};

function leafPhrase(l: Leaf): string {
  const [k, v] = Object.entries(l)[0] ?? ["", undefined];
  switch (k) {
    case "allowedFiles": return `it only changes files matching ${joinOr(v as string[])}`;
    case "companionFiles": return `its other files match ${joinOr(v as string[])}`;
    case "maxPatchFiles": return `it changes at most ${v} files`;
    case "maxModifiedLines": return `it changes at most ${v} lines`;
    case "modifyOnly": return "it only modifies files, adding or deleting none";
    case "diffShape": return shapes[String(v)] ?? `the diff has the shape ${String(v)}`;
    case "holdoutChecks": {
      const c = v as string[];
      return `the check${c.length > 1 ? "s" : ""} ${joinOr(quoted(c), "and")} pass${c.length > 1 ? "" : "es"}`;
    }
    case "releaseAgeCheck": return `the status “${String(v)}” is green (the release is old enough)`;
    case "requiredLabel": return `it carries the label “${String(v)}”`;
    case "oracle": return "the dead-links oracle has no finding open";
  }
  return `a condition this page does not describe (${k})`;
}

function who(c: ClassPolicy): string {
  if (c.author !== "adopt") return "an agent's pull request";
  const logins = c.adopt?.logins ?? [];
  const branches = c.adopt?.branches ?? [];
  return `a pull request from ${joinOr(logins) || "an adopted bot"}${branches.length ? ` on a branch matching ${joinOr(branches)}` : ""}`;
}

export function describeClass(c: ClassPolicy, guardrails: Record<string, unknown> = {}): ClassStory {
  const all = c.conditions?.all ?? [];
  const has = (k: string) => all.some((l) => k in l);
  const tail = "on its own when all of these hold";
  const lead = c.mode === "live" ? `Merges ${who(c)} ${tail}:`
    : c.mode === "shadow" ? `Would merge ${who(c)} ${tail} (shadow: it records the decision and merges nothing):`
    : `Off: decides nothing. If turned on, it would merge ${who(c)} ${tail}:`;
  const when = all.map(leafPhrase);
  for (const [k, unit] of [["maxPatchFiles", "files"], ["maxModifiedLines", "lines"]] as const) {
    if (!has(k) && typeof guardrails[k] === "number" && (guardrails[k] as number) > 0) when.push(`it changes at most ${guardrails[k]} ${unit} (the factory's cap)`);
  }
  const any = c.conditions?.any ?? [];
  if (any.length) when.push(`one of: ${any.map((b) => `(${joinOr(b.map(leafPhrase), "and")})`).join(" or ")}`);
  if (when.length === 0) when.push("the factory's base checks pass (the class adds no condition of its own)");

  const also: string[] = [];
  const deny = (c.conditions?.deny ?? []).flatMap((d) => d.protectedFiles);
  if (deny.length) also.push(`Never if it touches ${joinOr(deny)}.`);
  const ask = (c.requiresApproval ?? []).flatMap((a) => a.changedFiles);
  if (ask.length) also.push(`A maintainer approves it first if it changes ${joinOr(ask)}.`);
  if (c.limits?.maxPerDay) also.push(`At most ${c.limits.maxPerDay} a day.`);
  else if (typeof guardrails.maxPerDay === "number" && guardrails.maxPerDay > 0) also.push(`At most ${guardrails.maxPerDay} a day (the factory's cap).`);
  const fw = c.monitor?.followUpWindow;
  if (typeof fw === "string" && fw) also.push(`Watched for ${fw} after it merges.`);
  return { lead, when, also };
}

export function describeGuardrails(g: Record<string, unknown>): string[] {
  const out: string[] = [];
  const list = (k: string) => (Array.isArray(g[k]) ? (g[k] as string[]) : []);
  if (list("protectedFiles").length) out.push(`Every class refuses a pull request that touches ${joinOr(list("protectedFiles"))}.`);
  if (list("mandatoryChecks").length) out.push(`Every class also needs the check${list("mandatoryChecks").length > 1 ? "s" : ""} ${joinOr(quoted(list("mandatoryChecks")), "and")}.`);
  const caps = ([["maxPatchFiles", "files"], ["maxModifiedLines", "lines"], ["maxPerDay", "merges a day"]] as const)
    .filter(([k]) => typeof g[k] === "number" && (g[k] as number) > 0).map(([k, unit]) => `${g[k]} ${unit}`);
  if (caps.length) out.push(`No class may allow more than ${joinOr(caps)}.`);
  return out.length ? out : ["The factory sets no guardrail beyond its base checks."];
}

const modes: Record<string, { label: string; meaning: string }> = {
  live: { label: "Live", meaning: "merges on its own" },
  shadow: { label: "Shadow", meaning: "records what it would do, merges nothing" },
  off: { label: "Off", meaning: "decides nothing" },
};

export function modeMeaning(mode: string): { label: string; meaning: string } {
  return modes[mode] ?? { label: mode, meaning: "a mode this page does not know" };
}
