// SPDX-License-Identifier: Apache-2.0

// The run rail: the task's runs as one vertical path, then its milestones. Every string from the
// flow goes in as text; role only reaches the DOM through tone(), never raw. set re-renders the
// path keyed by data-run, so a later set can tell new nodes from old.

import type { RoomEvent } from "./conn";
import { idTail } from "./chat";
import { el } from "./controls";
import { chip, detail, ghostMerge, isLive, modelsOf, parts, personaLabel, rows, terminal, tone, type Row } from "./flow";
import { glyph, glyphFor } from "./glyphs";
import { replay } from "./replay";
import type { Flow, FlowNode } from "./summary";

export interface RailHandlers { select(run: string | undefined): void }

// Replay's clock and motion preference, injectable so a test needs no real time.
export interface RailOptions {
  reduced?: () => boolean;
  schedule?: (fn: () => void, ms: number) => unknown;
  cancel?: (h: unknown) => void;
}

const prefersReduced = () => typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;

export interface Rail {
  el: HTMLElement;
  // cursor is the seq the summary folded through: live counts only past it. A replay frame has none.
  set(f: Flow | undefined, phase: string, now: number, cursor?: number): void;
  tick(now: number): void;
  live(e: RoomEvent): void;
  mark(run: string | undefined): void;
  chosen(run: string | undefined): void;
  // The page is leaving: end a running replay without applying a held refetch.
  dispose(): void;
}

// rows() never folds a one-node round, so a path can run past this.
const maxRows = 8;

// The card keeps this far from the window's edges, and this far from its node.
const edgeGap = 8;
const cardGap = 4;
// How long a pointer has to cross that gap.
const crossMs = 150;

// A room id is a C2 id; nothing else reaches an href.
const roomId = /^[a-z2-7]{8}$/;

function roomLink(room: string | undefined, text: string): HTMLElement {
  if (!room || !roomId.test(room)) return el("span", { textContent: text });
  return el("a", { href: `/r/${room}`, textContent: text });
}

const NS = "http://www.w3.org/2000/svg";

let cards = 0;

// A ghost's index shifts when an earlier one goes live; its persona is what stays.
const stable = (run: string) => run.replace(/^plan-\d+-/, "plan-");

// Without animationend the class would stay and a re-render would replay it.
function animate(e: Element, cls: string): void {
  e.classList.add(cls);
  e.addEventListener("animationend", (ev) => { if (ev.target === e) e.classList.remove(cls); });
}

function edge(from: string, to: string, label: string | undefined, ghost: boolean): HTMLLIElement {
  const li = el("li", { className: "rail-edge" });
  li.dataset.from = from;
  li.dataset.to = to;
  if (ghost) li.dataset.state = "ghost";
  const svg = document.createElementNS(NS, "svg");
  for (const [k, v] of Object.entries({ viewBox: "0 0 24 32", width: "24", height: "32", preserveAspectRatio: "none", "aria-hidden": "true" })) svg.setAttribute(k, v);
  const p = document.createElementNS(NS, "path");
  p.setAttribute("d", "M12 0V32");
  p.setAttribute("class", "rail-stroke");
  svg.append(p);
  li.append(svg);
  if (label) li.append(el("span", { className: "rail-edge-label", textContent: label }));
  return li;
}

