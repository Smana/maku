// SPDX-License-Identifier: Apache-2.0

// The room page's regions for an offline page, mounted as room.ts mounts them, with no
// broker: every act is inert. The rail and the run view are mounted as room.ts mounts them. The developer preview (full.ts) and the live demo (demo.ts)
// both mount through here, so the demo can never drift from what the preview shows.

import { mountBrand } from "../src/brand";
import type { RoomEvent, Snapshot } from "../src/conn";
import { mountComposer, mountControls } from "../src/controls";
import type { Dashboard } from "../src/dashboards";
import { mountFork } from "../src/fork";
import type { NeedsPage } from "../src/api";
import { mountNeeds } from "../src/needs";
import { mountRailPane } from "../src/railpane";
import { RoomState } from "../src/room-state";
import { mountViews } from "../src/roomview";
import { mountRunView, mountScrollIndex } from "../src/runview";
import { mountStrip } from "../src/strip";
import { renderNotes, type Summary } from "../src/summary";
import { mountThemeButton, type ThemeController } from "../src/theme";

export interface PreviewPage {
  apply(e: RoomEvent): void;
  settle(): void; // refresh needs, controls, composer, strip and notes
  setSummary(s: Summary): void; // the summary the next settle() shows
  say(text: string): void; // the dock's act notice
  setStatus(text: string): void;
  header: HTMLElement;
  footer: HTMLElement;
}

export interface PreviewOptions {
  snap: Snapshot;
  summary: Summary;
  count: number; // the seq the snapshot stands at: events at or below it are already in it
  nothingSent: string; // what an act says, since it goes nowhere
  sessionOpen: boolean;
  sessionLabel?: string; // the session toggle's text
  homeHref?: string; // the brand link target; "/" unless the demo keeps it on its own page
  assetBase?: string; // where the brand marks are served; "/assets" unless the demo serves them relative
  theme: ThemeController; // initialised by the caller, so a ?theme override can follow it
  unsandboxed?: boolean; // the page's "Not sandboxed" banner above the header, as room.ts shows it
  dashboards?: Dashboard[]; // the strip's link templates; none by default
  now?: () => number; // the rail's clock; Date.now unless the fixture's events are not recent
  needs?: () => Promise<NeedsPage>; // the needs route's answer; none: the summary's needs, as before a read
}

export function mountPreviewPage(app: HTMLElement, o: PreviewOptions): PreviewPage {
  const { snap } = o;
  let summary = o.summary;
  const header = document.createElement("header");
  header.className = "page-header";
  mountBrand(header, o.assetBase ?? "/assets", o.homeHref ?? "/");
  const strip = mountStrip(header);
  const needsPanel = document.createElement("section");
  const lead = document.createElement("section");
  lead.className = "summary";
  const blocks = document.createElement("div");
  lead.append(blocks);
  const main = document.createElement("main");
  const section = document.createElement("section");
  section.className = "controls";
  // The session toggle, as room.ts mounts it.
  const session = document.createElement("details");
  session.className = "session";
  session.open = o.sessionOpen;
  session.append(Object.assign(document.createElement("summary"), { textContent: o.sessionLabel ?? "Session · driver: you · a run is on" }), section);
  const forkPanel = document.createElement("section");
  const footer = document.createElement("footer");
  footer.className = "page-dock";
  footer.dataset.status = "live";
  const composerEl = document.createElement("div");
  composerEl.className = "composer";
  const meta = document.createElement("div");
  meta.className = "dock-meta";
  const notice = document.createElement("span");
  notice.className = "notice act-notice";
  const status = document.createElement("span");
  status.className = "status";
  meta.append(notice);
  mountThemeButton(meta, o.theme);
  meta.append(status);
  footer.append(session, composerEl, meta);
  const pane = mountRailPane((run) => { void runView.select(run); }, needsPanel, lead, main, forkPanel);
  const { rail } = pane;
  const sandbox = document.createElement("div");
  sandbox.className = "banner-unsandboxed";
  sandbox.setAttribute("role", "alert");
  sandbox.hidden = !o.unsandboxed;
  sandbox.textContent = "Not sandboxed: run ikely2yk runs without gVisor (ALLOW_UNSANDBOXED=1). Use only on a machine you trust.";
  app.replaceChildren(sandbox, header, pane.body, footer);
  // The sticky rail sits between the header and the dock, as room.ts measures them.
  if (typeof ResizeObserver !== "undefined") {
    const size = () => {
      const banner = sandbox.hidden ? 0 : sandbox.offsetHeight;
      app.style.setProperty("--banner-h", `${banner}px`);
      app.style.setProperty("--header-h", `${banner + header.offsetHeight}px`);
      app.style.setProperty("--dock-h", `${footer.offsetHeight}px`);
    };
    const ro = new ResizeObserver(size);
    ro.observe(header);
    ro.observe(sandbox);
    ro.observe(footer);
    size();
  }
  const now = o.now ?? Date.now;

  const say = (text: string) => { notice.textContent = text; };
  const sender = { send: () => { say(o.nothingSent); return false; } };
  const views = mountViews(main, (seq) => fork.open(seq), () => snap.you.principal);
  // The run view reads the events held here instead of fetching them.
  const log: RoomEvent[] = [];
  const runView = mountRunView(main, {
    load: async (after, limit) => log.filter((e) => e.seq > after).slice(0, limit),
    views: (el) => mountViews(el, (seq) => fork.open(seq), () => snap.you.principal),
    chosen: (run) => rail.chosen(run),
  });
  const scrollIndex = mountScrollIndex(main.querySelector<HTMLElement>(".view-chat")!,
    { nodes: () => summary.flow?.nodes ?? [], mark: (run) => rail.mark(run) });
  const state = new RoomState();
  state.reset(snap, o.count);
  const needs = mountNeeds(needsPanel, sender, state, () => snap.you, views, () => undefined, {
    needs: o.needs,
    hands: { openSession: () => { session.open = true; }, focusCoordination: () => say("Offline preview: no coordination panel.") } });
  const you = { ...snap.you };
  const controls = mountControls(section, sender, state, you, say);
  const composer = mountComposer(composerEl, sender, state, you);
  composer.setLive(true);
  const fork = mountFork(forkPanel, sender, state);

  let applied = 0;
  setInterval(() => rail.tick(now()), 1000);
  return {
    apply(e) {
      log.push(e);
      views.apply(e);
      rail.live(e);
      runView.apply(e);
      scrollIndex.refresh();
      state.apply(e);
      views.counters(`seq ${++applied} · gaps 0 · dups 0`);
    },
    settle() {
      needs.refresh();
      needs.setSummary(summary);
      controls.setSummary(summary);
      controls.refresh();
      composer.refresh();
      renderNotes(blocks, summary);
      strip.update({ roomId: summary.room, snap, phase: state.phase, driver: state.driver, sealedBy: state.closed, sealed: state.sealed,
        summary, needs: needs.count(), now: state.latest(), running: state.running(), ran: state.ran(),
        dashboards: o.dashboards });
      pane.show(summary.flow, summary.status.phase, now(), undefined, summary.queued?.items?.length ?? 0);
      runView.set(summary.flow?.nodes ?? []);
    },
    setSummary(s) { summary = s; },
    say,
    setStatus(text) { status.textContent = text; },
    header,
    footer,
  };
}
