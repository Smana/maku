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

  describe("the models line", () => {
    const line = (o: Partial<FlowNode>) => {
      const { rail } = mount();
      rail.set({ nodes: [node(o)] }, "Implementing", now);
      rail.el.querySelector<HTMLButtonElement>("li.rail-node button")!.dispatchEvent(new MouseEvent("mouseenter"));
      return card(rail).textContent!;
    };
    it("marks the asked model among the served ones, then the effort", () =>
      expect(line({ model: "glm-5.3", models: ["claude-haiku-4.5", "glm-5.3"], effort: "high" })).toContain("Models: glm-5.3 (asked) · claude-haiku-4.5 · effort high"));
    it("lists an asked model that did not serve first", () =>
      expect(line({ model: "glm-5.3", models: ["claude-haiku-4.5"] })).toContain("Models: glm-5.3 (asked) · claude-haiku-4.5"));
    it("lists served models alone, without an asked mark", () =>
      expect(line({ models: ["a", "b"] })).toContain("Models: a · b"));
    it("shows effort alone", () => expect(line({ effort: "low" })).toContain("Models: effort low"));
    it("is absent without models or effort", () => expect(line({})).not.toContain("Models"));
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

  it("a flow with no plan draws no ghost merge", () => {
    const { plan: _plan, ...noPlan } = flow;
    const rail = mountRail({ select: () => {} });
    rail.set(noPlan, "Implementing", now);
    expect(rail.el.querySelector('.rail-milestone[data-state="ghost"]')).toBeNull();
  });

  it("absent flow hides the rail", () => {
    const { rail } = mount();
    rail.set(flow, "AwaitingCI", now);
    expect(rail.el.hidden).toBe(false);
    rail.set(undefined, "Closed", now);
    expect(rail.el.hidden).toBe(true);
  });

  describe("motion", () => {
    const judge = node({ run: "r-j", persona: "reviewer", role: "reviewer", fromSeq: 60, edge: "review", judge: { count: 1, last: "approve" } });
    const a: Flow = { nodes: [scoper, implementer, judge], ghosts: [], milestones: [{ phase: "AwaitingCI", at: "2026-11-03T10:10:00Z", seq: 90 }] };
    const extra = node({ run: "r-x", persona: "fixer", role: "implementer", fromSeq: 90, edge: "fix" });
    const b: Flow = {
      nodes: [scoper, implementer, { ...judge, judge: { count: 2, last: "approve" } }, extra], ghosts: [],
      milestones: [...a.milestones!, { phase: "Merged", at: "2026-11-03T10:15:00Z", seq: 99 }],
    };
    const count = (rail: { el: HTMLElement }, c: string) => rail.el.querySelectorAll(`.${c}`).length;

    it("marks what changed", () => {
      const { rail } = mount();
      rail.set(a, "AwaitingCI", now);
      rail.set(b, "Merged", now);
      expect(["enter", "draw", "tick", "fill"].map((c) => count(rail, c))).toEqual([1, 1, 1, 1]);
      expect(nodeOf(rail, "r-x").classList.contains("enter")).toBe(true);
      expect((rail.el.querySelector("path.draw") as SVGElement).style.getPropertyValue("--len")).not.toBe("");
    });

    it("first paint animates nothing", () => {
      const { rail } = mount();
      rail.set(b, "Merged", now);
      for (const c of ["enter", "draw", "tick", "fill"]) expect(count(rail, c)).toBe(0);
    });

    it("a ghost that turned live enters", () => {
      const { rail } = mount();
      rail.set({ nodes: [scoper], ghosts: ["implementer"] }, "Implementing", now);
      rail.set({ nodes: [scoper, implementer], ghosts: [] }, "Implementing", now);
      expect(count(rail, "enter")).toBe(1);
    });

    it("classes leave on animationend", () => {
      const { rail } = mount();
      rail.set(a, "AwaitingCI", now);
      rail.set(b, "Merged", now);
      for (const c of ["enter", "draw", "tick", "fill"]) {
        rail.el.querySelector(`.${c}`)!.dispatchEvent(new Event("animationend"));
        expect(count(rail, c)).toBe(0);
      }
    });
  });
});

describe("split", () => {
  const split: Flow = {
    ...flow, nodes: [scoper], ghosts: [],
    split: { of: 3, parts: [{ index: 1, issue: 11, room: "aaaaaaaa", outcome: "landed" }, { index: 2, issue: 12, room: "bbbbbbbb" }] },
  };

  it("split chips link each part", () => {
    const { rail } = mount();
    rail.set(split, "Implementing", now);
    const lis = [...rail.el.querySelectorAll<HTMLElement>("ol.rail-parts > li.rail-part")];
    expect(lis.map((l) => l.dataset.state)).toEqual(["done", "running", "queued"]);
    expect(lis.map((l) => l.querySelector("a")?.getAttribute("href") ?? null)).toEqual(["/r/aaaaaaaa", "/r/bbbbbbbb", null]);
    expect(lis[2].textContent).toContain("#?");
    expect(rail.el.querySelector('li.rail-node[data-run="r-s"] ol.rail-parts')).not.toBeNull();
  });

  it("a room id that is not a C2 id gets no link", () => {
    const { rail } = mount();
    rail.set({ ...split, split: { of: 1, parts: [{ index: 1, issue: 11, room: "javascript:x" }] } }, "Implementing", now);
    expect(rail.el.querySelector("li.rail-part a")).toBeNull();
  });

  it("the parent node", () => {
    const { rail } = mount();
    rail.set({ ...flow, parent: { room: "cccccccc", issue: 50, index: 2, of: 3 } }, "Implementing", now);
    const first = rail.el.querySelector(".rail-path")!.firstElementChild as HTMLElement;
    expect(first.className).toContain("rail-parent");
    expect(first.textContent).toBe("part 2 of 3 of #50");
    expect(first.querySelector("a")!.getAttribute("href")).toBe("/r/cccccccc");
  });
});

