import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import type { RoomEvent } from "../src/conn";
import type { Summary } from "../src/summary";
import { summaryAt } from "../preview/demo-summary";

const dir = join(process.cwd(), "test/fixtures");
const events: RoomEvent[] = readFileSync(join(dir, "demo-session.jsonl"), "utf8").trim().split("\n").map((l) => JSON.parse(l));
const final: Summary = JSON.parse(readFileSync(join(dir, "demo-summary.json"), "utf8"));
const upTo = (pred: (e: RoomEvent) => boolean) => events.slice(0, events.findIndex(pred) + 1);
const at = (shown: RoomEvent[]) => summaryAt(final, shown, events.length, false);

describe("summaryAt", () => {
  it("gives nothing of the ending away at the start", () => {
    const s = at(events.slice(0, 1));
    expect([s.status.phase, s.status.run, s.status.pr, s.status.lastVerdict, s.needsYou, s.notes.items]).toEqual(["Implementing", null, null, null, [], []]);
  });
  it("names the running run, and the pull request once it is opened", () => {
    const running = at(upTo((e) => e.payload.kind === "run_phase"));
    expect(running.status.run).toEqual({ id: "w4hn8c2r", role: "implementer" });
    expect(running.status.pr).toBeNull();
    const opened = at(upTo((e) => e.type === "tool_result" && String(e.payload.output).includes("/pull/214")));
    expect(opened.status.pr).toEqual(final.status.pr);
  });
  it("moves to Reviewing on the handoff, and to the recorded phase on the verdict", () => {
    const reviewing = at(upTo((e) => e.type === "handoff"));
    expect(reviewing.status.phase).toBe("Reviewing");
    const decided = at(upTo((e) => e.payload.kind === "review_verdict"));
    expect([decided.status.phase, decided.status.lastVerdict?.verdict, decided.needsYou]).toEqual([final.status.phase, "approve", final.needsYou]);
  });
  it("lists the notes shown so far, stamped as shown", () => {
    const shown = upTo((e) => e.payload.kind === "progress").map((e) => ({ ...e, ts: "2030-01-01T00:00:00.000Z" }));
    expect(at(shown).notes.items).toEqual([{ at: "2030-01-01T00:00:00.000Z", run: "w4hn8c2r", text: shown.at(-1)!.payload.text }]);
  });
  it("is the recorded summary once done, with the shown notes", () => {
    const s = summaryAt(final, events, events.length, true);
    expect({ ...s, notes: final.notes, flow: final.flow }).toEqual(final);
    expect(s.notes.items).toHaveLength(final.notes.items.length);
  });

  describe("flow", () => {
    const flowAt = (pred: (e: RoomEvent) => boolean) => at(upTo(pred)).flow!;
    it("plans the implementer and the reviewer, both ghosts, before any run", () => {
      expect(at(events.slice(0, 1)).flow).toEqual({ plan: ["implementer", "reviewer"], nodes: [], ghosts: ["implementer", "reviewer"] });
    });
    it("opens a live node as its run joins, with the role the request named", () => {
      const f = flowAt((e) => e.seq === 6);
      expect(f.nodes).toHaveLength(1);
      expect(f.nodes[0]).toMatchObject({ run: "w4hn8c2r", persona: "implementer", role: "implementer", fromSeq: 6, toSeq: 0, tools: { ok: 0, failed: 0 } });
      expect(f.nodes[0].endedAt).toBeUndefined();
      expect(f.nodes[0].edge).toBeUndefined();
      expect(f.ghosts).toEqual(["reviewer"]);
    });
    it("counts tool results as they are shown", () => {
      const f = flowAt((e) => e.seq === 41);
      const results = events.filter((e) => e.seq <= 41 && e.type === "tool_result");
      expect(f.nodes[0].tools.ok + f.nodes[0].tools.failed).toBe(results.length);
      expect(results.length).toBeGreaterThan(0);
    });
    it("ends the implementer when it leaves and starts the reviewer behind a PR edge", () => {
      const f = flowAt((e) => e.seq === 77);
      expect(f.nodes.map((n) => [n.run, n.fromSeq, n.toSeq, n.outcome])).toEqual([["w4hn8c2r", 6, 75, "succeeded"], ["p9tz3b6d", 77, 0, undefined]]);
      expect(f.nodes[1].edge).toBe("PR #214");
      expect(f.ghosts).toEqual([]);
    });
    it("names each node's model and effort from the events shown", () => {
      const f = flowAt((e) => e.seq === 93);
      expect(f.nodes.map((n) => [n.model, n.models, n.effort])).toEqual([["glm-5.3", ["glm-5.3"], "high"], ["claude-sonnet-4.5", ["claude-sonnet-4.5"], "medium"]]);
      expect(flowAt((e) => e.seq === 8).nodes[0].effort).toBeUndefined();
    });
    it("takes the served models from the recorded runs once done", () => {
      const withRuns = { ...final, runs: [{ id: "w4hn8c2r", role: "implementer", provenance: { servedModels: ["claude-haiku-4.5", "glm-5.3"] } }] };
      const f = summaryAt(withRuns, events, events.length, true).flow!;
      expect(f.nodes[0].models).toEqual(["claude-haiku-4.5", "glm-5.3"]);
      expect(f.nodes[1].models).toEqual(["claude-sonnet-4.5"]);
    });
    it("carries the reviewer's verdict, and no milestone, until the replay is done", () => {
      const f = flowAt((e) => e.seq === 93);
      expect(f.nodes[1]).toMatchObject({ verdict: "approve", toSeq: 93 });
      expect(f.milestones).toBeUndefined();
    });
    it("ends on the milestone the recorded phase implies", () => {
      const s = summaryAt(final, events, events.length, true);
      expect(s.flow!.milestones!.map((m) => m.phase)).toEqual(["AwaitingHuman"]);
      expect(s.flow!.milestones![0].seq).toBe(events.at(-1)!.seq);
      const merged = summaryAt({ ...final, status: { ...final.status, phase: "Done" } }, events, events.length, true);
      expect(merged.flow!.milestones!.map((m) => m.phase)).toEqual(["AwaitingHuman", "Done"]);
    });
  });
});
