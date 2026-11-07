// SPDX-License-Identifier: Apache-2.0

// The room page's summary parts, rendered from the broker's summary/v1 JSON
// (GET /api/rooms/{id}/summary): the merges and holds the needs-you panel lists
// (needs.ts) and the agents' notes; the status heads the page in the title strip (strip.ts). Every string here may be agent-written, so all of
// it goes in as text. A field the broker omits when empty (omitempty, omitzero) is
// optional here, and its part is skipped.

import { api } from "./api";
import type { RoomEvent } from "./conn";
import { approvalAnchor, el } from "./controls";

export interface RunModel { run: string; model?: string; effort?: string }

export interface FlowNode {
  run: string; persona: string; role: string; round: number;
  startedAt?: string; endedAt?: string; fromSeq: number; toSeq: number; outcome?: string;
  verdict?: string; microUSD?: number; model?: string; models?: string[]; effort?: string; judge?: { count: number; last: string };
  tools: { ok: number; failed: number }; edge?: string; startedBy?: string; reason?: string; handoff?: string;
}
export interface Milestone { phase: string; at: string; seq: number }
export interface SplitPart { index: number; issue: number; room: string; outcome?: string }
// The task's path as the rail draws it (internal/summary Flow). Role is the log's text and may be
// any string: key nothing on it without a fallback.
export interface Flow {
  untrusted?: boolean; plan?: string[]; nodes: FlowNode[]; ghosts?: string[]; milestones?: Milestone[];
  split?: { of: number; parts?: SplitPart[] }; parent?: { room: string; issue: number; index: number; of: number };
  coordination?: CoordinationFlow;
}

// A coordination parent's family (internal/summary CoordinationFlow), filtered for this viewer by
// the broker: a withheld child is only restrictedChildren. Drawn apart from split/parent.
export interface CoordinationNode {
  id: string; kind: "child" | "integration"; childID?: string; repository?: string; phase?: string; reason?: string;
  roles: string[]; room?: string; issueURL?: string; prURL?: string; disabled: boolean; requiredConfirmation?: "parent" | "child";
}
export interface CoordinationEdge { from: string; to: string; kind: "artifact" | "integration"; name?: string; state: string }
export interface CoordinationFlow { planVersion: number; planHash: string; nodes: CoordinationNode[]; edges: CoordinationEdge[]; restrictedChildren: number }

export interface Summary {
  apiVersion: "summary/v1";
  room: string;
  url: string;
  status: {
    phase: string; // a task phase, or Closed/Sealed
    state?: string; // the coarse state (queued, running, waiting_for_human, done, failed, stopped)
    task?: string; // the room's Task, named as the room; absent without one
    run: { id: string; role: string; trigger?: string; startedAt?: string; endedAt?: string } | null;
    budget: { usedTokens: number; limitTokens: number } | null;
    // spend is the task's dollars, beside budget's tokens, and spendFinal is CD23: the factory's claim
    // that the figure is the task's last. Both absent while the dollars may still move, and for a task
    // whose runs the meter could not price — spendWords reads them, and no surface prints the figure
    // without it.
    spend?: { usedMicroUSD: number; limitMicroUSD?: number };
    spendFinal?: boolean;
    pr: { number: number; url: string; author?: string; reviewers?: string[] } | null;
    issue: { number: number; url: string; title?: string; author?: string; labelledBy?: string } | null;
    lastVerdict: { by: string; verdict: string; at: string } | null;
    reason?: string; // why the task waits (#59)
    reasonCode?: string; // the factory's raw reason code, the status line's key
    next?: string; // what happens or who acts next
    // What a run runs on: model is its claim's spec.model, effort what its harness reported. The
    // present run's, and the last run's (the one before the present run, or the last when none is
    // present). Absent until the log says.
    runModel?: RunModel;
    lastRunModel?: RunModel;
    // The merge policy that governs the task, the factory's config (policy.ts renders it): how is what
    // it takes to merge, decision the gate's word on the task. Absent until the factory posts one.
    policy?: {
      class?: string; mode: "live" | "shadow" | "unarmed"; gateOff?: boolean; approveToMerge?: boolean; adopted?: boolean;
      conditions?: {
        allowedFiles?: string[]; protectedFiles?: string[]; modifyOnly?: boolean; maxPatchFiles?: number;
        maxModifiedLines?: number; diffShape?: string; holdoutChecks?: string[]; maxPerDay?: number;
        adoptLogins?: string[]; requiredLabel?: string; oracle?: boolean;
      };
      classes?: { name: string; mode: string; adopted?: boolean }[];
      mergedHow?: "class" | "approved"; truncated?: boolean; how: string; decision?: string;
    };
  };
  // A hold (N3): a factory task waits on the viewer's hold on this room; id is its wait reason.
  runs?: { id: string; role: string; provenance?: { servedModels?: string[] } }[]; // the task's run history, oldest first
  // needs/v1's eight kinds, in the order the broker lists them (CD21). The panel's list is the needs
  // route's (needs.ts, /api/needs); these entries stand in until that read answers, and stay if it
  // fails, so the parts rendered here are the ones a summary has always carried.
  // question: a task the scoper bounced waits on its issue's author (scoper SC8); id is the issue
  // number, url the issue, author its author's GitHub login. approve: a merge need a maintainer's
  // approval settles (approveToMerge).
  needsYou: { kind: "plan_confirmation" | "approval" | "question" | "merge" | "review" | "failed_check" | "recovery" | "hold";
    id: string; what: string; deadline?: string; url: string; author?: string; approve?: boolean }[];
  // The one thing to do for the room and where: "pr" and "answer" are on GitHub, "approve" on this page.
  // approve: the pr step is an approval of the current commit, not a merge by hand.
  step?: { kind: "approve" | "pr" | "answer"; label: string; url?: string; approve?: boolean };
  actions: { kind: string; what: string; cli?: string }[];
  notes: { untrusted: true; items: { at: string; run: string; text: string }[] };
  flow?: Flow;
  cursor: string;
}

