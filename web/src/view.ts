// SPDX-License-Identifier: Apache-2.0

import { api, nav, SignInRequired } from "./api";
import { type AutoRefreshOptions, mountAutoRefresh } from "./autorefresh";
import { mountBrand } from "./brand";
import { timeEl } from "./duration";
import { statusIcon } from "./icons";
import { maxRows } from "./limits";
import { NEED_TYPES, needType, needTypeRank } from "./needs";
import { ago } from "./summary";
import { mountThemeButton, type ThemeController } from "./theme";
import { mountRoutinesLink, mountSettingsLink } from "./settingslink";
import { newTaskForm, offerServedRepositories } from "./newtask";

// A room's rows by seq, in seq order on the page. A resume the broker clamped can
// resend a seq: its row is replaced in place, never doubled, and one no longer held
// goes back in order.
export class RoomLog {
  private rows = new Map<number, HTMLElement>();
  private seqs: number[] = []; // the held seqs, ascending
  constructor(private el: HTMLElement, private cap = maxRows) {}

  put(seq: number, row: HTMLElement) {
    const old = this.rows.get(seq);
    if (old) {
      old.replaceWith(row);
      this.rows.set(seq, row);
      return;
    }
    let i = this.seqs.length;
    while (i > 0 && this.seqs[i - 1] > seq) i--; // from the end: live events land there
    const next = i < this.seqs.length ? this.rows.get(this.seqs[i]) : undefined;
    if (next) next.before(row); else this.el.append(row);
    this.seqs.splice(i, 0, seq);
    this.rows.set(seq, row);
    while (this.seqs.length > this.cap) {
      const s = this.seqs.shift()!;
      this.rows.get(s)?.remove();
      this.rows.delete(s);
    }
  }
}

// One row of GET /api/rooms (docs/reference/api.md). lastEventAt is omitted for a room with no event.
export interface RoomRow {
  id: string; phase: string; owner: string; driver: string; dataClass: string; lastSeq: number;
  repository?: string; issue?: number; title?: string; task?: string; pr?: number; needsYou?: number;
  need?: { kind: string; type?: string; id: string; what: string; url?: string };
  lastEventAt?: string;
  // status is the room page's status line, computed by the broker (summary.StatusLine): fixed words
  // and numbers, never agent text. Absent from an older broker, or when it does not know the state.
  status?: RowStatus;
  // coordination is a coordination parent's family as the broker filtered it for this viewer
  // (internal/summary CoordinationOverview): a compact line, not a second needs list.
  coordination?: { planVersion: number; restrictedChildren: number; recoveryCode?: string;
    children: { id: string; phase?: string; reason?: string; issueURL?: string; prURL?: string; dependsOn: string[] }[] };
}

// familyLine is a parent's family on one line, as maku's overview writes it.
function familyLine(c: NonNullable<RoomRow["coordination"]>): string {
  let s = `family v${c.planVersion}: ${c.children.map((ch) => [ch.id, ch.phase].filter(Boolean).join(" ")).join(", ")}`;
  if (c.restrictedChildren > 0) s += ` (+${c.restrictedChildren} restricted)`;
  if (c.recoveryCode) s += `, recovery ${c.recoveryCode}`;
  return s;
}

export interface RowStatus { kind: string; icon: string; text: string }

// lastMs is a row's last event in ms, 0 when omitted, zero or unreadable (never 1970 on screen).
function lastMs(row: RoomRow): number {
  const t = Date.parse(row.lastEventAt ?? "");
  return isNaN(t) || t < Date.UTC(2000, 0, 1) ? 0 : t;
}

function rowNeedType(row: RoomRow): string {
  if (!row.need) return "";
  return row.need.type || needType(row.need.kind);
}

// needsFirst puts the rooms that need a human first (in the five-type inbox urgency order when a
// need is present: question, plan, review, blocked, failed), then the most recent activity, then id.
function needsFirst(a: RoomRow, b: RoomRow): number {
  const an = a.needsYou ?? 0;
  const bn = b.needsYou ?? 0;
  if ((an > 0) !== (bn > 0)) return bn > 0 ? 1 : -1;
  if (an > 0 && bn > 0) {
    const at = rowNeedType(a);
    const bt = rowNeedType(b);
    if (at || bt) {
      const diff = needTypeRank(at) - needTypeRank(bt);
      if (diff !== 0) return diff;
    }
    if (bn !== an) return bn - an;
  }
  return lastMs(b) - lastMs(a) || byId(a, b);
}

function byId(a: RoomRow, b: RoomRow): number {
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}

