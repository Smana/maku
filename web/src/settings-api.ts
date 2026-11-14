// SPDX-License-Identifier: Apache-2.0

// What the settings pages and their class cards share: the broker's calls, a refusal's words, and
// a class's evidence in one line. Kept apart from settings.ts so a card can use them without
// importing the page that draws it.

import { api } from "./api";
import { ago } from "./duration";
import type { Condition, Evidence, Report } from "./settings";

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

// LADDER_LEGEND is the one line that says what the 29 / 46 / 61 ladder is; it is the same for every
// class, so a page says it once rather than on every row.
export const LADDER_LEGEND = "29 / 46 / 61 clean merges in a row prove a bad rate under ~10 % / 6.5 % / 5 % at 95 % confidence; shown, never acted on.";

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

// trackRecordParts is a class's evidence in words, each with the explanation shown on hover.
export function trackRecordParts(e: Evidence): { text: string; title: string }[] {
  const parts = [
    { text: `${e.clean} of ${e.minimum} clean merges`, title: "Shadow decisions a maintainer merged unchanged, toward the minimum needed to promote." },
    { text: `${e.bad} bad`, title: "Merges of this class that were reverted, followed up or regressed. One must be explained before promotion." },
  ];
  if (e.immature > 0) parts.push({ text: `${e.immature} awaiting outcome`, title: "Merged, but still inside the monitor window: they neither count nor block yet." });
  if (e.pending > 0) parts.push({ text: `${e.pending} awaiting a verdict`, title: "Past the monitor window, with no verdict recorded yet." });
  parts.push({ text: `worst-case bad rate \u2264 ${Math.round(e.bound * 100)} %`, title: `Worst-case bad rate \u2264 ${Math.round(e.bound * 100)} % at 95 % confidence (the Clopper-Pearson upper bound on the clean merges so far). It falls as clean merges accumulate.` });
  parts.push(e.drill === "passed"
    ? { text: "rollback drill passed", title: "A factory/drill revert of one of the class's pull requests merged." }
    : { text: e.drill === "none" ? "no rollback drill yet" : `rollback drill ${e.drill.replaceAll("_", " ")}`, title: "Promotion needs a factory/drill revert of one of the class's pull requests to merge." });
  return parts;
}

// trackRecord is trackRecordParts as one line, each part carrying its explanation as a title.
export function trackRecord(e: Evidence): HTMLElement {
  const box = document.createElement("span");
  box.className = "track-record";
  trackRecordParts(e).forEach((p, i) => {
    if (i > 0) box.append(" \u00b7 ");
    const s = document.createElement("span");
    s.textContent = p.text;
    s.title = p.title;
    box.append(s);
  });
  return box;
}

// blockerActions are a class's promotion blockers as what to do; an unknown code is shown with spaces.
export function blockerActions(e: Evidence): string[] {
  return (e.blockers ?? []).map((k) => {
    if (k === "below_minimum") return `needs ${plural(Math.max(1, e.minimum - e.clean), "more clean merge", "more clean merges")}`;
    if (k === "bad_outcome") return `${plural(Math.max(1, e.bad), "bad merge", "bad merges")} to explain`;
    if (k === "no_drill" || k === "drill_not_passed") return "run a rollback drill (factory/drill)";
    return k.replaceAll("_", " ");
  });
}

export type PolicyState = "central" | "repository" | "not-migrated";

// policyState says who holds the merge authority: a repository with no Repository object is
// not-migrated (its config file serves it); a bound one's status says central or repository (B3).
export function policyState(r: Report): PolicyState {
  if (!r.repositoryObject) return "not-migrated";
  return r.repositoryStatus?.status.authority === "repository" ? "repository" : "central";
}

export type Tone = "ok" | "warn" | "bad" | "unknown";

// differences is how many differences an InSync message lists: statuswriter joins up to five with
// "; " and, past five, appends "; and N more".
export function differences(message?: string): number {
  const parts = (message ?? "").split(";").map((x) => x.trim()).filter(Boolean);
  const more = parts.at(-1)?.match(/^and (\d+) more$/);
  return more ? parts.length - 1 + Number(more[1]) : parts.length;
}

// Why an InSync or RepositoryFileValid condition is Unknown, as statuswriter's reasons say it.
const unknownWhy: Record<string, string> = {
  PolicyReadOff: "the factory does not read repository files",
  NotServed: "the Repository is not served yet",
  NotRead: "the file has not been read yet",
};

