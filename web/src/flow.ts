// SPDX-License-Identifier: Apache-2.0

// The run rail's pure model: what a node's lane, chip and detail line say, which rounds fold, and
// the plan steps still to come. Role, persona, outcome and model are the log's text: each is
// matched against a fixed table with a fallback, never rendered into a class name.

import type { Flow, FlowNode } from "./summary";

export type Tone = "judge" | "writer";

const judges = new Set(["triager", "reviewer"]);

export function tone(role: string): Tone {
  return judges.has(role) ? "judge" : "writer";
}

export function personaLabel(persona: string): string {
  return persona.replaceAll("-", " ");
}

export type ChipKind = "ok" | "changes" | "fail" | "stop" | "live" | "";

export function chip(n: FlowNode): { text: string; kind: ChipKind } {
  if (n.verdict === "approve") return { text: "✓ approve", kind: "ok" };
  if (n.verdict === "changes") return { text: "✗ changes", kind: "changes" };
  switch (n.outcome) {
    case "succeeded": return { text: "✓", kind: "ok" };
    case "failed": return n.judge?.last === "impossible" ? { text: "impossible", kind: "fail" } : { text: "failed", kind: "fail" };
    case "lost": return { text: "failed", kind: "fail" };
    case "revoked": case "budget_exhausted": return { text: "stopped", kind: "stop" };
  }
  if (!n.endedAt) return { text: "running", kind: "live" };
  return { text: "ended", kind: "" };
}

function duration(ms: number): string {
  const m = Math.max(0, Math.round(ms / 60000));
  if (m >= 1) return `${m} m`;
  return `${Math.max(0, Math.round(ms / 1000))} s`;
}

// detail is the node's duration, cost and served model; a part the log lacks is left out.
export function detail(n: FlowNode, now: number): string {
  const parts: string[] = [];
  const start = n.startedAt ? Date.parse(n.startedAt) : NaN;
  if (!Number.isNaN(start)) {
    const end = n.endedAt ? Date.parse(n.endedAt) : now;
    if (!Number.isNaN(end)) parts.push(duration(end - start));
  }
  if (n.microUSD) parts.push(`$${(n.microUSD / 1e6).toFixed(2)}`);
  if (n.model) parts.push(n.model);
  return parts.join(" · ");
}

export type Row = { node: FlowNode } | { round: number; nodes: FlowNode[] };

// rows is the rail's rows: the oldest closed rounds fold into one row each until at most `max`
// remain. The round holding a live node and the last round stay open.
export function rows(nodes: FlowNode[], max: number): Row[] {
  const rounds: { round: number; nodes: FlowNode[] }[] = [];
  for (const n of nodes) {
    const last = rounds[rounds.length - 1];
    if (last && last.round === n.round) last.nodes.push(n);
    else rounds.push({ round: n.round, nodes: [n] });
  }
  let count = nodes.length;
  const out: Row[] = [];
  rounds.forEach((r, i) => {
    const foldable = count > max && i < rounds.length - 1 && r.nodes.length > 1 && r.nodes.every((n) => n.endedAt);
    if (foldable) {
      count -= r.nodes.length - 1;
      out.push({ round: r.round, nodes: r.nodes });
    } else {
      for (const node of r.nodes) out.push({ node });
    }
  });
  return out;
}

// The phases that close a task's path (internal/summary terminalPhase).
const terminal = new Set(["Merged", "Done", "Reverted", "Escalated", "Closed", "Stopped", "NoOp", "Rejected"]);

// ghostMerge is whether the rail draws a ghost merge step: the task is not over and nothing merged.
export function ghostMerge(f: Flow, phase: string): boolean {
  return !terminal.has(phase) && !f.milestones?.some((m) => m.phase === "Merged");
}

// ghostsAfter mirrors the broker's cursor (internal/summary flow.go): each node advances past the
// first plan step, at or after the cursor, named for its persona.
export function ghostsAfter(plan: string[], nodes: FlowNode[]): string[] {
  let cursor = 0;
  for (const n of nodes) {
    const k = plan.slice(Math.min(cursor, plan.length)).indexOf(n.persona);
    if (k >= 0) cursor += k + 1;
  }
  return plan.slice(cursor);
}
