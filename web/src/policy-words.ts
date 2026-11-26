// SPDX-License-Identifier: Apache-2.0

export type PolicySpec = Record<string, unknown>;

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

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
