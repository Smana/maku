// SPDX-License-Identifier: Apache-2.0

// The room's Workflow card (WR3): open by default with Workflow, Will run, and Cost rows built
// from structured facts only, plus a collapsed "How it was chosen" fold holding the raw fields.

import { el } from "./controls";
import type { Flow, Summary } from "./summary";

export type ProfileInput = NonNullable<Summary["status"]["profile"]>;

const SOURCE_REASONS: Record<string, string> = {
  repository: "this repository's default",
  floor: "this repository's default",
  control: "this repository's default",
  schedule: "this schedule's workflow",
  revert: "a revert",
  oracle: "a finding a check verifies",
  treatment: "the comparison's second arm",
  scoper: "the scoper recommended it",
};

const TRIGGER_WORDS: Record<string, string> = {
  stalled: "a run that committed nothing",
  "budget-run": "spending its budget",
  "budget-task": "spending its budget",
  outOfBudget: "spending its budget",
  not_met: "missing acceptance checks",
  notConverged: "missing acceptance checks",
  review_rounds_exhausted: "exhausting review rounds",
  ci_red: "a red check",
};

const SHOWN_REFUSALS: Record<string, string> = {
  profile_unknown: "workflow_unknown",
  profile_conflict: "workflow_conflict",
  profile_no_tests_paths: "workflow_no_tests_paths",
  profile_not_offered: "workflow_not_offered",
};

function shownRefusal(code: string): string {
  return SHOWN_REFUSALS[code] ?? code;
}

function fmtDollars(microUSD: number): string {
  return `$${(microUSD / 1e6).toFixed(2)}`;
}

function reasonOf(p: ProfileInput): string {
  let base: string;
  if (p.source === "label") {
    base = p.by ? `pinned by @${p.by}` : "pinned by a maintainer";
  } else if (p.source === "escalation") {
    base = `stepped up after ${TRIGGER_WORDS[p.reason ?? ""] ?? "a failed run"}`;
  } else {
    base = SOURCE_REASONS[p.source] ?? "this repository's default";
  }
  if (p.recommended && p.recommended !== p.name && p.source !== "scoper") {
    base += `; the planner suggested ${p.recommended}`;
  }
  return base;
}

function pathOf(p: ProfileInput, flow?: Pick<Flow, "plan" | "nodes">): string[] {
  if (p.path && p.path.length > 0) return p.path;
  if (flow?.plan && flow.plan.length > 0) {
    if (flow.plan.length === 1 && flow.plan[0] === "triager") return flow.plan;
    return [...flow.plan, "CI", "merge gate"];
  }
  return [];
}

function cardRow(label: string, value: string): HTMLElement {
  const row = el("div", { className: "workflow-row" });
  row.append(
    el("span", { className: "workflow-label", textContent: label }),
    el("span", { className: "workflow-value", textContent: value }),
  );
  return row;
}

// costWords is the Cost row, as roomview.CostLine words it: a confirmed cost below the workflow's
// own ceiling caps the task, and the row says so instead of showing a figure the task will not reach.
// Under a confirmed cost a step-up runs only when its ceiling fits: one above it is offered, never
// run (Maku #720). A step-up figure no higher than the ceiling is left out.
export function costWords(p: ProfileInput, ceiling: number): string {
  if (ceiling <= 0) return "";
  const confirmed = p.confirmedMicroUSD ?? 0;
  const capped = confirmed > 0 && ceiling <= confirmed;
  let cost = `up to ${fmtDollars(ceiling)}`;
  if (capped && (p.workflowCeilingMicroUSD ?? 0) > ceiling) {
    cost = `capped at your confirmed ${fmtDollars(confirmed)} (${p.name}'s ceiling is ${fmtDollars(p.workflowCeilingMicroUSD!)})`;
  }
  const stepUp = p.stepUpCeilingMicroUSD ?? 0;
  if (stepUp > 0 && capped && stepUp > confirmed) {
    cost += ` \u00b7 a step-up${p.stepUpTo ? ` to ${p.stepUpTo}` : ""} (${fmtDollars(stepUp)}) is offered, not run`;
  } else if (stepUp > ceiling && capped) cost += ` \u00b7 up to ${fmtDollars(stepUp)} if it steps up, within your ${fmtDollars(confirmed)}`;
  else if (stepUp > 0 && capped) cost += ` \u00b7 a step-up stays within your ${fmtDollars(confirmed)}`;
  else if (stepUp > ceiling) cost += ` \u00b7 up to ${fmtDollars(stepUp)} if it steps up`;
  return cost;
}

// workflowCard renders the open Workflow card for the room header (WR3).
export function workflowCard(
  profile: ProfileInput,
  flow?: Pick<Flow, "plan" | "nodes">,
  fallbackCeilingMicroUSD?: number,
  detailsOpen = false,
): HTMLElement {
  const section = el("section", { className: "workflow-card" });
  section.dataset.block = "profile";

  const head = el("div", { className: "workflow-head" });
  head.append(cardRow("Workflow", `${profile.name} \u00b7 ${reasonOf(profile)}`));

  const path = pathOf(profile, flow);
  if (path.length > 0) {
    head.append(cardRow("Will run", path.join(" \u2192 ")));
  }

  const cost = costWords(profile, profile.ceilingMicroUSD || fallbackCeilingMicroUSD || 0);
  if (cost) head.append(cardRow("Cost", cost));

  if (profile.requested && profile.refused) {
    head.append(el("p", {
      className: "workflow-refused",
      textContent: `${profile.requested} was not applied (${shownRefusal(profile.refused)})`,
    }));
  }
  section.append(head);

  const d = el("details", { className: "workflow-details", open: detailsOpen });
  d.append(
    el("summary", { textContent: "How it was chosen" }),
    el("p", { className: "profile-source", textContent: `Source: ${profile.source}` }),
  );
  if (profile.cohort === "control" || profile.cohort === "treatment") {
    d.append(el("p", { className: "profile-cohort", textContent: `Cohort: ${profile.cohort}` }));
  }
  if (profile.requested) d.append(el("p", { className: "profile-requested", textContent: `Requested: ${profile.requested}` }));
  if (profile.refused) d.append(el("p", { className: "profile-refused", textContent: `Refused: ${shownRefusal(profile.refused)}` }));
  if (profile.recommended) d.append(el("p", { className: "profile-recommended", textContent: `Recommended: ${profile.recommended}` }));
  if (profile.reason) d.append(el("p", { className: "profile-reason", textContent: `Reason: ${profile.reason}` }));
  if (profile.why) d.append(el("p", { className: "profile-why", textContent: `Why: ${profile.why}` }));
  section.append(d);

  return section;
}

// renderProfile fills root with the workflow card, or empties it when the summary predates profile facts.
export function renderProfile(root: HTMLElement, s: Summary): void {
  const p = s.status.profile;
  if (!p) {
    root.replaceChildren();
    return;
  }
  const open = !!root.querySelector<HTMLDetailsElement>("details.workflow-details")?.open;
  root.replaceChildren(workflowCard(p, s.flow, s.status.spend?.limitMicroUSD, open));
}
