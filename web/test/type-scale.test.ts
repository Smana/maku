import { readFileSync, existsSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const css = readFileSync(join(__dirname, "../src/app.css"), "utf8");
// A shorthand's size is a token, followed by its own leading token, at the start of the size slot.
const SHORTHAND = /^inherit$|^(?:[\w.]+\s+)*var\(--text-(display|heading|body|small|eyebrow)\)\/var\(--leading-\1\)(?:\s|$)/;
const fonts = join(__dirname, "../src/assets/fonts");

describe("fonts", () => {
  it.each(["SpaceGrotesk-Variable.woff2", "Inter-Variable.woff2", "Inter-Variable-Italic.woff2", "JetBrainsMono-Variable.woff2",
    "LICENSE-space-grotesk.txt", "LICENSE-inter.txt", "LICENSE-jetbrains-mono.txt"])("ships %s", (f) => {
    expect(existsSync(join(fonts, f))).toBe(true);
  });
  it("loads nothing from another origin", () => {
    expect(css).not.toMatch(/url\(\s*["']?https?:/);
    expect(css).not.toMatch(/@import/);
  });
  it("names a family only through the three tokens", () => {
    const rules = css.replace(/@font-face\s*{[^}]*}/g, "");
    const decls = [...rules.matchAll(/font-family:\s*([^;]+);/g)].map((m) => m[1].trim());
    for (const d of decls) expect(d, d).toMatch(/^var\(--font-(display|sans|mono)\)$|^inherit$/);
  });
});

describe("type scale", () => {
  it("sizes text only through the five tokens", () => {
    const decls = [...css.matchAll(/font-size:\s*([^;]+);/g)].map((m) => m[1].trim());
    for (const d of decls) expect(d, d).toMatch(/^var\(--text-(display|heading|body|small|eyebrow)\)$|^inherit$|^1em$/);
  });
  it("sizes a font shorthand only through a token and its own leading", () => {
    const rules = css.replace(/\/\*[\s\S]*?\*\//g, "");
    const decls = [...rules.matchAll(/(?:^|[{;\s])font:\s*([^;}]+)/g)].map((m) => m[1].trim());
    expect(decls.length).toBeGreaterThan(0);
    for (const d of decls) expect(d, d).toMatch(SHORTHAND);
  });
  it("refuses a literal size in a shorthand, next to a token or not", () => {
    for (const bad of ["500 13px/16px var(--font-mono)", "500 var(--text-small)/20px var(--font-mono)",
      "500 14px/var(--leading-small) var(--font-mono)", "500 var(--text-small)/var(--leading-body) var(--font-mono)"])
      expect(bad).not.toMatch(SHORTHAND);
    expect("500 var(--text-eyebrow)/var(--leading-eyebrow) var(--font-mono)").toMatch(SHORTHAND);
  });
  it("defines the scale", () => {
    for (const [k, v] of [["display", "32px"], ["heading", "22px"], ["body", "16px"], ["small", "14px"], ["eyebrow", "13px"]])
      expect(css).toMatch(new RegExp(`--text-${k}:\\s*${v}`));
    for (const [k, v] of [["display", "40px"], ["heading", "30px"], ["body", "26px"], ["small", "20px"], ["eyebrow", "16px"]])
      expect(css).toMatch(new RegExp(`--leading-${k}:\\s*${v}`));
  });
  it("keeps 13px for mono uppercase eyebrows", () => {
    // every rule that sets var(--text-eyebrow) also sets the mono family and uppercase
    for (const m of css.matchAll(/([^{}]+)\{([^}]*var\(--text-eyebrow\)[^}]*)\}/g)) {
      expect(m[2], m[1]).toMatch(/var\(--font-mono\)/);
      expect(m[2], m[1]).toMatch(/text-transform:\s*uppercase/);
      expect(m[2], m[1]).toMatch(/letter-spacing:\s*0\.08em/);
    }
  });
});

it("sets the markdown h1 at the heading size and h2 to h6 at body size, bold", () => {
  expect(css).toMatch(/\.md h1\s*{[^}]*font-size:\s*var\(--text-heading\)/);
  expect(css).toMatch(/\.md :is\(h2, h3, h4, h5, h6\)\s*{[^}]*font-size:\s*var\(--text-body\)[^}]*font-weight:\s*700/);
});

it("keeps a column head on one line", () => {
  expect(css).toMatch(/\.rooms th\s*{[^}]*white-space:\s*nowrap/);
});
