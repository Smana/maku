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
import { decideStatus, deadlineText, ruleFor } from "./approval-help";
import { focusRow } from "./chat";
import type { Snapshot } from "./conn";
import { act, approvalAnchor, approvalCard, button, el, keyed, named, type Sender } from "./controls";
import type { RoomState } from "./room-state";
import { inlineCode, needsItems, safeLink, type Summary } from "./summary";

// routeEvery is how often the panel asks the route. refresh runs on every frame the page draws — the
// ticker's one a second, and each event a replay delivers — and one read is a scan of every room the
// caller may read, so a burst of frames costs one read, not one each.
const routeEvery = 5000;

// needList is one item the route lists: the broker's own kind, its one sentence (a need's what names a
// command, so its backticked spans are code), and the action's label linked where the action happens —
// https only, as every link on this page. The route ordered the items (CD21): this list never reorders
// them.
function needList(n: NeedItem): HTMLLIElement {
  const li = el("li", { className: "need" });
  li.dataset.kind = n.kind;
  li.append(`${n.kind}: `);
  if (n.what) li.append(...inlineCode(n.what), " · ");
  const label = n.action?.label || n.what;
  const url = n.action?.url || n.url;
  li.append(url ? safeLink(label, url) : document.createTextNode(label));
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
  opts: { needs?: () => Promise<NeedsPage> } = {}):
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
  const standing = (a: Approval) => decideStatus({ you: you(), sealed: state.sealed, fourEyes: fourEyes(), prompters: a.prompters });
  let others = 0;
  // The list under the cards: the route's items once a read answered (listed), the summary's until
  // then (summarised), and two lines — why a read failed, and the rooms the route could not read.
  let listed: HTMLLIElement[] | undefined;
  let summarised: HTMLLIElement[] = [];
  let unread: string[] = [];
  let stale = "";
  let reading: Promise<void> | undefined; // one read at a time
  let readAt = 0; // when the last read started, for routeEvery

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
    rows(pending.map((a) => { const st = standing(a); return { key: a.approvalId, sig: st.text, build: () => card(a, st) }; }));
    // The deadline counts down without a rebuild, which would drop a reason being typed.
    for (const d of approvals.querySelectorAll<HTMLElement>("p.approval-deadline")) {
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
    // that failed and for a room the route could not read. A failure's line is only shown beside
    // something else: silence over an otherwise empty panel is the page's own error line's to carry,
    // and a "Needs you" heading over one error would say the opposite of it.
    const items = listed ?? summarised;
    const show = items.length + pending.length + unread.length > 0;
    const notes: HTMLLIElement[] = [];
    if (stale) notes.push(el("li", { className: "needs-stale", textContent: stale }));
    if (unread.length) notes.push(el("li", { className: "needs-unread", textContent: unreadLine(unread) }));
    other.replaceChildren(...items, ...(show ? notes : []));
    others = items.length;
    // A viewer who decides nothing here and has no merge, answer or hold reads the cards as
    // someone else's turn.
    const heading = panel.count() > 0 ? "Needs you" : "Waiting for an approver";
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
        listed = (page.items ?? []).filter((n) => n.kind !== "approval").map(needList);
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
      summarised = needsItems(s);
      render();
    },
    // count is what needs this viewer: the approvals it may decide, then merges, answers and holds.
    count(): number { return panel.approvalCounts().mine + others; },
    // approvalCounts: the pending approvals, and those this viewer may decide.
    approvalCounts(): { total: number; mine: number } {
      if (state.sealed) return { total: 0, mine: 0 };
      const all = state.approvals();
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
