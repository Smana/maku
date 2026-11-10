// SPDX-License-Identifier: Apache-2.0

// What the settings pages and their class cards share: the broker's calls, a refusal's words, and
// a class's evidence in one line. Kept apart from settings.ts so a card can use them without
// importing the page that draws it.

import { api } from "./api";
import type { Evidence, MergePolicyStatus, Report } from "./settings";

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

export type PolicyState = "central" | "repository" | "not-migrated";

// policyState says who holds the merge authority: no MergePolicy is not-migrated; the object's
// authority (else its classes' sources, when it carries no status) says central or repository.
export function policyState(r: Report): PolicyState {
  if (!r.mergePolicy) return "not-migrated";
  const authority = r.mergePolicyStatus?.status.authority;
  if (authority === "repository" || r.classes.some((c) => c.source === "repository")) return "repository";
  return "central";
}

// centralManifest is the report's MergePolicy manifest when the central classes are the merge
// authority; undefined otherwise.
export function centralManifest(r: Report): string | undefined {
  return policyState(r) === "central" ? r.manifest : undefined;
}

export type Tone = "ok" | "warn" | "bad" | "unknown";

const differences = (message?: string) => (message ? message.split(";").filter((x) => x.trim() !== "").length : 0);

// conditionWords reads each condition of a MergePolicy's status in the page's words; a type it
// does not know shows as sent.
export function conditionWords(st?: MergePolicyStatus): { word: string; tone: Tone; message?: string }[] {
  return (st?.status.conditions ?? []).map((c) => {
    const message = c.message || undefined;
    const known = c.status === "True" || c.status === "False" || c.status === "Unknown";
    switch (c.type) {
      case "Accepted":
        if (c.status === "True") return { word: "Accepted", tone: "ok" as Tone };
        if (c.status === "False") return { word: "Refused", tone: "bad" as Tone, message };
        break;
      case "InSync":
        if (c.status === "True") return { word: "In sync with .github/agent-factory.yaml", tone: "ok" as Tone };
        if (c.status === "False") {
          const n = differences(c.message);
          return { word: n ? `Not in sync: ${n} difference${n > 1 ? "s" : ""}` : "Not in sync", tone: "warn" as Tone, message };
        }
        if (c.status === "Unknown" && c.reason === "PolicyReadOff") return { word: "File not read", tone: "unknown" as Tone, message };
        break;
      case "RepositoryFileValid":
        if (c.status === "True") return { word: "Repository file valid", tone: "ok" as Tone };
        if (c.status === "False") return { word: "Repository file invalid", tone: "bad" as Tone, message };
        break;
    }
    return { word: known ? `${c.type}: ${c.status}` : `${c.type}: ${String(c.status)}`, tone: "unknown" as Tone, message };
  });
}

const condition = (r: Report, type: string) => r.mergePolicyStatus?.status.conditions?.find((c) => c.type === type);

// fileLine says what the repository's .github/agent-factory.yaml is to its classes now; prominent
// only when it needs the reader's attention (an invalid file under repository authority).
export function fileLine(r: Report): { text: string; prominent: boolean } {
  const state = policyState(r);
  if (state === "not-migrated") return { text: "Classes come from .github/agent-factory.yaml.", prominent: false };
  if (state === "repository") {
    if (condition(r, "RepositoryFileValid")?.status === "False") {
      return { text: ".github/agent-factory.yaml is invalid: auto-merge is paused until it is fixed.", prominent: true };
    }
    return { text: "The classes come from .github/agent-factory.yaml, which the MergePolicy defers to.", prominent: false };
  }
  const sync = condition(r, "InSync");
  if (sync?.status === "False") {
    const n = differences(sync.message);
    return { text: `.github/agent-factory.yaml differs from the MergePolicy${n ? ` in ${n} place${n > 1 ? "s" : ""}` : ""}; the MergePolicy wins.`, prominent: false };
  }
  return { text: ".github/agent-factory.yaml matches the MergePolicy.", prominent: false };
}

// servedFrom names what serves a repository's classes: its MergePolicy only while that is the
// authority, else its own file once it declares a class, else the factory's central config.
export function servedFrom(r: Report): string {
  if (centralManifest(r) !== undefined) return `the MergePolicy ${r.mergePolicy}`;
  return r.classes.some((c) => c.source === "repository") ? "its own .github/agent-factory.yaml" : "the factory's central config";
}