// The room list's sortable columns, and the order each takes on its first click: activity and
// numbers newest first, a repository A to Z, a status most severe first.
export type SortKey = "last" | "task" | "repo" | "status" | "pr";
export type SortDir = "asc" | "desc";
export interface RoomSort { key: SortKey; dir: SortDir }
export const SORT_COLUMNS: { key: SortKey; label: string; first: SortDir }[] = [
  { key: "task", label: "Task", first: "desc" },
  { key: "repo", label: "Repository", first: "asc" },
  { key: "status", label: "Status", first: "asc" },
  { key: "pr", label: "PR", first: "desc" },
  { key: "last", label: "Last activity", first: "desc" },
];
export const DEFAULT_SORT: RoomSort = { key: "last", dir: "desc" };
export const SORT_STORAGE_KEY = "maku:rooms-sort";

// STATUS_SEVERITY ranks the broker's status kinds (internal/summary runstatus.go) most severe
// first: what failed or waits on a person (failed, waiting for you, escalated or a question on
// the issue) together on top, then a PR to review, another approver, the live and queued runs,
// and the ended ones. A kind this page predates, or no status, sorts last.
export const STATUS_SEVERITY = ["failed", "waiting-you", "waiting", "waiting-pr", "waiting-other", "running", "queued", "stopped", "done"];

function statusRank(row: RoomRow): number {
  const i = STATUS_SEVERITY.indexOf(row.status?.kind ?? "");
  return i >= 0 ? i : STATUS_SEVERITY.length;
}

// isDefaultSort: the one order that pins the rooms that need you on top.
export function isDefaultSort(s: RoomSort): boolean {
  return s.key === DEFAULT_SORT.key && s.dir === DEFAULT_SORT.dir;
}

// nextSort is the order a click on a column's header asks for: the same column reverses, another
// starts in its natural order.
export function nextSort(cur: RoomSort, key: SortKey): RoomSort {
  if (cur.key === key) return { key, dir: cur.dir === "asc" ? "desc" : "asc" };
  return { key, dir: SORT_COLUMNS.find((c) => c.key === key)?.first ?? "asc" };
}

// readSort is the stored order, or the default when unset, unreadable or the storage throws (a
// private window); storeSort keeps a choice, and a refused write only loses it on reload.
export function readSort(store?: Storage): RoomSort {
  try {
    const v = JSON.parse((store ?? localStorage).getItem(SORT_STORAGE_KEY) ?? "null") as Partial<RoomSort> | null;
    if (v && SORT_COLUMNS.some((c) => c.key === v.key) && (v.dir === "asc" || v.dir === "desc")) return { key: v.key!, dir: v.dir };
  } catch { /* the default */ }
  return { ...DEFAULT_SORT };
}

export function storeSort(s: RoomSort, store?: Storage): void {
  try {
    (store ?? localStorage).setItem(SORT_STORAGE_KEY, JSON.stringify(s));
  } catch { /* holds for this page */ }
}

// sortValue is a row's value under a column, undefined when it has none: those sort last whichever
// way the column runs.
function sortValue(row: RoomRow, key: SortKey): number | string | undefined {
  switch (key) {
    case "last": return lastMs(row) || undefined;
    case "task": return row.issue || undefined;
    case "pr": return row.pr || undefined;
    case "repo": return row.repository?.toLowerCase() || undefined;
    case "status": return statusRank(row);
  }
}

// sortRooms orders the loaded rows by the chosen column. The default (last activity, newest
// first) keeps the rooms that need you pinned on top; any other drops the pin. Ties fall to the
// issue number, newest first, then the room id: fields a refresh does not change, so an
// unchanged list keeps its order and rows do not jump.
export function sortRooms(rows: RoomRow[], sort: RoomSort = DEFAULT_SORT): RoomRow[] {
  if (isDefaultSort(sort)) return [...rows].sort(needsFirst);
  const sign = sort.dir === "asc" ? 1 : -1;
  return [...rows].sort((a, b) => {
    const av = sortValue(a, sort.key);
    const bv = sortValue(b, sort.key);
    if (av !== bv) {
      if (av === undefined) return 1;
      if (bv === undefined) return -1;
      return (av < bv ? -1 : 1) * sign;
    }
    return (b.issue ?? 0) - (a.issue ?? 0) || byId(a, b);
  });
}

// matchesQuery is the search box's filter over the loaded rows, case-insensitive: a number, with
// or without its #, matches an issue or a PR by that exact number (#57 is not #579); any text
// matches the title, repository or room id.
export function matchesQuery(row: RoomRow, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  const n = /^#(\d+)$/.exec(q) ?? /^(\d+)$/.exec(q);
  if (n && (row.issue === Number(n[1]) || row.pr === Number(n[1]))) return true;
  if (q.startsWith("#") && n) return false;
  return [row.title, row.repository, row.id].some((f) => !!f && f.toLowerCase().includes(q));
}

