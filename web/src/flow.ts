// SPDX-License-Identifier: Apache-2.0

// The run rail's pure model: what a node's lane, chip and detail line say, which rounds fold, and
// the plan steps still to come. Role, persona, outcome and model are the log's text: each is
// matched against a fixed table with a fallback, never rendered into a class name.

import { span, timeEl } from "./duration";
import type { Flow, FlowNode } from "./summary";

export type Tone = "judge" | "writer";

const judges = new Set(["triager", "reviewer"]);

export function tone(role: string): Tone {
  return judges.has(role) ? "judge" : "writer";
}

export function personaLabel(persona: string): string {
  return persona.replaceAll("-", " ");
}

// isLive is the one liveness rule: a run is over once it has an end time or an outcome. The
// envelope allows an outcome with no end time: factsOf leaves one on a superseded run.
export function isLive(n: FlowNode): boolean {
  return !n.endedAt && !n.outcome;
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
  if (isLive(n)) return { text: "running", kind: "live" };
  if (n.judge?.last === "met") return { text: "✓", kind: "ok" };
  return { text: "ended", kind: "" };
}

// detailParts is the node's duration (with the instant it counts from) and its cost; a part the log
// lacks is left out. Its models are modelsOf's.
export function detailParts(n: FlowNode, now: number): { ms?: number; at?: string; cost?: string } {
  const out: { ms?: number; at?: string; cost?: string } = {};
  const start = n.startedAt ? Date.parse(n.startedAt) : NaN;
  if (!Number.isNaN(start) && (n.endedAt || isLive(n))) {
    const end = n.endedAt ? Date.parse(n.endedAt) : now;
    if (!Number.isNaN(end)) { out.ms = end - start; out.at = n.startedAt; }
  }
  if (n.microUSD) out.cost = `$${(n.microUSD / 1e6).toFixed(2)}`;
  return out;
}

// detail is detailParts as one line of text: "14 min · $0.82".
export function detail(n: FlowNode, now: number): string {
  const d = detailParts(n, now);
  return [d.ms === undefined ? "" : span(d.ms), d.cost ?? ""].filter(Boolean).join(" · ");
}

// fillDetail sets a node's age line: the duration as a <time>, then the cost.
export function fillDetail(e: HTMLElement, n: FlowNode, now: number): void {
  const d = detailParts(n, now);
  const kids: (Node | string)[] = [];
  if (d.ms !== undefined) kids.push(timeEl(d.ms, d.at!, "span"));
  if (d.cost) kids.push(kids.length ? ` · ${d.cost}` : d.cost);
  e.replaceChildren(...kids);
}

// modelsOf is what a node ran on: every model served (provenance.servedModels), then the alias its
// claim asked for when no served model is that alias. A tier alias such as agent-default says
// nothing about the model that answered, so it never stands in for one.
export function modelsOf(n: FlowNode): { served: string[]; asked?: string } {
  const served = n.models ?? [];
  return n.model && !served.includes(n.model) ? { served, asked: n.model } : { served };
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
    const foldable = count > max && i < rounds.length - 1 && r.nodes.length > 1 && r.nodes.every((n) => !isLive(n));
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
export const terminal = new Set(["Merged", "Done", "Reverted", "Escalated", "Closed", "Stopped", "NoOp", "Rejected", "Sealed"]);

// ghostMerge is whether the rail draws a ghost merge step: a plan that can reach a merge (not a
// manual room's, nor an investigation's triager alone), the task not over, nothing merged and no
// approved split, whose parts merge instead. The factory sets Verifying once an auto-merge landed,
// and never sets Merged.
export function ghostMerge(f: Flow, phase: string): boolean {
  const plan = f.plan ?? [];
  const merged = (p: string) => p === "Merged" || p === "Verifying";
  return plan.length > 0 && !(plan.length === 1 && plan[0] === "triager") && !f.split && !terminal.has(phase) && !merged(phase) && !f.milestones?.some((m) => merged(m.phase));
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

export type PartState = "done" | "dropped" | "reverted" | "running" | "queued";

// Parts open one at a time, so a part with no outcome is the one running; indices past the
// opened ones are not opened yet.
export function parts(split: NonNullable<Flow["split"]>): { index: number; issue?: number; room?: string; state: PartState }[] {
  const byIndex = new Map((split.parts ?? []).map((p) => [p.index, p]));
  const out: { index: number; issue?: number; room?: string; state: PartState }[] = [];
  for (let index = 1; index <= split.of; index++) {
    const p = byIndex.get(index);
    if (!p) { out.push({ index, state: "queued" }); continue; }
    const state: PartState = p.outcome === "landed" ? "done" : p.outcome === "dropped" ? "dropped" : p.outcome === "reverted" ? "reverted" : "running";
    out.push({ index, issue: p.issue, room: p.room, state });
  }
  return out;
}
