// SPDX-License-Identifier: Apache-2.0

// Curated static Room Replay catalog (v5 P0-P3): three sanitized, realistic rooms
// rendered by Maku's room UI with zero cluster or network beyond the static site's own files.

export interface DemoRoom {
  id: string;
  label: string;
  badge: string;
  cost: string;
  summary: string;
  snapshot: string;
  session: string;
}

export const DEMO_ROOMS: readonly DemoRoom[] = [
  {
    id: "billing-retry",
    label: "Steered fix + push approval",
    badge: "Shadow gate · AwaitingHuman",
    cost: "$0.24",
    summary: "fixtures/demo-summary.json",
    snapshot: "fixtures/demo-snapshot.json",
    session: "fixtures/demo-session.jsonl",
  },
  {
    id: "docs-automerge",
    label: "Scoper → Judge → Auto-merge",
    badge: "Live gate · Done",
    cost: "$0.11",
    summary: "fixtures/demo-automerge-summary.json",
    snapshot: "fixtures/demo-automerge-snapshot.json",
    session: "fixtures/demo-automerge-session.jsonl",
  },
  {
    id: "private-guarded",
    label: "Private repo · Policy refusal",
    badge: "Internal class · Escalated",
    cost: "$0.19",
    summary: "fixtures/demo-guarded-summary.json",
    snapshot: "fixtures/demo-guarded-snapshot.json",
    session: "fixtures/demo-guarded-session.jsonl",
  },
] as const;

export function roomById(id: string | null): DemoRoom {
  return DEMO_ROOMS.find((r) => r.id === id) ?? DEMO_ROOMS[0];
}
