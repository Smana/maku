// SPDX-License-Identifier: Apache-2.0

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  DEFAULT_SORT, listRooms, matchesQuery, moreMenu, nextSort, readSort, searchDebounce, SORT_STORAGE_KEY, sortRooms,
  storeSort, type RoomRow,
} from "../src/view";

const base = { owner: "", driver: "", dataClass: "internal", lastSeq: 1, phase: "Active" };
const at = (h: number) => `2026-11-26T${String(h).padStart(2, "0")}:00:00Z`;
const st = (kind: string) => ({ kind, icon: "", text: kind });
const rows: RoomRow[] = [
  { ...base, id: "aaaaaaaa", repository: "o/zeta", issue: 579, pr: 580, title: "Fix the install link", lastEventAt: at(9), status: st("running") },
  { ...base, id: "bbbbbbbb", repository: "o/alpha", issue: 12, title: "Rooms sorting", lastEventAt: at(11), status: st("done") },
  { ...base, id: "cccccccc", repository: "O/Beta", issue: 300, pr: 9, title: "Escalated thing", lastEventAt: at(8), status: st("waiting"), needsYou: 1,
    need: { kind: "question", id: "n", what: "q" } },
  { ...base, id: "dddddddd", repository: "o/alpha", lastEventAt: at(10), status: st("failed") },
];
const ids = (r: RoomRow[]) => r.map((x) => x.id);

describe("sortRooms", () => {
  it("pins the rooms that need you on top in the default sort, then newest activity", () => {
    expect(ids(sortRooms(rows, DEFAULT_SORT))).toEqual(["cccccccc", "bbbbbbbb", "dddddddd", "aaaaaaaa"]);
  });
  it("drops the pin on any other column, and on last activity reversed", () => {
    expect(ids(sortRooms(rows, { key: "last", dir: "asc" }))).toEqual(["cccccccc", "aaaaaaaa", "dddddddd", "bbbbbbbb"]);
    expect(ids(sortRooms(rows, { key: "repo", dir: "asc" }))).toEqual(["bbbbbbbb", "dddddddd", "cccccccc", "aaaaaaaa"]);
    expect(ids(sortRooms(rows, { key: "repo", dir: "desc" }))).toEqual(["aaaaaaaa", "cccccccc", "bbbbbbbb", "dddddddd"]);
  });
  it("orders a status most severe first: failed, escalated and needs-human together, ended last", () => {
    expect(ids(sortRooms(rows, { key: "status", dir: "asc" }))).toEqual(["dddddddd", "cccccccc", "aaaaaaaa", "bbbbbbbb"]);
    expect(ids(sortRooms(rows, { key: "status", dir: "desc" }))).toEqual(["bbbbbbbb", "aaaaaaaa", "cccccccc", "dddddddd"]);
    const unknown = { ...rows[0], id: "eeeeeeee", status: undefined };
    expect(sortRooms([unknown, rows[1]], { key: "status", dir: "asc" })[1].id).toBe("eeeeeeee");
  });
  it("sorts PR and Task by number, rooms without one last whichever way", () => {
    expect(ids(sortRooms(rows, { key: "pr", dir: "desc" }))).toEqual(["aaaaaaaa", "cccccccc", "bbbbbbbb", "dddddddd"]);
    expect(ids(sortRooms(rows, { key: "pr", dir: "asc" }))).toEqual(["cccccccc", "aaaaaaaa", "bbbbbbbb", "dddddddd"]);
    expect(ids(sortRooms(rows, { key: "task", dir: "desc" }))).toEqual(["aaaaaaaa", "cccccccc", "bbbbbbbb", "dddddddd"]);
    expect(ids(sortRooms(rows, { key: "task", dir: "asc" }))).toEqual(["bbbbbbbb", "cccccccc", "aaaaaaaa", "dddddddd"]);
  });
  it("breaks ties on fields a refresh does not change, so an order holds whatever the input order", () => {
    const same = rows.map((r) => ({ ...r, repository: "o/same" }));
    const one = ids(sortRooms(same, { key: "repo", dir: "asc" }));
    expect(ids(sortRooms([...same].reverse(), { key: "repo", dir: "asc" }))).toEqual(one);
    // newer activity on a row does not move it within its repository
    const touched = same.map((r) => (r.id === "dddddddd" ? { ...r, lastEventAt: at(12) } : r));
    expect(ids(sortRooms(touched, { key: "repo", dir: "asc" }))).toEqual(one);
  });
  it("reverses on a second click and starts each column in its natural order", () => {
    expect(nextSort(DEFAULT_SORT, "last")).toEqual({ key: "last", dir: "asc" });
    expect(nextSort(DEFAULT_SORT, "repo")).toEqual({ key: "repo", dir: "asc" });
    expect(nextSort(DEFAULT_SORT, "status")).toEqual({ key: "status", dir: "asc" });
    expect(nextSort(DEFAULT_SORT, "pr")).toEqual({ key: "pr", dir: "desc" });
    expect(nextSort({ key: "pr", dir: "desc" }, "pr")).toEqual({ key: "pr", dir: "asc" });
  });
});

