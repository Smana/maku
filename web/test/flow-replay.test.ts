import { describe, expect, it, vi } from "vitest";
import { ghostsAfter } from "../src/flow";
import { mountRail } from "../src/rail";
import { beats, frames, maxReplay, replay } from "../src/replay";
import type { Flow, FlowNode } from "../src/summary";

const node = (i: number, persona = "implementer"): FlowNode => ({
  run: `r-${i}`, persona, role: "implementer", round: 1, fromSeq: i * 10, toSeq: i * 10 + 9,
  startedAt: "2026-11-03T10:00:00Z", endedAt: "2026-11-03T10:05:00Z", outcome: "succeeded", tools: { ok: 0, failed: 0 },
});
const flow: Flow = {
  plan: ["scoper", "implementer", "reviewer", "merger"],
  nodes: [node(1, "scoper"), node(2), node(3, "reviewer")],
  ghosts: ["merger"],
  milestones: [{ phase: "AwaitingCI", at: "2026-11-03T10:10:00Z", seq: 40 }, { phase: "Merged", at: "2026-11-03T10:11:00Z", seq: 50 }],
};

// fakeTimers runs the scheduled steps in order when the test says so.
function fakeTimers() {
  const q: { fn: () => void; ms: number; live: boolean }[] = [];
  return {
    schedule: (fn: () => void, ms: number) => { const t = { fn, ms, live: true }; q.push(t); return t; },
    cancel: (h: unknown) => { (h as { live: boolean }).live = false; },
    fire() { const t = q.shift(); if (t?.live) t.fn(); return t?.ms; },
    pending: () => q.filter((t) => t.live).length,
  };
}

const spy = () => { const calls: Flow[] = []; return { calls, rail: { set: (f: Flow | undefined) => void calls.push(f!) } }; };

describe("frames and beats", () => {
  it("cuts the flow after each node, then each milestone", () => {
    const fs = frames(flow);
    expect(fs).toHaveLength(6);
    expect(fs[0].nodes).toEqual([]);
    expect(fs[0].ghosts).toEqual(flow.plan);
    expect(fs[1].ghosts).toEqual(ghostsAfter(flow.plan!, flow.nodes.slice(0, 1)));
    expect(fs[3].milestones).toEqual([]);
    expect(fs[4].milestones).toHaveLength(1);
    expect(fs[5]).toEqual(flow);
  });

  it("a milestone recorded before a later node shows with it", () => {
    const f: Flow = { ...flow, milestones: [{ phase: "AwaitingCI", at: "x", seq: 25 }] };
    const fs = frames(f);
    expect(fs.map((x) => x.milestones?.length ?? 0)).toEqual([0, 0, 0, 1]);
  });

  it("a fact-only node at seq 0 cannot make a later frame show fewer milestones", () => {
    const f: Flow = { ...flow, nodes: [node(1, "scoper"), node(2), { ...node(3, "reviewer"), fromSeq: 0 }], milestones: [{ phase: "AwaitingCI", at: "x", seq: 15 }] };
    const counts = frames(f).map((x) => x.milestones?.length ?? 0);
    expect(counts).toEqual([0, 0, 1, 1]);
    expect(counts).toEqual([...counts].sort());
  });

  it("beats are 1200 per node, 400 per milestone", () => {
    expect(beats(flow)).toEqual([1200, 1200, 1200, 400, 400]);
  });

  it("scales a long task to fit the cap by one factor", () => {
    const nodes = Array.from({ length: 20 }, (_, i) => node(i + 1));
    const ms = Array.from({ length: 4 }, (_, i) => ({ phase: `P${i}`, at: "x", seq: 1000 + i }));
    const b = beats({ plan: [], nodes, milestones: ms });
    expect(b).toHaveLength(24);
    expect(b.reduce((a, x) => a + x, 0)).toBeLessThanOrEqual(maxReplay);
    expect(b[0] / b[23]).toBeCloseTo(3);
  });
});