function stamp(iso: string | undefined): string {
  const t = iso ? Date.parse(iso) : NaN;
  return Number.isNaN(t) ? "" : `${new Date(t).toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

function roundMark(n: FlowNode): string {
  const c = chip(n);
  if (c.kind === "ok") return "✓";
  if (c.kind === "changes") return "✗";
  return c.text;
}

export function mountRail(h: RailHandlers, opts: RailOptions = {}): Rail {
  const root = el("aside", { className: "rail", hidden: true });
  const nav = el("nav");
  nav.setAttribute("aria-label", "Task path");
  const path = el("ol", { className: "rail-path" });
  const milestones = el("ol", { className: "rail-milestones" });
  nav.append(path, milestones);
  const card = el("div", { className: "rail-card", id: `rail-card-${++cards}`, hidden: true });
  card.setAttribute("role", "tooltip");
  const announcer = el("p", { className: "rail-live" });
  announcer.setAttribute("aria-live", "polite");
  const controls = el("div", { className: "rail-replays", hidden: true });
  const mkBtn = (label: string) => el("button", { type: "button", className: "rail-replay", textContent: label, hidden: true });
  const replayBtn = mkBtn("Replay");
  const nextBtn = mkBtn("Next");
  const skipBtn = mkBtn("Skip");
  controls.append(replayBtn, nextBtn, skipBtn);
  root.append(controls, nav, card, announcer);

  let flow: Flow | undefined;
  let phaseNow = "";
  let clock = 0;
  let known: Set<string> | undefined;
  let current: string | undefined;
  let picked: string | undefined;
  let carded: string | undefined;
  const opened = new Set<number>();
  // The tool results the stream delivered past the last summary's cursor, in seq order: the socket's
  // tail may replay what the summary already counted, and may deliver what it has not yet.
  let through = 0;
  let extra: { seq: number; run: string; failed: boolean }[] = [];
  const toolsOf = (n: FlowNode) => {
    const t = { ...n.tools };
    for (const x of extra) if (x.run === n.run) { if (x.failed) t.failed++; else t.ok++; }
    return t;
  };
  const ages = new Map<string, HTMLElement>();
  const buttons = new Map<string, HTMLButtonElement>();

  // While a replay runs, a refetch is held here and applied when it ends.
  let running: { skip(): void; next(): void; stop(): void } | undefined;
  let held: { f: Flow | undefined; phase: string; now: number; cursor?: number } | undefined;

  let prev: { runs: Set<string>; edges: Set<string>; judges: Map<string, number>; done: Set<string> } | undefined;

  const nodeOf = (run: string) => flow?.nodes.find((n) => n.run === run);

  function fillCard(n: FlowNode): void {
    const t = toolsOf(n);
    const lines: HTMLElement[] = [
      el("p", { textContent: `start ${stamp(n.startedAt) || "unknown"}` }),
      el("p", { textContent: n.endedAt ? `end ${stamp(n.endedAt)}` : isLive(n) ? "still running" : "end unknown" }),
      el("p", { textContent: `tool calls ${t.ok} ok · ${t.failed} failed` }),
    ];
    const names = [...(n.model ? [`${n.model} (asked)`] : []), ...(n.models ?? []).filter((m) => m !== n.model)];
    if (n.effort) names.push(`effort ${n.effort}`);
    if (names.length) lines.push(el("p", { textContent: `Models: ${names.join(" · ")}` }));
    if (n.reason) lines.push(el("p", { textContent: n.reason }));
    if (n.startedBy) lines.push(el("p", { textContent: `started by ${idTail(n.startedBy)}` }));
    const first = n.handoff?.split("\n")[0].trim();
    if (first) {
      const claim = el("p", { className: "rail-claim" });
      claim.append(el("span", { textContent: first }), el("span", { className: "hint", textContent: "the agent's claim" }));
      lines.push(claim);
    }
    card.replaceChildren(...lines);
  }

  // The card is fixed to the viewport beside its button, outside the rail's scroll box, which would
  // clip it: below the button, or above when more room is there, its height capped to that room.
  // The room is the window less the pinned header and dock (room.ts sets their heights): the sticky
  // rail is a stacking context under both, so a card reaching them would slide beneath.
  function place(b: HTMLElement): void {
    const r = b.getBoundingClientRect();
    const vw = window.innerWidth, vh = window.innerHeight;
    const cs = getComputedStyle(card);
    // The drawer (railpane.ts: .open) is fixed above both, so it keeps the whole window.
    const inset = (v: string) => root.classList.contains("open") ? 0 : parseFloat(cs.getPropertyValue(v)) || 0;
    const width = Math.min(Math.max(r.width, 280), vw - 2 * edgeGap);
    const below = vh - inset("--dock-h") - r.bottom - cardGap - edgeGap;
    const above = r.top - inset("--header-h") - cardGap - edgeGap;
    card.style.width = `${width}px`;
    card.style.left = `${Math.max(edgeGap, Math.min(r.left, vw - width - edgeGap))}px`;
    card.style.maxHeight = "none";
    const up = card.scrollHeight > below && above > below;
    card.dataset.side = up ? "above" : "below";
    card.style.top = up ? "" : `${r.bottom + cardGap}px`;
    card.style.bottom = up ? `${vh - r.top + cardGap}px` : "";
    card.style.maxHeight = `${Math.max(up ? above : below, 0)}px`;
  }

  let leaving: ReturnType<typeof setTimeout> | undefined;

  function showCard(run: string): void {
    clearTimeout(leaving);
    const n = nodeOf(run);
    const b = buttons.get(run);
    if (!n || !b) return;
    carded = run;
    fillCard(n);
    card.hidden = false;
    place(b);
  }

  function hideCard(): void {
    clearTimeout(leaving);
    carded = undefined;
    card.hidden = true;
  }
  card.onmouseenter = () => clearTimeout(leaving);
  card.onmouseleave = (e) => { if (!carded || e.relatedTarget !== buttons.get(carded)) hideCard(); };
  // A fixed card follows its node as the rail or the page scrolls (focus scrolls the rail too).
  const follow = (e: Event) => {
    const b = carded && buttons.get(carded);
    if (b && e.target !== card) place(b);
  };
  addEventListener("scroll", follow, { capture: true, passive: true });

  function nodeItem(n: FlowNode): HTMLLIElement {
    const li = el("li", { className: "rail-node" });
    li.dataset.run = n.run;
    li.dataset.tone = tone(n.role);
    li.dataset.state = isLive(n) ? "live" : "done";
    const b = el("button", { type: "button", className: "rail-button" });
    b.setAttribute("aria-describedby", card.id);
    const c = chip(n);
    const persona = el("span", { className: "rail-persona", textContent: personaLabel(n.persona) });
    const chipEl = el("span", { className: "rail-chip", textContent: c.text });
    chipEl.dataset.kind = c.kind;
    const line = el("span", { className: "rail-line" });
    line.append(glyph(glyphFor(n.persona, n.role)), persona, chipEl);
    if (n.judge?.count) line.append(el("span", { className: "rail-judge", textContent: `◇×${n.judge.count}`, title: `${n.judge.count} judge verdicts` }));
    const age = el("span", { className: "rail-detail", textContent: detail(n, clock) });
    if (isLive(n)) ages.set(n.run, age);
    b.append(line, age);
    const m = modelsOf(n);
    if (m.served.length || m.asked) {
      const models = el("span", { className: "rail-models" });
      models.append(...m.served.map((x) => el("span", { className: "rail-model", textContent: x })));
      if (m.asked) {
        const asked = el("span", { className: "rail-asked", textContent: m.asked });
        asked.title = `asked for ${m.asked}`;
        models.append(asked);
      }
      b.append(models);
    }
    // On touch there is no hover: the first tap opens the card, a tap on its open card selects.
    // Read at pointerdown: the tap's focus opens the card before its click arrives.
    let tapToOpen = false;
    b.onpointerdown = (e) => { tapToOpen = (e as PointerEvent).pointerType === "touch" && carded !== n.run; };
    b.onclick = () => {
      if (tapToOpen) showCard(n.run);
      else h.select(n.run);
      tapToOpen = false;
    };
    b.onfocus = () => showCard(n.run);
    b.onmouseenter = () => showCard(n.run);
    b.onblur = hideCard;
    // A pointer crossing the gap into the card (to scroll it) keeps it open.
    b.onmouseleave = (e) => { if (e.relatedTarget !== card) leaving = setTimeout(hideCard, crossMs); };
    b.onkeydown = (e) => { if (e.key === "Escape") hideCard(); };
    buttons.set(n.run, b);
    li.append(b);
    return li;
  }

  function ghostItem(persona: string, i: number): HTMLLIElement {
    const li = el("li", { className: "rail-node" });
    li.dataset.run = `plan-${i}-${persona}`;
    li.dataset.tone = tone(persona);
    li.dataset.state = "ghost";
    const b = el("button", { type: "button", className: "rail-button", disabled: true });
    const chipEl = el("span", { className: "rail-chip", textContent: "planned" });
    chipEl.dataset.kind = "";
    const line = el("span", { className: "rail-line" });
    line.append(glyph(glyphFor(persona, "")), el("span", { className: "rail-persona", textContent: personaLabel(persona) }), chipEl);
    b.append(line, el("span", { className: "rail-detail" }));
    li.append(b);
    return li;
  }

  function roundItem(r: { round: number; nodes: FlowNode[] }): HTMLLIElement {
    const li = el("li", { className: "rail-round" });
    li.dataset.round = String(r.round);
    const last = r.nodes[r.nodes.length - 1];
    const b = el("button", { type: "button", textContent: `round ${r.round} · ${r.nodes.map((n) => personaLabel(n.persona)).join(" → ")} ${roundMark(last)}` });
    b.setAttribute("aria-expanded", "false");
    b.onclick = () => {
      opened.add(r.round);
      render();
      buttons.get(r.nodes[0].run)?.focus();
    };
    li.append(b);
    return li;
  }

  // Compares the fresh DOM with the previous set; the first set has nothing to compare with.
  function motion(): void {
    const runs = new Set<string>();
    const edges = new Set<string>();
    const judges = new Map<string, number>();
    const done = new Set<string>();
    const last = prev;
    for (const li of path.querySelectorAll<HTMLElement>("li.rail-node")) {
      const k = stable(li.dataset.run!);
      runs.add(k);
      const c = nodeOf(li.dataset.run!)?.judge?.count ?? 0;
      judges.set(k, c);
      if (!last) continue;
      if (!last.runs.has(k)) animate(li, "enter");
      else if (c > (last.judges.get(k) ?? 0)) {
        const mark = li.querySelector(".rail-judge");
        if (mark) animate(mark, "tick");
      }
    }
    for (const li of path.querySelectorAll<HTMLElement>("li.rail-edge")) {
      const k = `${stable(li.dataset.from!)}→${stable(li.dataset.to!)}`;
      edges.add(k);
      if (!last || last.edges.has(k)) continue;
      const p = li.querySelector<SVGPathElement>("path.rail-stroke");
      if (!p) continue;
      const len = typeof p.getTotalLength === "function" ? p.getTotalLength() : 40;
      p.style.setProperty("--len", String(len));
      animate(p, "draw");
    }
    for (const li of milestones.querySelectorAll<HTMLElement>("li.rail-milestone")) {
      if (li.dataset.state !== "done") continue;
      done.add(li.dataset.phase!);
      if (last && !last.done.has(li.dataset.phase!)) animate(li, "fill");
    }
    prev = { runs, edges, judges, done };
  }

  function applyMarks(): void {
    for (const [run, b] of buttons) {
      if (run === current) b.setAttribute("aria-current", "true");
      else b.removeAttribute("aria-current");
      b.setAttribute("aria-pressed", String(run === picked));
    }
  }

  function render(): void {
    // A refetch must not steal focus from the node a keyboard user is on.
    const focused = [...buttons].find(([, b]) => b === document.activeElement)?.[0];
    hideCard();
    ages.clear();
    buttons.clear();
    if (!flow) {
      path.replaceChildren();
      milestones.replaceChildren();
      return;
    }
    const out: Row[] = [];
    for (const r of rows(flow.nodes, maxRows)) {
      if ("round" in r && opened.has(r.round)) out.push(...r.nodes.map((node) => ({ node })));
      else out.push(r);
    }
    const items: HTMLLIElement[] = [];
    let prev: string | undefined;
    for (const r of out) {
      const first = "node" in r ? r.node : r.nodes[0];
      const last = "node" in r ? r.node : r.nodes[r.nodes.length - 1];
      if (prev) items.push(edge(prev, first.run, first.edge, false));
      items.push("node" in r ? nodeItem(r.node) : roundItem(r));
      prev = last.run;
    }
    (flow.ghosts ?? []).forEach((g, i) => {
      const li = ghostItem(g, i);
      if (prev) items.push(edge(prev, li.dataset.run!, undefined, true));
      items.push(li);
      prev = li.dataset.run;
    });
    if (flow.split) {
      const ol = el("ol", { className: "rail-parts" });
      for (const p of parts(flow.split)) {
        const li = el("li", { className: "rail-part" });
        li.dataset.state = p.state;
        li.append(roomLink(p.room, `#${p.issue ?? "?"} ${p.state}`));
        ol.append(li);
      }
      const scoper = items.find((li) => li.classList.contains("rail-node") && nodeOf(li.dataset.run!)?.persona === "scoper");
      if (scoper) scoper.append(ol);
    }
    if (flow.parent) {
      const li = el("li", { className: "rail-parent" });
      li.append(roomLink(flow.parent.room, `part ${flow.parent.index} of ${flow.parent.of} of #${flow.parent.issue}`));
      items.unshift(li);
    }
    path.replaceChildren(...items);

    const ms: HTMLLIElement[] = (flow.milestones ?? []).map((m) => {
      const li = el("li", { className: "rail-milestone", textContent: m.phase });
      li.dataset.phase = m.phase;
      li.dataset.state = "done";
      return li;
    });
    if (ghostMerge(flow, phaseNow)) {
      const li = el("li", { className: "rail-milestone", textContent: "merge" });
      li.dataset.phase = "Merged";
      li.dataset.state = "ghost";
      ms.push(li);
    }
    milestones.replaceChildren(...ms);
    applyMarks();
    motion();
    if (focused) buttons.get(focused)?.focus();
  }


  function show(f: Flow | undefined, phase: string, now: number, cursor?: number): void {
    flow = f;
    phaseNow = phase;
    clock = now;
    if (cursor === undefined) extra = [];
    else {
      through = cursor;
      extra = extra.filter((x) => x.seq > cursor);
    }
    root.hidden = !f;
    if (f) {
      if (known) {
        const started = f.nodes.filter((n) => isLive(n) && !known!.has(n.run));
        if (started.length) announcer.textContent = started.map((n) => `${personaLabel(n.persona)} started`).join(", ");
      }
      known = new Set(f.nodes.map((n) => n.run));
    }
    render();
    sync();
  }

  function sync(): void {
    const ended = !!flow && flow.nodes.length > 0 && terminal.has(phaseNow);
    controls.hidden = !ended && !running;
    replayBtn.hidden = !ended || !!running;
    skipBtn.hidden = !running;
    nextBtn.hidden = !running || !(opts.reduced ?? prefersReduced)();
  }

  function finish(): void {
    running = undefined;
    const h = held;
    held = undefined;
    if (h) show(h.f, h.phase, h.now, h.cursor);
    sync();
  }

  replayBtn.onclick = () => {
    if (!flow || running) return;
    const f = flow, phase = phaseNow;
    const r = replay({ set: (g, p, n) => show(g, p, n) }, f, phase, {
      reduced: (opts.reduced ?? prefersReduced)(), schedule: opts.schedule, cancel: opts.cancel,
    });
    running = r;
    void r.done.then(() => { if (running === r) finish(); });
    sync();
  };
  nextBtn.onclick = () => running?.next();
  skipBtn.onclick = () => {
    if (!running) return;
    running.skip();
    finish();
  };

  return {
    el: root,
    set(f, phase, now, cursor) {
      if (running) held = { f, phase, now, cursor };
      else show(f, phase, now, cursor);
    },
    tick(now) {
      clock = now;
      for (const [run, age] of ages) {
        const n = nodeOf(run);
        if (n) age.textContent = detail(n, now);
      }
    },
    live(e) {
      if (e.type !== "tool_result" || !e.runId) return;
      const n = nodeOf(e.runId);
      if (!n || !isLive(n) || e.seq <= Math.max(through, extra.at(-1)?.seq ?? 0)) return;
      const s = e.payload?.status;
      extra.push({ seq: e.seq, run: n.run, failed: s === "error" || s === "rejected" });
      if (carded === n.run) fillCard(n);
    },
    mark(run) {
      current = run;
      applyMarks();
    },
    chosen(run) {
      picked = run;
      applyMarks();
    },
    dispose() {
      removeEventListener("scroll", follow, { capture: true });
      running?.stop();
      running = undefined;
      held = undefined;
      sync();
    },
  };
}
