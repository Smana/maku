// SPDX-License-Identifier: Apache-2.0

// The run rail's flows for full.html, built on test/fixtures/live-session.jsonl: one implementer
// run, ikely2yk, whose log seqs are 4 (joined) to 27 (the last event; 3 ok and 1 rejected result).
// The runs before it are facts the log of this room does not hold, so their fromSeq is 0.

import { ghostsAfter } from "../src/flow";
import type { Flow, FlowNode, Milestone } from "../src/summary";

export type FlowName = "default" | "finished" | "split" | "part";

export interface FlowFixture { flow: Flow; phase: string }

const none = { ok: 0, failed: 0 };

const scoper: FlowNode = {
  run: "scopera2", persona: "scoper", role: "triager", round: 1, startedAt: "2026-10-04T13:20:00Z", endedAt: "2026-10-04T13:31:00Z",
  fromSeq: 0, toSeq: 0, outcome: "succeeded", microUSD: 40_000, model: "glm-5.3", tools: none,
};
// A node's edge names the edge into it, from the node before.
const tester: FlowNode = {
  run: "testerb3", persona: "tester", role: "tester", round: 1, startedAt: "2026-10-04T13:32:00Z", endedAt: "2026-10-04T13:48:00Z",
  fromSeq: 0, toSeq: 0, outcome: "succeeded", tools: none, edge: "plan",
};
const implementer: FlowNode = {
  run: "ikely2yk", persona: "implementer", role: "implementer", round: 1, startedAt: "2026-10-04T13:59:58.050000Z",
  fromSeq: 4, toSeq: 0, judge: { count: 2, last: "not_yet" }, tools: { ok: 3, failed: 1 }, edge: "failing test",
};
const reviewer: FlowNode = {
  run: "reviewc4", persona: "reviewer", role: "reviewer", round: 1, startedAt: "2026-10-04T14:09:00Z", endedAt: "2026-10-04T14:14:00Z",
  fromSeq: 0, toSeq: 0, outcome: "succeeded", verdict: "approve", tools: none, edge: "PR #41",
};

const plan = ["scoper", "tester", "implementer", "reviewer"];

function flow(nodes: FlowNode[], extra: Partial<Flow> = {}): Flow {
  return { plan, nodes, ghosts: ghostsAfter(plan, nodes), ...extra };
}

const ended = (n: FlowNode): FlowNode => ({ ...n, endedAt: "2026-10-04T14:05:00Z", toSeq: 27, outcome: "succeeded" });

const milestones: Milestone[] = [
  { phase: "AwaitingCI", at: "2026-10-04T14:06:00Z", seq: 28 },
  { phase: "AwaitingHuman", at: "2026-10-04T14:12:00Z", seq: 29 },
  { phase: "Merged", at: "2026-10-04T14:20:00Z", seq: 30 },
  { phase: "Done", at: "2026-10-04T14:20:05Z", seq: 31 },
];

export function flowFixture(name: FlowName): FlowFixture {
  switch (name) {
    case "finished":
      return { phase: "Done", flow: flow([scoper, tester, ended(implementer), reviewer], { milestones }) };
    case "split": {
      const s: FlowNode = { ...scoper, fromSeq: 4, toSeq: 27, handoff:"Too big for one pull request: three parts" };
      return { phase: "Done", flow: { plan: ["scoper"], nodes: [s], ghosts: [], split: { of: 3, parts: [
        { index: 1, issue: 52, room: "k2mqx7ba", outcome: "landed" }, { index: 2, issue: 53, room: "p4zt6wce" }] } } };
    }
    case "part":
      return { phase: "Implementing", flow: { ...flow([{ ...implementer, edge: undefined }]), plan: ["implementer", "reviewer"], ghosts: ["reviewer"], parent: { room: "m3pq6xzb", issue: 50, index: 2, of: 3 } } };
    default:
      return { phase: "Implementing", flow: flow([scoper, tester, implementer]) };
  }
}

// flowName reads the page's query: ?finished, ?split or ?part, else the default.
export function flowName(params: URLSearchParams): FlowName {
  for (const n of ["finished", "split", "part"] as const) if (params.has(n)) return n;
  return "default";
}
