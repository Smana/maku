// SPDX-License-Identifier: Apache-2.0

// The factory's guardrails and a class's mode in words, for the settings pages. Pure: values in,
// sentences out. A value the page has no words for is named, never dropped.

export function joinOr(xs: string[], word: "or" | "and" = "or"): string {
  if (xs.length <= 1) return xs[0] ?? "";
  return `${xs.slice(0, -1).join(", ")} ${word} ${xs.at(-1)}`;
}

const quoted = (xs: string[]) => xs.map((x) => `“${x}”`);
const pos = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v) && v > 0;

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
