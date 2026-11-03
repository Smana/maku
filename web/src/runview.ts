// SPDX-License-Identifier: Apache-2.0

// One run's events in a view of their own, and the scroll index that marks the rail for the rows
// on screen. A separate mountViews leaves the room's conversation and its trim untouched.

import type { RoomEvent } from "./conn";
import type { Views } from "./roomview";
import type { FlowNode } from "./summary";

export const maxRunEvents = 2000;
const pageSize = 500;

// nodeAt is the run whose range holds seq: the last node started at or before it. A node
// with fromSeq 0 has no range in this log and never holds a seq.
export function nodeAt(nodes: FlowNode[], seq: number): string | undefined {
  let best: FlowNode | undefined;
  for (const n of nodes) {
    if (n.fromSeq > 0 && n.fromSeq <= seq && (!best || n.fromSeq >= best.fromSeq)) best = n;
  }
  return best?.run;
}

export interface RunView { set(nodes: FlowNode[]): void; select(run: string | undefined): Promise<void>; apply(e: RoomEvent): void }

function hashRun(): string | undefined {
  const m = /^#run=(.+)$/.exec(location.hash);
  if (!m) return undefined;
  try { return decodeURIComponent(m[1]); } catch { return undefined; }
}

// replaceState, not location.hash: a hash assignment fires hashchange, which the room page
// reads as an approval to land on.
function setHash(run: string | undefined) {
  const base = location.pathname + location.search;
  history.replaceState(history.state, "", run ? `${base}#run=${encodeURIComponent(run)}` : base);
}

// mountRunView shows a chosen run's events in place of roomMain's children. load is one page
// of the room's log after a seq; views mounts the chat and raw views into the run's element.
export function mountRunView(roomMain: HTMLElement, o: {
  load: (after: number, limit: number) => Promise<RoomEvent[]>;
  views: (el: HTMLElement) => Views;
  chosen: (run: string | undefined) => void;
}): RunView {
  let nodes: FlowNode[] = [];
  let restore = hashRun();
  let first = true;
  let chosen: string | undefined;
  let view: { el: HTMLElement; views: Views; note: HTMLElement } | undefined;
  let gen = 0;
  // While a load runs, live events wait; after it, only seqs past the last applied count.
  let loading = false;
  let through = 0;
  let count = 0;
  let waiting: RoomEvent[] = [];

  const capped = () => count >= maxRunEvents;
  const say = (text: string) => { if (view) { view.note.textContent = text; view.note.hidden = !text; } };
  const put = (e: RoomEvent) => {
    view?.views.apply(e);
    count++;
    through = Math.max(through, e.seq);
    if (capped()) say(`Showing the first ${maxRunEvents.toLocaleString("en-US")} events of this run`);
  };
  const close = () => {
    view?.el.remove();
    view = undefined;
    for (const c of roomMain.children) (c as HTMLElement).hidden = false;
  };
  const open = (n: FlowNode) => {
    close();
    for (const c of roomMain.children) (c as HTMLElement).hidden = true;
    const el = document.createElement("div");
    el.className = "run-view";
    const bar = document.createElement("div");
    bar.className = "run-view-bar";
    const back = document.createElement("button");
    back.type = "button";
    back.className = "run-view-back";
    back.textContent = "All runs";
    back.onclick = () => void api.select(undefined);
    const title = document.createElement("span");
    title.className = "run-view-title";
    title.textContent = `${n.persona} · ${n.run}`;
    bar.append(back, title);
    const note = document.createElement("p");
    note.className = "run-view-note";
    note.setAttribute("role", "status");
    note.hidden = true;
    const body = document.createElement("div");
    el.append(bar, note, body);
    roomMain.append(el);
    view = { el, views: o.views(body), note };
  };

  const api: RunView = {
    set(next) {
      nodes = next;
      if (first) {
        first = false;
        const r = restore;
        restore = undefined;
        if (r && nodes.some((n) => n.run === r)) { void api.select(r); return; }
      }
      if (chosen && !nodes.some((n) => n.run === chosen)) void api.select(undefined);
    },
    async select(run) {
      const g = ++gen;
      const n = run === undefined ? undefined : nodes.find((x) => x.run === run);
      chosen = n?.run;
      setHash(chosen);
      o.chosen(chosen);
      loading = false;
      waiting = [];
      through = 0;
      count = 0;
      if (!n) { close(); return; }
      open(n);
      if (n.fromSeq <= 0) { say("This run's events are not in this room's log"); return; }
      loading = true;
      let after = n.fromSeq - 1;
      try {
        for (;;) {
          const page = await o.load(after, pageSize);
          if (g !== gen) return;
          let done = page.length < pageSize;
          for (const e of page) {
            if (n.toSeq > 0 && e.seq > n.toSeq) { done = true; break; }
            put(e);
            if (capped()) { done = true; break; }
          }
          if (page.length) after = page[page.length - 1].seq;
          if (done || (n.toSeq > 0 && after >= n.toSeq)) break;
        }
      } catch (err) {
        if (g === gen) say(err instanceof Error ? err.message : String(err));
      }
      if (g !== gen) return;
      loading = false;
      const held = waiting;
      waiting = [];
      for (const e of held) api.apply(e);
    },
    apply(e) {
      if (!chosen || e.runId !== chosen) return;
      const n = nodes.find((x) => x.run === chosen);
      if (!n || n.toSeq > 0 || n.fromSeq <= 0) return;
      if (loading) { waiting.push(e); return; }
      if (e.seq > through && !capped()) put(e);
    },
  };
  return api;
}

// mountScrollIndex marks the node holding the topmost chat row on screen. refresh picks up
// rows added since; it is coalesced, since it runs on every event of a replay.
export function mountScrollIndex(chat: HTMLElement, o: {
  nodes: () => FlowNode[]; mark: (run: string | undefined) => void;
  observe?: (cb: IntersectionObserverCallback) => { observe(el: Element): void; disconnect(): void };
}): { refresh(): void } {
  const visible = new Map<Element, number>();
  const seen = new WeakSet<Element>();
  // jsdom has no IntersectionObserver: the room page's own tests run without one.
  const make = o.observe ?? ((cb) => typeof IntersectionObserver === "undefined" ? { observe() {}, disconnect() {} } : new IntersectionObserver(cb));
  const io = make((entries) => {
    for (const en of entries) {
      const seq = Number((en.target as HTMLElement).dataset.seq);
      if (en.isIntersecting && seq > 0) visible.set(en.target, seq); else visible.delete(en.target);
    }
    for (const el of visible.keys()) if (!el.isConnected) visible.delete(el);
    const top = visible.size ? Math.min(...visible.values()) : 0;
    o.mark(top ? nodeAt(o.nodes(), top) : undefined);
  });
  const scan = () => {
    timer = undefined;
    for (const el of chat.querySelectorAll("[data-seq]")) {
      if (seen.has(el)) continue;
      seen.add(el);
      io.observe(el);
    }
  };
  let timer: ReturnType<typeof setTimeout> | undefined;
  scan();
  return { refresh() { timer ??= setTimeout(scan, 200); } };
}
