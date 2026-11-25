// SPDX-License-Identifier: Apache-2.0

import { REASONS } from "./reasons";

export interface BacktestOutcome { verdict: string; reason?: string }
export interface BacktestRow { pullRequest: number; url: string; policy: string; before: BacktestOutcome; after: BacktestOutcome; change: string }
export interface Backtest {
  valid: boolean; errors?: string[]; repository: string; since: string;
  counts: { considered: number; changed: number; unchanged: number; unknown: number; matched: number };
  rows: BacktestRow[];
}

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

export function backtestOutcome(o: BacktestOutcome): string {
  const reason = REASONS[o.reason ?? ""]?.short ?? (o.reason ?? "").replaceAll("_", " ");
  return `${o.verdict.replaceAll("_", " ")}${reason ? `: ${reason}` : ""}`;
}

export function backtestView(bt: Backtest): Node[] {
  if (!bt.valid) return [el("p", `The factory refuses this backtest: ${(bt.errors ?? []).join("; ")}`, "notice")];
  const c = bt.counts;
  const summary = el("p", `${c.considered} decisions: ${c.changed} changed, ${c.unchanged} unchanged, ${c.unknown} unknown.`, "backtest-counts");
  const out: Node[] = [summary];
  if (c.considered > 0 && c.matched === 0) out.push(el("p", "Warning: the edited rule matched none of these pull requests.", "backtest-warning"));
  if (bt.rows.length === 0) {
    out.push(el("p", "No recorded decision changed."));
    return out;
  }
  const table = el("table", undefined, "rooms settings-table backtest-table");
  const headings = ["Pull request", "Policy", "Before", "After"];
  const head = el("tr");
  for (const h of headings) head.append(el("th", h));
  table.append(el("thead"), el("tbody"));
  table.tHead!.append(head);
  for (const row of bt.rows) {
    const tr = el("tr");
    const link = el("a", `#${row.pullRequest}`);
    link.href = row.url;
    const cells: (string | Node)[] = [link, row.policy, backtestOutcome(row.before), backtestOutcome(row.after)];
    for (const [i, value] of cells.entries()) {
      const td = el("td");
      td.dataset.label = headings[i];
      td.dataset.change = row.change;
      td.append(value);
      tr.append(td);
    }
    table.tBodies[0].append(tr);
  }
  out.push(table);
  return out;
}
