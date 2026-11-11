// SPDX-License-Identifier: Apache-2.0

// The room body with its rail: the conversation's regions beside the run rail, the dots button
// and the drawer a narrow screen opens. room.ts and the offline preview mount it the same way.

import { chip, isLive, personaLabel, tone } from "./flow";
import { mountRail, type Rail, type RailHandlers } from "./rail";
import type { Flow } from "./summary";

// stripItem says one step of the path as a glyph and a word: the glyph is text, so the strip reads
// without colour, and the word goes to the accessible name. ✗ is the rail's own mark for a run that
// did not land (flow.ts chip), – one that ended with no outcome.
function stripItem(f: Flow, i: number): { who: string; role: string; glyph: string; word: string; st: string } {
  const n = f.nodes[i];
  if (!n) { const g = f.ghosts![i - f.nodes.length]; return { who: personaLabel(g), role: g, glyph: "○", word: "planned", st: "ghost" }; }
  const who = personaLabel(n.persona), role = n.role;
  if (isLive(n)) return { who, role, glyph: "●", word: "running", st: "live" };
  switch (chip(n).kind) {
    case "ok": return { who, role, glyph: "✓", word: "done", st: "done" };
    case "changes": return { who, role, glyph: "✗", word: "changes requested", st: "done" };
    case "fail": return { who, role, glyph: "✗", word: "failed", st: "done" };
    case "stop": return { who, role, glyph: "✗", word: "stopped", st: "done" };
    default: return { who, role, glyph: "–", word: "ended", st: "done" };
  }
}

export interface RailPane {
  body: HTMLElement; // div.room-body: dots, the regions, then the rail
  rail: Rail;
  show(flow: Flow | undefined, phase: string, now: number, cursor?: number, queued?: number): void;
}

// mountRailPane puts regions in order inside the body. h.select is called with the run a node
// picks, after the drawer has closed; h.you goes to the rail as is.
export function mountRailPane(h: RailHandlers, ...regions: HTMLElement[]): RailPane {
  const rail = mountRail({ select: (run) => { setOpen(false); h.select(run); }, you: h.you });
  const dots = document.createElement("button");
  dots.type = "button";
  dots.className = "rail-dots";
  dots.setAttribute("aria-label", "Show the task path");
  dots.hidden = true;
  rail.el.id = "task-rail";
  dots.setAttribute("aria-controls", rail.el.id);
  dots.setAttribute("aria-expanded", "false");
  const setOpen = (open: boolean) => { rail.el.classList.toggle("open", open); dots.setAttribute("aria-expanded", String(open)); };
  const drawerClose = document.createElement("button");
  drawerClose.type = "button";
  drawerClose.className = "rail-close";
  drawerClose.textContent = "Close";
  rail.el.prepend(drawerClose);
  dots.onclick = () => { setOpen(true); drawerClose.focus(); };
  drawerClose.onclick = () => { setOpen(false); dots.focus(); };
  rail.el.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && rail.el.classList.contains("open")) { setOpen(false); dots.focus(); }
  });
  const body = document.createElement("div");
  body.className = "room-body";
  body.append(dots, ...regions, rail.el);
  return {
    body, rail,
    show(f, phase, now, cursor, queued) {
      rail.set(f, phase, now, cursor, queued);
      body.classList.toggle("has-rail", !!f);
      dots.hidden = !f;
      if (!f) setOpen(false);
      const items = f ? Array.from({ length: f.nodes.length + (f.ghosts?.length ?? 0) }, (_, i) => stripItem(f, i)) : [];
      // The name starts with the strip's own words, so a voice user can say what they see.
      dots.setAttribute("aria-label", items.length ? `${items.map((x) => `${x.who} ${x.word}`).join(", ")}: show the task path` : "Show the task path");
      dots.replaceChildren(...items.flatMap((x, i) => {
        const d = document.createElement("span");
        d.className = "rail-dot";
        d.dataset.tone = tone(x.role);
        d.dataset.state = x.st;
        const g = document.createElement("span");
        g.className = "rail-dot-glyph";
        g.textContent = x.glyph;
        // A no-break space keeps a name with its glyph; items wrap at the separators, and
        // .rail-dots' overflow-wrap still breaks a name too long for the line.
        d.append(`${x.who}\u00a0`, g);
        return i ? [" · ", d] : [d];
      }));
    },
  };
}
