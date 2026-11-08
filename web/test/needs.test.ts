import { describe, expect, it, vi } from "vitest";
import type { NeedsPage } from "../src/api";
import type { Snapshot } from "../src/conn";
import { RULES } from "../src/approval-help";
import { approvalCard } from "../src/controls";
import { mountNeeds, type NeedHands } from "../src/needs";
import { NEED_FIXTURES } from "../preview/needs-fixtures";
import { RoomState } from "../src/room-state";
import type { Summary } from "../src/summary";

const you = { principal: "human:a", role: "owner", approver: true, driver: true, webUI: true };
const approval = { approvalId: "01M4A", runId: "ikely2yk", callId: "c9", class: "forge.other",
  action: { kind: "TerminalAction", command: "git push origin agent/ibbay5ud", timeout: 600 }, expiresAt: "2026-10-15T15:18:52Z", seq: 13 };
const sum = (needs: Summary["needsYou"]): Summary => ({ apiVersion: "summary/v1", room: "r", url: "u",
  status: { phase: "Implementing", run: null, budget: null, pr: null, issue: null, lastVerdict: null }, needsYou: needs, actions: [],
  notes: { untrusted: true, items: [] }, cursor: "seq:1" });
function mount(state = new RoomState(), sent: Record<string, unknown>[] = [], opts: { needs?: () => Promise<NeedsPage> } = {}) {
  const root = document.createElement("section");
  const row = document.createElement("details"); row.dataset.callId = "c9"; row.dataset.seq = "12";
  const needs = mountNeeds(root, { send: (f) => { sent.push(f); return true; } }, state, () => you, { rowFor: (id) => (id === "c9" ? row : undefined) }, () => undefined, opts);
  return { root, needs, sent, row };
}

