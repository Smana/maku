// SPDX-License-Identifier: Apache-2.0

import { REASONS } from "./reasons";
import { timeText } from "./settings-api";

export interface DecisionRows {
  counted: boolean; files: number; maxFiles: number; lines: number; maxLines: number;
  shape?: string; holdout?: string; oracle?: string; age?: string; reviewer?: string;
}
export interface RecentDecision {
  at: string; pullRequest: number; head: string; outcome: string; policy: string; reason: string;
  rows: DecisionRows; considered?: string; commentURL: string;
}
export interface Recent { repository: string; since: string; decisions: RecentDecision[] }
export interface DecisionCounts { automatic: number; wouldMerge: number; waited: number }

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

function reasonWords(code: string): string {
  return REASONS[code]?.long ?? code.replaceAll("_", " ");
}

export function outcomeWords(d: RecentDecision): string {
  if (["arm", "auto", "merge", "merged"].includes(d.outcome)) return "Merged automatically";
  if (d.outcome === "shadow" && d.reason === "shadow_would_arm") return "Would merge";
  if (d.outcome === "shadow") return "Shadow decision";
  return "Waited for a maintainer";
}

function measurement(d: RecentDecision, limits: boolean): string[] {
  if (!d.rows.counted) return [];
  const out: string[] = [];
  if (limits && d.rows.maxLines > 0) out.push(`${d.rows.lines} lines changed, limit ${d.rows.maxLines}`);
  if (limits && d.rows.maxFiles > 0) out.push(`${d.rows.files} files changed, limit ${d.rows.maxFiles}`);
  if (out.length === 0) out.push(`${d.rows.files} files and ${d.rows.lines} lines changed`);
  return out;
}

export function decisionCriterion(d: RecentDecision): string {
  const measured = measurement(d, d.reason === "patch_too_large");
  const words = d.reason === "clean" || d.reason === "armed" ? "" : reasonWords(d.reason);
  const main = d.reason === "patch_too_large" && measured.length ? measured.join("; ") : [...measured, words].filter(Boolean).join("; ");
  if (d.reason !== "no_policy_matched" || !d.considered) return main;
  const considered = d.considered.split(",").filter(Boolean).map((entry) => {
    const [policy, reason] = entry.split(":", 2);
    return `${policy}: ${reasonWords(reason ?? "")}`;
  });
  return [main, ...considered].join("; ");
}

export function recentDecisions(recent: Recent, now: number = Date.now()): HTMLTableElement {
  const table = el("table", undefined, "rooms settings-table decisions-table");
  const headings = ["Pull request", "Outcome", "Policy", "Deciding criterion", "When"];
  const head = el("tr");
  for (const label of headings) head.append(el("th", label));
  table.append(el("thead"), el("tbody"));
  table.tHead!.append(head);
  for (const d of recent.decisions) {
    const row = el("tr");
    const link = el("a", `#${d.pullRequest}`);
    link.href = d.commentURL;
    const cells: (string | Node)[] = [link, outcomeWords(d), d.policy, decisionCriterion(d), timeText(d.at, now)];
    for (const [i, value] of cells.entries()) {
      const cell = el("td");
      cell.dataset.label = headings[i];
      cell.append(value);
      row.append(cell);
    }
    table.tBodies[0].append(row);
  }
  return table;
}

const rank = (d: RecentDecision): number => {
  if (["arm", "auto", "merge", "merged"].includes(d.outcome)) return 3;
  if (d.outcome === "shadow" && d.reason === "shadow_would_arm") return 2;
  return 1;
};

export function decisionCounts(recent: Recent, now: number = Date.now()): DecisionCounts {
  const byPR = new Map<number, RecentDecision>();
  for (const d of recent.decisions) {
    const old = byPR.get(d.pullRequest);
    if (!old || rank(d) > rank(old)) byPR.set(d.pullRequest, d);
  }
  const out: DecisionCounts = { automatic: 0, wouldMerge: 0, waited: 0 };
  const cutoff = now - 7 * 24 * 60 * 60 * 1000;
  for (const d of byPR.values()) {
    if (rank(d) === 3) out.automatic++;
    else if (rank(d) === 2) out.wouldMerge++;
    else if (Date.parse(d.at) >= cutoff) out.waited++;
  }
  return out;
}
