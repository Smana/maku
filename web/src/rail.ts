// SPDX-License-Identifier: Apache-2.0

// The run rail: the task's runs as one vertical path, then its milestones. Every string from the
// flow goes in as text; role only reaches the DOM through tone(), never raw. set re-renders the
// path keyed by data-run, so a later set can tell new nodes from old.

import type { RoomEvent } from "./conn";
import { actorName, idTail } from "./chat";
import { el } from "./controls";
import { chip, detail, ghostMerge, isLive, modelsOf, parts, personaLabel, rows, tone, type Row } from "./flow";
import { glyph, glyphFor } from "./glyphs";
import { ago, type Flow, type FlowNode } from "./summary";

// you, when given, is the viewer's principal, so the steer card names the viewer's own steers "you".
export interface RailHandlers { select(run: string | undefined): void; you?(): string | undefined }

export interface Rail {
  el: HTMLElement;
  // cursor is the seq the summary folded through: live counts only past it. queued is how many
  // notes wait for the next run (the summary's queued).
  set(f: Flow | undefined, phase: string, now: number, cursor?: number, queued?: number): void;
  tick(now: number): void;
  live(e: RoomEvent): void;
  mark(run: string | undefined): void;
  chosen(run: string | undefined): void;
  // The page is leaving: drop what mount() listens to.
  dispose(): void;
}

// rows() never folds a one-node round, so a path can run past this.
const maxRows = 8;

// The card keeps this far from the window's edges, and this far from its node.
const edgeGap = 8;
const cardGap = 4;
// How long a pointer has to cross that gap.
const crossMs = 150;
// The steers kept for the card, newest last: the summary's fold keeps as many (internal/summary
// maxHumanMsgs), so a node's count past this is "earlier" either way.
const maxSteers = 512;

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

// A steer is a human's steering message (internal/summary noteHuman): a run's own message, or a
// system actor's, is not.
interface Steer { seq: number; actor: RoomEvent["actor"]; ts: string }
const steerOf = (e: RoomEvent): Steer | undefined =>
  e.type === "message" && !e.runId && e.actor.kind === "human" && e.payload?.delivery === "steering"
    ? { seq: e.seq, actor: e.actor, ts: e.ts } : undefined;

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

export function mountRail(h: RailHandlers): Rail {
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
  root.append(nav, card, announcer);

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
  let steers: Steer[] = [];
  let queued = 0;
  const ages = new Map<string, HTMLElement>();
  const buttons = new Map<string, HTMLButtonElement>();

  let prev: { runs: Set<string>; edges: Set<string>; judges: Map<string, number>; done: Set<string>; marks: Map<string, number> } | undefined;

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
    if (n.steered) lines.push(...steerLines(n, n.steered));
    const first = n.handoff?.split("\n")[0].trim();
    if (first) {
      const claim = el("p", { className: "rail-claim" });
      claim.append(el("span", { textContent: first }), el("span", { className: "hint", textContent: "the agent's claim" }));
      lines.push(claim);
    }
    card.replaceChildren(...lines);
  }

  // Who steered the node and when, newest first, from the steers the page holds in its window
  // (from..to inclusive, open while to is 0, as the fold counts them); "earlier" for the rest.
  function steerLines(n: FlowNode, count: number): HTMLElement[] {
    const held = steers.filter((x) => x.seq >= n.fromSeq && (n.toSeq === 0 || x.seq <= n.toSeq)).reverse();
    const ol = el("ol", { className: "rail-steers" });
    ol.append(...held.map((x) => el("li", { textContent: `${actorName(x.actor, h.you?.())}, ${ago(x.ts, clock)}` })));
    if (count > held.length) ol.append(el("li", { className: "rail-earlier", textContent: `${count - held.length} earlier` }));
    return [el("p", { className: "rail-card-head", textContent: "Steered by" }), ol];
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
    const marks = activity(n);
    if (marks) b.append(marks);
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

  // The room's activity on a node: the steers, then the chat, each absent at zero.
  function activity(n: FlowNode): HTMLElement | undefined {
    if (!n.steered && !n.chat) return undefined;
    const row = el("span", { className: "rail-activity" });
    if (n.steered) row.append(el("span", { className: "rail-steered", textContent: `↪ steered ×${n.steered}` }));
    if (n.chat) {
      const c = el("span", { className: "rail-chat" });
      c.setAttribute("role", "img");
      c.setAttribute("aria-label", `${n.chat} chat message${n.chat === 1 ? "" : "s"} during this run`);
      c.append(glyph("chat"), String(n.chat));
      row.append(c);
    }
    return row;
  }

  const queuedText = () => `${queued} queued → next run`;

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
    if (i === 0 && queued) li.append(el("span", { className: "rail-queued", textContent: queuedText() }));
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
    const marks = new Map<string, number>();
    const last = prev;
    // A mark or chip that is new, or whose count grew, arrives.
    const arrive = (key: string, e: Element | null, n: number) => {
      if (!e) return;
      marks.set(key, n);
      if (last && n > (last.marks.get(key) ?? 0)) animate(e, "arrive");
    };
    for (const li of path.querySelectorAll<HTMLElement>("li.rail-node")) {
      const n = nodeOf(li.dataset.run!);
      const k = stable(li.dataset.run!);
      arrive(`steered:${k}`, li.querySelector(".rail-steered"), n?.steered ?? 0);
      arrive(`chat:${k}`, li.querySelector(".rail-chat"), n?.chat ?? 0);
    }
    arrive("queued", path.querySelector(".rail-queued"), queued);
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
    prev = { runs, edges, judges, done, marks };
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
    // With no ghost to carry it, the queue sits in the gap after the last (the live) node.
    if (queued && !flow.ghosts?.length) items.push(el("li", { className: "rail-queued", textContent: queuedText() }));
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

  function show(f: Flow | undefined, phase: string, now: number, cursor?: number, q = 0): void {
    flow = f;
    queued = q;
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
  }

  return {
    el: root,
    set(f, phase, now, cursor, q) {
      show(f, phase, now, cursor, q);
    },
    tick(now) {
      clock = now;
      for (const [run, age] of ages) {
        const n = nodeOf(run);
        if (n) age.textContent = detail(n, now);
      }
    },
    live(e) {
      const st = steerOf(e);
      if (st && !steers.some((x) => x.seq === st.seq)) {
        steers = [...steers, st].sort((a, b) => a.seq - b.seq).slice(-maxSteers);
        const n = carded ? nodeOf(carded) : undefined;
        if (n) fillCard(n);
      }
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
    },
  };
}
