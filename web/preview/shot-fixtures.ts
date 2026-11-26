// SPDX-License-Identifier: Apache-2.0

// Three screenshots by the live implementer (ikely2yk) for full.html?shots: a login page, a
// dashboard and an error page, the last one taken on a text-only model. The images are in
// preview/media/shots by digest, and serve.mjs answers the broker's image route from there.

import type { RoomEvent } from "../src/conn";
import type { Flow } from "../src/summary";

const run = "ikely2yk";

const shots = [
  { digest: "2257d5fba1a77e1bbd1532e6cb45d3426319be0bd247804a9f9d4310037c428a", bytes: 5490, page: "/login", vision: true },
  { digest: "b0516186e2123edfb2dfb350d22fb125f028a74a2c2ac21cdb4ad1f1b3fd185f", bytes: 7169, page: "/dashboard", vision: true },
  { digest: "796bdac87b428b35972b635449864e156b5552164c2c5fa1166669756712a1ef", bytes: 5386, page: "/settings/billing", vision: false },
];

// shotEvents is the three screenshot events, after last.
export function shotEvents(last: RoomEvent): RoomEvent[] {
  return shots.map((s, i) => ({ ...last, id: `shot${i + 1}`, seq: last.seq + i + 1, runId: run,
    actor: { kind: "agent", id: `agent:${run}`, role: "implementer" }, type: "screenshot", origin: "harness",
    ts: new Date(Date.parse(last.ts) + (i + 1) * 20_000).toISOString(), redactions: [],
    payload: { run, n: i + 1, digest: s.digest, mime: "image/png", width: 1280, height: 800, bytes: s.bytes, source: "browser",
      pageUrl: `http://127.0.0.1:3000${s.page}`, vision: s.vision } }));
}

// withShots counts them on the implementer's node, as the broker's fold does.
export function withShots(flow: Flow): Flow {
  return { ...flow, nodes: flow.nodes.map((n) => (n.run === run ? { ...n, screenshots: shots.length } : n)) };
}
