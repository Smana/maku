import { describe, expect, it } from "vitest";
import { detail, ghostsAfter, terminal } from "../src/flow";
import { flowFixture, flowName, type FlowName } from "../preview/flow-fixtures";

const names: FlowName[] = ["default", "finished", "split", "part"];

describe("flow fixtures", () => {
  it.each(names)("%s: nodes keep their order, unique runs and ghosts follow the plan", (name) => {
    const { flow } = flowFixture(name);
    const froms = flow.nodes.filter((n) => n.fromSeq > 0).map((n) => n.fromSeq);
    expect(froms).toEqual([...froms].sort((a, b) => a - b));
    expect(new Set(flow.nodes.map((n) => n.run)).size).toBe(flow.nodes.length);
    expect(flow.ghosts ?? []).toEqual(ghostsAfter(flow.plan ?? [], flow.nodes));
    const starts = flow.nodes.map((n) => Date.parse(n.startedAt ?? ""));
    expect(starts).toEqual([...starts].sort((a, b) => a - b));
  });
  it("default: one live run on the fixture's range, a ghost reviewer, no milestone", () => {
    const { flow } = flowFixture("default");
    expect(flow.nodes.map((n) => n.persona)).toEqual(["scoper", "tester", "implementer"]);
    expect(flow.nodes.filter((n) => !n.endedAt).map((n) => [n.run, n.fromSeq])).toEqual([["ikely2yk", 4]]);
    expect(flow.ghosts).toEqual(["reviewer"]);
    expect(flow.milestones).toBeUndefined();
  });
  it("default: the implementer's step shows its second model", () => {
    const n = flowFixture("default").flow.nodes.find((x) => x.persona === "implementer")!;
    expect(detail(n, Date.parse("2026-10-04T14:00:00Z"))).toMatch(/glm-5\.3 \+1$/);
  });
  it("finished: every run ended, a terminal phase, milestones with rising seqs", () => {
    const { flow, phase } = flowFixture("finished");
    expect(flow.nodes.every((n) => n.endedAt)).toBe(true);
    expect(flow.nodes.at(-1)).toMatchObject({ persona: "reviewer", verdict: "approve", edge: "PR #41" });
    expect(terminal.has(phase)).toBe(true);
    const seqs = flow.milestones!.map((m) => m.seq);
    expect(flow.milestones!.map((m) => m.phase)).toEqual(["AwaitingCI", "AwaitingHuman", "Merged", "Done"]);
    expect(seqs).toEqual([...seqs].sort((a, b) => a - b));
  });
  it("split and part carry their links", () => {
    expect(flowFixture("split").flow.split).toMatchObject({ of: 3, parts: [{ index: 1, issue: 52, room: "k2mqx7ba", outcome: "landed" }, { index: 2, issue: 53, room: "p4zt6wce" }] });
    expect(flowFixture("split").flow.nodes[0].edge).toBeUndefined();
    expect(flowFixture("part").flow.nodes[0].edge).toBeUndefined();
    expect(flowFixture("part").flow.parent).toEqual({ room: "m3pq6xzb", issue: 50, index: 2, of: 3 });
  });
  it("picks the fixture by query param", () => {
    expect(flowName(new URLSearchParams("?finished"))).toBe("finished");
    expect(flowName(new URLSearchParams("?theme=dark&part"))).toBe("part");
    expect(flowName(new URLSearchParams("?theme=dark"))).toBe("default");
  });
});