describe("the needs-you panel", () => {
  it("leads an approval card with its command, keeps the JSON behind a toggle, links the call", () => {
    const state = new RoomState(); state.reset({ driver: "human:a", driverEpoch: 1, approvals: [approval] }, 20);
    const { root, needs } = mount(state); needs.refresh();
    const card = root.querySelector("#approval-01M4A")!;
    expect(card.querySelector(".approval-command")?.textContent).toBe("git push origin agent/ibbay5ud");
    expect(card.querySelector("details.approval-json pre")?.textContent).toContain('"timeout": 600');
    expect(card.querySelector("a.approval-call")?.textContent).toBe("see the call #12");
    expect(root.querySelector("h2")?.textContent).toBe("Needs you");
    expect(card.querySelector('button[data-act="approve"]')?.classList.contains("primary")).toBe(true);
    expect(card.querySelector('input[name="decisionReason"]')?.getAttribute("aria-label")).toBe("reason for the decision (optional)");
    expect(needs.count()).toBe(1);
  });
  it("sends a decide act and drops the card when the log decides it (review focus 3)", () => {
    const state = new RoomState(); state.reset({ driver: "human:a", driverEpoch: 1, approvals: [approval] }, 20);
    const { root, needs, sent } = mount(state); needs.refresh();
    (root.querySelector('button[data-act="approve"]') as HTMLButtonElement).click();
    expect(sent.at(-1)).toMatchObject({ type: "act", action: { kind: "decide", approvalId: "01M4A", decision: "approved" } });
    state.apply({ v: 1, id: "x", seq: 21, roomId: "r", actor: { kind: "system", id: "system:policy" }, type: "approval_decided", origin: "broker",
      ts: "2026-10-15T15:00:00Z", redactions: [], payload: { approvalId: "01M4A", decision: "approved" } });
    needs.refresh();
    expect(root.querySelector("#approval-01M4A")).toBeNull();
    expect(needs.count()).toBe(0);
  });
  it("lists a merge and a hold from the summary, and is empty when nothing needs you", () => {
    const { root, needs } = mount();
    needs.setSummary(sum([{ kind: "merge", id: "77", what: "a maintainer with admin merges PR #77", url: "https://github.com/o/r/pull/77" },
      { kind: "hold", id: "waiting_human_driver", what: "hand the driver back", url: "u" }]));
    expect([...root.querySelectorAll("li.need .need-title")].map((l) => l.textContent)).toEqual(["Merge the pull request", "The task is on hold"]);
    expect(root.querySelector("li.need a.need-go")?.textContent).toBe("Open the pull request ↗");
    expect(needs.count()).toBe(2);
    needs.setSummary(sum([])); needs.refresh();
    expect(root.hidden).toBe(true);
  });
  // U-2: the answer a bounced task waits for is listed, linked to the issue, and counted in the pill.
  it("lists an answer need from the summary and counts it", () => {
    const { root, needs } = mount();
    needs.setSummary(sum([{ kind: "question", id: "3", what: "@alice answers the scoper's question by editing issue #3",
      url: "https://github.com/o/r/issues/3", author: "alice" }]));
    expect(root.querySelector("li.need .need-what")?.textContent).toBe("@alice answers the scoper's question by editing issue #3");
    expect(root.querySelector("li.need a.need-go")?.getAttribute("href")).toBe("https://github.com/o/r/issues/3");
    expect(needs.count()).toBe(1);
    expect(root.hidden).toBe(false);
  });
  it("renders read-only for a watcher who may not decide", () => {
    const state = new RoomState(); state.reset({ driver: "human:a", driverEpoch: 1, approvals: [approval] }, 20);
    const root = document.createElement("section");
    const needs = mountNeeds(root, { send: () => true }, state, () => ({ ...you, role: "watcher", approver: false }), { rowFor: () => undefined });
    needs.refresh();
    expect(root.querySelector("button")).toBeNull();
    expect(root.querySelector(".approval-command")?.textContent).toBe("git push origin agent/ibbay5ud");
    // The pill counts what the viewer may decide: a read-only card is shown, not counted.
    expect(needs.count()).toBe(0);
    expect(root.hidden).toBe(false);
    expect(root.querySelector("h2")?.textContent).toBe("Waiting for an approver");
  });

  // Fix round 1: no empty heading before the first refresh, nor in a room that never connects.
  it("starts hidden, before any refresh", () => {
    expect(mount().root.hidden).toBe(true);
  });

  // A hold or a merge needs this viewer even when it may not decide the approvals.
  it("heads the panel Needs you for a merge or a hold, whoever decides the approvals", () => {
    const state = new RoomState(); state.reset({ driver: "human:a", driverEpoch: 1, approvals: [approval] }, 20);
    const root = document.createElement("section");
    const needs = mountNeeds(root, { send: () => true }, state, () => ({ ...you, role: "collaborator", approver: false }), { rowFor: () => undefined });
    needs.setSummary(sum([{ kind: "hold", id: "waiting_human_driver", what: "hand the driver back", url: "u" }]));
    expect(root.querySelector("h2")?.textContent).toBe("Needs you");
  });

  // The strip's pill and the panel agree: approver OR owner may decide.
  it("counts an approval for an approver and for an owner, never for a sealed room", () => {
    const cases: [string, Snapshot["you"], boolean, number][] = [
      ["an approver collaborator", { ...you, role: "collaborator", approver: true }, false, 1],
      ["an owner who is no approver", { ...you, role: "owner", approver: false }, false, 1],
      ["a collaborator who is neither", { ...you, role: "collaborator", approver: false }, false, 0],
      ["an owner in a sealed room", you, true, 0],
    ];
    for (const [name, who, sealed, want] of cases) {
      const state = new RoomState(); state.reset({ driver: "human:a", driverEpoch: 1, approvals: [approval], sealed }, 20);
      const needs = mountNeeds(document.createElement("section"), { send: () => true }, state, () => who, { rowFor: () => undefined });
      needs.refresh();
      expect(needs.count(), name).toBe(want);
    }
  });

  it("takes the call link to its row, opened, and hides the link while the row is gone", () => {
    const state = new RoomState(); state.reset({ driver: "human:a", driverEpoch: 1, approvals: [approval] }, 20);
    const { root, needs, row } = mount(state); needs.refresh();
    const scrolled = vi.fn();
    row.scrollIntoView = scrolled;
    const link = root.querySelector<HTMLAnchorElement>("a.approval-call")!;
    expect(link.getAttribute("href")).toBe("#");
    link.click();
    expect(scrolled).toHaveBeenCalled();
    expect(row.open).toBe(true);
    const gone = new RoomState(); gone.reset({ driver: "human:a", driverEpoch: 1, approvals: [{ ...approval, callId: "c-old" }] }, 20);
    const g = mount(gone); g.needs.refresh();
    expect(g.root.querySelector<HTMLAnchorElement>("a.approval-call")!.hidden).toBe(true);
  });

  it("names every control, and ties the decide buttons to the command they decide", () => {
    const state = new RoomState(); state.reset({ driver: "human:a", driverEpoch: 1, approvals: [approval] }, 20);
    const { root, needs } = mount(state); needs.refresh();
    for (const f of root.querySelectorAll("input")) expect(f.getAttribute("aria-label")?.trim()).toBeTruthy();
    for (const b of root.querySelectorAll("button")) {
      expect(b.textContent?.trim()).toBeTruthy();
      expect(root.querySelector(`#${b.getAttribute("aria-describedby")}`)).toBe(root.querySelector(".approval-command"));
    }
    expect(root.querySelector('button[data-act="deny"]')?.classList.contains("deny")).toBe(true);
  });
});

