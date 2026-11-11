import { readFileSync } from "node:fs";
import { join } from "node:path";
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

  it("a run with an outcome and no end time is over, not running", () => {
    const { rail } = mount();
    const over = node({ run: "r-o", outcome: "ended", tools: { ok: 1, failed: 0 } });
    rail.set({ nodes: [over] }, "Implementing", now);
    const li = nodeOf(rail, "r-o");
    expect(li.dataset.state).toBe("done");
    expect(li.querySelector(".rail-chip")!.textContent).toBe("ended");
    expect(li.querySelector(".rail-detail")!.textContent).toBe("");
    rail.tick(now + 60_000);
    expect(li.querySelector(".rail-detail")!.textContent).toBe("");
    li.querySelector("button")!.focus();
    expect(card(rail).textContent).not.toContain("still running");
    rail.live(ev({ runId: "r-o", payload: { status: "ok" } }));
    expect(card(rail).textContent).toContain("1 ok · 0 failed");
    rail.set({ nodes: [over, node({ run: "r-n" })] }, "Implementing", now);
    expect(rail.el.querySelector(".rail-live")!.textContent).toBe("implementer started");
  });

  it("live counts a tool result", () => {
    const { rail } = mount();
    rail.set(flow, "AwaitingCI", now);
    const button = nodeOf(rail, "r-i").querySelector("button")!;
    button.focus();
    expect(card(rail).textContent).toContain("3 ok · 1 failed");
    rail.live(ev({ seq: 1, runId: "r-i", payload: { status: "error" } }));
    expect(card(rail).textContent).toContain("3 ok · 2 failed");
    rail.live(ev({ seq: 2, runId: "r-s", payload: { status: "error" } }));
    rail.live(ev({ seq: 3, runId: "r-i", type: "message", payload: { status: "error" } }));
    expect(card(rail).textContent).toContain("3 ok · 2 failed");
    rail.live(ev({ seq: 4, runId: "r-i", payload: { status: "ok" } }));
    expect(card(rail).textContent).toContain("4 ok · 2 failed");
  });

  it("live counts only what the summary has not, in either order", () => {
    const { rail } = mount();
    const withTools = (failed: number) => ({ ...flow, nodes: [scoper, { ...implementer, tools: { ok: 3, failed } }] });
    const shown = () => {
      nodeOf(rail, "r-i").querySelector("button")!.focus();
      return card(rail).textContent;
    };
    rail.set(withTools(1), "AwaitingCI", now, 100);
    // The summary came first: the socket's tail replays what it already counted.
    rail.live(ev({ seq: 90, runId: "r-i", payload: { status: "error" } }));
    rail.live(ev({ seq: 100, runId: "r-i", payload: { status: "error" } }));
    expect(shown()).toContain("3 ok · 1 failed");
    rail.live(ev({ seq: 101, runId: "r-i", payload: { status: "error" } }));
    rail.live(ev({ seq: 101, runId: "r-i", payload: { status: "error" } })); // a reconnect's replay
    expect(shown()).toContain("3 ok · 2 failed");
    // A summary older than the stream keeps what it has not folded.
    rail.set(withTools(1), "AwaitingCI", now, 100);
    expect(shown()).toContain("3 ok · 2 failed");
    // A summary that folded it does not count it twice.
    rail.set(withTools(2), "AwaitingCI", now, 101);
    expect(shown()).toContain("3 ok · 2 failed");
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

  // The CSP refuses a style attribute in markup; the card's place is set through the CSSOM.
  it("no style attribute but the card's CSSOM place", () => {
    const { rail } = mount();
    rail.set(flow, "AwaitingCI", now);
    nodeOf(rail, "r-i").querySelector("button")!.focus();
    expect(rail.el.querySelectorAll("[style]:not(.rail-card)")).toHaveLength(0);
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
    // The pointer may cross into the card (to scroll it); leaving the card hides it.
    b.dispatchEvent(new MouseEvent("mouseleave", { relatedTarget: c }));
    expect(c.hidden).toBe(false);
    c.dispatchEvent(new MouseEvent("mouseleave"));
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

  it("a node names its served models, the alias asked for second, and never truncates to +N", () => {
    const { rail } = mount();
    const done = { endedAt: "2026-11-03T10:06:00Z", outcome: "succeeded", microUSD: 230_000 };
    rail.set({ nodes: [
      node({ run: "a", ...done, model: "agent-default", models: ["glm-5.3"] }),
      node({ run: "b", ...done, model: "glm-5.3", models: ["glm-5.3"] }),
      node({ run: "c", ...done, model: "agent-default", models: ["glm-5.3", "claude-haiku-4.5"] }),
    ] }, "Implementing", now);
    const models = (run: string) => {
      const m = nodeOf(rail, run).querySelector<HTMLElement>(".rail-models")!;
      return { served: [...m.querySelectorAll(".rail-model")].map((x) => x.textContent), asked: m.querySelector(".rail-asked")?.textContent };
    };
    expect(nodeOf(rail, "a").querySelector(".rail-detail")!.textContent).toBe("6 min · $0.23");
    const t = nodeOf(rail, "a").querySelector<HTMLTimeElement>(".rail-detail time")!;
    expect(t.dateTime).toBe("2026-11-03T10:00:00.000Z");
    expect(t.title).toBe(new Date("2026-11-03T10:00:00Z").toLocaleString());
    expect(models("a")).toEqual({ served: ["glm-5.3"], asked: "agent-default" });
    expect(models("b")).toEqual({ served: ["glm-5.3"], asked: undefined });
    expect(models("c")).toEqual({ served: ["glm-5.3", "claude-haiku-4.5"], asked: "agent-default" });
    expect(rail.el.querySelector(".rail-path")!.textContent).not.toMatch(/\+\d/);
  });

  it("a node with no model draws no model line", () => {
    const { rail } = mount();
    rail.set({ nodes: [node()] }, "Implementing", now);
    expect(rail.el.querySelector(".rail-models")).toBeNull();
  });

  // The card sits outside every node and the rail's scroll box, placed in the viewport from the
  // button's box: a rail that clips its overflow cannot cut it (v0.40.0).
  it("the card escapes the rail's clipping and fits the viewport", () => {
    const style = document.createElement("style");
    style.textContent = readFileSync(join(__dirname, "../src/app.css"), "utf8");
    document.head.append(style);
    try {
      const { rail } = mount();
      rail.set({ nodes: [node({ handoff: `Fixed all 12 runbook_url links in ${"deploy/base/metrics-stack/alerting/".repeat(4)}rules.yaml` })] }, "Implementing", now);
      const b = rail.el.querySelector<HTMLButtonElement>("li.rail-node button")!;
      b.getBoundingClientRect = () => ({ top: 300, bottom: 350, left: 1050, right: 1350, width: 300, height: 50, x: 1050, y: 300, toJSON() {} });
      b.dispatchEvent(new MouseEvent("mouseenter"));
      const c = card(rail);
      expect(c.hidden).toBe(false);
      expect(c.closest(".rail-node")).toBeNull();
      expect(c.parentElement).toBe(rail.el);
      const cs = getComputedStyle(c);
      expect(cs.position).toBe("fixed");
      expect(cs.overflowY).toBe("auto");
      expect(cs.overflowWrap).toBe("anywhere");
      expect(c.style.top).toBe("354px");
      expect(parseFloat(c.style.left) + parseFloat(c.style.width)).toBeLessThanOrEqual(window.innerWidth - 8);
      expect(parseFloat(c.style.maxHeight)).toBeLessThanOrEqual(window.innerHeight - 354 - 8);
    } finally {
      style.remove();
    }
  });

  it("the card stops at the pinned dock, and flips above when the room is there", () => {
    const { rail } = mount();
    document.body.style.setProperty("--dock-h", "200px");
    try {
      rail.set({ nodes: [node()] }, "Implementing", now);
      const b = rail.el.querySelector<HTMLButtonElement>("li.rail-node button")!;
      const h = window.innerHeight;
      b.getBoundingClientRect = () => ({ top: h - 300, bottom: h - 250, left: 0, right: 300, width: 300, height: 50, x: 0, y: h - 300, toJSON() {} });
      Object.defineProperty(card(rail), "scrollHeight", { value: 120 });
      b.focus();
      // 250 px under the node, 200 of them the dock's: 38 left below, more above.
      expect(card(rail).dataset.side).toBe("above");
      rail.el.classList.add("open"); // the drawer covers the dock
      b.blur(); b.focus();
      expect(card(rail).dataset.side).toBe("below");
      expect(card(rail).style.maxHeight).toBe(`${250 - 4 - 8}px`);
    } finally {
      document.body.style.removeProperty("--dock-h");
    }
  });

  it("the card flips above a node near the viewport's bottom", () => {
    const { rail } = mount();
    rail.set({ nodes: [node()] }, "Implementing", now);
    const b = rail.el.querySelector<HTMLButtonElement>("li.rail-node button")!;
    const h = window.innerHeight;
    b.getBoundingClientRect = () => ({ top: h - 60, bottom: h - 10, left: 0, right: 300, width: 300, height: 50, x: 0, y: h - 60, toJSON() {} });
    b.focus();
    const c = card(rail);
    expect(c.dataset.side).toBe("above");
    expect(c.style.bottom).toBe(`${h - (h - 60) + 4}px`);
    b.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(c.hidden).toBe(true);
  });

  it("on touch, a tap opens the card; a second tap selects the run", () => {
    const { rail, h } = mount();
    rail.set({ nodes: [node()] }, "Implementing", now);
    const b = rail.el.querySelector<HTMLButtonElement>("li.rail-node button")!;
    const tap = () => {
      const down = new Event("pointerdown");
      Object.defineProperty(down, "pointerType", { value: "touch" });
      b.dispatchEvent(down);
      b.focus(); // a browser focuses the button between pointerdown and click
      b.click();
    };
    tap();
    expect(card(rail).hidden).toBe(false);
    expect(h.select).not.toHaveBeenCalled();
    tap();
    expect(h.select).toHaveBeenCalledWith("r-1");
  });

  it("a mouse click selects at once", () => {
    const { rail, h } = mount();
    rail.set({ nodes: [node()] }, "Implementing", now);
    rail.el.querySelector<HTMLButtonElement>("li.rail-node button")!.click();
    expect(h.select).toHaveBeenCalledWith("r-1");
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

describe("a finished task", () => {
  it("has no replay controls, and a refetch applies at once", () => {
    const { rail } = mount();
    rail.set(flow, "Merged", now);
    expect(rail.el.querySelector(".rail-replays, .rail-replay")).toBeNull();
    const names = [...rail.el.querySelectorAll("button")].map((b) => b.textContent);
    expect(names).not.toContain("Replay");
    expect(names).not.toContain("Next");
    expect(names).not.toContain("Skip");
    const later: Flow = { ...flow, nodes: [...flow.nodes, node({ run: "r-m", persona: "merger", fromSeq: 91 })] };
    rail.set(later, "Merged", now);
    expect(nodeOf(rail, "r-m")).not.toBeNull();
  });
});

// The room's activity on a node (summary/v1 steered and chat, the summary's queued notes).
describe("activity", () => {
  const msg = (seq: number, delivery: string, o: Partial<RoomEvent> = {}) =>
    ev({ seq, type: "message", actor: { kind: "human", id: "human:ana" }, ts: "2026-11-03T10:18:00Z", payload: { kind: "chat", text: "x", delivery }, ...o });

  it("a steered node shows its count, and its card lists who and when from the events the page holds", () => {
    const { rail } = mount();
    rail.live(msg(5, "steering")); // before the node's window
    rail.live(msg(12, "steering"));
    rail.live(msg(12, "steering")); // a reconnect's replay
    rail.live(msg(13, "none")); // chat, not a steer
    rail.live(msg(14, "steering", { actor: { kind: "system", id: "system:factory" } })); // not a human
    rail.live(msg(15, "steering", { runId: "r-1" })); // a run's own message
    rail.set({ nodes: [node({ fromSeq: 10, steered: 2 })] }, "Implementing", now);
    const li = rail.el.querySelector<HTMLElement>("li.rail-node")!;
    expect(li.querySelector(".rail-steered")!.textContent).toBe("↪ steered ×2");
    li.querySelector("button")!.focus();
    const lines = [...card(rail).querySelectorAll(".rail-steers > li")].map((x) => x.textContent);
    expect(lines).toEqual(["ana, 2 min ago", "1 earlier"]);
  });

  it("names the viewer's steers you, and another human's as the conversation does", () => {
    const rail = mountRail({ select: () => {}, you: () => "human:ana" });
    document.body.replaceChildren(rail.el);
    rail.live(msg(12, "steering"));
    rail.live(msg(13, "steering", { actor: { kind: "human", id: "human:bo", role: "owner" } }));
    rail.set({ nodes: [node({ fromSeq: 10, steered: 2 })] }, "Implementing", now);
    rail.el.querySelector<HTMLButtonElement>("li.rail-node button")!.focus();
    expect([...card(rail).querySelectorAll(".rail-steers > li")].map((x) => x.textContent)).toEqual(["owner · bo, 2 min ago", "you, 2 min ago"]);
  });

  it("a steer the stream delivers after the summary joins the open card", () => {
    const { rail } = mount();
    rail.set({ nodes: [node({ fromSeq: 10, steered: 1 })] }, "Implementing", now);
    rail.el.querySelector<HTMLButtonElement>("li.rail-node button")!.focus();
    expect([...card(rail).querySelectorAll(".rail-steers > li")].map((x) => x.textContent)).toEqual(["1 earlier"]);
    rail.live(msg(20, "steering"));
    expect([...card(rail).querySelectorAll(".rail-steers > li")].map((x) => x.textContent)).toEqual(["ana, 2 min ago"]);
  });

  it.each([[0], [undefined]])("steered %s draws no mark and lists nothing", (steered) => {
    const { rail } = mount();
    rail.live(msg(12, "steering"));
    rail.set({ nodes: [node({ fromSeq: 10, steered })] }, "Implementing", now);
    expect(rail.el.querySelector(".rail-steered")).toBeNull();
    rail.el.querySelector<HTMLButtonElement>("li.rail-node button")!.focus();
    expect(card(rail).querySelector(".rail-steers")).toBeNull();
  });

  // One fixture for Go and TS: the broker's golden summary carries a steer and a chat on its node.
  it("draws the marks of the broker's golden flow", () => {
    const golden = JSON.parse(readFileSync(join(__dirname, "../../internal/summary/testdata/normal.json"), "utf8")) as { want: { flow: Flow } };
    const { rail } = mount();
    rail.set(golden.want.flow, "Implementing", now);
    expect(rail.el.querySelector(".rail-steered")!.textContent).toBe("↪ steered ×1");
    expect(rail.el.querySelector(".rail-chat")!.textContent).toBe("1");
  });

  it("a node with chat shows the chat icon and its count, named for assistive tech", () => {
    const { rail } = mount();
    rail.set({ nodes: [node({ chat: 3 })] }, "Implementing", now);
    const c = rail.el.querySelector<HTMLElement>("li.rail-node .rail-chat")!;
    expect(c.querySelector("svg")).not.toBeNull();
    expect(c.textContent).toBe("3");
    expect(c.getAttribute("aria-label")).toBe("3 chat messages during this run");
    rail.set({ nodes: [node({ chat: 0 })] }, "Implementing", now);
    expect(rail.el.querySelector(".rail-chat")).toBeNull();
  });

  it("queued notes sit on the next ghost node", () => {
    const { rail } = mount();
    rail.set(flow, "Implementing", now, undefined, 2);
    const chips = rail.el.querySelectorAll(".rail-queued");
    expect(chips).toHaveLength(1);
    expect(chips[0].closest('li.rail-node[data-state="ghost"]')).not.toBeNull();
    expect(chips[0].textContent).toBe("2 queued → next run");
  });

  it("with no ghost, the queued chip sits in the gap after the live node", () => {
    const { rail } = mount();
    rail.set({ nodes: [scoper, implementer] }, "Implementing", now, undefined, 1);
    const next = nodeOf(rail, "r-i").nextElementSibling!;
    expect(next.classList.contains("rail-queued")).toBe(true);
    expect(next.textContent).toBe("1 queued → next run");
  });

  it("an empty queue draws no chip", () => {
    const { rail } = mount();
    rail.set(flow, "Implementing", now, undefined, 0);
    expect(rail.el.querySelector(".rail-queued")).toBeNull();
    rail.set({ nodes: [implementer] }, "Implementing", now);
    expect(rail.el.querySelector(".rail-queued")).toBeNull();
  });

  it("a new mark or chip arrives; the first paint and an unchanged one do not", () => {
    const { rail } = mount();
    const arrived = () => [...rail.el.querySelectorAll(".arrive")].map((x) => x.className);
    rail.set({ nodes: [node({ steered: 1, chat: 1 })], ghosts: ["reviewer"] }, "Implementing", now, undefined, 1);
    expect(arrived()).toEqual([]);
    rail.set({ nodes: [node({ steered: 1, chat: 1 })], ghosts: ["reviewer"] }, "Implementing", now, undefined, 1);
    expect(arrived()).toEqual([]);
    rail.set({ nodes: [node({ steered: 2, chat: 1 })], ghosts: ["reviewer"] }, "Implementing", now, undefined, 2);
    expect(arrived()).toEqual(["rail-steered arrive", "rail-queued arrive"]);
    rail.el.querySelector(".rail-steered")!.dispatchEvent(new Event("animationend"));
    expect(arrived()).toEqual(["rail-queued arrive"]);
  });
});

