// SPDX-License-Identifier: Apache-2.0

// The offline full-page preview: the room page's regions, mounted as room.ts mounts
// them, fed by the recorded fixtures instead of a socket. `npm run preview`, then open
// /preview/full.html. No broker, no network beyond the static fixtures. Query params:
//   ?theme=dark   the dark palette
//   ?session      the session toggle open
//   ?escalated    the factory's escalated room
//   ?stress       a long Now line and a 64-hex digest (the phone bugs)
//   ?running      the factory's running room: a status line, spend, model and effort, every fact the header shows
// One rail fixture (flow-fixtures.ts), the first of:
//   ?finished     every run ended, a reviewer, the milestones to Done: Replay shows
//   ?split        a scoper whose handoff split into three parts
//   ?many         a long task: retries and reviewers, a rail taller than the window, the unsandboxed banner above the header
//   ?part         a part's room, linked back to its parent
//   (none)        a task in flight: scoper, tester, the live implementer, a ghost reviewer

import type { RoomEvent, Snapshot } from "../src/conn";
import type { Summary } from "../src/summary";
import { applyTheme, initTheme } from "../src/theme";
import { flowFixture, flowName } from "./flow-fixtures";
import { mountPreviewPage } from "./page";

const get = async <T>(path: string): Promise<T> => (await fetch(path)).json() as Promise<T>;

async function run() {
  const [summary, snap, text] = await Promise.all([
    get<Summary>("/test/fixtures/summary.json"),
    get<Snapshot>("/test/fixtures/snapshot.json"),
    fetch("/test/fixtures/live-session.jsonl").then((r) => r.text()),
  ]);
  const events = text.trim().split("\n").map((l) => JSON.parse(l) as RoomEvent);
  const theme = initTheme();
  const override = new URLSearchParams(location.search).get("theme");
  if (override === "light" || override === "dark") applyTheme(override);

  // Example dashboards (human.dashboards), filled with the fixture's room, run and task.
  const dashboards = [
    { label: "Room", url: "https://grafana.example.com/d/agent-room/agent-room?var-room={room}" },
    { label: "Run", url: "https://grafana.example.com/d/agent-run/agent-run?var-run={run}" },
    { label: "Task", url: "https://grafana.example.com/d/agent-factory/agent-factory?var-task={task}" },
  ];
  // ?escalated: the factory's escalated room, as a maintainer meets it: the agent stopped, a
  // /factory retry is what comes next, the factory drives a public room.
  const params = new URLSearchParams(location.search);
  const status0 = params.has("escalated")
    ? { ...summary.status, phase: "Escalated", state: "", run: null, pr: null, budget: { usedTokens: 196_000, limitTokens: 3_000_000 },
      reason: "the agent stopped on an error", next: "a maintainer comments `/factory retry` on the issue to run it again, or pushes to the branch" }
    : summary.status;
  const running = params.has("running");
  const status = running
    ? { ...status0, state: "running", reason: undefined, next: undefined, spend: { usedMicroUSD: 40_000, limitMicroUSD: 5_100_000 },
      runModel: { run: status0.run!.id, model: "agent-default", effort: "high" } }
    : status0;
  if (params.has("escalated") || running) Object.assign(snap, { driver: "system:factory", dataClass: "public" });
  const fx = flowFixture(flowName(params));
  const finished = params.has("finished") || params.has("split");
  const phase = params.has("escalated") ? status.phase : fx.phase;
  // The fixture's events are weeks old: the rail's clock starts at the last one.
  const loaded = Date.now();
  const lastAt = Date.parse(events[events.length - 1].ts);
  const page = mountPreviewPage(document.getElementById("app")!, { snap, unsandboxed: params.has("many"),
    summary: { ...summary, status: { ...status, phase, task: summary.room, ...(finished ? { run: null } : {}) }, flow: fx.flow },
    count: events.length, dashboards, now: () => lastAt + Date.now() - loaded,
    nothingSent: "Offline preview: nothing was sent.", sessionOpen: new URLSearchParams(location.search).has("session"), theme });
  for (const e of events) page.apply(e);
  page.setStatus(`live · ${events.length} events (offline fixture)`);
  page.settle();
  // ?stress: a long Now line and a 64-hex digest in a message, the two phone bugs this fixture shows.
  if (new URLSearchParams(location.search).has("stress")) {
    const now = document.querySelector(".now-link");
    if (now) now.textContent = "Bash: rc=${PIPESTATUS[0]} ; if [ \"$rc\" -ne 0 ]; then echo failed with a long command line that keeps going and going past two lines of a phone strip so the clamp has to cut it, and then a third line keeps going on";
    const md = document.querySelector(".bubble .md");
    if (md) md.append(document.createTextNode(" sha256 401a9043056621bfce8b6261112f74236f83c0d5e7a9b1c2d3e4f5061728394a5b6c7d8"));
  }
}

run().catch((e) => { document.body.textContent = `preview failed: ${e instanceof Error ? e.message : e}`; });
