// SPDX-License-Identifier: Apache-2.0

// What the settings pages and their class cards share: the broker's calls, a refusal's words, and
// a class's evidence in one line. Kept apart from settings.ts so a card can use them without
// importing the page that draws it.

import { api } from "./api";
import type { Evidence, Report } from "./settings";

// A refusal's bounded code, as the broker writes it, else the status.
async function refusal(r: Response): Promise<string> {
  try {
    const body = (await r.json()) as { error?: unknown };
    if (typeof body.error === "string" && /^[a-z][a-z_]{0,47}$/.test(body.error)) return body.error;
  } catch { /* not JSON */ }
  return `HTTP ${r.status}`;
}

const reasons: Record<string, string> = {
  not_a_maintainer: "Only a maintainer of the repository (write on GitHub) may do this.",
  not_an_operator: "Only a platform operator may decide an exception.",
  not_eligible: "The evidence does not meet the minimum, has a bad outcome, or has no passed drill.",
  not_promotable: "The class is not a shadow class of the repository's own policy file.",
  promotion_open: "The class already has an open promotion request.",
  exception_unusable: "The exception is not an approved, unexpired one of this class.",
  minimum_not_below: "An exception's minimum is below the configured one, and at least 1.",
  justification_length: "A justification is 20 to 2,000 characters.",
  already_moved: "That record has already moved.",
  evidence_unavailable: "The evidence could not be read from GitHub. Retry.",
  evidence_pending: "The evidence is still being read from GitHub. Wait for it, then retry.",
  not_found: "Not found, or not readable with your GitHub access.",
};

export function explain(code: string): string {
  return reasons[code] ?? `Refused: ${code}`;
}

export async function post(path: string, body: unknown, get: typeof fetch): Promise<unknown> {
  const r = await api(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }, get);
  if (!r.ok) throw new Error(await refusal(r));
  return r.json();
}

export async function read<T>(path: string, get: typeof fetch): Promise<T> {
  const r = await api(path, {}, get);
  if (!r.ok) throw new Error(await refusal(r));
  return (await r.json()) as T;
}

// evidenceLine is a class's evidence in one line, the numbers MP7 decides on.
export function evidenceLine(e: Evidence): string {
  return `${e.clean}/${e.minimum} clean · ${e.bad} bad · ${e.immature} immature · ${e.pending} pending · bound ${e.bound.toFixed(3)} · drill ${e.drill}`;
}

export function ladderLine(e: Evidence): string {
  return e.ladder.map((s) => `${s.merges}${s.reached ? " ✓" : ""}`).join(" / ");
}

// centralManifest is the report's MergePolicy manifest when the central classes are the merge
// authority (no class comes from the repository's file); undefined otherwise.
export function centralManifest(r: Report): string | undefined {
  return r.manifest && !r.classes.some((c) => c.source === "repository") ? r.manifest : undefined;
}

// servedFrom names what serves a repository's classes: its MergePolicy only while that is the
// authority, else its own file once it declares a class, else the factory's central config.
export function servedFrom(r: Report): string {
  if (centralManifest(r) !== undefined) return `the MergePolicy ${r.mergePolicy}`;
  return r.classes.some((c) => c.source === "repository") ? "its own .github/agent-factory.yaml" : "the factory's central config";
}
