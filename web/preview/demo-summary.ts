// SPDX-License-Identifier: Apache-2.0

// The demo's summary as the replay goes. The recorded summary is the run's end state;
// shown from the first event it would give the ending away (the verdict, the merge
// waiting). summaryAt folds the events shown so far into the fields the page reads,
// and hands back the recorded summary once the replay is done.

import type { RoomEvent } from "../src/conn";
import { ghostsAfter } from "../src/flow";
import type { Flow, FlowNode, Summary } from "../src/summary";

const plan = ["implementer", "reviewer"];
// The phases a task passes after the review, in order: the final phase brings the ones before it.
const afterReview = ["AwaitingHuman", "Merged", "Done"];

// flowAt is the rail's path from the events shown: a node per run as it joins, ended when it
// leaves; the milestones only once the replay is done.
function flowAt(final: Summary, shown: RoomEvent[], done: boolean): Flow {
  const nodes = new Map<string, FlowNode>();
  const roles = new Map<string, string>();
  const outcomes = new Map<string, string>();
  for (const e of shown) {
    const p = e.payload;
    const id = e.runId;
    if (e.type === "state_changed" && id && p.kind === "run_requested") roles.set(id, p.role);
    if (e.type === "state_changed" && id && p.kind === "run_phase" && (p.phase === "Succeeded" || p.phase === "Failed")) outcomes.set(id, String(p.phase).toLowerCase());
    if (e.type === "participant" && id && p.change === "joined" && !nodes.has(id)) {
      const role = roles.get(id) ?? String(p.role);
      nodes.set(id, { run: id, persona: role, role, round: 1, startedAt: e.ts, fromSeq: e.seq, toSeq: 0, tools: { ok: 0, failed: 0 },
        ...(role === "reviewer" && final.status.pr ? { edge: `PR #${final.status.pr.number}` } : {}) });
    }
    const n = id ? nodes.get(id) : undefined;
    if (!n) continue;
    if (e.type === "participant" && p.change === "left") { n.endedAt = e.ts; n.toSeq = e.seq; n.outcome = outcomes.get(n.run); }
    if (e.type === "tool_result") n.tools[p.status === "ok" ? "ok" : "failed"]++;
    if (e.type === "message" && p.kind === "review_verdict") n.verdict = p.verdict;
  }
  const list = [...nodes.values()];
  const flow: Flow = { plan, nodes: list, ghosts: ghostsAfter(plan, list) };
  const last = shown[shown.length - 1];
  if (done && last) {
    const upTo = afterReview.indexOf(final.status.phase);
    flow.milestones = afterReview.slice(0, Math.max(upTo, 0) + 1).map((phase) => ({ phase, at: last.ts, seq: last.seq }));
  }
  return flow;
}

export function summaryAt(final: Summary, shown: RoomEvent[], total: number, done: boolean): Summary {
  const notes = { untrusted: true as const, items: shown.filter((e) => e.type === "message" && e.payload.kind === "progress")
    .map((e) => ({ at: e.ts, run: e.runId ?? "", text: String(e.payload.text) })) };
  const flow = flowAt(final, shown, done);
  if (done) return { ...final, notes, flow };

  const roles = new Map<string, string>();
  let run = null as Summary["status"]["run"];
  let handedOff = false;
  let verdict: Summary["status"]["lastVerdict"] = null;
  let pr: Summary["status"]["pr"] = null;
  for (const e of shown) {
    const p = e.payload;
    if (e.type === "state_changed" && p.kind === "run_requested") roles.set(p.runId, p.role);
    if (e.type === "state_changed" && p.kind === "run_phase" && e.runId)
      run = p.phase === "Running" ? { id: e.runId, role: roles.get(e.runId) ?? "" } : run?.id === e.runId ? null : run;
    if (e.type === "handoff") handedOff = true;
    if (e.type === "message" && p.kind === "review_verdict") verdict = { by: e.actor.id, verdict: p.verdict, at: e.ts };
    if (e.type === "tool_result" && final.status.pr && String(p.output).includes(final.status.pr.url)) pr = final.status.pr;
  }
  const budget = final.status.budget && { ...final.status.budget, usedTokens: Math.round(final.status.budget.usedTokens * shown.length / total) };
  return {
    ...final,
    status: { ...final.status, phase: verdict ? final.status.phase : handedOff ? "Reviewing" : "Implementing", run, budget, pr, lastVerdict: verdict,
      reason: verdict ? final.status.reason : undefined, next: verdict ? final.status.next : undefined },
    needsYou: verdict ? final.needsYou : [],
    notes,
    flow,
  };
}
