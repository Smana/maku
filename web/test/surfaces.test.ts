import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const css = readFileSync(join(__dirname, "../src/app.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
const rules = [...css.matchAll(/([^{}@][^{}]*)\{([^{}]*)\}/g)].map((m) => ({ sel: m[1].trim(), body: m[2] }));
const rule = (sel: string) => rules.filter((r) => r.sel === sel).map((r) => r.body).join(";");

describe("the aurora gradient", () => {
  it("is drawn on the budget bar only", () => {
    expect(css).not.toMatch(/radial-gradient/);
    const users = rules.filter((r) => /var\(--aurora-bar\)/.test(r.body)).map((r) => r.sel).sort();
    // At most one gradient per screen: the repository page draws one evidence bar per class, so
    // its fill is solid and the aurora stays on the room page's one budget bar.
    expect(users).toEqual([".meter-bar::after"]);
    expect(rule(".evidence-fill")).toMatch(/background:\s*var\(--primary\)/);
    // the only other gradients are the two scroll masks and the clip fade, never a fill of colour
    for (const r of rules) {
      if (/linear-gradient\(90deg, #/.test(r.body)) expect([":root", ".dark"]).toContain(r.sel);
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
    ".settings .expert-yaml:focus-visible": "the textarea fills an overflow:hidden editor frame",
  };
  const focusRules = rules.filter((r) => /:focus-visible/.test(r.sel) && /outline:/.test(r.body));
  it("has focus rules to check", () => expect(focusRules.length).toBeGreaterThan(5));
  it.each(focusRules.map((r) => [r.sel, r.body]))("%s is a 2px solid ring with a 2px gap", (sel, body) => {
    expect(body).toMatch(/outline:\s*2px solid var\(--ring\)/);
    const off = body.match(/outline-offset:\s*([^;]+)/)?.[1].trim();
    // the nav's base rule is the outer ring; its inset sits in the 640px media block (checked below)
    expect(off, sel).toBe(sel === ".settings .expert-yaml:focus-visible" ? "-2px" : "2px");
  });
  it("rings every allowlisted rule, so each inset is checked as a ring too", () => {
    for (const sel of Object.keys(insetAllowed)) expect(focusRules.map((r) => r.sel), sel).toContain(sel);
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

describe("a status", () => {
  // A status is a word plus a colour; the colour sits on the card's own 1 px border or on the
  // status icon, never on a side stripe curving round a 16 px corner.
  const noStripe = [".tool.st-rejected", ".tool.st-error", ".rooms .row-status"];
  it.each(noStripe)("%s draws no side stripe", (sel) => {
    const b = rule(sel);
    expect(b).not.toMatch(/inset\s+-?\d+px\s+0\s+0/);
    expect(b).not.toMatch(/border-(left|right|inline)/);
  });
  // The one allowed status stripe: the approval and need cards keep the owner's warning bar
  // (#345), a ::before drawn straight between the 16 px corners, not a border or an inset shadow.
  it.each([".approval", ".needs-other li.need"])("%s carries no border or inset stripe on the card itself", (sel) => {
    const b = rule(sel);
    expect(b).not.toMatch(/inset\s+-?\d+px\s+0\s+0/);
    expect(b).not.toMatch(/border-(left|right|inline)/);
  });
  it("draws the approval and need bar as the one allowed stripe, flush with the card's outer edge", () => {
    const bar = rule(".approval::before, .needs-other li.need::before");
    expect(bar).toMatch(/left:\s*-1px/);
    expect(bar).toMatch(/width:\s*3px/);
    expect(bar).toMatch(/background:\s*var\(--warning-border\)/);
  });
  it.each([
    [".tool.st-rejected", "--warning-border"],
    [".tool.st-error", "--destructive"],
  ])("%s tones its border with %s", (sel, tok) => {
    expect(rule(sel)).toMatch(new RegExp(`border-color:\\s*var\\(${tok}\\)`));
  });
  it.each([
    ["running", "--primary"], ["waiting-you", "--warning-border"], ["finished", "--success"], ["failed", "--destructive"],
  ])("colours a room row's %s icon with %s", (kind, tok) => {
    const sel = rules.filter((r) => r.sel.includes(`.rooms .row-status[data-kind="${kind}"] .row-status-icon`));
    expect(sel.map((r) => r.body).join(";")).toMatch(new RegExp(`color:\\s*var\\(${tok}\\)`));
  });
});

describe("a selected card", () => {
  // The one card a person selects is the class a link points at (#class-<name>).
  it("adds a 1 px primary border", () => {
    expect(rule(".class-card:target")).toMatch(/border-color:\s*var\(--primary\)/);
  });
});

describe("the browser's own surfaces", () => {
  it("tints checkboxes, radios and text selection from the palette", () => {
    expect(rule("html")).toMatch(/accent-color:\s*var\(--primary\)/);
    expect(rule("::selection")).toMatch(/background:\s*color-mix\(in srgb, var\(--primary\) 25%, transparent\)/);
  });
});