const failures: Record<number, string> = {
  404: "No such room, or you cannot read it.",
  503: "The summary is unavailable: your access cannot be verified, or the room log is unreadable. Retry.",
};

export async function fetchSummary(id: string, get: typeof fetch = fetch): Promise<Summary> {
  const r = await api(`/api/rooms/${encodeURIComponent(id)}/summary`, {}, get);
  if (!r.ok) throw new Error(failures[r.status] ?? `The summary is unavailable: ${r.status}`);
  const s = (await r.json()) as Summary;
  if (s?.apiVersion !== "summary/v1") throw new Error("The summary is in a format this page does not know. Reload the page.");
  return s;
}

// changesSummary is whether the broker's fold (internal/summary) reads e: only then is the
// summary refetched, never on a run's chat or tool calls.
export function changesSummary(e: RoomEvent): boolean {
  const kind = e.payload?.kind;
  switch (e.type) {
    case "state_changed": return kind === "task" || kind === "room_phase" || kind === "limit" || kind === "run_phase";
    case "message": return kind === "progress" || kind === "review_verdict" || kind === "judge_verdict";
    case "handoff": case "participant": return true;
    case "approval_requested": case "approval_decided": return true;
  }
  return false;
}

// Agent-written URLs become a link only when https; anything else stays text.
export function safeLink(text: string, url: string): Node {
  try {
    if (new URL(url).protocol === "https:") return el("a", { href: url, textContent: text, rel: "noopener noreferrer", target: "_blank" });
  } catch { /* not a URL: text */ }
  return document.createTextNode(`${text} (${url})`);
}

