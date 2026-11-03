// SPDX-License-Identifier: Apache-2.0

// The run rail: the task's runs as one vertical path, then its milestones. Every string from the
// flow goes in as text; role only reaches the DOM through tone(), never raw. set re-renders the
// path keyed by data-run, so a later set can tell new nodes from old.

import type { RoomEvent } from "./conn";
import { idTail } from "./chat";
import { el } from "./controls";
import { chip, detail, ghostMerge, parts, personaLabel, rows, terminal, tone, type Row } from "./flow";
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
  set(f: Flow | undefined, phase: string, now: number): void;
  tick(now: number): void;
  live(e: RoomEvent): void;
  mark(run: string | undefined): void;
  chosen(run: string | undefined): void;
  // The page is leaving: end a running replay without applying a held refetch.
  dispose(): void;
}

// rows() never folds a one-node round, so a path can run past this.
const maxRows = 8;

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
  // The tool counts the stream adds between two summaries; the next set starts again from its own.
  const tools = new Map<string, { ok: number; failed: number }>();
  const ages = new Map<string, HTMLElement>();
  const buttons = new Map<string, HTMLButtonElement>();

  // While a replay runs, a refetch is held here and applied when it ends.
  let running: { skip(): void; next(): void; stop(): void } | undefined;
  let held: { f: Flow | undefined; phase: string; now: number } | undefined;

  let prev: { runs: Set<string>; edges: Set<string>; judges: Map<string, number>; done: Set<string> } | undefined;

  const nodeOf = (run: string) => flow?.nodes.find((n) => n.run === run);

  function fillCard(n: FlowNode): void {
    const t = tools.get(n.run) ?? n.tools;
    const lines: HTMLElement[] = [
      el("p", { textContent: `start ${stamp(n.startedAt) || "unknown"}` }),
      el("p", { textContent: n.endedAt ? `end ${stamp(n.endedAt)}` : "still running" }),
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

  function showCard(run: string): void {
    const n = nodeOf(run);
    const li = buttons.get(run)?.parentElement;
    if (!n || !li) return;
    carded = run;
    fillCard(n);
    li.append(card);
    card.hidden = false;
  }

  function hideCard(): void {
    carded = undefined;
    card.hidden = true;
    root.insertBefore(card, announcer);
  }

  function nodeItem(n: FlowNode): HTMLLIElement {
    const li = el("li", { className: "rail-node" });
    li.dataset.run = n.run;
    li.dataset.tone = tone(n.role);
    li.dataset.state = n.endedAt ? "done" : "live";
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
    if (!n.endedAt) ages.set(n.run, age);
    b.append(line, age);
    b.onclick = () => h.select(n.run);
    b.onfocus = () => showCard(n.run);
    b.onmouseenter = () => showCard(n.run);
    b.onblur = hideCard;
    b.onmouseleave = hideCard;
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


  function show(f: Flow | undefined, phase: string, now: number): void {
    flow = f;
    phaseNow = phase;
    clock = now;
    tools.clear();
    root.hidden = !f;
    if (f) {
      if (known) {
        const started = f.nodes.filter((n) => !n.endedAt && !known!.has(n.run));
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
    if (h) show(h.f, h.phase, h.now);
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
    set(f, phase, now) {
      if (running) held = { f, phase, now };
      else show(f, phase, now);
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
      if (!n || n.endedAt) return;
      const t = { ...(tools.get(n.run) ?? n.tools) };
      const s = e.payload?.status;
      if (s === "error" || s === "rejected") t.failed++;
      else t.ok++;
      tools.set(n.run, t);
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
      running?.stop();
      running = undefined;
      held = undefined;
      sync();
    },
  };
}
