import { afterEach, describe, expect, it, vi } from "vitest";
import type { RoomEvent } from "../src/conn";
import { mountViews } from "../src/roomview";
import { maxRunEvents, mountRunView, mountScrollIndex, nodeAt } from "../src/runview";
import type { FlowNode } from "../src/summary";

const node = (run: string, fromSeq: number, toSeq: number): FlowNode => ({ run, persona: "implementer", role: "implementer",
  round: 1, fromSeq, toSeq, tools: { ok: 0, failed: 0 } });

const ev = (seq: number, runId = "r-1"): RoomEvent => ({ v: 1, id: String(seq), seq, roomId: "3kq7x2ma", runId,
  actor: { kind: "agent", id: "agent:x" }, type: "message", origin: "bridge", ts: "2026-10-06T10:00:00Z", redactions: [],
  payload: { text: `m${seq}` } });

// A log of seqs 1..last, every event run r-1's unless the caller says otherwise.
const log = (last: number) => vi.fn(async (after: number, limit: number) => {
  const out: RoomEvent[] = [];
  for (let s = after + 1; s <= Math.min(last, after + limit); s++) out.push(ev(s));
  return out;
});

function setup(load = log(200)) {
  const main = document.createElement("main");
  mountViews(main);
  const chosen = vi.fn();
  const rv = mountRunView(main, { load, views: (el) => mountViews(el), chosen });
  const seqs = () => [...main.querySelectorAll<HTMLElement>(".run-view .view-chat [data-seq]")].map((r) => Number(r.dataset.seq));
  return { main, rv, load, chosen, seqs };
}

afterEach(() => history.replaceState(null, "", "/"));

describe("nodeAt", () => {
  const nodes = [node("a", 5, 10), node("b", 11, 0)];
  it.each([
    ["a seq inside the first node", 7, "a"],
    ["a seq of the live node", 40, "b"],
    ["a seq before any node", 3, undefined],
  ])("%s", (_, seq, want) => expect(nodeAt(nodes, seq)).toBe(want));

  it("a seq between two ranges is the node that started before it", () => {
    expect(nodeAt([node("a", 5, 10), node("b", 20, 30)], 15)).toBe("a");
  });

  it("a node with no range holds no seq", () => {
    expect(nodeAt([node("f", 0, 0)], 7)).toBeUndefined();
  });
});

