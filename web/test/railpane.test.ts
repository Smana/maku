// SPDX-License-Identifier: Apache-2.0
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { mountRailPane } from "../src/railpane";
import type { Flow, FlowNode } from "../src/summary";

const node = (persona: string, o: Partial<FlowNode> = {}): FlowNode =>
  ({ run: persona, persona, role: persona, round: 1, fromSeq: 1, toSeq: 2, tools: { ok: 0, failed: 0 }, ...o });
const flow: Flow = {
  nodes: [node("scoper", { endedAt: "2026-01-01T00:00:00Z", outcome: "succeeded" }), node("tester", { endedAt: "2026-01-01T00:00:00Z", outcome: "succeeded" }), node("implementer")],
  ghosts: ["reviewer"],
};
const mount = (f: Flow) => {
  const pane = mountRailPane(() => {});
  document.body.append(pane.body);
  pane.show(f, "running", Date.now());
  return { pane, dots: pane.body.querySelector<HTMLButtonElement>(".rail-dots")! };
};

describe("the narrow strip", () => {
  it("reads as a labelled path", () => {
    expect(mount(flow).dots.textContent).toBe("scoper ✓ · tester ✓ · implementer ● · reviewer ○");
  });
  it("names every step in words", () => {
    expect(mount(flow).dots.getAttribute("aria-label")).toBe(
      "Show the task path: scoper done, tester done, implementer running, reviewer planned");
  });
  it("gives a failed run the rail's ✗ and its word", () => {
    const { dots } = mount({ nodes: [node("implementer", { endedAt: "x", outcome: "failed" })] });
    expect(dots.textContent).toBe("implementer ✗");
    expect(dots.getAttribute("aria-label")).toContain("implementer failed");
  });
  it("opens the drawer and says so", () => {
    const { pane, dots } = mount(flow);
    expect(dots.getAttribute("aria-controls")).toBe(pane.rail.el.id);
    expect(dots.getAttribute("aria-expanded")).toBe("false");
    dots.click();
    expect(pane.rail.el.classList.contains("open")).toBe(true);
    expect(dots.getAttribute("aria-expanded")).toBe("true");
    pane.body.querySelector<HTMLElement>(".rail-close")!.click();
    expect(dots.getAttribute("aria-expanded")).toBe("false");
  });
});

describe("the strip's stylesheet", () => {
  const css = readFileSync(join(__dirname, "../src/app.css"), "utf8");
  const body = (sel: string) => css.match(new RegExp(`\\n${sel.replace(/[.]/g, "\\.")}\\s*{([^}]*)}`))![1];
  it("wraps within the window, on the type scale", () => {
    expect(body(".rail-dot")).toMatch(/white-space:\s*nowrap/);
    expect(body(".rail-dots")).toMatch(/overflow-wrap:\s*anywhere/);
    expect(body(".rail-dots")).toMatch(/font-size:\s*var\(--text-small\)/);
  });
});
