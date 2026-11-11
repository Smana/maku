import { describe, expect, it } from "vitest";
import table from "../../internal/roomview/testdata/durations.json";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { ago, span, timeEl } from "../src/duration";

describe("duration", () => {
  it.each(table)("$ms ms", ({ ms, span: s, ago: a }) => {
    expect(span(ms)).toBe(s);
    expect(ago(ms)).toBe(a);
  });
  it("reads a negative input as 0", () => {
    expect(span(-5000)).toBe("0 s");
    expect(ago(-5000)).toBe("just now");
  });
  it("keeps a duration on one line: \"37 d ago\" never breaks between its number and unit", () => {
    expect(timeEl(5000, "2026-11-11T10:00:00Z", "ago").classList.contains("dur")).toBe(true);
    const css = readFileSync(join(__dirname, "../src/app.css"), "utf8");
    expect(css).toMatch(/\.dur \{ white-space: nowrap; \}/);
  });
});
