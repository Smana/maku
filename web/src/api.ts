// SPDX-License-Identifier: Apache-2.0

import type { RoomEvent } from "./conn";

// Where the page goes; an object so tests can replace it (jsdom does not navigate).
export const nav = { go: (url: string) => location.assign(url) };

export class SignInRequired extends Error {
  constructor() { super("signing in again"); }
}

// signIn sends the browser through oauth2-proxy and back to this page.
export function signIn() {
  nav.go(`/oauth2/start?rd=${encodeURIComponent(location.pathname + location.search)}`);
}

// api is fetch for the human API. A 401 means oauth2-proxy could not refresh the
// session: without a new sign-in every later call fails the same way, and the page
// sits dead until a reload.
export async function api(path: string, init: RequestInit = {}, get: typeof fetch = fetch): Promise<Response> {
  const r = await get(path, { credentials: "same-origin", ...init });
  if (r.status === 401) {
    signIn();
    throw new SignInRequired();
  }
  return r;
}

// fetchEvents is one page of a room's log after seq `after`, oldest first.
export async function fetchEvents(id: string, after: number, limit: number, get: typeof fetch = fetch): Promise<RoomEvent[]> {
  const r = await api(`/api/rooms/${encodeURIComponent(id)}/events?after=${after}&limit=${limit}`, {}, get);
  if (!r.ok) throw new Error(`The room's log is unavailable: ${r.status}`);
  const body = (await r.json()) as { events?: RoomEvent[] };
  return body.events ?? [];
}

// NeedItem is one item of the needs route's answer, as needs/v1 serves it (internal/summary's Need):
// the kind in the one order's vocabulary, what it asks and why, when its cause landed, and the one
// action with the label to show and where to do it. source is where the broker derived it from: it is
// for the broker's tests, never rendered. Every string is the broker's or an agent's, so the panel
// writes all of it as text.
export interface NeedItem {
  kind: string; // plan_confirmation | approval | question | merge | review | failed_check | recovery | hold
  id: string;
  room?: string;
  what: string;
  why?: string;
  url?: string;
  issue?: number;
  pr?: number;
  deadline?: string;
  at?: string;
  author?: string;
  approve?: boolean;
  action?: { kind: string; label: string; url?: string };
  source?: { kind: string; seq?: number; reason?: string };
}

// NeedsPage is GET /api/needs' answer (needs/v1): the caller's needs across the rooms they may read,
// in the route's one order, with the cursor to ask from next and the rooms whose state could not be
// read — a list may be short, and it says so rather than looking quiet (CD21, CD28).
export interface NeedsPage {
  apiVersion: string;
  items: NeedItem[];
  cursor?: string;
  access?: string;
  unread?: string[];
}

const needsFailures: Record<number, string> = {
  // Review Focus 3: the route's one 400 is a `since` it would not read, and it names the format.
  400: "The needs list was refused: the cursor is not an RFC 3339 instant.",
  503: "The needs list is unavailable: your access cannot be verified, or the rooms could not be read. Retry.",
};

// fetchNeeds is one room's needs from the human API (GET /api/needs?room=<id>), the same list the CLI
// and the tools read (CD21, CD28). since is the route's cursor: the items whose cause landed after it.
export async function fetchNeeds(room: string, since?: string, get: typeof fetch = fetch): Promise<NeedsPage> {
  const q = new URLSearchParams({ room });
  if (since) q.set("since", since);
  const r = await api(`/api/needs?${q}`, {}, get);
  if (!r.ok) throw new Error(needsFailures[r.status] ?? `The needs list is unavailable: ${r.status}.`);
  const p = (await r.json()) as NeedsPage;
  // A broker that predates needs/v1, or one answering another route: an empty list would read as
  // "nothing needs you", which is the one thing this page must not say by mistake.
  if (p?.apiVersion !== "needs/v1") throw new Error("The needs list is in a format this page does not know. Reload the page.");
  return p;
}
