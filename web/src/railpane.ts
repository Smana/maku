// SPDX-License-Identifier: Apache-2.0

// The room body with its rail: the conversation's regions beside the run rail, the dots button
// and the drawer a narrow screen opens. room.ts and the offline preview mount it the same way.

import { isLive, tone } from "./flow";
import { mountRail, type Rail } from "./rail";
import type { Flow } from "./summary";

export interface RailPane {
  body: HTMLElement; // div.room-body: dots, the regions, then the rail
  rail: Rail;
  show(flow: Flow | undefined, phase: string, now: number, cursor?: number): void;
}

// mountRailPane puts regions in order inside the body. select is called with the run a node
// picks, after the drawer has closed.
export function mountRailPane(select: (run: string | undefined) => void, ...regions: HTMLElement[]): RailPane {
  const rail = mountRail({ select: (run) => { rail.el.classList.remove("open"); select(run); } });
  const dots = document.createElement("button");
  dots.type = "button";
  dots.className = "rail-dots";
  dots.setAttribute("aria-label", "Show the task path");
  dots.hidden = true;
  const drawerClose = document.createElement("button");
  drawerClose.type = "button";
  drawerClose.className = "rail-close";
  drawerClose.textContent = "Close";
  rail.el.prepend(drawerClose);
  dots.onclick = () => { rail.el.classList.add("open"); drawerClose.focus(); };
  drawerClose.onclick = () => { rail.el.classList.remove("open"); dots.focus(); };
  rail.el.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && rail.el.classList.contains("open")) { rail.el.classList.remove("open"); dots.focus(); }
  });
  const body = document.createElement("div");
  body.className = "room-body";
  body.append(dots, ...regions, rail.el);
  return {
    body, rail,
    show(f, phase, now, cursor) {
      rail.set(f, phase, now, cursor);
      body.classList.toggle("has-rail", !!f);
      dots.hidden = !f;
      if (!f) rail.el.classList.remove("open");
      dots.replaceChildren(...[...f?.nodes.map((n) => [n.role, isLive(n) ? "live" : "done"]) ?? [], ...(f?.ghosts ?? []).map((g) => [g, "ghost"])].map(([who, st]) => {
        const d = document.createElement("span");
        d.className = "rail-dot";
        d.dataset.tone = tone(who);
        d.dataset.state = st;
        return d;
      }));
    },
  };
}
