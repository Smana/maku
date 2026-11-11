import { describe, expect, it } from "vitest";
import { chip, detail, ghostMerge, modelsOf, ghostsAfter, isLive, parts, personaLabel, rows, tone } from "../src/flow";
import type { Flow, FlowNode } from "../src/summary";

const node = (o: Partial<FlowNode> = {}): FlowNode => ({
  run: "r", persona: "implementer", role: "implementer", round: 1, fromSeq: 1, toSeq: 2,
  startedAt: "2026-10-08T10:00:00Z", endedAt: "2026-10-08T10:14:00Z", tools: { ok: 0, failed: 0 }, ...o,
});

describe("tone", () => {
  it.each([["triager", "judge"], ["reviewer", "judge"], ["implementer", "writer"], ["tester", "writer"], ["unknown", "writer"], ["<b>x</b>", "writer"], ["constructor", "writer"]])(
    "%s is %s", (role, want) => expect(tone(role)).toBe(want));
});

it("personaLabel spaces a hyphen", () => expect(personaLabel("security-reviewer")).toBe("security reviewer"));

describe("chip", () => {
  it.each<[string, Partial<FlowNode>, string, string]>([
    ["approve", { verdict: "approve" }, "✓ approve", "ok"],
    ["changes", { verdict: "changes" }, "✗ changes", "changes"],
    ["succeeded", { outcome: "succeeded" }, "✓", "ok"],
    ["impossible", { outcome: "failed", judge: { count: 1, last: "impossible" } }, "impossible", "fail"],
    ["failed", { outcome: "failed" }, "failed", "fail"],
    ["lost", { outcome: "lost" }, "failed", "fail"],
    ["revoked", { outcome: "revoked" }, "stopped", "stop"],
    ["budget", { outcome: "budget_exhausted" }, "stopped", "stop"],
    ["live", { endedAt: undefined }, "running", "live"],
    ["ended, no outcome", {}, "ended", ""],
    ["ended, judge met", { judge: { count: 1, last: "met" } }, "✓", "ok"],
    ["live, judge met", { endedAt: undefined, judge: { count: 1, last: "met" } }, "running", "live"],
    ["an outcome and no end time", { endedAt: undefined, outcome: "ended" }, "ended", ""],
    ["an outcome, no end time, judge met", { endedAt: undefined, outcome: "ended", judge: { count: 1, last: "met" } }, "✓", "ok"],
  ])("%s", (_n, o, text, kind) => expect(chip(node(o))).toEqual({ text, kind }));
});

describe("isLive", () => {
  it.each<[string, Partial<FlowNode>, boolean]>([
    ["no end, no outcome", { endedAt: undefined }, true],
    ["an end time", {}, false],
    ["an outcome and no end time", { endedAt: undefined, outcome: "ended" }, false],
  ])("%s", (_n, o, want) => expect(isLive(node(o))).toBe(want));
});

describe("detail", () => {
  const now = Date.parse("2026-10-08T11:00:00Z");
  it("joins duration and cost; the models have their own line", () =>
    expect(detail(node({ microUSD: 820000, model: "glm-5.3", models: ["glm-5.3"] }), now)).toBe("14 min · $0.82"));
  it("leaves a missing part out with its dot", () =>
    expect(detail(node({ startedAt: undefined, microUSD: 10000 }), now)).toBe("$0.01"));
  it("counts a live run up to now", () => expect(detail(node({ endedAt: undefined }), now)).toBe("1 h"));
  it("a run over with no end time has no duration, rather than one that grows", () =>
    expect(detail(node({ endedAt: undefined, outcome: "ended", microUSD: 10000 }), now)).toBe("$0.01"));
});

// The served models lead; the alias asked for is secondary, and only when no served model is it.
describe("modelsOf", () => {
  it.each<[string, Partial<FlowNode>, { served: string[]; asked?: string }]>([
    ["one served model", { models: ["glm-5.3"] }, { served: ["glm-5.3"] }],
    ["the alias equal to the served model", { model: "glm-5.3", models: ["glm-5.3"] }, { served: ["glm-5.3"] }],
    ["an alias served by another model", { model: "agent-default", models: ["glm-5.3"] }, { served: ["glm-5.3"], asked: "agent-default" }],
    ["several models, every one", { model: "agent-default", models: ["glm-5.3", "claude-haiku-4.5"] }, { served: ["glm-5.3", "claude-haiku-4.5"], asked: "agent-default" }],
    ["the alias among several", { model: "glm-5.3", models: ["claude-haiku-4.5", "glm-5.3"] }, { served: ["claude-haiku-4.5", "glm-5.3"] }],
    ["nothing served yet", { model: "agent-default" }, { served: [], asked: "agent-default" }],
    ["no model at all", {}, { served: [] }],
  ])("%s", (_n, o, want) => expect(modelsOf(node(o))).toEqual(want));
});