// rowCells is a room's table cells as text: the task it works on when it has one, else the room alone (#50).
export function rowCells(row: RoomRow, now: Date): { needs: string; task: string; repo: string; phase: string; pr: string; last: string; id: string } {
  const t = lastMs(row);
  return {
    needs: row.needsYou ? String(row.needsYou) : "",
    task: row.issue ? [`#${row.issue}`, row.title ?? ""].filter(Boolean).join(" ") : `Room ${row.id}`,
    repo: row.repository ?? "",
    phase: [row.task || row.phase || "Open", row.coordination ? familyLine(row.coordination) : ""].filter(Boolean).join(" · "),
    pr: row.pr ? `#${row.pr}` : "",
    last: t ? ago(new Date(t).toISOString(), now.getTime()) : "",
    id: row.id,
  };
}

// closed asks for the closed rooms instead (the factory deleted their Room objects): mine and
// needs me mean nothing there, so they are left out.
// closedCount asks the default view to also count the closed view's rooms (X-Rooms-Closed): the
// page asks on its first load and on a new repository, not on each refresh.
export interface RoomFilters { mine?: boolean; needsMe?: boolean; repo?: string; closed?: boolean; closedCount?: boolean }

// The closed view's window, as the API bounds it (humanapi closedWindow).
const closedDays = 30;

function roomsURL(f: RoomFilters): string {
  const q = new URLSearchParams();
  if (f.closed) q.set("closed", "1");
  else {
    if (f.mine) q.set("mine", "1");
    if (f.needsMe) q.set("needs_me", "1");
    if (f.closedCount) q.set("closed_count", "1");
  }
  if (f.repo) q.set("repo", f.repo);
  const s = q.toString();
  return s ? `/api/rooms?${s}` : "/api/rooms";
}

function cell(tag: "td" | "th", ...kids: (Node | string)[]): HTMLElement {
  const c = document.createElement(tag);
  if (tag === "th") c.scope = "col";
  c.append(...kids);
  return c;
}

// statusCell is the room page's status line, compact: the kind's accent, the icon and the words, as
// plain text with no live region (a list of them would chatter). The raw phase is the tooltip. A row
// without a status shows the phase as the room page's phase chip, as before, with no tooltip to repeat it.
function statusCell(st: RowStatus | undefined, phase: string): HTMLElement {
  const td = cell("td");
  td.className = "status";
  if (!st?.kind) {
    const chip = document.createElement("span");
    chip.className = "chip chip-phase";
    chip.textContent = phase;
    td.append(chip);
    return td;
  }
  const line = document.createElement("span");
  line.className = "row-status";
  line.dataset.kind = st.kind;
  const icon = document.createElement("span");
  icon.className = "row-status-icon";
  icon.setAttribute("aria-hidden", "true"); // the words say it all
  const svg = statusIcon(st.kind);
  if (svg) icon.append(svg);
  else icon.textContent = st.icon; // a kind this page predates: the broker's character
  const words = document.createElement("span");
  words.className = "row-status-words";
  // The list does not tick between refreshes: a run's age in seconds would stand still, so it is left out.
  words.textContent = st.kind === "running" ? st.text.replace(/(?:, | — )\d+ s$/, "") : st.text;
  line.append(icon, " ", words);
  td.title = phase;
  td.append(line);
  return td;
}

function roomRow(row: RoomRow, now: Date): HTMLTableRowElement {
  const c = rowCells(row, now);
  const needs = cell("td");
  if (c.needs) {
    const pill = document.createElement("span");
    pill.className = "needs-pill";
    const t = rowNeedType(row);
    if (t) {
      pill.dataset.type = t;
      pill.textContent = t;
      pill.title = `${t} · ${c.needs} need you`;
    } else {
      pill.textContent = c.needs;
      pill.title = `${c.needs} need you`;
    }
    needs.append(pill);
  }
  // The task link is stretched over its row in CSS (.task-link::after), so the whole row
  // opens the room with no click handler: one link for the keyboard and assistive tech.
  const a = document.createElement("a");
  a.className = "task-link";
  a.href = `/r/${encodeURIComponent(row.id)}`;
  a.textContent = c.task;
  const repo = cell("td", c.repo);
  repo.className = "repo";
  const status = statusCell(row.status, c.phase);
  const t = lastMs(row);
  const last = cell("td", t ? timeEl(now.getTime() - t, new Date(t).toISOString(), "ago") : "—");
  const code = document.createElement("code");
  code.textContent = c.id;
  const id = cell("td", code);
  id.className = "room-id";
  const tr = document.createElement("tr");
  tr.className = "room-row";
  tr.append(needs, cell("td", a), repo, status, cell("td", c.pr), last, id);
  return tr;
}