// The list under the cards is the needs route's (CD21), not the summary frame's: the panel reads
// /api/needs?room=<id>, so the page lists what the CLI and the tools list, in the route's order.
describe("the panel's list from the needs route", () => {
  // CD21: the panel's non-approval items are the route's, whatever the summary frame says.
  it("renders the needs route's items", async () => {
    const page: NeedsPage = { apiVersion: "needs/v1", cursor: "2026-11-04T19:10:00Z", items: [
      { kind: "plan_confirmation", id: "2238", room: "26zfnuxm", what: "the scoper proposes a split",
        action: { kind: "comment", label: "comment /factory split on issue #2238", url: "https://github.com/Smana/ai-native-ref/issues/2238" }, source: { kind: "facts" } }] };
    const { root, needs: panel } = mount(new RoomState(), [], { needs: async () => page });
    // The summary's items hold the panel until the read answers, never an empty panel.
    panel.setSummary(sum([{ kind: "merge", id: "77", what: "a maintainer merges PR #77", url: "https://github.com/o/r/pull/77" }]));
    expect(root.textContent).toContain("a maintainer merges PR #77");
    await panel.refresh();
    expect(root.textContent).toContain("Comment /factory split on issue #2238 ↗");
    expect(root.textContent).not.toContain("a maintainer merges PR #77");
    expect(root.querySelector("ul.needs-other li.need a")?.getAttribute("href")).toBe("https://github.com/Smana/ai-native-ref/issues/2238");
    expect(panel.count()).toBe(1);
  });

  // Deciding is the cards': the route lists approvals too, and a second decision path on the same
  // item would be a button that does not know four-eyes or the policy.
  it("leaves an approval to its card, and links only https", async () => {
    const page: NeedsPage = { apiVersion: "needs/v1", items: [
      { kind: "approval", id: "01M4A", room: "r", what: "the agent wants to push", url: "https://broker/r/1#01M4A",
        action: { kind: "decide", label: "decide in the room", url: "https://broker/r/1#01M4A" } },
      { kind: "merge", id: "77", room: "r", what: "a maintainer merges PR #77", url: "javascript:alert(1)",
        action: { kind: "merge", label: "open the pull request", url: "javascript:alert(1)" } }] };
    const { root, needs: panel } = mount(new RoomState(), [], { needs: async () => page });
    await panel.refresh();
    const items = [...root.querySelectorAll("ul.needs-other li")];
    expect(items).toHaveLength(1);
    expect(items[0].textContent).not.toContain("decide in the room");
    expect(items[0].textContent).toContain("a maintainer merges PR #77");
    expect(items[0].querySelector("a")).toBeNull(); // safeLink: not https, text
    expect(items[0].textContent).toContain("javascript:alert(1)");
    expect((items[0] as HTMLElement).dataset.kind).toBe("merge");
  });

  // A read that failed says so in one line and leaves the list it could not refresh in place: a
  // quiet panel is the one thing it must not look like.
  it("keeps the summary's items and says why when the read fails", async () => {
    const { root, needs: panel } = mount(new RoomState(), [], { needs: async () => { throw new Error("The needs list is unavailable: 503."); } });
    panel.setSummary(sum([{ kind: "hold", id: "waiting_human_driver", what: "hand the driver back", url: "u" }]));
    await panel.refresh();
    expect([...root.querySelectorAll("ul.needs-other li")].map((l) => l.className)).toEqual(["need", "needs-stale"]);
    expect(root.querySelector("li.needs-stale")?.textContent).toBe("The needs list is unavailable: 503.");
    expect(root.hidden).toBe(false);
    expect(panel.count()).toBe(1); // the line is not a need
  });

  // Review Focus 1: a room whose state could not be read contributes nothing, and the panel says so.
  it("says a room's state could not be read, in one line", async () => {
    const page: NeedsPage = { apiVersion: "needs/v1", items: [], unread: ["3kq7x2ma"] };
    const { root, needs: panel } = mount(new RoomState(), [], { needs: async () => page });
    await panel.refresh();
    const line = root.querySelector<HTMLElement>("ul.needs-other li.needs-unread");
    expect(line?.textContent).toContain("3kq7x2ma");
    expect(line?.textContent).toContain("could not be read");
    expect(root.hidden).toBe(false);
    expect(panel.count()).toBe(0);
  });

  // refresh runs on every frame a replay delivers; one read is a scan of every room the caller may
  // read, so a burst of frames costs one.
  it("reads the route on a cadence, not on every frame", async () => {
    vi.useFakeTimers();
    try {
      let reads = 0;
      const { needs: panel } = mount(new RoomState(), [], { needs: async () => { reads++; return { apiVersion: "needs/v1", items: [] }; } });
      vi.setSystemTime(new Date(2026, 10, 5, 10, 0, 0));
      await panel.refresh();
      await panel.refresh();
      await panel.refresh();
      expect(reads).toBe(1);
      vi.setSystemTime(new Date(2026, 10, 5, 10, 0, 6));
      await panel.refresh();
      expect(reads).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });

  // Fix round 1: a read that failed is itself something to show. With no summary items, no earlier
  // items and no approvals, the panel still lists the warning — never an empty panel — and its heading
  // stays the count's, so the reader is not told "Needs you" over one error line.
  it("shows the warning, and no Needs you heading, when only the read failed", async () => {
    const { root, needs: panel } = mount(new RoomState(), [], { needs: async () => { throw new Error("The needs list is unavailable: 503."); } });
    await panel.refresh();
    expect([...root.querySelectorAll("ul.needs-other li")].map((l) => l.textContent))
      .toEqual(["The needs list is unavailable: 503."]);
    expect(root.querySelector("ul.needs-other li.needs-stale")).not.toBeNull();
    expect(root.hidden).toBe(false);
    expect(root.querySelector("h2")?.textContent).toBe("Waiting for an approver");
    expect(panel.count()).toBe(0); // a warning is not a need
  });
});

// Moved from controls.test.ts: the cards left the controls for the panel.
describe("approval cards", () => {
  const owner = (principal: string, role: string): Snapshot["you"] => ({ principal, role, approver: false, driver: false, webUI: true });
  const pending = (id: string, seq: number, action: unknown = { command: "gh pr create" }) => ({ approvalId: id, runId: "7f3cq2xz",
    callId: "call_" + seq, class: "forge.pr", action, expiresAt: "2026-10-01T10:30:00Z", seq });
  const withApprovals = (who: Snapshot["you"], ...approvals: ReturnType<typeof pending>[]) => {
    const sent: Record<string, any>[] = [];
    const state = new RoomState("human:a", 4);
    state.reset({ driver: "human:a", driverEpoch: 4, approvals }, 10);
    const root = document.createElement("section");
    const needs = mountNeeds(root, { send: (f) => { sent.push(f); return true; } }, state, () => who, { rowFor: () => undefined });
    needs.refresh();
    const btn = (act: string) => root.querySelector<HTMLButtonElement>(`button[data-act="${act}"]`);
    return { root, state, needs, btn, acts: () => sent.map((f) => f.action) };
  };

  it("shows the raw action as text, never markup (T3)", () => {
    const hostile = { command: '<img src=x onerror="alert(1)">', body: "**not** markdown" };
    const c = withApprovals({ ...owner("human:apr", "collaborator"), approver: true }, pending("ap1", 3, hostile));
    const card = c.root.querySelector("ul.approvals li")!;
    expect(c.root.querySelector("img")).toBeNull();
    expect(card.querySelector("strong")).toBeNull();
    expect(card.querySelector(".approval-command")!.textContent).toBe('<img src=x onerror="alert(1)">');
    expect(card.querySelector(".approval-json pre")!.textContent).toBe(JSON.stringify(hostile, null, 2));
    expect(card.querySelector(".approval-meta")!.textContent).toMatch(/^forge\.pr · run 7f3cq2xz · expires /);
  });

  it("leads with the class when the action has no command", () => {
    const c = withApprovals(owner("human:own", "owner"), pending("ap1", 3, { kind: "FileEditorAction", path: "a.go" }));
    expect(c.root.querySelector(".approval-command")!.textContent).toBe("forge.pr");
  });

  it("lets approvers and owners approve or deny, with a reason", () => {
    const c = withApprovals({ ...owner("human:apr", "collaborator"), approver: true }, pending("ap1", 3), pending("ap2", 5));
    const cards = [...c.root.querySelectorAll("ul.approvals li")];
    expect(cards).toHaveLength(2);
    cards[0].querySelector<HTMLButtonElement>('[data-act="approve"]')!.click();
    cards[1].querySelector<HTMLInputElement>('[name="decisionReason"]')!.value = "  wrong branch ";
    c.needs.refresh(); // a typed reason survives a re-render
    c.root.querySelectorAll("ul.approvals li")[1].querySelector<HTMLButtonElement>('[data-act="deny"]')!.click();
    expect(c.acts()).toEqual([{ kind: "decide", approvalId: "ap1", decision: "approved" },
      { kind: "decide", approvalId: "ap2", decision: "denied", reason: "wrong branch" }]);

    const o = withApprovals(owner("human:own", "owner"), pending("ap1", 3));
    expect(o.btn("approve")).not.toBeNull();
  });

  it("offers no decision to others, nor in a sealed room", () => {
    const c = withApprovals(owner("human:col", "collaborator"), pending("ap1", 3));
    expect(c.root.querySelector("ul.approvals li pre")).not.toBeNull();
    expect(c.btn("approve")).toBeNull();
    expect(c.btn("deny")).toBeNull();
    const s = withApprovals(owner("human:own", "owner"));
    s.state.reset({ driver: "human:a", driverEpoch: 4, approvals: [pending("ap1", 3)], sealed: true }, 10);
    s.needs.refresh();
    expect(s.root.querySelector("ul.approvals li pre")).not.toBeNull();
    expect(s.btn("approve")).toBeNull();
  });

  // Review 5.3 I2: policy Decide is the approver flag, whatever the room role.
  it("gives a watcher who approves the cards and their buttons", () => {
    const c = withApprovals({ ...owner("human:wat", "watcher"), approver: true }, pending("ap1", 3));
    c.btn("approve")!.click();
    expect(c.acts()).toEqual([{ kind: "decide", approvalId: "ap1", decision: "approved" }]);
  });

  // R11: a deadline on another day shows its date, not a bare time.
  it("dates a deadline that is not today", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 9, 1, 9, 0));
    try {
      const meta = (due: Date) => approvalCard({ class: "forge.pr", callId: "c", action: {}, expiresAt: due.toISOString() })
        .querySelector(".approval-meta")!.textContent;
      const today = new Date(2026, 9, 1, 10, 30);
      const tomorrow = new Date(2026, 9, 2, 10, 30);
      expect(meta(today)).toBe(`forge.pr · expires ${today.toLocaleTimeString()}`);
      expect(meta(tomorrow)).toBe(`forge.pr · expires ${tomorrow.toLocaleString()}`);
    } finally {
      vi.useRealTimers();
    }
  });

  it("drops a card once decided", () => {
    const c = withApprovals(owner("human:own", "owner"), pending("ap1", 3));
    c.state.apply({ v: 1, id: "11", seq: 11, roomId: "r", actor: { kind: "human", id: "human:own" }, type: "approval_decided",
      origin: "client", ts: "2026-09-27T10:00:00Z", redactions: [], payload: { approvalId: "ap1", decision: "approved" } });
    c.needs.refresh();
    expect(c.root.querySelectorAll("ul.approvals li")).toHaveLength(0);
  });

  // R10: a refresh never takes the focus of the reason field the human is in.
  it("keeps the focused reason field across refreshes", () => {
    const c = withApprovals(owner("human:own", "owner"), pending("ap1", 3));
    document.body.append(c.root);
    try {
      const f = c.root.querySelector<HTMLInputElement>('[name="decisionReason"]')!;
      f.focus();
      c.needs.refresh();
      c.state.apply({ v: 1, id: "11", seq: 11, roomId: "r", actor: { kind: "human", id: "human:c" }, type: "message",
        origin: "client", ts: "2026-09-27T10:00:00Z", redactions: [], payload: { kind: "chat", text: "between", delivery: "queued" } });
      c.needs.refresh();
      expect(document.activeElement).toBe(f);
    } finally {
      c.root.remove();
    }
  });
});