// conditionWords reads each condition of a Repository's status in the page's words; a type it
// does not know shows as sent.
export function conditionWords(st?: { status: { conditions?: Condition[] } }): { word: string; tone: Tone; message?: string }[] {
  return (st?.status.conditions ?? []).map((c) => {
    const message = c.message || undefined;
    switch (c.type) {
      case "Accepted":
        if (c.status === "True") return { word: "Accepted", tone: "ok" as Tone };
        if (c.status === "False") return { word: "Refused", tone: "bad" as Tone, message };
        break;
      case "InSync":
        if (c.status === "True") return { word: "In sync with .github/agent-factory.yaml", tone: "ok" as Tone };
        if (c.status === "False") {
          if (c.reason === "FileInvalid") return { word: "Not in sync: file invalid", tone: "bad" as Tone, message };
          const n = differences(c.message);
          return { word: n ? `Not in sync: ${n} difference${n > 1 ? "s" : ""}` : "Not in sync", tone: "warn" as Tone, message };
        }
        if (c.status === "Unknown" && c.reason === "PolicyReadOff") return { word: "File not read", tone: "unknown" as Tone, message };
        if (c.status === "Unknown" && c.reason && Object.hasOwn(unknownWhy, c.reason)) return { word: "Not compared yet", tone: "unknown" as Tone, message };
        break;
      case "BindingsValid":
        if (c.status === "True") return { word: "Bindings valid", tone: "ok" as Tone };
        if (c.status === "False") return { word: "Bindings refused", tone: "bad" as Tone, message };
        break;
      case "RepositoryFileValid":
        if (c.status === "True") return { word: "Repository file valid", tone: "ok" as Tone };
        if (c.status === "False") return { word: "Repository file invalid", tone: "bad" as Tone, message };
        break;
    }
    return { word: `${c.type}: ${String(c.status)}`, tone: "unknown" as Tone, message };
  });
}

// status is what serves the classes: the Repository (B10).
export const servingStatus = (r: Report) => r.repositoryStatus;
const condition = (r: Report, type: string) => servingStatus(r)?.status.conditions?.find((c) => c.type === type);

// fileLine says what the repository's .github/agent-factory.yaml is to its classes now, and only
// what the report shows to be true; prominent only when it needs the reader's attention: an
// invalid file under repository authority, or a not-migrated repository whose file is refused.
export function fileLine(r: Report): { text: string; prominent: boolean } {
  const state = policyState(r);
  const F = ".github/agent-factory.yaml";
  if (state === "not-migrated") {
    if (r.policyError) return { text: `The repository's policy file is ${r.policyError}: the central classes are shown.`, prominent: true };
    return { text: `Classes come from ${F}.`, prominent: false };
  }
  const who = "the Repository";
  const valid = condition(r, "RepositoryFileValid");
  const sync = condition(r, "InSync");
  const invalid = !!r.policyError || valid?.status === "False" || sync?.reason === "FileInvalid";
  if (state === "repository") {
    if (invalid) return { text: `${F} is invalid: auto-merge is paused until it is fixed.`, prominent: true };
    return { text: `The classes come from ${F}, which ${who} defers to.`, prominent: false };
  }
  if (invalid) return { text: `${F} is invalid; ${who} serves the classes.`, prominent: false };
  if (sync?.status === "True") return { text: `${F} matches ${who}.`, prominent: false };
  if (sync?.status === "False") {
    const n = differences(sync.message);
    return { text: `${F} differs from ${who}${n ? ` in ${n} place${n > 1 ? "s" : ""}` : ""}; ${who} wins.`, prominent: false };
  }
  const why = sync?.reason && Object.hasOwn(unknownWhy, sync.reason) ? unknownWhy[sync.reason] : `${who} has no status yet`;
  return { text: `${F} has not been compared with ${who} yet (${why}).`, prominent: false };
}

// timeText is iso as a <time>: how long ago in words for a past time, the locale's date for a
// future one, the locale's date and time on hover; an unreadable value is shown as sent.
export function timeText(iso: string, now: number = Date.now()): HTMLTimeElement {
  const t = document.createElement("time");
  t.dateTime = iso;
  const at = Date.parse(iso);
  if (isNaN(at)) { t.textContent = iso; return t; }
  const d = new Date(at);
  t.title = d.toLocaleString();
  if (at > now + 60000) { t.textContent = d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" }); return t; }
  t.textContent = ago(now - at);
  return t;
}