describe("the stored sort", () => {
  afterEach(() => localStorage.clear());
  it("round-trips, and falls back to the default on junk or a storage that throws", () => {
    storeSort({ key: "repo", dir: "desc" }, localStorage);
    expect(readSort(localStorage)).toEqual({ key: "repo", dir: "desc" });
    localStorage.setItem(SORT_STORAGE_KEY, "{not json");
    expect(readSort(localStorage)).toEqual(DEFAULT_SORT);
    localStorage.setItem(SORT_STORAGE_KEY, JSON.stringify({ key: "room", dir: "asc" }));
    expect(readSort(localStorage)).toEqual(DEFAULT_SORT);
    const broken = { getItem: () => { throw new Error("denied"); }, setItem: () => { throw new Error("denied"); } } as unknown as Storage;
    expect(readSort(broken)).toEqual(DEFAULT_SORT);
    expect(() => storeSort({ key: "pr", dir: "asc" }, broken)).not.toThrow();
  });
});

describe("matchesQuery", () => {
  it("matches an issue or PR number, with or without #, and text in title, repository and room id", () => {
    const r = rows[0];
    for (const q of ["#579", "579", " 580 ", "install", "INSTALL LINK", "o/zeta", "aaaa", ""]) expect(matchesQuery(r, q), q).toBe(true);
    for (const q of ["#57", "581", "beta", "zzz"]) expect(matchesQuery(r, q), q).toBe(false);
  });
});

