import { describe, expect, it, vi } from "vitest";
import type { RoomEvent } from "../src/conn";
import { mountRail } from "../src/rail";
import type { Flow, FlowNode } from "../src/summary";

const now = Date.parse("2026-11-03T10:20:00Z");

const node = (o: Partial<FlowNode> = {}): FlowNode => ({
  run: "r-1", persona: "implementer", role: "implementer", round: 1, fromSeq: 1, toSeq: 0,
  startedAt: "2026-11-03T10:00:00Z", tools: { ok: 0, failed: 0 }, ...o,
});

const scoper = node({ run: "r-s", persona: "scoper", role: "triager", endedAt: "2026-11-03T10:05:00Z", toSeq: 40, outcome: "succeeded" });
const implementer = node({ run: "r-i", fromSeq: 41, edge: "plan", tools: { ok: 3, failed: 1 } });
const flow: Flow = {
  plan: ["scoper", "implementer", "reviewer"], nodes: [scoper, implementer], ghosts: ["reviewer"],
  milestones: [{ phase: "AwaitingCI", at: "2026-11-03T10:10:00Z", seq: 90 }],
};

const ev = (o: Partial<RoomEvent>): RoomEvent => ({
  v: 1, id: "e", seq: 1, roomId: "room", actor: { kind: "agent", id: "run:r-i" }, type: "tool_result",
  origin: "bridge", ts: "2026-11-03T10:19:00Z", redactions: [], payload: {}, ...o,
});

function mount() {
  const h = { select: vi.fn() };
  const rail = mountRail(h);
  document.body.replaceChildren(rail.el);
  return { rail, h };
}

const nodeOf = (rail: { el: HTMLElement }, run: string) => rail.el.querySelector<HTMLElement>(`li.rail-node[data-run="${run}"]`)!;
const card = (rail: { el: HTMLElement }) => rail.el.querySelector<HTMLElement>(".rail-card")!;

