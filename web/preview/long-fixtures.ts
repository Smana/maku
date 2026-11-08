// SPDX-License-Identifier: Apache-2.0

// Unbreakable lines, in the shapes a v0.40.0 room broke on: a sed command with two URLs and a
// path, a needs card naming a `gh api` call, and an implementer that asked for the agent-default
// alias and was served another model. full.html?long and test/overflow.test.ts read these.

import type { NeedItem } from "../src/api";
import type { RoomEvent } from "../src/conn";
import type { Flow } from "../src/summary";

export const LONG_PATH = "deploy/base/metrics-stack/alerting/rules/an-alert-rule-file-with-a-rather-long-name.yaml";
const OLD_URL = "https://git.example.com/o/r/blob/main/docs/an-old-runbook-page.md#an-anchor-without-any-break-point";
const NEW_URL = "https://docs.example.com/operations/runbooks/#";
export const LONG_COMMAND = `sed -i 's|https://git.example.com/o/r/blob/main/docs/an-old-runbook-page.md#|${NEW_URL}|g' ${LONG_PATH}`;

export const LONG_NEED: NeedItem = {
  kind: "merge", id: "77", approve: true, pr: 77, url: "https://github.com/o/r/pull/77",
  what: "a maintainer approves PR #77: approve the head you read on the PR page, or run `gh api repos/an-owner/a-repository-name/pulls/77/reviews -f commit_id=<sha> -f event=APPROVE`; the merger merges that head",
  action: { kind: "merge", label: "approve the head on the pull request", url: "https://github.com/o/r/pull/77" },
};

// longEvents rewrites the fixture's first terminal call (and its result) in place, then appends the
// shapes an implementer's run ends with: a progress call, a heredoc commit, a chat naming a path, a
// handoff and a finish, each carrying an unbreakable path or URL.
export function longEvents(events: RoomEvent[]): void {
  const call = events.find((e) => e.type === "tool_call" && (e.payload as { tool?: string })?.tool === "terminal");
  if (!call) return;
  const p = call.payload as { args: { command: string }; callId: string };
  p.args.command = LONG_COMMAND;
  const res = events.find((e) => e.type === "tool_result" && (e.payload as { callId?: string })?.callId === p.callId);
  if (res) (res.payload as { output: string }).output = `${LONG_PATH}:12:    runbook_url: ${OLD_URL}`;
  const last = events[events.length - 1];
  let seq = last.seq;
  const add = (type: RoomEvent["type"], payload: Record<string, unknown>) =>
    events.push({ ...last, id: `long${++seq}`, seq, type, payload } as RoomEvent);
  const tool = (n: number, t: string, args: Record<string, unknown>, output = "") => {
    add("tool_call", { callId: `call_long_${n}`, tool: t, args, risk: "UNKNOWN", decidedBy: null });
    add("tool_result", { callId: `call_long_${n}`, status: "ok", output, truncated: false, bytes: output.length });
  };
  tool(1, "room-broker_room_progress", { kind: "MCPToolAction", data: { text: `Plan: replace every dead runbook_url (${OLD_URL}) in ${LONG_PATH}` } });
  tool(2, "terminal", { kind: "TerminalAction", command: `git add ${LONG_PATH} && git commit -m "$(cat <<'EOF'\nfix(alerts): point runbook_url links at the docs site\n\nEvery runbook_url still referenced\n${OLD_URL}\nEOF\n)"` },
    `diff --git a/${LONG_PATH} b/${LONG_PATH}\n-    runbook_url: ${OLD_URL}`);
  add("message", { kind: "chat", text: `Fixed every runbook_url in \`${LONG_PATH}\`, see ${OLD_URL}`, delivery: "posted" });
  tool(3, "room-broker_room_handoff", { kind: "MCPToolAction", data: { toRole: "reviewer", commit: "0123456789abcdef0123456789abcdef01234567", summary: `Fixed every runbook_url in ${LONG_PATH}` } });
  tool(4, "finish", { kind: "FinishAction", message: `**Change:** every \`runbook_url\` in \`${LONG_PATH}\` now points at \`${NEW_URL}\`.` });
}

// longFlow gives the implementer the alias it asked for, the model it was served, and a long claim.
export function longFlow(f: Flow): Flow {
  return { ...f, nodes: f.nodes.map((n) => n.persona !== "implementer" ? n : {
    ...n, model: "agent-default", models: ["glm-5.3"], microUSD: 230_000,
    reason: "agent_finished", handoff: `Fixed every runbook_url in ${LONG_PATH}`,
  }) };
}
