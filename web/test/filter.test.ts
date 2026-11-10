// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { filterField } from "../src/filter";

const names = ["Smana/maku", "Smana/cloud-native-ref", "Smana/legacy"];
const mount = (n = names) => {
  const list = document.createElement("ul");
  for (const t of n) { const li = document.createElement("li"); li.textContent = t; list.append(li); }
  const f = filterField({ label: "Filter repositories", noun: ["repository", "repositories"], items: () => [...list.querySelectorAll("li")], text: (li) => li.textContent ?? "" });
  document.body.replaceChildren(f.box, list);
  return { ...f, list, input: f.box.querySelector("input") as HTMLInputElement };
};
const type = (input: HTMLInputElement, v: string) => { input.value = v; input.dispatchEvent(new Event("input")); };
const shown = (list: HTMLElement) => [...list.children].filter((c) => !(c as HTMLElement).hidden).map((c) => c.textContent);

describe("filterField", () => {
  beforeEach(() => history.replaceState(null, "", "/"));
  afterEach(() => { document.body.replaceChildren(); history.replaceState(null, "", "/"); });

  const four = [...names, "Smana/zeta"];

  it("renders nothing to filter at three items or fewer, a field from four", () => {
    expect(mount().box.children).toHaveLength(0);
    expect(mount(four).box.querySelector("input")).not.toBeNull();
  });

  it("matches case-insensitively, every word, and counts", () => {
    const m = mount(four);
    expect(m.box.querySelector("label")?.textContent).toBe("Filter repositories");
    expect(m.box.querySelector("[aria-live=polite]")?.textContent).toBe("4 of 4 repositories");
    type(m.input, "cloud");
    expect(shown(m.list)).toEqual(["Smana/cloud-native-ref"]);
    expect(m.box.querySelector("[aria-live=polite]")?.textContent).toBe("1 of 4 repositories");
    type(m.input, "smana LEG");
    expect(shown(m.list)).toEqual(["Smana/legacy"]);
    type(m.input, "cloud legacy");
    expect(shown(m.list)).toEqual([]);
  });

  it("says nothing matches and clears from a button", () => {
    const m = mount(four);
    type(m.input, "zzz");
    expect(shown(m.list)).toEqual([]);
    expect(m.box.querySelector(".filter-empty")?.textContent).toContain('No repository matches "zzz".');
    (m.box.querySelector(".filter-empty button") as HTMLButtonElement).click();
    expect(m.input.value).toBe("");
    expect(shown(m.list)).toHaveLength(4);
  });

  it("writes the query to ?q= keeping other params, and reads it on mount", () => {
    history.replaceState(null, "", "/?path=%2Fx&theme=dark");
    const m = mount(four);
    type(m.input, "legacy");
    const q = new URLSearchParams(location.search);
    expect([q.get("q"), q.get("path"), q.get("theme")]).toEqual(["legacy", "/x", "dark"]);
    const again = mount(four);
    expect(again.input.value).toBe("legacy");
    expect(shown(again.list)).toEqual(["Smana/legacy"]);
    type(again.input, "");
    expect(new URLSearchParams(location.search).has("q")).toBe(false);
  });

  it("focuses on / unless typing in a field, and clears on Escape", () => {
    const m = mount(four);
    const other = document.createElement("textarea");
    document.body.append(other);
    other.dispatchEvent(new KeyboardEvent("keydown", { key: "/", bubbles: true }));
    expect(document.activeElement).not.toBe(m.input);
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "/", bubbles: true }));
    expect(document.activeElement).toBe(m.input);
    type(m.input, "maku");
    m.input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(m.input.value).toBe("");
    expect(shown(m.list)).toHaveLength(4);
  });
});
