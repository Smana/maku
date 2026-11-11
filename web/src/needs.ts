// SPDX-License-Identifier: Apache-2.0

// The needs-you panel sits in the room's lead, under the title strip: one card per
// pending approval, command first, decided right there, then the list the needs route answers for
// this room — the same list the CLI and the tools read (CD21) — with the summary's merges, answers
// and holds standing in until the first read lands. This page is still the only place every approval
// class is decided; a `forge.push` approval is also decidable from its Slack card, under the same
// fence. A card leaves when the log decides it (approval_decided), whoever decided, so a decide that
// lost the race (already_decided) leaves no dead button. Every string here may be agent-written: all
// of it enters the DOM as text.

import type { NeedItem, NeedsPage } from "./api";
import { decideStatus, deadlineText, expiredAt, expiredText, ruleFor } from "./approval-help";
import { focusRow } from "./chat";
import type { Snapshot } from "./conn";
import { act, approvalAnchor, approvalCard, button, el, keyed, named, type Sender } from "./controls";
import type { RoomState } from "./room-state";
import { inlineCode, needsItems, type Summary } from "./summary";

// routeEvery is how often the panel asks the route. refresh runs on every frame the page draws — the
// ticker's one a second, and each event a replay delivers — and one read is a scan of every room the
// caller may read, so a burst of frames costs one read, not one each.
const routeEvery = 5000;

// NeedHands are the page's hands a need may take the reader to: the session's driver group or its
// close button, and the coordination panel. The preview has none: a need then names where to go.
export interface NeedHands {
  openSession?(to: "driver" | "close"): void;
  focusCoordination?(): void;
}

// What each kind asks, in a heading's words; the item's own sentence follows it.
const NEED_TITLE: Record<string, string> = {
  plan_confirmation: "Confirm the proposed plan",
  question: "Answer the scoper's question",
  merge: "Merge the pull request",
  review: "Review the pull request",
  failed_check: "A check needs a re-run",
  recovery: "Retry the task, or close the room",
  hold: "The task is on hold",
};

const isHttps = (url?: string) => { try { return !!url && new URL(url).protocol === "https:"; } catch { return false; } };
const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);
const primary = (b: HTMLButtonElement) => { b.classList.add("primary"); return b; };

// outLink is the action done elsewhere (GitHub): a button-shaped link, marked as opening a new tab.
// Not https: the label and the URL as text, as every link on this page.
function outLink(label: string, url: string): Node {
  if (!isHttps(url)) return document.createTextNode(`${cap(label)} (${url})`);
  const a = el("a", { className: "need-go", href: url, rel: "noopener noreferrer", target: "_blank", textContent: `${cap(label)} ↗` });
  a.setAttribute("aria-label", `${cap(label)} (opens in a new tab)`);
  return a;
}

// needCard is one item the route lists, as a card that always ends in its one action: the control
// on this page when the room has one (hand the driver back, close the room, the coordination panel),
// else a link to where it is done, with a line saying where it happens. The route
// ordered the items (CD21): this list never reorders them.
// Where the action happens, one line per kind (D3); a merge that wants an approval says whose.
const GITHUB_LINE: Record<string, string> = {
  merge: "Merging happens on GitHub.",
  review: "Reviewing happens on GitHub.",
  question: "Answer it on GitHub.",
  failed_check: "Fix it on GitHub.",
  plan_confirmation: "Confirm it on GitHub.",
};
const githubLine = (n: NeedItem) => n.kind === "merge" && n.approve
  ? "Approve the pull request on GitHub: the merge gate reads the approval there."
  : GITHUB_LINE[n.kind] ?? "This happens on GitHub.";