// listRooms renders the room list as a table, the filters and the new-room form, or why it
// cannot: a 401 signs in again; an expired session may also reach fetch as a redirect to
// the IdP, so a failed fetch or a body that is not JSON. The list refreshes on its own
// (autorefresh.ts); refresh carries that control's clock and storage, for tests.
export async function listRooms(app: HTMLElement, get: typeof fetch = fetch, filters: RoomFilters = {}, theme?: ThemeController,
  refresh: AutoRefreshOptions = {}) {
  // closedSeen is the count of closed rooms the last answer carried (X-Rooms-Closed), when it did.
  let closedSeen: number | undefined;
  const fetchRooms = async (f: RoomFilters, say: (msg: string) => void = (m) => { app.textContent = m; }): Promise<RoomRow[] | undefined> => {
    try {
      const r = await api(roomsURL(f), {}, get);
      if (!r.ok) {
        say(`The room list is unavailable (${r.status}). Retry.`);
        return;
      }
      const rows = (await r.json()) as RoomRow[];
      const n = Number(r.headers?.get("X-Rooms-Closed") ?? "");
      closedSeen = r.headers?.has("X-Rooms-Closed") && Number.isInteger(n) && n >= 0 ? n : undefined;
      return rows;
    } catch (e) {
      say(e instanceof SignInRequired ? "signing in again…"
        : `The room list is unavailable (reload to sign in again): ${e instanceof Error ? e.message : String(e)}`);
    }
  };
  const first = await fetchRooms({ ...filters, closedCount: !filters.closed }, (m) => {
    const p = document.createElement("p");
    p.textContent = m;
    app.replaceChildren(p);
    if (m.startsWith("signing")) return;
    const retry = document.createElement("button");
    retry.type = "button";
    retry.textContent = "Retry";
    retry.onclick = () => void listRooms(app, get, filters, theme, refresh);
    app.append(retry);
  });
  if (!first) return;
  // closedN is how many rooms the closed view holds for the page's repository filter: the default
  // list reads live Room objects, and the factory deletes a task's Room once it ends.
  let closedN = filters.closed ? first.length : closedSeen ?? 0;

  const header = document.createElement("header");
  header.className = "page-header";
  mountBrand(header);
  const h1 = document.createElement("h1");
  h1.className = "page-title";
  h1.textContent = "Rooms";
  header.append(h1);
  const headerActions = document.createElement("div");
  headerActions.className = "header-actions";
  mountRoutinesLink(headerActions);
  mountSettingsLink(headerActions);
  if (theme) mountThemeButton(headerActions, theme);
  header.append(headerActions);

  const check = (name: string, label: string, on?: boolean) => {
    const input = document.createElement("input");
    input.type = "checkbox";
    input.name = name;
    input.checked = !!on;
    const l = document.createElement("label");
    l.append(input, ` ${label}`);
    return { input, label: l };
  };
  const mine = check("mine", "mine", filters.mine);
  const needsMe = check("needsMe", "needs me", filters.needsMe);
  const needTypeFilter = document.createElement("select");
  needTypeFilter.name = "needType";
  needTypeFilter.setAttribute("aria-label", "need type");
  const allTypes = document.createElement("option");
  allTypes.value = "";
  allTypes.textContent = "all types";
  needTypeFilter.append(allTypes);
  for (const t of NEED_TYPES) {
    const o = document.createElement("option");
    o.value = o.textContent = t;
    needTypeFilter.append(o);
  }
  const closed = check("closed", "closed", filters.closed);
  const setAside = () => {
    mine.input.disabled = needsMe.input.disabled = closed.input.checked;
    needTypeFilter.hidden = closed.input.checked || !needsMe.input.checked;
    if (needTypeFilter.hidden) needTypeFilter.value = "";
  };
  setAside();
  const repo = document.createElement("select");
  repo.name = "repo";
  repo.setAttribute("aria-label", "repository");
  const all = document.createElement("option");
  all.value = "";
  all.textContent = "all repositories";
  repo.append(all);
  const knownRepos = [...new Set(first.map((r) => r.repository).filter((r): r is string => !!r))].sort();
  for (const name of knownRepos) {
    const o = document.createElement("option");
    o.value = o.textContent = name;
    repo.append(o);
  }
  repo.value = filters.repo ?? "";
  const taskForm = newTaskForm(get, knownRepos);
  taskForm.hidden = true;
  const form = newRoomForm(get);
  form.hidden = true;
  const addTask = document.createElement("button");
  addTask.type = "button";
  addTask.className = "primary";
  addTask.dataset.act = "new-task";
  addTask.textContent = "New task";
  // The normal way to ask for work: the planner drafts an issue, and the factory works on it.
  addTask.title = "Plans your request into a GitHub issue the factory works on";
  addTask.setAttribute("aria-description", addTask.title);
  let servedOffered = false;
  addTask.onclick = () => {
    form.hidden = true;
    taskForm.hidden = !taskForm.hidden;
    if (!servedOffered) {
      servedOffered = true;
      void offerServedRepositories(taskForm, get);
    }
  };
  // The advanced path, behind More beside New task: a room tied to no issue, driven by you.
  const more = moreMenu(() => {
    taskForm.hidden = true;
    form.hidden = false;
    form.querySelector("select")?.focus();
  });
  const tools = document.createElement("div");
  tools.className = "list-tools";
  const notice = document.createElement("span");
  notice.className = "notice";
  notice.setAttribute("role", "status");
  // The search box filters the loaded rows, as the need type does: no refetch, the closed view too.
  const search = document.createElement("input");
  search.type = "search";
  search.name = "q";
  search.className = "room-search";
  search.placeholder = "Search #issue, title, repository, room";
  search.setAttribute("aria-label", "Search rooms");
  search.autocomplete = "off";
  // The filters on the left, then the search, the refresh control, More, and New task, the
  // page's one primary act, at the row's end.
  tools.append(mine.label, needsMe.label, needTypeFilter, closed.label, repo, search, notice);

  // The closed hint: rooms that ended are not gone, they are on the closed view.
  const hint = document.createElement("p");
  hint.className = "closed-hint";
  const hintText = document.createElement("span");
  const showClosed = document.createElement("button");
  showClosed.type = "button";
  showClosed.dataset.act = "show-closed";
  showClosed.textContent = "show closed";
  showClosed.onclick = () => closed.input.click();
  hint.append(hintText, " — ", showClosed);
  const ended = (n: number) => `${n} ${n === 1 ? "room" : "rooms"} ended in the last ${closedDays} days`;
  const showHint = () => {
    hint.hidden = closed.input.checked || closedN <= 0;
    hintText.textContent = ended(closedN);
  };

  const table = document.createElement("table");
  table.className = "rooms";
  table.setAttribute("aria-label", "Rooms");
  // Each sortable column's header holds a button: a click sorts by it, a second reverses, and the
  // order is remembered in this browser (the auto-refresh's store, for tests).
  const store = refresh.store;
  let sort = readSort(store);
  const head = document.createElement("tr");
  const sortHeads = new Map<SortKey, { th: HTMLElement; arrow: HTMLElement }>();
  for (const h of ["Needs you", "Task", "Repository", "Status", "PR", "Last activity", "Room"]) {
    const col = SORT_COLUMNS.find((c) => c.label === h);
    if (!col) {
      head.append(cell("th", h));
      continue;
    }
    const b = document.createElement("button");
    b.type = "button";
    b.className = "sort";
    b.dataset.sort = col.key;
    const arrow = document.createElement("span");
    arrow.className = "sort-arrow";
    arrow.setAttribute("aria-hidden", "true");
    b.append(h, arrow);
    b.onclick = () => {
      sort = nextSort(sort, col.key);
      storeSort(sort, store);
      fill(latestRows);
    };
    const th = cell("th", b);
    sortHeads.set(col.key, { th, arrow });
    head.append(th);
  }
  const showSort = () => {
    for (const [key, { th, arrow }] of sortHeads) {
      const on = key === sort.key;
      if (on) th.setAttribute("aria-sort", sort.dir === "asc" ? "ascending" : "descending");
      else th.removeAttribute("aria-sort");
      arrow.textContent = on ? (sort.dir === "asc" ? "↑" : "↓") : "";
    }
  };
  const thead = document.createElement("thead");
  thead.append(head);
  const tbody = document.createElement("tbody");
  table.append(thead, tbody);
  // fill renders rows, unless they would read as the table already does: a refresh that
  // changes nothing leaves the DOM, and so the focus, alone. A rebuild puts the focus back on
  // the same room's link. The tab's title counts the rooms that need you, for a background tab.
  let shown = "";
  let latestRows: RoomRow[] = first;
  const fill = (rows: RoomRow[]) => {
    latestRows = rows;
    showHint();
    const needing = rows.filter((r) => (r.needsYou ?? 0) > 0).length;
    document.title = needing > 0 ? `(${needing}) Maku rooms` : "Maku rooms";
    const now = new Date();
    const chosenType = !needTypeFilter.hidden ? needTypeFilter.value : "";
    const visible = chosenType ? rows.filter((r) => rowNeedType(r) === chosenType) : rows;
    const query = search.value.trim();
    const sorted = sortRooms(visible.filter((r) => matchesQuery(r, query)), sort);
    const filtered = mine.input.checked || needsMe.input.checked || !!chosenType || !!repo.value;
    showSort();
    const key = JSON.stringify([filtered, closed.input.checked, chosenType, closedN, query, sort, sorted.map((r) => [rowCells(r, now), r.status, rowNeedType(r)])]);
    if (key === shown) return;
    shown = key;
    const focused = document.activeElement instanceof HTMLAnchorElement && tbody.contains(document.activeElement)
      ? document.activeElement.getAttribute("href") : null;
    if (sorted.length > 0) {
      tbody.replaceChildren(...sorted.map((r) => roomRow(r, now)));
      if (focused) [...tbody.querySelectorAll("a")].find((l) => l.getAttribute("href") === focused)?.focus();
      return;
    }
    // An empty table says why: a filter that leaves nothing, or no room at all.
    const td = cell("td", visible.length > 0 && query ? `No rooms match "${query}"`
      : closed.input.checked ? `No room closed in the last ${closedDays} days`
      : filtered ? "No rooms match" : closedN > 0 ? `No open rooms: ${ended(closedN)}, on the closed view` : "No rooms you can read yet");
    td.setAttribute("colspan", String(head.childElementCount));
    const tr = document.createElement("tr");
    tr.append(td);
    tbody.replaceChildren(tr);
  };
  fill(first);

  let seq = 0; // a slow answer to an older filter never overwrites a newer one
  let countFor = filters.repo ?? ""; // the repository filter closedN counts for
  // refetch reads the list under the filters the page shows; it resolves whether it read one. The
  // closed view's own rows are its count; the default view asks for one only on a new repository.
  const refetch = async (): Promise<boolean> => {
    const mark = ++seq;
    notice.textContent = "";
    const repoNow = repo.value;
    const recount = !closed.input.checked && repoNow !== countFor;
    const rows = await fetchRooms({ mine: mine.input.checked, needsMe: needsMe.input.checked, repo: repoNow || undefined, closed: closed.input.checked,
      closedCount: recount }, (m) => { if (mark === seq) notice.textContent = m; });
    if (rows && mark === seq) {
      if (closed.input.checked) [closedN, countFor] = [rows.length, repoNow];
      else if (recount) [closedN, countFor] = [closedSeen ?? 0, repoNow];
      fill(rows);
    }
    return !!rows;
  };
  mountAutoRefresh(tools, refetch, refresh);
  tools.append(more, addTask);
  // click, not change: a checkbox's change event needs a connected element; click fires either way.
  mine.input.addEventListener("click", () => void refetch());
  needsMe.input.addEventListener("click", () => { setAside(); void refetch(); });
  needTypeFilter.addEventListener("change", () => fill(latestRows));
  closed.input.addEventListener("click", () => { setAside(); void refetch(); });
  repo.addEventListener("change", () => void refetch());
  let typing: ReturnType<typeof setTimeout> | undefined;
  search.addEventListener("input", () => {
    clearTimeout(typing);
    typing = setTimeout(() => fill(latestRows), searchDebounce);
  });
  search.addEventListener("keydown", (e) => {
    if (e.key !== "Escape" || !search.value) return;
    e.preventDefault();
    clearTimeout(typing);
    search.value = "";
    fill(latestRows);
  });

  app.replaceChildren(header, tools, taskForm, form, hint, table, cliSetup(get));
}

