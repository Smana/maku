import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// Every text pair the page paints must meet WCAG AA (4.5:1); meaningful borders 3:1.
// color-mix(in srgb, A p%, B) is a channel-wise lerp, which is how the chips are tinted.
const css = readFileSync(join(__dirname, "../src/app.css"), "utf8");
function tokens(block: string): Record<string, string> {
  const m = css.match(new RegExp(`${block.replace(".", "\\.")}\\s*{([^}]*)}`))!;
  return Object.fromEntries([...m[1].matchAll(/(--[\w-]+):\s*(#[0-9a-f]{6})/gi)].map((x) => [x[1], x[2].toLowerCase()]));
}
const rgb = (h: string) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
const lum = (h: string) => rgb(h).map((c) => { c /= 255; return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; })
  .reduce((s, c, i) => s + c * [0.2126, 0.7152, 0.0722][i], 0);
export const ratio = (a: string, b: string) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
const mix = (a: string, b: string, pa: number) => "#" + rgb(a).map((c, i) => Math.round(c * pa + rgb(b)[i] * (1 - pa)).toString(16).padStart(2, "0")).join("");

const light = tokens(":root"), dark = tokens(".dark");

type Tokens = Record<string, string>;
// Pairs held to their minimum, with the Ogenki Day and Night values.
const pairs = (t: Tokens) => [
  ["body text on the page", t["--foreground"], t["--muted"], 4.5],
  ["muted text on a card", t["--muted-foreground"], t["--card"], 4.5],
  ["folded plumbing and marker text on the page", t["--muted-foreground"], t["--muted"], 4.5],
  ["placeholder and meta text on a card", t["--placeholder"], t["--card"], 4.5],
  ["placeholder and meta text on the page", t["--placeholder"], t["--muted"], 4.5],
  ["links on the page", t["--link"], t["--muted"], 4.5],
  ["links on a card", t["--link"], t["--card"], 4.5],
  ["success text on a card", t["--success-text"], t["--card"], 4.5],
  ["destructive text on a card", t["--destructive"], t["--card"], 4.5],
  ["deny pill text on a tool row", t["--destructive-text"], t["--card"], 4.5],
  ["warning text on its tint", t["--warning-foreground"], mix(t["--warning"], t["--card"], 0.18), 4.5],
  ["primary button text", t["--primary-foreground"], t["--primary"], 4.5],
  ["the header's text", t["--brand-header-foreground"], t["--brand-header"], 4.5],
  ["the strip's chip borders", mix(t["--brand-header-foreground"], t["--brand-header"], 0.5), t["--brand-header"], 3],
  ["the needs-you pill", t["--warning-solid-foreground"], t["--warning"], 4.5],
  ["the budget meter's fill", t["--primary"], mix(t["--brand-header-foreground"], t["--brand-header"], 0.12), 3],
  ["the needs-you border", t["--warning-border"], t["--card"], 3],
  ["the deny button's text on a card", t["--destructive-text"], t["--card"], 4.5],
  ["the controls' group names", t["--muted-foreground"], t["--background"], 4.5],
  ["a destructive button on the dock's session panel", t["--destructive-text"], t["--surface-raised"], 4.5],
  ["the dock's notice", t["--destructive-text"], t["--surface-raised"], 4.5],
  ["the room list's notice on the page", t["--destructive-text"], t["--muted"], 4.5],
  ["destructive text on the page (.status-error, .summary-error, .mk-harness_error)", t["--destructive"], t["--muted"], 4.5],
  ["the focus ring on the header", t["--ring"], t["--brand-header"], 3],
  // The strip (strip.ts): a fact's name, the run's receding parts and the Now line's lead and meta at
  // 68% of the header's ink; the status pill's words on each tone's 14% tint.
  ["a settings tab on the header", t["--muted-foreground"], t["--brand-header"], 4.5],
  ["the current tab's bar on the header", t["--primary"], t["--brand-header"], 3],
  ["the strip's fact names and receding parts", mix(t["--brand-header-foreground"], t["--brand-header"], 0.68), t["--brand-header"], 4.5],
  ...(["--muted-foreground", "--primary", "--warning-border", "--success", "--destructive"] as const).map((tone) =>
    [`the run status's words on its ${tone} tint`, t["--brand-header-foreground"], mix(t[tone], t["--brand-header"], 0.14), 4.5] as const),
  // The run rail's lanes and ghosts, drawn on a card.
  ["the writer lane on a card", t["--flow-writer"], t["--card"], 3],
  ["the judge lane on a card", t["--flow-judge"], t["--card"], 3],
  ["the plan's ghosts on a card", t["--flow-plan"], t["--card"], 3],
  // The dock (P4, P8): its own surface, the composer's boundary, the delivery segments.
  ["text on the dock", t["--foreground"], t["--surface-raised"], 4.5],
  ["muted text on the dock (session line, hints)", t["--muted-foreground"], t["--surface-raised"], 4.5],
  ["placeholder text in the composer", t["--placeholder"], t["--card"], 4.5],
  ["the composer's boundary on the dock", t["--input"], t["--surface-raised"], 3],
  ["an unchecked delivery segment", t["--muted-foreground"], t["--muted"], 4.5],
  ["the checked delivery segment", t["--foreground"], t["--card"], 4.5],
  ["the focus ring on the dock", t["--ring"], t["--surface-raised"], 3],
  // A step's line and a call's status (P1, P3).
  ["a step's exceptions on the page", t["--warning-foreground"], t["--muted"], 4.5],
  ["a rejected call's word on a card", t["--warning-foreground"], t["--card"], 4.5],
  // The policy editor's expert view: its highlight colors (key, string, number, comment) on a
  // card and on a line an error marks; a marked line number on the gutter.
  ...(["--link", "--success-text", "--warning-foreground", "--muted-foreground", "--card-foreground"] as const).flatMap((k) => [
    [`the expert view's ${k} on a card`, t[k], t["--card"], 4.5],
    [`the expert view's ${k} on a marked line`, t[k], mix(t["--destructive"], t["--card"], 0.1), 4.5],
  ] as const),
  ["a marked line's number on the gutter", t["--destructive-text"], t["--muted"], 4.5],
  // The room list: a hovered row's tint, the page title on the header, the phase chip.
  ["the list's page title on the header", t["--muted-foreground"], t["--brand-header"], 4.5],
  ["muted text on a hovered room row", t["--muted-foreground"], mix(t["--primary"], t["--card"], 0.06), 4.5],
  // The room list's CLI setup card: its commands on their inset block, the Copy button on
  // that block, the step notes and their inline code, and a step's number on its tint.
  ["a CLI setup command on its block", t["--foreground"], t["--muted"], 4.5],
  ["a CLI setup Copy label on its button", t["--card-foreground"], t["--card"], 4.5],
  ["a CLI setup step's note on the card", t["--muted-foreground"], t["--card"], 4.5],
  ["a CLI setup note's inline command on the card", t["--card-foreground"], t["--card"], 4.5],
  ["a CLI setup step's number on its tint", t["--primary"], mix(t["--primary"], t["--card"], 0.12), 4.5],
  ["a task link on a hovered room row", t["--link"], mix(t["--primary"], t["--card"], 0.06), 4.5],
  ["the phase chip's text on a hovered room row", t["--card-foreground"], mix(t["--primary"], t["--card"], 0.06), 4.5],
  ["the phase chip's border on a card", mix(t["--card-foreground"], t["--card"], 0.5), t["--card"], 3],
  ["the phase chip's border on a hovered room row", mix(t["--card-foreground"], t["--card"], 0.5), mix(t["--primary"], t["--card"], 0.06), 3],
  ["the focus ring on a hovered room row", t["--ring"], mix(t["--primary"], t["--card"], 0.06), 3],
  // The room list's status line: its words on a row, and each kind's accent bar on a card and a hovered row.
  ["a room row's status words on a hovered row", t["--card-foreground"], mix(t["--primary"], t["--card"], 0.06), 4.5],
  ...(["--primary", "--warning-border", "--success", "--destructive", "--muted-foreground"] as const).flatMap((k) => [
    [`a room row's ${k} status accent on a card`, t[k], t["--card"], 3],
    [`a room row's ${k} status accent on a hovered row`, t[k], mix(t["--primary"], t["--card"], 0.06), 3],
  ] as const),
  // The Ogenki surfaces: muted text on a raised surface (hover, table header), the selected
  // border, each status word on a card and on the page, the ink on an accent fill, the focus ring.
  ["muted text on a raised surface (hover, table header)", t["--muted-foreground"], t["--surface-raised"], 4.5],
  ["the selected card's 1 px primary border on a card", t["--primary"], t["--card"], 3],
  ...(["--success-text", "--destructive-text", "--warning-foreground"] as const).flatMap((k) => [
    [`the status word ${k} on a card`, t[k], t["--card"], 4.5],
    [`the status word ${k} on the page`, t[k], t["--muted"], 4.5],
  ] as const),
  ["ink-on-accent on primary", t["--ink-on-accent"], t["--primary"], 4.5],
  ["the focus ring on the page", t["--ring"], t["--muted"], 3],
  ["the focus ring on a card", t["--ring"], t["--card"], 3],
  ["the focus ring on a raised surface", t["--ring"], t["--surface-raised"], 3],
] as const;

// The matrices: [description, foreground, background, minimum, expectedRatio].
type Row = readonly [string, string, string, number, number];
const rows = (t: Tokens, i: 0 | 1): Row[] => {
  const warnTint = mix(t["--warning"], t["--card"], 0.18);
  const humanTint = mix(t["--primary"], t["--card"], 0.10);
  const hdr12 = mix(t["--brand-header-foreground"], t["--brand-header"], 0.12);
  const hdr50 = mix(t["--brand-header-foreground"], t["--brand-header"], 0.5);
  const T = 4.5, U = 3;
  const m = (d: string, fg: string, bg: string, min: number, e: [number, number]): Row => [d, fg, bg, min, e[i]];
  return [
    m("foreground on muted/page", t["--foreground"], t["--muted"], T, [16.20, 16.03]),
    m("card foreground on card", t["--card-foreground"], t["--card"], T, [17.20, 14.45]),
    m("muted foreground on card", t["--muted-foreground"], t["--card"], T, [6.44, 7.78]),
    m("muted foreground on muted/page", t["--muted-foreground"], t["--muted"], T, [6.06, 8.63]),
    m("muted foreground on raised/dock", t["--muted-foreground"], t["--surface-raised"], T, [5.65, 6.74]),
    m("muted foreground on background/controls", t["--muted-foreground"], t["--background"], T, [6.06, 8.63]),
    m("placeholder on card", t["--placeholder"], t["--card"], T, [6.44, 7.78]),
    m("placeholder on muted/page", t["--placeholder"], t["--muted"], T, [6.06, 8.63]),
    m("link on muted/page", t["--link"], t["--muted"], T, [5.62, 7.26]),
    m("link on card", t["--link"], t["--card"], T, [5.97, 6.54]),
    m("success text on card", t["--success-text"], t["--card"], T, [5.29, 9.29]),
    m("destructive on card", t["--destructive"], t["--card"], T, [5.24, 6.41]),
    m("destructive on muted/page", t["--destructive"], t["--muted"], T, [4.93, 7.11]),
    m("destructive text on card", t["--destructive-text"], t["--card"], T, [5.24, 6.41]),
    m("destructive text on muted/page", t["--destructive-text"], t["--muted"], T, [4.93, 7.11]),
    m("destructive text on raised/dock", t["--destructive-text"], t["--surface-raised"], T, [4.60, 5.55]),
    m("warning foreground on warning tint", t["--warning-foreground"], warnTint, T, [6.84, 7.78]),
    m("warning foreground on muted/page", t["--warning-foreground"], t["--muted"], T, [8.33, 12.59]),
    m("warning foreground on card", t["--warning-foreground"], t["--card"], T, [8.85, 11.35]),
    m("primary foreground on primary", t["--primary-foreground"], t["--primary"], T, [5.97, 7.26]),
    m("header foreground on header", t["--brand-header-foreground"], t["--brand-header"], T, [17.20, 16.03]),
    m("warning solid foreground on warning pill", t["--warning-solid-foreground"], t["--warning"], T, [5.60, 10.37]),
    m("foreground on raised/dock", t["--foreground"], t["--surface-raised"], T, [15.11, 12.52]),
    m("card foreground on human tint", t["--card-foreground"], humanTint, T, [14.96, 12.25]),
    m("link on human tint", t["--link"], humanTint, T, [5.19, 5.55]),
    m("success foreground on success fill", t["--success-foreground"], t["--success"], T, [5.29, 10.31]),
    m("destructive foreground on destructive fill", t["--destructive-foreground"], t["--destructive"], T, [5.24, 7.11]),
    m("accent foreground on accent fill", t["--accent-foreground"], t["--accent"], T, [5.29, 10.31]),
    // Non-text: meaningful boundaries and focus rings.
    m("header chip border (50%) on header", hdr50, t["--brand-header"], U, [3.35, 4.78]),
    m("primary meter fill on header-foreground 12% mix", t["--primary"], hdr12, U, [4.66, 5.31]),
    m("warning border on card", t["--warning-border"], t["--card"], U, [5.60, 9.35]),
    m("ring on header", t["--ring"], t["--brand-header"], U, [5.97, 7.26]),
    m("ring on card", t["--ring"], t["--card"], U, [5.97, 6.54]),
    m("ring on muted/page", t["--ring"], t["--muted"], U, [5.62, 7.26]),
    m("ring on raised/dock", t["--ring"], t["--surface-raised"], U, [5.24, 5.67]),
    m("input boundary on card", t["--input"], t["--card"], U, [3.70, 3.94]),
    m("input boundary on raised/dock", t["--input"], t["--surface-raised"], U, [3.25, 3.41]),
  ];
};

// The exact Ogenki semantic token table: [token, Day, Night].
const OGENKI_TOKENS: [string, string, string][] = [
  ["--background", "#f5f6fc", "#0d1330"],
  ["--muted", "#f5f6fc", "#0d1330"],
  ["--card", "#fdfdff", "#161c42"],
  ["--surface-raised", "#eceef8", "#1f2754"],
  ["--foreground", "#12163a", "#eef0fa"],
  ["--card-foreground", "#12163a", "#eef0fa"],
  ["--brand-header-foreground", "#12163a", "#eef0fa"],
  ["--muted-foreground", "#545b85", "#a9b1d6"],
  ["--border", "#b9bfdc", "#3a4478"],
  ["--primary", "#1d63b0", "#5aa9f0"],
  ["--link", "#1d63b0", "#5aa9f0"],
  ["--ring", "#1d63b0", "#5aa9f0"],
  ["--flow-writer", "#1d63b0", "#5aa9f0"],
  ["--primary-foreground", "#fdfdff", "#0d1330"],
  ["--accent-foreground", "#fdfdff", "#0d1330"],
  ["--success-foreground", "#fdfdff", "#0d1330"],
  ["--flow-judge", "#6a4bd8", "#a78bfa"],
  ["--flow-plan", "#965800", "#ffb547"],
  ["--warning", "#965800", "#ffb547"],
  ["--success", "#11795a", "#3ddc97"],
  ["--success-text", "#11795a", "#3ddc97"],
  ["--destructive", "#c2306f", "#ff6fb5"],
  ["--destructive-text", "#c2306f", "#ff6fb5"],
  // The header is the card in Day and the page in Night (ruling 2).
  ["--brand-header", "#fdfdff", "#0d1330"],
  // The raw Ogenki tokens.
  ["--surface-000", "#f5f6fc", "#0d1330"],
  ["--surface-100", "#fdfdff", "#161c42"],
  ["--surface-200", "#eceef8", "#1f2754"],
  ["--line", "#b9bfdc", "#3a4478"],
  ["--blue", "#1d63b0", "#5aa9f0"],
  ["--violet", "#6a4bd8", "#a78bfa"],
  ["--pink", "#c2306f", "#ff6fb5"],
  ["--green", "#11795a", "#3ddc97"],
  ["--amber", "#965800", "#ffb547"],
  ["--ink-on-accent", "#fdfdff", "#0d1330"],
  // Derived: no Ogenki twin (see the commit that introduced them for each ratio).
  ["--input", "#7a82a8", "#6f79b0"],
  ["--placeholder", "#545b85", "#a9b1d6"],
  ["--accent", "#11795a", "#3ddc97"],
  ["--warning-foreground", "#6b3f00", "#ffcf85"],
  ["--warning-solid-foreground", "#fdfdff", "#0d1330"],
  ["--warning-border", "#965800", "#ffb547"],
  ["--destructive-foreground", "#fdfdff", "#0d1330"],
];

describe("Ogenki tokens", () => {
  it("pins the Ogenki semantic token values", () => {
    for (const [name, l, d] of OGENKI_TOKENS) {
      expect(light[name], `Day ${name}`).toBe(l);
      expect(dark[name], `Night ${name}`).toBe(d);
    }
    expect(css).not.toMatch(/--brand-navy/);
  });

  it("keeps the on-accent ink off literal white and black", () => {
    for (const t of [light, dark]) {
      for (const k of ["--primary-foreground", "--accent-foreground", "--success-foreground"]) expect(t[k]).toBe(t["--ink-on-accent"]);
    }
  });
});

describe.each([["light", light, 0], ["dark", dark, 1]] as const)("%s theme", (_name, t, i) => {
  it.each(pairs(t))("%s", (_what, fg, bg, min) => {
    expect(fg, "token defined").toBeDefined();
    expect(ratio(fg, bg)).toBeGreaterThanOrEqual(min);
  });
  it.each(rows(t, i))("matrix: %s", (_what, fg, bg, min, expected) => {
    expect(fg, "token defined").toBeDefined();
    expect(ratio(fg, bg)).toBeGreaterThanOrEqual(min);
    expect(ratio(fg, bg)).toBeCloseTo(expected, 2);
  });
});
