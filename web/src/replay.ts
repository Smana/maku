// SPDX-License-Identifier: Apache-2.0

// A finished task replayed on its rail: the flow cut after each node, then after each milestone,
// each frame handed to Rail.set so the rail's own change marks animate the step. Beats are fixed
// whatever the real durations. Every frame is "the flow up to seq N".

import { ghostsAfter } from "./flow";
import type { Rail } from "./rail";
import type { Flow } from "./summary";

export const nodeBeat = 1200, milestoneBeat = 400, maxReplay = 20000;

// A milestone belongs to the node frame whose start it does not precede; one past every node
// gets a frame of its own. The cut is a running max, since a fact-only node sits at seq 0.
export function frames(f: Flow): Flow[] {
  const plan = f.plan ?? [];
  const ms = [...(f.milestones ?? [])].sort((a, b) => a.seq - b.seq);
  const out: Flow[] = [{ ...f, nodes: [], ghosts: plan, milestones: [] }];
  const last = f.nodes[f.nodes.length - 1];
  let upTo = 0;
  for (let k = 1; k <= f.nodes.length; k++) {
    const nodes = f.nodes.slice(0, k);
    upTo = Math.max(upTo, nodes[k - 1].fromSeq);
    out.push({ ...f, nodes, ghosts: ghostsAfter(plan, nodes), milestones: ms.filter((m) => m.seq <= upTo) });
  }
  const shown = last ? ms.filter((m) => m.seq <= upTo).length : 0;
  for (let j = shown + 1; j <= ms.length; j++) out.push({ ...f, milestones: ms.slice(0, j) });
  out[out.length - 1] = f;
  return out;
}

export function beats(f: Flow): number[] {
  const fs = frames(f);
  const raw = fs.slice(1).map((_, i) => (i < f.nodes.length ? nodeBeat : milestoneBeat));
  const total = raw.reduce((a, b) => a + b, 0);
  const scale = total > maxReplay ? maxReplay / total : 1;
  return raw.map((b) => b * scale);
}

export interface ReplayOptions {
  reduced: boolean;
  schedule?: (fn: () => void, ms: number) => unknown;
  cancel?: (h: unknown) => void;
}

export function replay(rail: { set: Rail["set"] }, f: Flow, phase: string, o: ReplayOptions): { skip(): void; next(): void; done: Promise<void>; stop(): void } {
  const schedule = o.schedule ?? ((fn, ms) => setTimeout(fn, ms));
  const cancel = o.cancel ?? ((h) => clearTimeout(h as ReturnType<typeof setTimeout>));
  const fs = frames(f);
  const delays = beats(f);
  let i = 0;
  let timer: unknown;
  let over = false;
  let finish!: () => void;
  const done = new Promise<void>((r) => { finish = r; });

  const show = () => rail.set(fs[i], phase, Date.now());
  const end = () => {
    if (over) return;
    over = true;
    if (timer !== undefined) cancel(timer);
    finish();
  };
  const step = () => {
    if (over) return;
    i++;
    show();
    if (i >= fs.length - 1) end();
    else if (!o.reduced) timer = schedule(step, delays[i]);
  };

  show();
  if (fs.length === 1) end();
  else if (!o.reduced) timer = schedule(step, delays[0]);

  return {
    skip() {
      if (over) return;
      i = fs.length - 1;
      show();
      end();
    },
    next() { step(); },
    stop: end,
    done,
  };
}