export const roomId = /^[a-z2-7]{8}$/;

// searchDebounce is how long the search box waits after a keystroke before it filters the rows.
export const searchDebounce = 150;

// moreMenu is the filters row's secondary actions behind a "More" button, beside New task: for
// now one, Investigate with an agent, which opens the live-session form (open). The menu closes
// on a choice, Escape (focus back on the button) or a click elsewhere.
export function moreMenu(open: () => void): HTMLElement {
  const box = document.createElement("span");
  box.className = "more-menu";
  const btn = document.createElement("button");
  btn.type = "button";
  btn.dataset.act = "more";
  btn.setAttribute("aria-haspopup", "menu");
  btn.setAttribute("aria-expanded", "false");
  btn.setAttribute("aria-label", "More actions");
  const dots = document.createElement("span");
  dots.setAttribute("aria-hidden", "true");
  dots.textContent = "⋯";
  btn.append(dots, " More");
  const menu = document.createElement("div");
  menu.className = "more-items";
  menu.setAttribute("role", "menu");
  menu.hidden = true;
  const item = document.createElement("button");
  item.type = "button";
  item.setAttribute("role", "menuitem");
  item.dataset.act = "new-room";
  item.textContent = "Investigate with an agent";
  item.title = "A room you drive live in the sandbox, tied to no issue";
  item.setAttribute("aria-description", item.title);
  menu.append(item);
  const away = (e: Event) => { if (!box.contains(e.target as Node)) show(false); };
  const show = (on: boolean) => {
    menu.hidden = !on;
    btn.setAttribute("aria-expanded", String(on));
    if (on) {
      document.addEventListener("click", away);
      item.focus();
    } else document.removeEventListener("click", away);
  };
  btn.onclick = () => show(menu.hidden);
  item.onclick = () => {
    show(false);
    open();
  };
  box.addEventListener("keydown", (e) => {
    if (e.key !== "Escape" || menu.hidden) return;
    e.preventDefault();
    show(false);
    btn.focus();
  });
  box.append(btn, menu);
  return box;
}