describe("rows", () => {
  const mk = (rounds: number[], live = false) => rounds.map((round, i) => node({ run: `n${i}`, round, endedAt: live && i === rounds.length - 1 ? undefined : "2026-10-08T10:14:00Z" }));
  it("folds the oldest closed round", () => {
    const r = rows(mk([1, 1, 1, 2, 2, 2, 3, 3, 3, 3]), 8);
    expect(r).toHaveLength(8);
    expect("round" in r[0] && r[0].round).toBe(1);
    expect("round" in r[0] && r[0].nodes).toHaveLength(3);
    expect(r.slice(1).every((x) => "node" in x)).toBe(true);
  });
  it("folds nothing at the cap", () => expect(rows(mk([1, 1, 2, 2, 2, 3, 3, 3]), 8).every((x) => "node" in x)).toBe(true));
  it("never folds the round holding a live node", () => {
    const r = rows(mk([1, 1, 1, 1, 1, 1, 1, 1, 1, 1], true), 8);
    expect(r.every((x) => "node" in x)).toBe(true);
  });
  it("folds a round whose runs have an outcome and no end time", () => {
    const over = mk([1, 1, 1, 2, 2, 2, 3, 3, 3, 3]).map((n) => (n.round === 1 ? { ...n, endedAt: undefined, outcome: "ended" } : n));
    expect("round" in rows(over, 8)[0]).toBe(true);
  });
});

describe("ghosts", () => {
  const plan = ["scoper", "tester", "implementer", "reviewer"];
  const f = (milestones: Flow["milestones"], p: string[] | undefined = plan): Flow => ({ nodes: [], milestones, plan: p });
  it("ghostMerge", () => {
    expect(ghostMerge(f(undefined), "Reviewing")).toBe(true);
    expect(ghostMerge({ nodes: [] }, "Reviewing")).toBe(false);
    expect(ghostMerge(f(undefined, []), "Reviewing")).toBe(false);
    expect(ghostMerge(f(undefined, ["triager"]), "Reviewing")).toBe(false);
    expect(ghostMerge(f(undefined), "Sealed")).toBe(false);
    expect(ghostMerge(f([{ phase: "Merged", at: "t", seq: 1 }]), "Reviewing")).toBe(false);
    expect(ghostMerge(f(undefined), "Done")).toBe(false);
    expect(ghostMerge(f([{ phase: "AutoMerging", at: "t", seq: 1 }]), "AutoMerging")).toBe(true);
    expect(ghostMerge(f(undefined), "Verifying")).toBe(false);
    expect(ghostMerge({ ...f(undefined), split: { of: 2 } }, "Split")).toBe(false);
    expect(ghostMerge(f([{ phase: "Verifying", at: "t", seq: 1 }]), "Reverted")).toBe(false);
  });
  it("ghostsAfter the matched steps", () => {
    expect(ghostsAfter(plan, [node({ persona: "scoper" }), node({ persona: "tester" })])).toEqual(["implementer", "reviewer"]);
  });
  it("ghostsAfter a skipped step", () => {
    expect(ghostsAfter(plan, [node({ persona: "scoper" }), node({ persona: "implementer" })])).toEqual(["reviewer"]);
  });
  it("ghostsAfter a repeated persona does not move back", () => {
    expect(ghostsAfter(plan, [node({ persona: "implementer" }), node({ persona: "scoper" })])).toEqual(["reviewer"]);
  });
  it("ghostsAfter no runs", () => expect(ghostsAfter(plan, [])).toEqual(plan));
});

describe("parts", () => {
  it("reads each chip's state, the newest unfinished part running and the rest queued", () => {
    expect(parts({ of: 3, parts: [{ index: 1, issue: 11, room: "aaaaaaaa", outcome: "landed" }, { index: 2, issue: 12, room: "bbbbbbbb" }] }).map((p) => p.state))
      .toEqual(["done", "running", "queued"]);
  });
  it.each([["dropped", "dropped"], ["reverted", "reverted"]])("outcome %s is %s", (outcome, state) => {
    expect(parts({ of: 1, parts: [{ index: 1, issue: 11, room: "aaaaaaaa", outcome }] })[0].state).toBe(state);
  });
  it("a queued part has no issue or room yet", () => {
    expect(parts({ of: 2 })).toEqual([{ index: 1, state: "queued" }, { index: 2, state: "queued" }]);
  });
});