describe("replay", () => {
  it("sets each frame in order on the beats, then resolves", async () => {
    const t = fakeTimers();
    const { rail, calls } = spy();
    const r = replay(rail, flow, "Merged", { reduced: false, schedule: t.schedule, cancel: t.cancel });
    expect(calls).toHaveLength(1);
    const delays: (number | undefined)[] = [];
    while (t.pending()) delays.push(t.fire());
    await r.done;
    expect(calls).toEqual(frames(flow));
    expect(delays).toEqual([1200, 1200, 1200, 400, 400]);
  });

  it("skip sets the last frame at once and resolves", async () => {
    const t = fakeTimers();
    const { rail, calls } = spy();
    const r = replay(rail, flow, "Merged", { reduced: false, schedule: t.schedule, cancel: t.cancel });
    r.skip();
    await r.done;
    expect(calls.at(-1)).toEqual(flow);
    expect(t.pending()).toBe(0);
  });

  it("reduced motion schedules nothing and steps on next", async () => {
    const t = fakeTimers();
    const { rail, calls } = spy();
    const r = replay(rail, flow, "Merged", { reduced: true, schedule: t.schedule, cancel: t.cancel });
    expect(t.pending()).toBe(0);
    expect(calls).toHaveLength(1);
    for (let i = 0; i < 5; i++) r.next();
    await r.done;
    expect(calls).toEqual(frames(flow));
    expect(t.pending()).toBe(0);
  });
});

describe("rail replay button", () => {
  const mount = (reduced = false) => {
    const t = fakeTimers();
    const rail = mountRail({ select: vi.fn() }, { reduced: () => reduced, schedule: t.schedule, cancel: t.cancel });
    document.body.replaceChildren(rail.el);
    const btn = (name: string) => [...rail.el.querySelectorAll<HTMLButtonElement>("button.rail-replay")].find((b) => b.textContent === name && !b.hidden);
    return { rail, t, btn };
  };
  const nodesShown = (rail: { el: HTMLElement }) => rail.el.querySelectorAll('li.rail-node:not([data-state="ghost"])').length;

  it("shows Replay only once the phase is terminal", () => {
    const { rail, btn } = mount();
    rail.set(flow, "Implementing", 0);
    expect(btn("Replay")).toBeUndefined();
    rail.set(flow, "Merged", 0);
    expect(btn("Replay")).toBeDefined();
  });

  it("swaps Replay for Skip while it runs, and back at the end", () => {
    const { rail, t, btn } = mount();
    rail.set(flow, "Merged", 0);
    btn("Replay")!.click();
    expect(btn("Replay")).toBeUndefined();
    expect(nodesShown(rail)).toBe(0);
    t.fire();
    expect(nodesShown(rail)).toBe(1);
    btn("Skip")!.click();
    expect(nodesShown(rail)).toBe(3);
    expect(btn("Skip")).toBeUndefined();
    expect(btn("Replay")).toBeDefined();
  });

  it("a refetch during a replay waits for its end, then applies", () => {
    const { rail, t, btn } = mount();
    rail.set(flow, "Merged", 0);
    btn("Replay")!.click();
    t.fire();
    const later: Flow = { ...flow, nodes: [...flow.nodes, node(4, "merger")] };
    rail.set(later, "Merged", 0);
    expect(nodesShown(rail)).toBe(1);
    btn("Skip")!.click();
    expect(nodesShown(rail)).toBe(4);
  });

  it("dispose ends a running replay, drops its timers and never applies a held refetch", () => {
    const { rail, t, btn } = mount();
    rail.set(flow, "Merged", 0);
    btn("Replay")!.click();
    t.fire();
    rail.set({ ...flow, nodes: [...flow.nodes, node(4, "merger")] }, "Merged", 0);
    rail.dispose();
    expect(t.pending()).toBe(0);
    expect(nodesShown(rail)).toBe(1);
    expect(btn("Skip")).toBeUndefined();
  });

  it("under reduced motion Next steps one frame at a time", () => {
    const { rail, t, btn } = mount(true);
    rail.set(flow, "Merged", 0);
    btn("Replay")!.click();
    expect(t.pending()).toBe(0);
    expect(nodesShown(rail)).toBe(0);
    btn("Next")!.click();
    expect(nodesShown(rail)).toBe(1);
    btn("Next")!.click();
    expect(nodesShown(rail)).toBe(2);
  });
});