function needCard(n: NeedItem, ctx: { hands: NeedHands; release?: () => void }): HTMLLIElement {
  const li = el("li", { className: "need" });
  li.dataset.kind = n.kind;
  const title = n.kind === "merge" && n.approve ? "Approve the pull request" : NEED_TITLE[n.kind] ?? cap(n.kind.replace(/_/g, " "));
  const what = el("p", { className: "need-what" });
  what.append(...inlineCode(n.what));
  li.append(el("p", { className: "need-title", textContent: title }), what);
  if (n.why) li.append(el("p", { className: "need-why", textContent: `Why: ${n.why}` }));
  const bar = el("div", { className: "need-act" });
  const where = (text: string) => el("span", { className: "need-where", textContent: text });
  const a = n.action;
  const label = a?.label || n.what;
  if (a?.kind === "release") {
    // A hold the driver releases: one press here, when the viewer drives; else the Driver group.
    if (/wait for the other run/.test(label)) bar.append(where("Nothing to do here: the task starts when the other run in the room ends."));
    else if (ctx.release) bar.append(primary(button("Hand the driver back to the factory", "need-release", ctx.release)));
    else if (ctx.hands.openSession) bar.append(button("Open Session › Driver", "need-driver", () => ctx.hands.openSession!("driver")), where(cap(label) + ", from the Driver group."));
    else bar.append(where(cap(label) + ", under Session › Driver."));
  } else if (a?.kind === "decide" && a.url?.endsWith("#coordination")) {
    const go = cap(label.replace(/ in the parent room$/, ""));
    bar.append(ctx.hands.focusCoordination ? primary(button(go, "need-coordination", () => ctx.hands.focusCoordination!())) : where(`${go}, in the coordination panel.`));
  } else if (n.kind === "recovery") {
    // A retry is a comment on the issue; closing is the room's.
    if (isHttps(n.url) && n.url !== a?.url) bar.append(outLink(n.issue ? `retry: comment /factory retry on issue #${n.issue}` : "retry on the issue", n.url!));
    bar.append(ctx.hands.openSession ? button("Close the room…", "need-close", () => ctx.hands.openSession!("close")) : where("Close the room under Session › Room."));
  } else {
    const url = a?.url || n.url;
    bar.append(url ? outLink(label, url) : where(cap(label)));
    if (url && isHttps(url)) bar.append(where(githubLine(n)));
  }
  li.append(bar);
  return li;
}

// unreadLine is the one line for the rooms whose state could not be read (Review Focus 1). They
// contribute no item, and the route names only rooms the caller may read: a refusal never reveals
// another room's existence.
function unreadLine(rooms: string[]): string {
  return rooms.length === 1
    ? `Room ${rooms[0]}: its state could not be read, so it lists nothing here.`
    : `Rooms ${rooms.join(", ")}: their state could not be read, so they list nothing here.`;
}

