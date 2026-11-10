import { readFileSync, existsSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const css = readFileSync(join(__dirname, "../src/app.css"), "utf8");
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
