// SPDX-License-Identifier: Apache-2.0

// The live demo: the room page showing a recorded run in its end state.
// Every path is relative, so the site serves from a domain root or under GitHub Pages'
// /<repo>/ prefix. No broker, no network beyond the site's own files: every act is inert.

import type { RoomEvent, Snapshot } from "../src/conn";
import type { Summary } from "../src/summary";
import { applyTheme, initTheme } from "../src/theme";
import { summaryAt } from "./demo-summary";
import { mountPreviewPage } from "./page";

const get = async <T>(path: string): Promise<T> => (await fetch(path)).json() as Promise<T>;

// rebased stamps an event with the moment it is shown, so "5 s ago" reads true. Step
// durations then measure playback time, not the recording's; the order is unchanged.
// An approval's expiry moves by the same offset, so it stays in the future.
const rebased = (e: RoomEvent): RoomEvent => {
  const now = Date.now();
  const payload = typeof e.payload?.expiresAt === "string"
    ? { ...e.payload, expiresAt: new Date(Date.parse(e.payload.expiresAt) + now - Date.parse(e.ts)).toISOString() } : e.payload;
  return { ...e, ts: new Date(now).toISOString(), payload };
};

async function run() {
  const [summary, snap, text] = await Promise.all([
    get<Summary>("fixtures/demo-summary.json"),
    get<Snapshot>("fixtures/demo-snapshot.json"),
    fetch("fixtures/demo-session.jsonl").then((r) => r.text()),
  ]);
  const events = text.trim().split("\n").map((l) => JSON.parse(l) as RoomEvent);
  const theme = initTheme();
  const override = new URLSearchParams(location.search).get("theme");
  if (override === "light" || override === "dark") applyTheme(override);

  const shown: RoomEvent[] = [];
  const page = mountPreviewPage(document.getElementById("app")!, { snap, summary: summaryAt(summary, shown, events.length, false),
    // The snapshot stands before the first event, so every event applies onto it.
    count: 0, nothingSent: "Demo: nothing is sent.", sessionOpen: false, sessionLabel: "Session · driver: the factory", assetBase: "assets", homeHref: "./", theme });

  const banner = document.createElement("div");
  banner.className = "banner demo-banner";
  const about = Object.assign(document.createElement("a"), { href: "https://github.com/Smana/maku#readme", textContent: "What is this?" });
  banner.append("Demo: a recorded run. Nothing here reaches a cluster. ", about);
  page.header.after(banner);

  for (const e of events) {
    const r = rebased(e);
    shown.push(r);
    page.apply(r);
  }
  page.setSummary(summaryAt(summary, shown, events.length, true));
  page.settle();
  page.setStatus(`demo · ${events.length} events`);
}

run().catch((e) => { document.body.textContent = `demo failed: ${e instanceof Error ? e.message : e}`; });