// installMaku downloads the maku release for this machine, checks it against the release's
// checksums and installs it: the broker's own release, or the latest when the broker is not a
// release build. The version lands in a pasted shell block: only a whole semver passes. macOS
// has shasum, not always sha256sum.
function installMaku(version?: string): string {
  const base = /^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version ?? "")
    ? `https://github.com/Smana/maku/releases/download/${version}`
    : "https://github.com/Smana/maku/releases/latest/download";
  return [
    `os=$(uname -s | tr A-Z a-z); arch=$(uname -m); case $arch in x86_64) arch=amd64;; aarch64) arch=arm64;; esac`,
    `base=${base}`,
    `curl -fsSLO "$base/maku-$os-$arch" && curl -fsSLO "$base/maku.sha256"`,
    `command -v sha256sum >/dev/null && sum=sha256sum || sum="shasum -a 256"`,
    `grep " maku-$os-$arch$" maku.sha256 | $sum -c - && sudo install -m 0755 "maku-$os-$arch" /usr/local/bin/maku`,
  ].join("\n");
}

// continued is a command with each flag on its own line, continued with a backslash, so a
// long one reads at any width and still pastes and runs as is. One flag stays on its line.
function continued(cmd: string, flags: [string, string][]): string {
  const parts = flags.map(([f, v]) => `${f} ${v}`);
  return parts.length > 1 ? [cmd, ...parts.map((p) => `  ${p}`)].join(" \\\n") : [cmd, ...parts].join(" ");
}