// mountNeeds renders the panel into root. you reads the viewer as the latest state frame
// has it; chat finds a call's row for the card's link; opts.needs reads the route's list for this
// room, and no reader leaves the summary's items as the list (the preview, which has no broker).
export function mountNeeds(root: HTMLElement, conn: Sender, state: RoomState, you: () => Snapshot["you"] | undefined,
  chat: { rowFor(callId: string): HTMLElement | undefined; reveal?(): void }, fourEyes: () => boolean | undefined = () => undefined,
  opts: { needs?: () => Promise<NeedsPage>; hands?: NeedHands } = {}):
  { refresh(): Promise<void>; setSummary(s: Summary): void; count(): number; approvalCounts(): { total: number; mine: number }; focusFirst(): void } {
  root.classList.add("needs");
  root.setAttribute("aria-labelledby", "needs-title");
  const approvals = el("ul", { className: "approvals" });
  const other = el("ul", { className: "needs-other" });
  const title = el("h2", { id: "needs-title", textContent: "Needs you" });
  root.replaceChildren(title, approvals, other);
  root.hidden = true; // until something needs showing: never an empty heading
  const rows = keyed(approvals);
  // Approvers and owners decide (policy Decide); a sealed room answers every act sealed;
  // with four-eyes, whoever prompted the turn waits for another. The card says which, and
  // the broker still checks four-eyes and who decided first.
  type Approval = ReturnType<RoomState["approvals"]>[number];
  const standing = (a: Approval, withDeadline = true) =>
    decideStatus({ you: you(), sealed: state.sealed, fourEyes: fourEyes(), prompters: a.prompters, expiresAt: withDeadline ? a.expiresAt : undefined });
  // An approval past its deadline is closed by the broker within ~30 s (approval_decided{expired});
  // by the browser's clock the card stops offering a decision at the deadline, in place.
  const expire = (li: HTMLElement, hhmm: string) => {
    li.dataset.state = "expired";
    const status = li.querySelector<HTMLElement>(".approval-status");
    if (status) { status.textContent = expiredText(hhmm); status.dataset.can = "false"; }
    const dl = li.querySelector<HTMLElement>(".approval-deadline");
    if (dl) dl.textContent = "Expired";
    const bar = li.querySelector(".approval-decide");
    // The focus would fall to the body with the button it sat on: it stays on the card.
    const held = !!bar?.contains(document.activeElement);
    bar?.remove();
    if (held) { li.tabIndex = -1; li.focus({ preventScroll: true }); }
  };
  let others = 0;
  // The list under the cards: the route's items once a read answered (listed), the summary's until
  // then (summarised), and two lines — why a read failed, and the rooms the route could not read.
  let unread: string[] = [];
  let stale = "";
  let reading: Promise<void> | undefined; // one read at a time
  let readAt = 0; // when the last read started, for routeEvery
  // A hold's one press: the driver hands the token back to the factory, as Session › Driver's give does.
  const ctx = () => ({ hands: opts.hands ?? {},
    release: you()?.driver ? () => { act(conn, state, { kind: "driver_give", to: "system:factory" }); } : undefined });
  let listedItems: NeedItem[] | undefined;
  let summarisedItems: NeedItem[] = [];
  const cards = new Map<string, HTMLLIElement>();

  const card = (a: Approval, st: { can: boolean; text: string }) => {
    const li = approvalCard(a, "li");
    li.id = approvalAnchor(a.approvalId);
    const command = li.querySelector<HTMLElement>(".approval-command")!;
    command.id = `${li.id}-command`;
    // What it wants, why it stopped, what each answer does, by when, who may: fixed prose
    // per class. The agent's own command and raw action sit folded under "details".
    const rule = ruleFor(a.class);
    const intent = el("p", { className: "approval-intent", id: `${li.id}-intent`, textContent: rule.intent });
    const why = el("p", { className: "approval-why", textContent: `Why it stopped: ${rule.why}` });
    const outcomes = el("div", { className: "approval-outcomes" });
    outcomes.append(el("p", { textContent: `If you approve: ${rule.approve}` }), el("p", { textContent: `If you deny: ${rule.deny}` }));
    const deadline = el("p", { className: "approval-deadline", textContent: deadlineText(a.expiresAt) });
    deadline.dataset.expires = a.expiresAt;
    const late = expiredAt(a.expiresAt);
    const status = el("p", { className: "approval-status", textContent: st.text });
    status.dataset.can = String(st.can);
    const details = el("details", { className: "approval-details" });
    // What each answer does folds too: the card leads with what, why, by when and who may.
    details.append(el("summary", { textContent: "details" }), outcomes, ...Array.from(li.children));
    li.replaceChildren(intent, why, deadline, status, details);
    // A same-page link to the call's row, shown while the chat holds that row; refresh
    // names it by the row's seq, which also tells two cards' links apart.
    const link = el("a", { className: "approval-call", href: "#" });
    link.dataset.callId = a.callId;
    link.onclick = (e) => {
      e.preventDefault();
      chat.reveal?.();
      const row = chat.rowFor(a.callId);
      if (row) focusRow(row); // the row, and any fold around it, opened and focused
    };
    details.before(link);
    if (late) { expire(li, late); return li; }
    if (!st.can) return li;
    const reason = named(el("input", { name: "decisionReason", maxLength: 1024, placeholder: "reason (optional)" }), "reason for the decision (optional)");
    const send = (decision: string) => () => {
      const action: Record<string, unknown> = { kind: "decide", approvalId: a.approvalId, decision };
      if (reason.value.trim()) action.reason = reason.value.trim();
      act(conn, state, action);
    };
    const approve = button("approve", "approve", send("approved"));
    approve.classList.add("primary");
    const deny = button("deny", "deny", send("denied"));
    deny.classList.add("deny");
    // Each button names what it decides: "approve" alone repeats on every card.
    for (const e of [reason, approve, deny]) e.setAttribute("aria-describedby", command.id);
    const bar = el("div", { className: "approval-decide" });
    bar.append(reason, approve, deny);
    details.before(bar);
    return li;
  };

  // render draws what the panel holds now: the cards, then the list and its two lines. It never reads
  // the route: a frame that draws cannot wait, and a replay draws hundreds of them.
  const render = () => {
    const pending = state.approvals();
    // The signature leaves the deadline out: a card turns expired in place, not rebuilt.
    rows(pending.map((a) => ({ key: a.approvalId, sig: standing(a, false).text, build: () => card(a, standing(a)) })));
    // The deadline counts down without a rebuild, which would drop a reason being typed.
    for (const d of approvals.querySelectorAll<HTMLElement>("p.approval-deadline")) {
      const li = d.closest<HTMLElement>("li.approval");
      const late = expiredAt(d.dataset.expires);
      if (late && li) { if (li.dataset.state !== "expired") expire(li, late); continue; }
      const text = deadlineText(d.dataset.expires ?? "");
      if (d.textContent !== text) d.textContent = text;
    }
    // A call's row comes and goes (it leaves at the chat's cap): the link follows it
    // without a rebuild, which would drop a reason being typed.
    for (const l of approvals.querySelectorAll<HTMLAnchorElement>("a.approval-call")) {
      const row = chat.rowFor(l.dataset.callId ?? "");
      l.hidden = !row;
      const text = row ? `see the call #${row.dataset.seq ?? "?"}` : "see the call";
      if (l.textContent !== text) l.textContent = text;
    }
    // The list the route answered, the summary's until then; and under it, one line each for a read
    // that failed and for a room the route could not read. A failure is itself something to show: the
    // line is the panel's, and it is never suppressed, so an unreadable list cannot pass for a quiet
    // one. The heading stays the count's — with nothing to count it reads "Waiting for an approver",
    // never "Needs you" over a single warning.
    // A card is rebuilt only when its item or the viewer's hands change: a frame a second would
    // otherwise drop the keyboard focus on its button.
    const c = ctx();
    const items = (listedItems ?? summarisedItems).map((n) => {
      const key = JSON.stringify([n, !!c.release]);
      let li = cards.get(key);
      if (!li) cards.set(key, li = needCard(n, c));
      return li;
    });
    for (const k of [...cards.keys()]) if (!items.includes(cards.get(k)!)) cards.delete(k);
    const note = (cls: string, text: string) => {
      const kept = other.querySelector<HTMLLIElement>(`:scope > li.${cls}`);
      return kept?.textContent === text ? kept : el("li", { className: cls, textContent: text });
    };
    const notes: HTMLLIElement[] = [];
    if (stale) notes.push(note("needs-stale", stale));
    if (unread.length) notes.push(note("needs-unread", unreadLine(unread)));
    const next = [...items, ...notes];
    if (next.length !== other.children.length || next.some((e, i) => other.children[i] !== e)) other.replaceChildren(...next);
    others = items.length;
    const show = items.length + pending.length + unread.length + (stale ? 1 : 0) > 0;
    // A viewer who decides nothing here and has no merge, answer or hold reads the cards as
    // someone else's turn; when every card left is expired, no one is awaited any more.
    const lapsed = pending.length > 0 && pending.every((a) => expiredAt(a.expiresAt));
    const heading = panel.count() > 0 ? "Needs you" : lapsed ? "Expired: no one decided" : "Waiting for an approver";
    if (title.textContent !== heading) title.textContent = heading;
    root.hidden = !show;
  };

  // read asks the route for this room's list and renders the answer: the items become the list, and a
  // failure leaves the list it could not refresh in place with its one line, never a blank panel.
  const read = (): Promise<void> => {
    if (!opts.needs) return Promise.resolve();
    readAt = Date.now();
    const p = opts.needs()
      .then((page) => {
        // Deciding is the cards': the route lists an approval for every door, and a second one here
        // would not know four-eyes or the policy.
        listedItems = (page.items ?? []).filter((n) => n.kind !== "approval");
        unread = page.unread ?? [];
        stale = "";
      })
      .catch((e: unknown) => {
        stale = e instanceof Error ? e.message : String(e);
      })
      .finally(() => {
        reading = undefined;
        render();
      });
    reading = p;
    return p;
  };

  const panel = {
    // refresh draws the panel and, on its cadence, reads the route beside it: the page calls it on
    // every frame, and the one read it starts is the promise a caller can wait on.
    refresh(): Promise<void> {
      render();
      if (reading) return reading;
      if (Date.now() - readAt < routeEvery) return Promise.resolve();
      return read();
    },
    setSummary(s: Summary): void {
      summarisedItems = needsItems(s);
      render();
    },
    // count is what needs this viewer: the approvals it may decide, then merges, answers and holds.
    count(): number { return panel.approvalCounts().mine + others; },
    // approvalCounts: the pending approvals, and those this viewer may decide.
    approvalCounts(): { total: number; mine: number } {
      if (state.sealed) return { total: 0, mine: 0 };
      const all = state.approvals().filter((a) => !expiredAt(a.expiresAt));
      return { total: all.length, mine: all.filter((a) => standing(a).can).length };
    },
    // focusFirst takes the reader, and the keyboard focus, to the first card.
    focusFirst(): void {
      const li = approvals.querySelector<HTMLElement>("li.approval");
      if (!li) return;
      li.tabIndex = -1;
      li.scrollIntoView?.({ block: "start" });
      li.focus({ preventScroll: true });
    },
  };
  return panel;
}
