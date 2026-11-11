import { describe, expect, it } from "vitest";
import { glyph, glyphFor, type GlyphName } from "../src/glyphs";

const names: GlyphName[] = ["scope", "search", "checklist", "tools", "eye", "branch", "person", "github", "shield", "chat"];

describe("glyphFor", () => {
  it.each([
    ["scoper", "triager", "scope"], ["triager", "triager", "search"], ["tester", "x", "checklist"],
    ["implementer", "x", "tools"], ["reviewer", "x", "eye"],
    ["security-reviewer", "reviewer", "eye"], ["unknown", "tester", "checklist"],
    ["x", "x", "tools"], ["constructor", "__proto__", "tools"],
  ])("%s / %s is %s", (p, r, want) => expect(glyphFor(p, r)).toBe(want));
});

describe("glyph", () => {
  it.each(names)("%s is a 24 grid outline in currentColor", (n) => {
    const svg = glyph(n);
    expect(svg.getAttribute("viewBox")).toBe("-12 -12 24 24");
    expect([svg.getAttribute("stroke"), svg.getAttribute("stroke-width")]).toEqual(["currentColor", "2"]);
    expect(svg.querySelectorAll("path").length).toBeGreaterThan(0);
    expect(svg.querySelector("circle, rect, line, g")).toBeNull();
    for (const x of [svg, ...svg.querySelectorAll("*")]) {
      for (const a of x.attributes) expect(a.name === "style" || a.name.startsWith("on"), a.name).toBe(false);
    }
  });
  it("names a glyph that is the only sign of a state", () => {
    const svg = glyph("eye", "reviewing");
    expect([svg.getAttribute("role"), svg.getAttribute("aria-label")]).toEqual(["img", "reviewing"]);
    expect(glyph("eye").getAttribute("aria-hidden")).toBe("true");
  });
});