describe("rail", () => {
  it("draws nodes, edges, ghosts and milestones in order", () => {
    const { rail } = mount();
    rail.set(flow, "AwaitingCI", now);
    const path = rail.el.querySelector('aside.rail > nav[aria-label="Task path"] > ol.rail-path')!;
    const items = [...path.children].map((li) => li.className + ":" + ((li as HTMLElement).dataset.state ?? ""));
    expect(items).toEqual(["rail-node:done", "rail-edge:", "rail-node:live", "rail-edge:ghost", "rail-node:ghost"]);
    expect(path.querySelectorAll("li.rail-node")).toHaveLength(3);
    const edge = path.children[1];
    expect(edge.querySelector("svg > path.rail-stroke")).not.toBeNull();
    expect(edge.querySelector(".rail-edge-label")!.textContent).toBe("plan");
    const ms = [...rail.el.querySelectorAll<HTMLElement>("ol.rail-milestones > li.rail-milestone")];
    expect(ms.map((m) => [m.dataset.phase, m.dataset.state ?? ""])).toEqual([["AwaitingCI", "done"], ["Merged", "ghost"]]);
  });

  it("renders a persona as text", () => {
    const { rail } = mount();
    rail.set({ nodes: [node({ persona: "security-reviewer", role: "reviewer" })] }, "Implementing", now);
    const n = rail.el.querySelector<HTMLElement>("li.rail-node")!;
    expect(n.querySelector(".rail-persona")!.textContent).toBe("security reviewer");
    expect(n.dataset.tone).toBe("judge");
  });

  it("an unknown role keys nothing on its text", () => {
    const { rail } = mount();
    rail.set({ nodes: [node({ role: '"><b>x</b>' })] }, "Implementing", now);
    const n = rail.el.querySelector<HTMLElement>("li.rail-node")!;
    expect(n.dataset.tone).toBe("writer");
    expect(rail.el.querySelector("b")).toBeNull();
  });

  it("the judge mark", () => {
    const { rail } = mount();
    rail.set({ nodes: [node({ judge: { count: 2, last: "not_yet" } })] }, "Implementing", now);
    expect(rail.el.querySelector("li.rail-node .rail-judge")!.textContent).toBe("◇×2");
  });

  it("tick ages the live node", () => {
    const { rail } = mount();
    rail.set(flow, "AwaitingCI", now);
    const before = nodeOf(rail, "r-i").querySelector(".rail-detail")!.textContent;
    const done = nodeOf(rail, "r-s").querySelector(".rail-detail")!.textContent;
    rail.tick(now + 60_000);
    expect(nodeOf(rail, "r-i").querySelector(".rail-detail")!.textContent).not.toBe(before);
    expect(nodeOf(rail, "r-s").querySelector(".rail-detail")!.textContent).toBe(done);
  });

  it("live counts a tool result", () => {
    const { rail } = mount();
    rail.set(flow, "AwaitingCI", now);
    const button = nodeOf(rail, "r-i").querySelector("button")!;
    button.focus();
    expect(card(rail).textContent).toContain("3 ok · 1 failed");
    rail.live(ev({ runId: "r-i", payload: { status: "error" } }));
    expect(card(rail).textContent).toContain("3 ok · 2 failed");
    rail.live(ev({ runId: "r-s", payload: { status: "error" } }));
    rail.live(ev({ runId: "r-i", type: "message", payload: { status: "error" } }));
    expect(card(rail).textContent).toContain("3 ok · 2 failed");
    rail.live(ev({ runId: "r-i", payload: { status: "ok" } }));
    expect(card(rail).textContent).toContain("4 ok · 2 failed");
  });

  it("announces a run that starts", () => {
    const { rail } = mount();
    rail.set({ nodes: [scoper] }, "Implementing", now);
    expect(rail.el.querySelector(".rail-live")!.textContent).toBe("");
    rail.set(flow, "Implementing", now);
    const live = rail.el.querySelector<HTMLElement>("p.rail-live")!;
    expect(live.getAttribute("aria-live")).toBe("polite");
    expect(live.textContent).toBe("implementer started");
  });

  it("select on click", () => {
    const { rail, h } = mount();
    rail.set(flow, "AwaitingCI", now);
    nodeOf(rail, "r-s").querySelector("button")!.click();
    expect(h.select).toHaveBeenCalledWith("r-s");
  });

  it("mark and chosen", () => {
    const { rail } = mount();
    rail.set(flow, "AwaitingCI", now);
    rail.mark("r-i");
    rail.chosen("r-s");
    expect(nodeOf(rail, "r-i").querySelector("button")!.getAttribute("aria-current")).toBe("true");
    expect(nodeOf(rail, "r-s").querySelector("button")!.getAttribute("aria-current")).toBeNull();
    expect(nodeOf(rail, "r-s").querySelector("button")!.getAttribute("aria-pressed")).toBe("true");
    expect(nodeOf(rail, "r-i").querySelector("button")!.getAttribute("aria-pressed")).toBe("false");
    rail.set(flow, "AwaitingCI", now);
    expect(nodeOf(rail, "r-i").querySelector("button")!.getAttribute("aria-current")).toBe("true");
    expect(nodeOf(rail, "r-s").querySelector("button")!.getAttribute("aria-pressed")).toBe("true");
  });

  it("no style attribute", () => {
    const { rail } = mount();
    rail.set(flow, "AwaitingCI", now);
    nodeOf(rail, "r-i").querySelector("button")!.focus();
    expect(rail.el.querySelectorAll("[style]")).toHaveLength(0);
  });

  it("a started-by mark", () => {
    const { rail } = mount();
    rail.set({ nodes: [node({ startedBy: "github:ana" })] }, "Implementing", now);
    rail.el.querySelector<HTMLButtonElement>("li.rail-node button")!.focus();
    expect(card(rail).textContent).toContain("started by ana");
  });

  it("the card", () => {
    const { rail } = mount();
    const n = node({ endedAt: "2026-11-03T10:15:00Z", outcome: "failed", reason: "tests red", handoff: "fixed the parser\nand more", tools: { ok: 5, failed: 2 } });
    rail.set({ nodes: [n] }, "Implementing", now);
    const c = card(rail);
    expect(c.getAttribute("role")).toBe("tooltip");
    expect(c.hidden).toBe(true);
    const b = rail.el.querySelector<HTMLButtonElement>("li.rail-node button")!;
    b.dispatchEvent(new MouseEvent("mouseenter"));
    expect(c.hidden).toBe(false);
    const hovered = c.textContent;
    b.dispatchEvent(new MouseEvent("mouseleave"));
    expect(c.hidden).toBe(true);
    b.focus();
    expect(c.hidden).toBe(false);
    expect(c.textContent).toBe(hovered);
    expect(b.getAttribute("aria-describedby")).toBe(c.id);
    const text = c.textContent!;
    expect(text).toContain("2026-11-03");
    expect(text).toContain("5 ok · 2 failed");
    expect(text).toContain("tests red");
    expect(text).toContain("fixed the parser");
    expect(text).not.toContain("and more");
    expect(c.querySelector(".rail-claim .hint")!.textContent).toBe("the agent's claim");
    b.blur();
    expect(c.hidden).toBe(true);
  });

  it("folds an old round and opens it on click", () => {
    const { rail } = mount();
    const ended = { endedAt: "2026-11-03T10:05:00Z", toSeq: 9 };
    const nodes: FlowNode[] = [];
    for (let r = 1; r <= 4; r++) {
      nodes.push(node({ run: `i${r}`, round: r, ...ended }), node({ run: `v${r}`, persona: "reviewer", role: "reviewer", round: r, verdict: "changes", ...ended }));
    }
    nodes.push(node({ run: "i5", round: 5 }));
    rail.set({ nodes }, "Implementing", now);
    const round = rail.el.querySelector<HTMLElement>("li.rail-round")!;
    expect(round.querySelector("button")!.textContent).toBe("round 1 · implementer → reviewer ✗");
    expect(rail.el.querySelectorAll("li.rail-node")).toHaveLength(7);
    round.querySelector("button")!.click();
    expect(rail.el.querySelector('li.rail-node[data-run="i1"]')).not.toBeNull();
  });

  it("a set keeps the focused node focused", () => {
    const { rail } = mount();
    rail.set(flow, "AwaitingCI", now);
    nodeOf(rail, "r-i").querySelector("button")!.focus();
    rail.set(flow, "AwaitingCI", now);
    expect(document.activeElement).toBe(nodeOf(rail, "r-i").querySelector("button"));
    expect(card(rail).hidden).toBe(false);
  });

  it("absent flow hides the rail", () => {
    const { rail } = mount();
    rail.set(flow, "AwaitingCI", now);
    expect(rail.el.hidden).toBe(false);
    rail.set(undefined, "Closed", now);
    expect(rail.el.hidden).toBe(true);
  });
});
