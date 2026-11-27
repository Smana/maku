// SPDX-License-Identifier: Apache-2.0

// The live Room Replay: Maku's room page replaying curated recorded runs at their recorded pace,
// sped up. Every path is relative, so the site serves from a domain root or under GitHub Pages'
// /<repo>/ prefix. No broker, no network beyond the site's own files: every act is inert.

import type { RoomEvent, Snapshot } from "../src/conn";
import type { Summary } from "../src/summary";
import { applyTheme, initTheme } from "../src/theme";
import { DEMO_ROOMS, roomById, type DemoRoom } from "./demo-rooms";
import { summaryAt } from "./demo-summary";
import { mountPreviewPage, type PreviewPage } from "./page";
import { Replay } from "./replay";
import { mountReplayBar } from "./replay-bar";

const APPROVAL_DWELL_MS = 4000;

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
  const app = document.getElementById("app")!;
  const params = new URLSearchParams(location.search);
  const theme = initTheme();
  const override = params.get("theme");
  if (override === "light" || override === "dark") applyTheme(override);
  // Sync with the parent maku.ogenki.io page when embedded in an iframe on the same origin.
  window.addEventListener("message", (ev) => {
    if (ev.origin !== window.location.origin) return;
    const d = ev.data as { type?: string; mode?: string } | null;
    if (d?.type === "maku:theme" && (d.mode === "light" || d.mode === "dark")) applyTheme(d.mode);
  });
  const speed = Number(params.get("speed"));
  const jumpFinal = params.get("view") === "final";

  let activePage: PreviewPage | undefined;
  let activeReplay: Replay | undefined;
  let loadSeq = 0;

  async function mountRoom(room: DemoRoom, skipToEnd: boolean) {
    const seq = ++loadSeq;
    activeReplay?.pause();
    activePage?.dispose();

    const [summary, snap, text] = await Promise.all([
      get<Summary>(room.summary),
      get<Snapshot>(room.snapshot),
      fetch(room.session).then((r) => r.text()),
    ]);
    if (seq !== loadSeq) return;

    const events = text.trim().split("\n").map((l) => JSON.parse(l) as RoomEvent);
    const shown: RoomEvent[] = [];
    const page = mountPreviewPage(app, {
      snap,
      summary: summaryAt(summary, shown, events.length, false),
      // The snapshot stands before the first event, so every event replays onto it.
      count: 0,
      nothingSent: "Room Replay: nothing is sent.",
      sessionOpen: false,
      sessionLabel: "Session · driver: the factory",
      assetBase: "assets",
      homeHref: "./",
      theme,
    });
    activePage = page;

    const banner = document.createElement("div");
    banner.className = "banner demo-banner";
    const lead = document.createElement("div");
    lead.className = "demo-banner-lead";
    const about = Object.assign(document.createElement("a"), {
      href: "https://maku.ogenki.io/",
      textContent: "What is Maku?",
      rel: "noopener noreferrer",
    });
    const pilot = Object.assign(document.createElement("a"), {
      href: "https://maku.ogenki.io/docs/get-started/pilot/",
      textContent: "Tier-1 Pilot",
      rel: "noopener noreferrer",
    });
    lead.append("Room Replay: recorded runs, replayed locally. Nothing reaches a cluster. ", about, " · ", pilot);

    const switcher = document.createElement("div");
    switcher.className = "demo-switcher";
    switcher.setAttribute("role", "group");
    switcher.setAttribute("aria-label", "Replay rooms");
    for (const r of DEMO_ROOMS) {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "demo-tab";
      btn.dataset.room = r.id;
      btn.setAttribute("aria-pressed", String(r.id === room.id));
      const title = Object.assign(document.createElement("span"), { className: "demo-tab-title", textContent: r.label });
      const meta = Object.assign(document.createElement("span"), { className: "demo-tab-meta", textContent: `${r.badge} · ${r.cost}` });
      btn.append(title, meta);
      btn.addEventListener("click", () => {
        if (r.id === room.id) return;
        const next = new URL(location.href);
        next.searchParams.set("room", r.id);
        history.replaceState(null, "", next);
        void mountRoom(r, false);
      });
      switcher.append(btn);
    }
    banner.append(lead, switcher);
    page.header.after(banner);

    const replay = new Replay(events, (e) => {
      const r = rebased(e);
      shown.push(r);
      page.apply(r);
      page.setSummary(summaryAt(summary, shown, events.length, shown.length === events.length));
      page.settle();
    }, {
      // The approval is the demo's point: hold its card on screen, whatever the speed.
      dwell: (e) => (e.type === "approval_requested" ? APPROVAL_DWELL_MS : 0),
      onChange: () => {
        bar.refresh();
        page.setStatus(`replay · ${replay.applied} / ${events.length} events`);
      },
    });
    activeReplay = replay;
    // The bar is the dock's first row, so it stays on screen while the run scrolls.
    const slot = document.createElement("div");
    const bar = mountReplayBar(slot, replay, events.length);
    page.footer.prepend(...slot.childNodes);
    page.setStatus(`replay · 0 / ${events.length} events`);
    page.settle();
    // ?speed=N sets a speed the bar does not offer: the README screencast records at 2.
    if (speed > 0) replay.setSpeed(speed);
    if (skipToEnd) replay.skip();
    else replay.play();
  }

  await mountRoom(roomById(params.get("room")), jumpFinal);
}

run().catch((e) => { document.body.textContent = `demo failed: ${e instanceof Error ? e.message : e}`; });