// A setup step: its title, its command, and a muted note before or after it. A note is
// text and inline code, never markup.
interface SetupStep { title: string; code: string; before?: (string | { code: string })[]; after?: (string | { code: string })[] }

// setupSteps is what the fold shows for GET /api/roomctl's answer: a local broker's own
// steps, an OIDC broker's three, or, with no maku client, nothing.
function setupSteps(c: { url?: string; issuer?: string; clientID?: string; projectID?: string; version?: string; humanMode?: string }): SetupStep[] | undefined {
  const installStep: SetupStep = {
    title: "Install", code: installMaku(c.version),
    before: ["linux and macOS · amd64 and arm64 · checked against the release's sha256"],
  };
  if (c.humanMode === "local") {
    return [installStep,
      { title: "Point it at this broker", code: continued("maku configure", [["--url", `${c.url}`]]) },
      { title: "Start a run", code: continued("maku run", [["--repo", "<owner/name>"], ["--task", "'<what the run should do>'"]]) }];
  }
  if (!c.clientID) return;
  return [installStep,
    { title: "Point it at this broker", code: continued("maku configure",
      [["--url", `${c.url}`], ["--issuer", `${c.issuer}`], ["--client-id", c.clientID], ["--project-id", `${c.projectID}`]]) },
    { title: "Sign in", code: "maku login",
      after: ["Then: ", { code: "maku status" }, " · ", { code: "maku mcp" }, " for your agent's tools"] }];
}

// copiedFor is how long a Copy button says Copied before it reverts.
const copiedFor = 1500;

function note(parts: (string | { code: string })[]): HTMLElement {
  const p = document.createElement("p");
  p.className = "cli-note";
  p.append(...parts.map((x) => {
    if (typeof x === "string") return x;
    const c = document.createElement("code");
    c.textContent = x.code;
    return c;
  }));
  return p;
}

// stepItem renders one step: the command in its block, with a Copy button that writes the
// block's exact text. The label says Copied only once the clipboard took it; with no
// clipboard (an insecure origin) or a refused write it stays Copy, and the reader selects.
function stepItem(step: SetupStep, n: number, live: HTMLElement): HTMLLIElement {
  const li = document.createElement("li");
  li.className = "cli-step";
  const title = document.createElement("h3");
  title.className = "cli-step-title";
  title.id = `cli-step-${n}`;
  title.textContent = step.title;
  const pre = document.createElement("pre");
  pre.textContent = step.code;
  const copy = document.createElement("button");
  copy.type = "button";
  copy.className = "cli-copy";
  copy.textContent = "Copy";
  copy.setAttribute("aria-describedby", title.id);
  let revert: ReturnType<typeof setTimeout> | undefined;
  copy.onclick = () => {
    const write = navigator.clipboard?.writeText(step.code);
    write?.then(() => {
      clearTimeout(revert);
      copy.textContent = "Copied ✓";
      live.textContent = `Copied: ${step.title}`;
      revert = setTimeout(() => {
        copy.textContent = "Copy";
        if (live.textContent === `Copied: ${step.title}`) live.textContent = "";
      }, copiedFor);
    }, () => {});
  };
  const block = document.createElement("div");
  block.className = "cli-code";
  block.append(copy, pre); // the button floats top-right: the command's lines flow past it
  li.append(title);
  if (step.before) li.append(note(step.before));
  li.append(block);
  if (step.after) li.append(note(step.after));
  return li;
}