export function when(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

// ago reads a timestamp relative to now: "12 s ago", "4 min ago", "2 h ago".
export function ago(iso: string, now: number): string {
  const t = Date.parse(iso);
  if (isNaN(t)) return "";
  const s = Math.max(0, Math.floor((now - t) / 1000));
  if (s < 60) return `${s} s ago`;
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  return `${Math.floor(s / 3600)} h ago`;
}

// focusApproval reveals the approval card for id in the needs-you panel and focuses its
// Approve button, or its first control. False when there is no card (it is decided, or
// its request has not arrived yet).
export function focusApproval(id: string): boolean {
  const card = document.getElementById(approvalAnchor(id));
  if (!card) return false;
  card.scrollIntoView?.({ block: "center" });
  (card.querySelector<HTMLElement>('button[data-act="approve"]') ?? card.querySelector<HTMLElement>("button, input"))?.focus();
  return true;
}

// spendWords is what the room says about its dollars (CD23): the figure while the task runs and once
// the factory has claimed it final, "spend settling" while a finished task's figure may still move,
// and "spend unpriced" when the meter priced none of its runs — the case where no final figure is ever
// posted, so "settling" there would promise a change that never comes. A sealed room is the exception:
// its log takes no event, so the figure in it is the last the page will see. Empty: nothing to say.
export function spendWords(st: Summary["status"]): string {
  const spent = st.spend?.usedMicroUSD ?? 0;
  if (spent <= 0) {
    // A finished task that spent tokens it has no dollars for: the meter priced none of its runs.
    if (over(st) && !sealedRoom(st) && (st.budget?.usedTokens ?? 0) > 0) return "spend unpriced";
    return "";
  }
  if (over(st) && !sealedRoom(st) && !st.spendFinal) return "spend settling";
  const limit = st.spend?.limitMicroUSD ?? 0;
  const usd = (micro: number) => `$${(micro / 1e6).toFixed(2)}`;
  return limit > 0 ? `${usd(spent)} of ${usd(limit)} (${Math.round((spent / limit) * 100)} %)` : usd(spent);
}

// The phases of a room whose task ended, and of one whose log is closed or sealed: the coarse state is
// the caller's to set and may be absent, so the phase decides when it is not there.
const OVER_PHASES = new Set(["Done", "NoOp", "Rejected", "Reverted", "Stopped", "Closed"]);
const SEALED_PHASES = new Set(["Closed", "Sealed"]);

const over = (st: Summary["status"]) =>
  st.state ? ["done", "failed", "stopped"].includes(st.state) : OVER_PHASES.has(st.phase);
const sealedRoom = (st: Summary["status"]) => SEALED_PHASES.has(st.phase);

// inlineCode sets text with its backticked spans as <code>, every part as text: a need's what
// names a command (`gh api …`) that must not show its backticks.
export function inlineCode(text: string): Node[] {
  return text.split(/`([^`]+)`/).flatMap<Node>((part, n) =>
    n % 2 ? [el("code", { textContent: part })] : part ? [document.createTextNode(part)] : []);
}

// linkedNeed is a need decided on GitHub: its label, its instruction with the backticked commands
// as code, and the link out (https only).
function linkedNeed(label: string, what: string, link: Node): HTMLLIElement {
  const li = el("li");
  li.append(`${label}: `, ...inlineCode(what), " · ", link);
  return li;
}

// needsItems is the summary's merges, questions and holds, one list item each; the approvals come
// from the live log instead (needs.ts), so the summary's approval entries are skipped. It is the
// panel's list until the needs route answers (and after a read that failed), which is the list every
// surface reads (CD21).
export function needsItems(s: Summary): HTMLLIElement[] {
  const out: HTMLLIElement[] = [];
  for (const n of s.needsYou) {
    if (n.kind === "hold") out.push(el("li", { textContent: `Hold: ${n.what}` })); // the page is the room: nothing to link to
    // approve: under approveToMerge an approval of the current commit merges it (#308, #310).
    if (n.kind === "merge") out.push(linkedNeed(n.approve ? "Approve" : "Merge", n.what, safeLink("open the PR", n.url)));
    if (n.kind === "question") out.push(linkedNeed("Answer", n.what, safeLink("open the issue", n.url)));
  }
  return out;
}

// renderNotes renders the agents' notes (oldest first in the summary): the latest on one
// line, labelled as the agent's claim, and the earlier ones, newest first, behind a
// "Notes (N)" toggle. No notes, no block.
export function renderNotes(root: HTMLElement, s: Summary, now = Date.now()): void {
  const items = s.notes.items;
  if (!items.length) {
    root.replaceChildren();
    return;
  }
  const last = items[items.length - 1];
  const box = el("div", { className: "notes" });
  box.dataset.block = "notes";
  const latest = el("p", { className: "note-latest" });
  latest.append(el("span", { textContent: [last.run, ago(last.at, now), last.text].filter(Boolean).join(" · ") }),
    el("span", { className: "hint", textContent: "the agent's claim" }));
  box.append(latest);
  const rest = items.slice(0, -1).reverse();
  if (rest.length) {
    const ul = el("ul");
    for (const i of rest) ul.append(el("li", { textContent: `${when(i.at)} · ${i.run}: ${i.text}` }));
    const d = el("details");
    d.append(el("summary", { textContent: `Notes (${rest.length})` }), ul);
    box.append(d);
  }
  root.replaceChildren(box);
}
