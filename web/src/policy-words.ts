// SPDX-License-Identifier: Apache-2.0

import type { Binding } from "./settings";

export type PolicySpec = Record<string, unknown>;

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

const list = (v: unknown): string[] => Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
const number = (v: unknown): number => typeof v === "number" && Number.isFinite(v) && v > 0 ? v : 0;
const natural = (xs: string[], word = "and") => xs.length < 2 ? xs[0] ?? "" : `${xs.slice(0, -1).join(", ")} ${word} ${xs.at(-1)}`;

export function patternGloss(pattern: string): string {
  const p = pattern.replace(/^\^/, "").replace(/\$$/, "");
  const exact = !p.endsWith("/") && !p.includes(".+") && !p.includes(".*") && !p.includes("[") && !p.includes("(");
  if (exact) return p.replaceAll("\\.", ".").replaceAll("\\/", "/");
  const alternatives = p.match(/^\(([^)]+)\)(\/.*)$/);
  const rest = alternatives?.[2] ?? p;
  const firstMeta = p.search(/[.[\]()+*?|]/);
  const prefixes = alternatives?.[1].split("|").map((x) => `${x}/`) ?? [firstMeta < 0 ? p : p.slice(0, firstMeta)];
  const extension = rest.match(/\\\.(mdx?|ya?ml|json|go|ts)(?:\?|$)/)?.[1];
  const kind = extension?.startsWith("md") ? "Markdown" : extension?.startsWith("ya") ? "YAML" : extension?.toUpperCase() ?? "files";
  const dirs = prefixes.map((x) => x.replaceAll("\\/", "/").replaceAll("\\.", ".")).filter(Boolean);
  if (!dirs.length) return `${kind} files`;
  return `${kind} under ${natural(dirs, "or")}`;
}

export function patternDetails(patterns: string[]): HTMLDetailsElement {
  const details = el("details", undefined, "raw-patterns");
  details.append(el("summary", "Show raw file patterns"));
  const list = el("ul");
  for (const pattern of patterns) list.append(el("li", pattern, "raw-pattern"));
  details.append(list);
  return details;
}

const shapeWords: Record<string, string> = {
  "links-only": "link targets only", "semver-patch": "patch dependency updates",
  "semver-minor": "minor or patch dependency updates", "added-entry": "one added entry",
};

export function policyWords(spec: PolicySpec, binding?: Partial<Binding>): string {
  const patterns = binding?.allowedFiles?.length ? binding.allowedFiles : list(spec.allowedFiles);
  const parts: string[] = [];
  if (patterns.length) parts.push(natural(patterns.map(patternGloss), "or"));
  const shape = typeof spec.diffShape === "string" ? shapeWords[spec.diffShape] ?? spec.diffShape.replaceAll("-", " ") : "";
  if (shape) parts.push(shape);
  if (spec.modifyOnly === true) parts.push("edits only");
  const caps: string[] = [];
  const files = number(binding?.maxPatchFiles ?? spec.maxPatchFiles);
  const lines = number(binding?.maxModifiedLines ?? spec.maxModifiedLines);
  const daily = number(binding?.maxPerDay ?? spec.maxPerDay);
  if (files) caps.push(`${files} files`);
  if (lines) caps.push(`${lines} lines`);
  if (caps.length) parts.push(`at most ${natural(caps)}`);
  if (daily) parts.push(`${daily} a day`);
  const main = parts.join(", ") || "No extra policy restriction";
  const checks = [...list(spec.holdoutChecks), ...(binding?.holdoutChecks ?? [])];
  const requirements: string[] = [];
  if (checks.length) requirements.push(`${natural(checks)} must pass`);
  if (spec.oracle || binding?.oracleWorkflow) requirements.push("the requested findings must be resolved");
  if (spec.releaseAgeCheck) requirements.push(`${String(spec.releaseAgeCheck)} must pass`);
  if (binding?.requiredLabel) requirements.push(`the ${binding.requiredLabel} label is required`);
  const approvals = (spec.requiresApproval as unknown[] | undefined)?.length || (binding?.requiresApproval?.length ?? 0);
  if (approvals) requirements.push("a maintainer must approve matching changes");
  return [main, ...requirements].join("; ");
}