describe("the approval card's guidance", () => {
  const mk = (cls: string, who = you, extra: Record<string, unknown> = {}, four?: boolean) => {
    const state = new RoomState(); state.reset({ driver: "human:a", driverEpoch: 1, approvals: [{ ...approval, class: cls, ...extra }] }, 20);
    const root = document.createElement("section");
    const needs = mountNeeds(root, { send: () => true }, state, () => who, { rowFor: () => undefined }, () => four);
    needs.refresh();
    return { root, needs };
  };
  it("says what it wants, why it stopped and what each answer does, per rule code", () => {
    for (const [code, rule] of Object.entries(RULES)) {
      const { root } = mk(code);
      expect(root.querySelector(".approval-intent")?.textContent).toBe(rule.intent);
      expect(root.querySelector(".approval-why")?.textContent).toBe(`Why it stopped: ${rule.why}`);
      const out = [...root.querySelectorAll(".approval-outcomes p")].map((p) => p.textContent);
      expect(out).toEqual([`If you approve: ${rule.approve}`, `If you deny: ${rule.deny}`]);
    }
  });
  it("folds the raw command and action under details, and writes agent text as text", () => {
    const evil = `<img src=x onerror=alert(1)>`;
    const { root } = mk("forge.other", you, { action: { command: evil } });
    const details = root.querySelector("details.approval-details")!;
    expect(details.querySelector("summary")?.textContent).toBe("details");
    expect(details.querySelector(".approval-command")?.textContent).toBe(evil);
    // What each answer does reads on demand: the card leads with what, why, by when and who.
    expect(details.firstElementChild?.tagName).toBe("SUMMARY");
    expect(details.querySelector(":scope > .approval-outcomes")).not.toBeNull();
    expect(root.querySelector("img")).toBeNull();
    expect(root.querySelector(".approval-intent")?.textContent).not.toContain("img");
  });
  it("shows the deadline, and none when the expiry is missing", () => {
    const soon = new Date(Date.now() + 5 * 60_000 + 5000).toISOString();
    expect(mk("forge.pr", you, { expiresAt: soon }).root.querySelector(".approval-deadline")?.textContent).toMatch(/^Decide by \d\d:\d\d \(in 5 min\); if nobody decides: the request expires/);
    expect(mk("forge.pr", you, { expiresAt: "" }).root.querySelector(".approval-deadline")?.textContent).toBe("No deadline.");
  });
  it("says you can decide, with the buttons", () => {
    const { root } = mk("forge.pr");
    expect(root.querySelector(".approval-status")?.textContent).toBe("You can decide.");
    expect(root.querySelector('button[data-act="approve"]')).not.toBeNull();
  });
  it("says only approvers decide, with no buttons", () => {
    const { root, needs } = mk("forge.pr", { ...you, role: "collaborator", approver: false });
    expect(root.querySelector(".approval-status")?.textContent).toContain("only approvers decide");
    expect(root.querySelector("button")).toBeNull();
    expect(needs.approvalCounts()).toEqual({ total: 1, mine: 0 });
  });
  it("explains four-eyes to the prompter and hides the buttons; others still decide", () => {
    const { root, needs } = mk("forge.pr", you, { prompters: ["human:a"] }, true);
    expect(root.querySelector(".approval-status")?.textContent).toBe("Waiting for another approver: you prompted this turn (four-eyes).");
    expect(root.querySelector("button")).toBeNull();
    expect(needs.count()).toBe(0);
    const other = mk("forge.pr", { ...you, principal: "human:b" }, { prompters: ["human:a"] }, true);
    expect(other.root.querySelector('button[data-act="approve"]')).not.toBeNull();
  });
  it("focuses the first card for the banner's link", () => {
    const { root, needs } = mk("forge.pr");
    document.body.append(root);
    needs.focusFirst();
    expect(document.activeElement).toBe(root.querySelector("li.approval"));
    root.remove();
  });
});

