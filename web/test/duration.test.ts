import { describe, expect, it } from "vitest";
import table from "../../internal/roomview/testdata/durations.json";
import { ago, span } from "../src/duration";

describe("duration", () => {
  it.each(table)("$ms ms", ({ ms, span: s, ago: a }) => {
    expect(span(ms)).toBe(s);
    expect(ago(ms)).toBe(a);
  });
  it("reads a negative input as 0", () => {
    expect(span(-5000)).toBe("0 s");
    expect(ago(-5000)).toBe("just now");
  });
});