describe("the run view", () => {
  it("select loads the run in a view of its own", async () => {
    const { main, rv, load, chosen, seqs } = setup();
    rv.set([node("r-1", 100, 130)]);
    await rv.select("r-1");
    expect(load).toHaveBeenCalledWith(99, 500);
    expect(seqs()).toEqual(Array.from({ length: 31 }, (_, i) => 100 + i));
    expect(main.querySelector("div.run-view")).not.toBeNull();
    for (const c of main.children) if (!c.classList.contains("run-view")) expect((c as HTMLElement).hidden).toBe(true);
    expect(location.hash).toBe("#run=r-1");
    expect(chosen).toHaveBeenLastCalledWith("r-1");
  });

  it("All runs returns", async () => {
    const { main, rv, chosen } = setup();
    rv.set([node("r-1", 100, 130)]);
    await rv.select("r-1");
    main.querySelector<HTMLElement>(".run-view-back")!.click();
    expect(main.querySelector("div.run-view")).toBeNull();
    expect(main.querySelector<HTMLElement>(".view-chat")!.hidden).toBe(false);
    expect(main.querySelector<HTMLElement>("details.raw-events")!.hidden).toBe(false);
    expect(location.hash).toBe("");
    expect(chosen).toHaveBeenLastCalledWith(undefined);
  });

  it("stops at 2000", async () => {
    const { main, rv, load, seqs } = setup(log(2600));
    rv.set([node("r-1", 1, 2600)]);
    await rv.select("r-1");
    expect(load).toHaveBeenCalledTimes(4);
    expect(seqs()).toHaveLength(maxRunEvents);
    expect(main.querySelector(".run-view-note")!.textContent).toBe("Showing the first 2,000 events of this run");
  });

  it("live events in the chosen run's range, whoever wrote them", async () => {
    const { rv, seqs } = setup(log(150));
    rv.set([node("r-1", 100, 0)]);
    await rv.select("r-1");
    const human: RoomEvent = { ...ev(152), runId: undefined, actor: { kind: "human", id: "human:a" } };
    rv.apply(ev(151));
    rv.apply(human);
    expect(seqs().slice(-2)).toEqual([151, 152]);
  });

  it("no live events past a closed run's toSeq", async () => {
    const { rv, seqs } = setup(log(150));
    rv.set([node("r-1", 100, 0)]);
    await rv.select("r-1");
    rv.set([node("r-1", 100, 151)]);
    rv.apply(ev(151));
    rv.apply(ev(152));
    expect(seqs().slice(-1)).toEqual([151]);
  });

  it("a run that ends during its load keeps the events it held", async () => {
    let release: (v: RoomEvent[]) => void = () => {};
    const load = vi.fn(() => new Promise<RoomEvent[]>((r) => { release = r; }));
    const { rv, seqs } = setup(load);
    rv.set([node("r-1", 100, 0)]);
    const done = rv.select("r-1");
    rv.apply(ev(151));
    rv.set([node("r-1", 100, 150)]);
    release(Array.from({ length: 51 }, (_, i) => ev(100 + i)));
    await done;
    expect(seqs().slice(-1)).toEqual([151]);
  });

  it("a run with no range in the log says so and loads nothing", async () => {
    const { main, rv, load } = setup();
    rv.set([node("r-f", 0, 0)]);
    await rv.select("r-f");
    expect(load).not.toHaveBeenCalled();
    expect(main.querySelector(".run-view-note")!.textContent).toBe("This run's events are not in this room's log");
  });

  it("a refetch drops the chosen run", async () => {
    const { main, rv, chosen } = setup();
    rv.set([node("r-1", 100, 130)]);
    await rv.select("r-1");
    rv.set([node("r-2", 140, 0)]);
    await vi.waitFor(() => expect(main.querySelector("div.run-view")).toBeNull());
    expect(chosen).toHaveBeenLastCalledWith(undefined);
  });

  it("the hash restores", async () => {
    history.replaceState(null, "", "/#run=r-1");
    const { rv, chosen, seqs } = setup();
    rv.set([node("r-1", 100, 130)]);
    await vi.waitFor(() => expect(seqs()).toHaveLength(31));
    expect(chosen).toHaveBeenLastCalledWith("r-1");
  });
});

describe("the scroll index", () => {
  it("the scroll index marks the topmost visible row's node", () => {
    const chat = document.createElement("div");
    for (const s of [100, 120, 140]) {
      const row = document.createElement("div");
      row.dataset.seq = String(s);
      chat.append(row);
    }
    document.body.append(chat);
    let cb: IntersectionObserverCallback = () => {};
    const observed: Element[] = [];
    const mark = vi.fn();
    mountScrollIndex(chat, {
      nodes: () => [node("a", 90, 110), node("b", 111, 0)],
      mark,
      observe: (c) => { cb = c; return { observe: (el) => observed.push(el), disconnect: () => {} }; },
    });
    expect(observed).toHaveLength(3);
    const row = chat.querySelector('[data-seq="120"]')!;
    cb([{ target: row, isIntersecting: true } as unknown as IntersectionObserverEntry], {} as IntersectionObserver);
    expect(mark).toHaveBeenLastCalledWith("b");
    chat.remove();
  });

  it("disconnect stops the observer", () => {
    const disconnect = vi.fn();
    const index = mountScrollIndex(document.createElement("div"), {
      nodes: () => [], mark: () => {}, observe: () => ({ observe: () => {}, disconnect }),
    });
    index.disconnect();
    expect(disconnect).toHaveBeenCalledOnce();
  });
});