describe("the room list's headers, search and More menu", () => {
  const manualClock = { now: () => 0, set: () => 0, clear: () => {} };
  const mount = async (answers: RoomRow[][], filters = {}) => {
    let n = 0;
    const get = vi.fn(async () => new Response(JSON.stringify(answers[Math.min(n++, answers.length - 1)])));
    const root = document.createElement("div");
    document.body.append(root);
    await listRooms(root, get as unknown as typeof fetch, filters, undefined, { clock: manualClock, store: localStorage });
    return root;
  };
  const shown = (root: HTMLElement) => [...root.querySelectorAll("tbody td.room-id")].map((td) => td.textContent);
  const head = (root: HTMLElement, key: string) => root.querySelector(`button.sort[data-sort="${key}"]`) as HTMLButtonElement;
  afterEach(() => { document.body.replaceChildren(); localStorage.clear(); vi.useRealTimers(); });

  it("puts a button in each sortable header and marks the active one with aria-sort and an arrow", async () => {
    const root = await mount([rows]);
    expect([...root.querySelectorAll("th button.sort")].map((b) => (b as HTMLElement).dataset.sort)).toEqual(["task", "repo", "status", "pr", "last"]);
    expect(root.querySelectorAll("th[aria-sort]")).toHaveLength(1);
    expect(head(root, "last").closest("th")?.getAttribute("aria-sort")).toBe("descending");
    expect(head(root, "last").textContent).toBe("Last activity↓");
    head(root, "repo").click();
    expect(head(root, "repo").closest("th")?.getAttribute("aria-sort")).toBe("ascending");
    expect(head(root, "repo").textContent).toBe("Repository↑");
    expect(head(root, "last").closest("th")?.hasAttribute("aria-sort")).toBe(false);
    expect(shown(root)).toEqual(["bbbbbbbb", "dddddddd", "cccccccc", "aaaaaaaa"]);
    head(root, "repo").click();
    expect(shown(root)).toEqual(["aaaaaaaa", "cccccccc", "bbbbbbbb", "dddddddd"]);
    expect(JSON.parse(localStorage.getItem(SORT_STORAGE_KEY)!)).toEqual({ key: "repo", dir: "desc" });
  });
  it("opens on the order this browser chose last", async () => {
    storeSort({ key: "status", dir: "asc" }, localStorage);
    const root = await mount([rows]);
    expect(shown(root)).toEqual(["dddddddd", "cccccccc", "aaaaaaaa", "bbbbbbbb"]);
  });
  it("filters the loaded rows after a pause in typing, says what matched nothing, and clears on Escape", async () => {
    vi.useFakeTimers();
    const root = await mount([rows]);
    const search = root.querySelector('input[name="q"]') as HTMLInputElement;
    expect(search.getAttribute("aria-label")).toBe("Search rooms");
    search.value = "#579";
    search.dispatchEvent(new Event("input"));
    expect(shown(root)).toHaveLength(4); // debounced
    vi.advanceTimersByTime(searchDebounce);
    expect(shown(root)).toEqual(["aaaaaaaa"]);
    search.value = "nothing here";
    search.dispatchEvent(new Event("input"));
    vi.advanceTimersByTime(searchDebounce);
    expect(root.querySelector("tbody td")?.textContent).toBe('No rooms match "nothing here"');
    search.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(search.value).toBe("");
    expect(shown(root)).toHaveLength(4);
  });
  it("searches the closed view's rows too", async () => {
    vi.useFakeTimers();
    const root = await mount([rows], { closed: true });
    const search = root.querySelector('input[name="q"]') as HTMLInputElement;
    search.value = "alpha";
    search.dispatchEvent(new Event("input"));
    vi.advanceTimersByTime(searchDebounce);
    expect(shown(root)).toEqual(["bbbbbbbb", "dddddddd"]);
  });
  it("opens Investigate with an agent from More, and explains it in the form", async () => {
    const root = await mount([rows]);
    const more = root.querySelector('button[data-act="more"]') as HTMLButtonElement;
    const menu = root.querySelector("[role=menu]") as HTMLElement;
    const form = root.querySelector("form.new-room") as HTMLFormElement;
    expect(more.getAttribute("aria-expanded")).toBe("false");
    more.click();
    expect(menu.hidden).toBe(false);
    expect(more.getAttribute("aria-expanded")).toBe("true");
    expect(document.activeElement?.textContent).toBe("Investigate with an agent");
    (menu.querySelector("[role=menuitem]") as HTMLButtonElement).click();
    expect(menu.hidden).toBe(true);
    expect(form.hidden).toBe(false);
    expect(form.querySelector(".new-room-lead")?.textContent).toMatch(/drive live in the sandbox, tied to no issue.*cluster tools before filing a task/);
  });
});

describe("moreMenu", () => {
  afterEach(() => document.body.replaceChildren());
  it("closes on Escape with the focus back on its button, and on a click elsewhere", () => {
    const open = vi.fn();
    const m = moreMenu(open);
    document.body.append(m);
    const btn = m.querySelector("button[data-act=more]") as HTMLButtonElement;
    const menu = m.querySelector("[role=menu]") as HTMLElement;
    btn.click();
    m.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(menu.hidden).toBe(true);
    expect(document.activeElement).toBe(btn);
    btn.click();
    document.body.click();
    expect(menu.hidden).toBe(true);
    expect(open).not.toHaveBeenCalled();
  });
});
