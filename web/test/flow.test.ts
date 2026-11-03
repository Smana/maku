import { describe, expect, it } from "vitest";
import { chip, detail, ghostMerge, ghostsAfter, parts, personaLabel, rows, tone } from "../src/flow";
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
  ])("%s", (_n, o, text, kind) => expect(chip(node(o))).toEqual({ text, kind }));
});

describe("detail", () => {
  const now = Date.parse("2026-10-08T11:00:00Z");
  it("joins duration, cost and model", () =>
    expect(detail(node({ microUSD: 820000, model: "glm-5.3" }), now)).toBe("14 m · $0.82 · glm-5.3"));
  it("leaves a missing part out with its dot", () => {
    expect(detail(node({ model: "glm-5.3" }), now)).toBe("14 m · glm-5.3");
    expect(detail(node({ startedAt: undefined, microUSD: 10000 }), now)).toBe("$0.01");
  });
  it("counts a live run up to now", () => expect(detail(node({ endedAt: undefined }), now)).toBe("60 m"));
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
});

describe("ghosts", () => {
  const plan = ["scoper", "tester", "implementer", "reviewer"];
  const f = (milestones: Flow["milestones"]): Flow => ({ nodes: [], milestones });
  it("ghostMerge", () => {
    expect(ghostMerge(f(undefined), "Reviewing")).toBe(true);
    expect(ghostMerge(f([{ phase: "Merged", at: "t", seq: 1 }]), "Reviewing")).toBe(false);
    expect(ghostMerge(f(undefined), "Done")).toBe(false);
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
