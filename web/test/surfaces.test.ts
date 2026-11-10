import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const css = readFileSync(join(__dirname, "../src/app.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
const rules = [...css.matchAll(/([^{}@][^{}]*)\{([^{}]*)\}/g)].map((m) => ({ sel: m[1].trim(), body: m[2] }));
const rule = (sel: string) => rules.filter((r) => r.sel === sel).map((r) => r.body).join(";");

describe("the aurora gradient", () => {
  it("is drawn on the budget bar and the evidence bar only", () => {
    expect(css).not.toMatch(/radial-gradient/);
    const users = rules.filter((r) => /var\(--aurora-bar\)/.test(r.body)).map((r) => r.sel).sort();
    expect(users).toEqual([".evidence-fill", ".meter-bar::after"].sort());
    // the only other gradients are the two scroll masks and the clip fade, never a fill of colour
    for (const r of rules) {
      if (/linear-gradient\(90deg, #/.test(r.body)) expect(r.sel).toBe(":root");
    }
  });
  it("keeps a blocked evidence bar muted, not gradient", () => {
    const b = rule('.evidence-bar[data-blocked="true"] .evidence-fill');
    expect(b).toMatch(/background:\s*var\(--muted-foreground\)/);
  });
});

describe("cards", () => {
  const cards = [".class-card", ".settings-card", ".cli-card", ".example-card", ".policy-header", ".settings .proposal",
    ".approval", ".needs-other li.need", ".tool", ".not-migrated", ".grant", ".sk-msg"];
  it.each(cards)("%s takes the card shape", (sel) => {
    const b = rule(sel);
    expect(b).toMatch(/border-radius:\s*var\(--radius-md\)/);
    // a .tool is a dense one-line row: its summary keeps its own padding, the card shape is the frame
    if (sel !== ".tool" && sel !== ".sk-msg") expect(b).toMatch(/padding:\s*var\(--space-4\)/);
    expect(b).toMatch(/border:\s*1px solid var\(--border\)/);
    expect(b).not.toMatch(/box-shadow:\s*var\(--shadow-border/);
  });
  it("a hovered card raises through opacity or transform, never a background transition", () => {
    expect(rule(".settings-card::after")).toMatch(/background:\s*var\(--surface-raised\)/);
    for (const r of rules) {
      if (/transition/.test(r.body)) expect(r.body, r.sel).not.toMatch(/transition(-property)?:[^;]*\bbackground\b(?!-color)/);
    }
  });
});

describe("focus", () => {
  // A ring that must sit inside a clipped container keeps an inset offset.
  const insetAllowed: Record<string, string> = {
    ".settings-nav a:focus-visible": "inset only inside the max-width 640px media block, where the nav is an overflow-x:auto row that clips an outset ring; above 640px it takes the standard outer ring",
    ".settings .expert-yaml": "the textarea fills an overflow:hidden editor frame",
  };
  const focusRules = rules.filter((r) => /:focus-visible/.test(r.sel) && /outline:/.test(r.body));
  it("has focus rules to check", () => expect(focusRules.length).toBeGreaterThan(5));
  it.each(focusRules.map((r) => [r.sel, r.body]))("%s is a 2px solid ring with a 2px gap", (sel, body) => {
    expect(body).toMatch(/outline:\s*2px solid var\(--ring\)/);
    const off = body.match(/outline-offset:\s*([^;]+)/)?.[1].trim();
    // the nav's base rule is the outer ring; its inset sits in the 640px media block (checked below)
    expect(off, sel).toBe(sel === ".settings .expert-yaml" ? "-2px" : "2px");
  });
  it("lets only the allowlisted rules set an inset offset", () => {
    for (const r of rules) {
      if (/outline-offset:\s*-/.test(r.body)) expect(Object.keys(insetAllowed), r.sel).toContain(r.sel);
    }
  });
  it("insets the settings nav ring only in the phone block", () => {
    const m = css.match(/@media \(max-width: 640px\) \{[\s\S]*?\.settings-nav a:focus-visible \{\s*outline-offset:\s*-2px/);
    expect(m).not.toBeNull();
  });
  it("draws no focus ring with a box-shadow", () => {
    for (const r of rules) if (/:focus|:target/.test(r.sel)) expect(r.body, r.sel).not.toMatch(/box-shadow/);
  });
});

describe("the next-step box", () => {
  it("has no side stripe", () => {
    const b = rule(".strip-next");
    expect(b).not.toMatch(/border-(left|right|inline)/);
  });
});
