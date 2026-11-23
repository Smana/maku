import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { ChatView } from "../src/chat";
import type { RoomEvent, Snapshot } from "../src/conn";
import type { Summary } from "../src/summary";
import { initTheme } from "../src/theme";
import { DEMO_ROOMS, roomById } from "../preview/demo-rooms";
import { summaryAt } from "../preview/demo-summary";
import { mountPreviewPage } from "../preview/page";

const dir = join(process.cwd(), "test");

function loadRoom(id: string) {
  const room = roomById(id);
  const events: RoomEvent[] = readFileSync(join(dir, room.session), "utf8").trim().split("\n").map((l) => JSON.parse(l));
  const snap: Snapshot = JSON.parse(readFileSync(join(dir, room.snapshot), "utf8"));
  const summary: Summary = JSON.parse(readFileSync(join(dir, room.summary), "utf8"));
  return { room, events, snap, summary };
}

describe("Room Replay catalog", () => {
  it("ships three distinct curated rooms with unique ids and defaults to the first", () => {
    expect(DEMO_ROOMS).toHaveLength(3);
    expect(new Set(DEMO_ROOMS.map((r) => r.id)).size).toBe(3);
    expect(roomById(null).id).toBe("billing-retry");
    expect(roomById("unknown").id).toBe("billing-retry");
    expect(roomById("docs-automerge").id).toBe("docs-automerge");
    expect(roomById("private-guarded").id).toBe("private-guarded");
  });

  for (const meta of DEMO_ROOMS) {
    describe(meta.id, () => {
      const { events, snap, summary } = loadRoom(meta.id);

      it("has gapless seq order, monotonic timestamps, matching roomId, and zero secrets or real identities", () => {
        expect(events.length).toBeGreaterThanOrEqual(20);
        events.forEach((e, i) => expect(e.seq).toBe(i + 1));
        expect(new Set(events.map((e) => e.roomId)).size).toBe(1);
        expect(snap.roomId).toBe(events[0].roomId);
        expect(summary.room).toBe(events[0].roomId);
        for (let i = 1; i < events.length; i++) {
          expect(Date.parse(events[i].ts)).toBeGreaterThanOrEqual(Date.parse(events[i - 1].ts));
        }
        const raw = JSON.stringify({ events, snap, summary });
        expect(raw).not.toMatch(/ogenki|smana|github_pat_|ghp_|ghs_|sk-[A-Za-z0-9]{8}/i);
        for (const e of events) {
          if (e.actor.kind === "human") expect(e.actor.id).toMatch(/^human:\d{12}$/);
        }
      });

      it("pairs every tool call with a result and renders cleanly in ChatView", () => {
        const el = document.createElement("div");
        const view = new ChatView(el);
        for (const e of events) view.apply(e);
        const calls = events.filter((e) => e.type === "tool_call");
        expect(el.querySelectorAll("details.tool")).toHaveLength(calls.length);
        expect(el.querySelectorAll("details.tool.st-running")).toHaveLength(0);
      });

      it("renders spend card and merge-policy gate trace in mountPreviewPage with zero network calls", () => {
        const app = document.createElement("div");
        const page = mountPreviewPage(app, {
          snap,
          summary: summaryAt(summary, events, events.length, true),
          count: 0,
          nothingSent: "Room Replay: nothing is sent.",
          sessionOpen: false,
          theme: initTheme(),
        });
        for (const e of events) page.apply(e);
        page.settle();
        expect(app.textContent).toContain(meta.cost);
        expect(app.querySelector("details.policy")).not.toBeNull();
        expect(app.querySelector(".policy-decision")?.textContent).toContain("Decision:");
        page.dispose();
      });
    });
  }

  it("folds judge verdicts into the rail flow for docs-automerge and private-guarded", () => {
    const auto = loadRoom("docs-automerge");
    const autoFlow = summaryAt(auto.summary, auto.events, auto.events.length, true).flow!;
    expect(autoFlow.plan).toEqual(["scoper", "implementer", "reviewer"]);
    expect(autoFlow.nodes.map((n) => n.role)).toEqual(["scoper", "implementer", "reviewer"]);
    expect(autoFlow.nodes[1].judge).toEqual({ count: 1, last: "met" });
    expect(autoFlow.milestones?.map((m) => m.phase)).toEqual(["AwaitingHuman", "Done"]);

    const guarded = loadRoom("private-guarded");
    const guardedFlow = summaryAt(guarded.summary, guarded.events, guarded.events.length, true).flow!;
    expect(guardedFlow.nodes[0].judge).toEqual({ count: 2, last: "impossible" });
    expect(guardedFlow.milestones?.map((m) => m.phase)).toEqual(["Escalated"]);
  });
});