describe("a need's one action", () => {
  const draw = async (opts: { hands?: NeedHands; driver?: boolean } = {}) => {
    const root = document.createElement("section");
    const sent: Record<string, unknown>[] = [];
    const who = { ...you, driver: opts.driver ?? false };
    const panel = mountNeeds(root, { send: (f) => { sent.push(f); return true; } }, new RoomState(), () => who, { rowFor: () => undefined }, () => undefined,
      { needs: async () => ({ apiVersion: "needs/v1", items: NEED_FIXTURES }), hands: opts.hands });
    await panel.refresh();
    const cards = [...root.querySelectorAll<HTMLElement>("li.need")];
    return { root, panel, sent, cards, act: (i: number) => cards[i].querySelector(".need-act")! };
  };

  it("ends every card in an action: a control, a link out, or a line saying why there is none", async () => {
    const { cards } = await draw({ hands: { openSession: () => {}, focusCoordination: () => {} }, driver: true });
    expect(cards).toHaveLength(NEED_FIXTURES.length);
    for (const c of cards) expect(c.querySelector(".need-act button, .need-act a.need-go, .need-act .need-where"), c.dataset.kind).not.toBeNull();
  });
  it("links the GitHub kinds out, as a button-shaped link, and says the room has no button for them", async () => {
    const { act } = await draw();
    for (const [i, href] of [[0, "https://github.com/o/r/issues/2238"], [2, "https://github.com/o/r/issues/3"], [3, "https://github.com/o/r/pull/2760"],
      [4, "https://github.com/o/r/pull/77"], [5, "https://github.com/o/r/pull/77"]] as const) {
      const a = act(i).querySelector<HTMLAnchorElement>("a.need-go")!;
      expect(a.getAttribute("href")).toBe(href);
      expect([a.target, a.rel]).toEqual(["_blank", "noopener noreferrer"]);
      expect(a.getAttribute("aria-label")).toMatch(/\(opens in a new tab\)$/);
      expect(act(i).querySelector(".need-where")?.textContent).toMatch(/^On GitHub/);
    }
    expect(act(3).textContent).toContain("Approve the head on the pull request ↗");
    expect(act(3).textContent).toContain("the merge gate reads the approval of the head");
  });
  it("hands the driver back in one press, for the driver", async () => {
    const { act, sent } = await draw({ driver: true });
    const b = act(7).querySelector<HTMLButtonElement>("button")!;
    expect(b.textContent).toBe("Hand the driver back to the factory");
    b.click();
    expect(sent.at(-1)).toMatchObject({ type: "act", action: { kind: "driver_give", to: "system:factory" } });
    expect(act(8).querySelector("button")).toBeNull(); // a busy room: nothing to press
    expect(act(8).textContent).toMatch(/starts when the other run in the room ends/);
  });
  it("takes a viewer who does not drive to Session › Driver, a recovery to the close button, a plan to the coordination panel", async () => {
    const went: string[] = [];
    const { act } = await draw({ hands: { openSession: (to) => went.push(to), focusCoordination: () => went.push("coordination") } });
    act(7).querySelector<HTMLButtonElement>("button")!.click();
    act(6).querySelector<HTMLButtonElement>("button")!.click();
    act(1).querySelector<HTMLButtonElement>("button")!.click();
    expect(went).toEqual(["driver", "close", "coordination"]);
    expect(act(6).querySelector("a.need-go")?.textContent).toBe("Retry: comment /factory retry on issue #3 ↗");
    expect(act(1).querySelector("button")?.textContent).toBe("Confirm");
  });
  it("never links a need back to the room page it sits on", async () => {
    const { root } = await draw({ hands: { openSession: () => {}, focusCoordination: () => {} } });
    expect([...root.querySelectorAll("a")].map((a) => a.getAttribute("href")).filter((h) => h?.startsWith("https://rooms.example/"))).toEqual([]);
  });
  it("keeps a card, and the focus on its button, across frames", async () => {
    const { root, panel, cards } = await draw({ driver: true });
    document.body.append(root);
    const b = cards[7].querySelector<HTMLButtonElement>("button")!;
    b.focus();
    await panel.refresh();
    expect(root.querySelectorAll("li.need")[7]).toBe(cards[7]);
    expect(document.activeElement).toBe(b);
    root.remove();
  });
});