// cliSetup is the CLI setup view: maku's install, its configure line for this broker,
// then signing in (or, on a local broker, a first run), as numbered steps each with its
// own Copy button, read from GET /api/roomctl the first time it opens.
export function cliSetup(get: typeof fetch = fetch): HTMLDetailsElement {
  const d = document.createElement("details");
  d.className = "cli-setup";
  const summary = document.createElement("summary");
  summary.textContent = "Use maku from your terminal";
  const card = document.createElement("div");
  card.className = "cli-card";
  const live = document.createElement("span");
  live.className = "cli-live";
  live.setAttribute("role", "status");
  live.setAttribute("aria-live", "polite");
  d.append(summary, card, live);
  const say = (msg: string) => {
    const p = document.createElement("p");
    p.className = "cli-message";
    p.textContent = msg;
    card.replaceChildren(p);
  };
  let asked = false;
  d.addEventListener("toggle", async () => {
    if (!d.open || asked) return;
    asked = true;
    try {
      const r = await api("/api/roomctl", {}, get);
      const c = (await r.json()) as Parameters<typeof setupSteps>[0];
      const steps = r.ok ? setupSteps(c) : undefined;
      if (!steps) return say("maku is not set up on this broker: it has no maku client.");
      const ol = document.createElement("ol");
      ol.className = "cli-steps";
      ol.append(...steps.map((s, i) => stepItem(s, i + 1, live)));
      card.replaceChildren(ol);
    } catch (err) {
      asked = false;
      if (!(err instanceof SignInRequired)) say(`CLI setup unavailable: ${err instanceof Error ? err.message : String(err)}`);
    }
  });
  return d;
}

// Why POST /api/rooms refused (docs/reference/api.md).
const createErrors: Record<number, string> = {
  400: "That data class or repository is not accepted (repository is owner/name).",
  404: "You cannot read that repository on GitHub, or it does not exist.",
  409: "A room with that id already exists. Retry.",
  429: "Too many actions at once. Wait a moment and retry.",
  500: "The broker could not create the room: see its logs.",
  503: "Rooms cannot be created right now, or your GitHub access cannot be checked. Retry.",
};

// newRoomForm creates a room owned and driven by the caller, then opens it.
export function newRoomForm(get: typeof fetch = fetch): HTMLFormElement {
  const form = document.createElement("form");
  form.className = "new-room";
  const dataClass = document.createElement("select");
  dataClass.name = "dataClass";
  dataClass.setAttribute("aria-label", "data class");
  dataClass.required = true;
  // No default, least of all public: the class outlives every choice made after it (R11).
  const choose = document.createElement("option");
  choose.value = "";
  choose.textContent = "choose a data class";
  choose.disabled = choose.defaultSelected = true;
  dataClass.append(choose);
  for (const v of ["internal", "public"]) {
    const o = document.createElement("option");
    o.value = o.textContent = v;
    dataClass.append(o);
  }
  const repository = document.createElement("input");
  repository.name = "repository";
  repository.placeholder = "owner/name";
  repository.setAttribute("aria-label", "repository, owner/name");
  // D7: who can read the repository on GitHub can read the room, so a room names one.
  repository.required = true;
  const submit = document.createElement("button");
  submit.type = "submit";
  submit.textContent = "new room";
  const notice = document.createElement("span");
  notice.className = "notice";
  const hint = document.createElement("small");
  hint.className = "hint";
  hint.id = "data-class-hint";
  hint.textContent = "The data class cannot change once the room exists. It sets the model route, the tools and the egress " +
    "its runs get; it does not change who can read the room. The repository does: anyone who can read it on GitHub can.";
  dataClass.setAttribute("aria-describedby", hint.id);
  repository.setAttribute("aria-describedby", hint.id);
  // What this room is for, first: New task stays the way to ask the factory for work.
  const lead = document.createElement("p");
  lead.className = "new-room-lead";
  lead.textContent = "Investigate with an agent: a room you drive live in the sandbox, tied to no issue. " +
    "Explore or investigate with cluster tools before filing a task.";
  form.setAttribute("aria-label", "Investigate with an agent");
  form.append(lead, dataClass, repository, submit, notice, hint);
  form.onsubmit = async (e) => {
    e.preventDefault();
    if (!dataClass.value) { // required stops a click; this stops any other submit
      notice.textContent = "Choose a data class first.";
      return;
    }
    if (!repository.value.trim()) {
      notice.textContent = "Name the room's repository first (owner/name).";
      return;
    }
    const body = { dataClass: dataClass.value, repository: repository.value.trim() };
    notice.textContent = "";
    try {
      const r = await api("/api/rooms", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }, get);
      if (r.status !== 201) {
        notice.textContent = createErrors[r.status] ?? `The room was not created: ${r.status}`;
        return;
      }
      const { id } = (await r.json()) as { id?: unknown };
      if (typeof id !== "string" || !roomId.test(id)) {
        notice.textContent = "The broker answered an unexpected room id.";
        return;
      }
      nav.go(`/r/${id}`);
    } catch (err) {
      if (!(err instanceof SignInRequired)) notice.textContent = `The room was not created: ${err instanceof Error ? err.message : String(err)}`;
    }
  };
  return form;
}
