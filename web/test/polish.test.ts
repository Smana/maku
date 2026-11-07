// SPDX-License-Identifier: Apache-2.0
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// The interface-polish pass (2026-10-28): press, focus and hover states, hit areas, concentric
// radii and steady numbers, asserted on the stylesheet the way brand-style.test.ts does.
const css = readFileSync(join(__dirname, "../src/app.css"), "utf8");

/** The bodies of every `@media <query> {…}` block, braces balanced. */
function media(query: string): string {
  const out: string[] = [];
  const head = `@media ${query}`;
  for (let i = css.indexOf(head); i >= 0; i = css.indexOf(head, i + 1)) {
    const open = css.indexOf("{", i);
    let depth = 1, j = open + 1;
    for (; j < css.length && depth; j++) depth += css[j] === "{" ? 1 : css[j] === "}" ? -1 : 0;
    out.push(css.slice(open + 1, j - 1));
  }
  return out.join("\n");
}
/** The stylesheet outside every @media block. */
const outside = (() => {
  let s = css;
  for (;;) {
    const i = s.indexOf("@media");
    if (i < 0) return s;
    const open = s.indexOf("{", i);
    let depth = 1, j = open + 1;
    for (; j < s.length && depth; j++) depth += s[j] === "{" ? 1 : s[j] === "}" ? -1 : 0;
    s = s.slice(0, i) + s.slice(j);
  }
})();
/** The declarations of the first rule in `src` whose selector list is exactly `sel`. */
function decl(src: string, sel: string): string {
  const esc = sel.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const m = src.match(new RegExp(`(?:^|\\n)\\s*${esc}\\s*{([^}]*)}`));
  expect(m, `rule ${sel}`).not.toBeNull();
  return m![1];
}
const phone = media("(max-width: 640px)");
const motionOK = media("(prefers-reduced-motion: no-preference)");

describe("performance", () => {
  it("never transitions all", () => {
    expect(css).not.toMatch(/transition(-property)?:\s*all\b/);
    expect(css).not.toMatch(/will-change:\s*all/);
  });
});

describe("press feedback", () => {
  const press = "button:active:not(:disabled, .now-link, .clip-toggle, .t-more)";
  it("scales a pressed button to .96, only where motion is welcome", () => {
    expect(decl(motionOK, press)).toMatch(/scale:\s*\.96/);
    expect(outside).not.toMatch(/button:active[^{]*{[^}]*scale/);
  });
  it("leaves the buttons drawn as text lines unscaled: they underline instead", () => {
    expect(decl(outside, ".page-header .now-link:hover")).toMatch(/text-decoration:\s*underline/);
    expect(decl(outside, ".clip-toggle:hover")).toMatch(/text-decoration:\s*underline/);
    expect(decl(outside, ".t-more:hover")).toMatch(/text-decoration:\s*underline/);
  });
});

describe("focus", () => {
  it("rings every focused link in the ring token, like the buttons", () => {
    const a = decl(outside, "a:focus-visible");
    expect(a).toMatch(/outline:\s*2px solid var\(--ring\)/);
    expect(a).toMatch(/outline-offset:\s*2px/);
  });
});

describe("hover", () => {
  it("lifts an unchecked delivery segment toward the foreground", () => {
    expect(decl(outside, ".seg input:not(:checked, :disabled):hover + span")).toMatch(/color:\s*var\(--foreground\)/);
  });
  it("darkens a muted fold toggle on hover, with a colour-only transition", () => {
    const sel = ".session > summary, .cli-setup > summary, .notes details > summary, .fold > summary, .step-calls > summary, .controls .advanced > summary, .approval-json > summary";
    expect(decl(outside, sel)).toMatch(/transition-property:\s*color;/);
    expect(decl(outside, sel.split(", ").map((s) => `${s}:hover`).join(", "))).toMatch(/color:\s*var\(--foreground\)/);
  });
  it("fades the fork toolbar in rather than popping it", () => {
    expect(decl(outside, ".row-tools")).toMatch(/transition-property:\s*opacity/);
  });
});

describe("hit areas", () => {
  it("sets a 40 px target, 44 px on a phone", () => {
    expect(decl(outside, ":root")).toMatch(/--hit:\s*40px/);
    expect(decl(phone, ":root")).toMatch(/--hit:\s*44px/);
  });
  it("extends the small standalone controls with a centred pseudo-element of --hit", () => {
    const small = ".theme-toggle, .fork-here, .clip-toggle, .t-more, .cli-setup > summary, .notes details > summary";
    expect(decl(outside, small)).toMatch(/position:\s*relative/);
    const after = decl(outside, small.split(", ").map((s) => `${s}::after`).join(", "));
    expect(after).toMatch(/content:\s*""/);
    expect(after).toMatch(/position:\s*absolute/);
    expect(after).toMatch(/inset:\s*min\(0px,\s*\(100% - var\(--hit\)\) \/ 2\)/);
  });
  it("gives the dock's delivery segments a taller radio than their pill", () => {
    expect(decl(outside, ".seg input")).toMatch(/inset:\s*-6px 0/);
    expect(decl(phone, ".seg input")).toMatch(/inset:\s*-8px 0/);
  });
  it("makes the session toggle a 40 px target inside the dock's gaps", () => {
    expect(decl(outside, ".session > summary")).toMatch(/padding-block:\s*6px/);
    expect(decl(outside, ".session > summary::after")).toMatch(/inset:\s*-4px -8px/);
  });
  it("grows the chip links and the Needs-you pill to their half of the row gap", () => {
    expect(decl(outside, ".page-header .chip a::after")).toMatch(/inset:\s*-5px -2px/);
    expect(decl(outside, ".page-header .needs-pill::after")).toMatch(/inset:\s*-3px 0/);
    expect(decl(phone, ".page-header .needs-pill::after")).toMatch(/inset:\s*-8px 0/);
    expect(decl(phone, ".page-header .strip-chips")).toMatch(/padding-block:\s*8px;[^}]*margin-block:\s*-8px/);
  });
  it("grows the Now line on a phone, where its clamp would clip a pseudo-element", () => {
    expect(decl(phone, ".page-header .now-link")).toMatch(/padding-block:\s*12px;[^}]*margin-block:\s*-12px/);
  });
  it("sizes the room list's filters to the target", () => {
    expect(decl(outside, ".list-tools label, .list-tools select, .list-tools button")).toMatch(/min-height:\s*var\(--hit\)/);
  });
  it("gives every form control on a phone a 44 px target", () => {
    const sel = ".controls button, .controls select, .controls input:not([type=\"checkbox\"]), .needs button, .approval-decide input, .fork button, .fork input, .new-room button, .new-room input";
    expect(decl(phone, sel)).toMatch(/min-height:\s*var\(--hit\)/);
    expect(decl(phone, sel)).toMatch(/box-sizing:\s*border-box/);
    expect(decl(outside, ".composer button, .needs button")).toMatch(/min-height:\s*var\(--hit\)/);
  });
});

describe("steady numbers", () => {
  it("sets the strip's counters, the list's cells and the fold counts in tabular numerals", () => {
    for (const sel of [".strip-chips", ".strip-now", ".rooms th, .rooms td"]) {
      expect(decl(outside, sel), sel).toMatch(/font-variant-numeric:\s*tabular-nums/);
    }
    expect(decl(outside, ".notes details > summary, .fold > summary, .step-calls > summary, .t-sec")).toMatch(/font-variant-numeric:\s*tabular-nums/);
  });
});

describe("text wrapping", () => {
  it("keeps short UI lines free of orphans", () => {
    expect(decl(outside, ".waiting, .closed, .mark-text, .sys, .hint, .approval-meta")).toMatch(/text-wrap:\s*pretty/);
  });
});

describe("surfaces", () => {
  it("rounds the approval card concentrically: its 6 px controls plus 8 px of padding", () => {
    const a = decl(outside, ".approval");
    expect(a).toMatch(/border-radius:\s*calc\(var\(--radius-control\)\s*\+\s*8px\)/);
    expect(a).toMatch(/padding:\s*8px 12px/);
  });
  it("squares an open call's summary at the body's edge", () => {
    expect(decl(outside, ".tool[open] > summary")).toMatch(/border-radius:\s*var\(--radius-row\) var\(--radius-row\) 0 0/);
  });
});

describe("icons", () => {
  it("weights Send's arrow to its bold label and trims the icon side's padding", () => {
    expect(decl(outside, "button.send .icon")).toMatch(/stroke-width:\s*2\.5/);
    expect(decl(outside, "button.send")).toMatch(/padding:\s*0 14px 0 16px/);
  });
});

describe("fork here on touch screens", () => {
  const touch = media("(hover: none), (pointer: coarse)");
  it("is never an invisible tap target: hidden means inert", () => {
    const hidden = decl(outside, ".row-tools");
    expect(hidden).toMatch(/opacity:\s*0/);
    expect(hidden).toMatch(/visibility:\s*hidden/);
    expect(hidden).toMatch(/pointer-events:\s*none/);
    expect(decl(outside, ".row:hover .row-tools, .row:focus-within .row-tools")).toMatch(/visibility:\s*visible/);
  });
  it("is shown on touch, in the flow under the row, clear of the timestamp", () => {
    const bar = decl(touch, ".row-tools");
    expect(bar).toMatch(/position:\s*static/);
    expect(bar).toMatch(/opacity:\s*1/);
    expect(bar).toMatch(/visibility:\s*visible/);
    expect(bar).toMatch(/pointer-events:\s*auto/);
  });
  it("is drawn quiet on touch: no border, background or shadow, muted 13 px text", () => {
    const b = decl(touch, ".row-tools .fork-here");
    expect(b).toMatch(/border:\s*(0|none)/);
    expect(b).toMatch(/background:\s*none|background:\s*transparent/);
    expect(b).toMatch(/box-shadow:\s*none/);
    expect(b).toMatch(/color:\s*var\(--muted-foreground\)/);
    expect(b).toMatch(/font-size:\s*13px/);
    expect(b).not.toMatch(/min-height:\s*[1-9]/);
  });
  it("gets its 44 px hit area from ::after, not from visible padding", () => {
    const a = decl(touch, ".row-tools .fork-here::after");
    expect(a).toMatch(/inset:\s*-12px\s+-8px/);
  });
});

describe("phone clamps and long tokens", () => {
  it("masks the clamped Now button's padding so no third line shows under the two", () => {
    const b = decl(phone, ".page-header .now-link");
    expect(b).toMatch(/-webkit-line-clamp:\s*2/);
    expect(b).toMatch(/(-webkit-)?mask(-image)?:[^;]*linear-gradient/);
    expect(b).toMatch(/calc\(100%\s*-\s*12px\)/);
  });
  it("wraps a long unbroken token in message prose; pre keeps its own wrapping", () => {
    expect(decl(outside, ".md")).toMatch(/overflow-wrap:\s*anywhere/);
    expect(decl(outside, "pre")).toMatch(/overflow-wrap:\s*anywhere/);
  });
});

// The layout pass (2026-11-06): a phone's first screen goes to the conversation, the lockup
// sits on the title's row, and a wide screen keeps the log, the rail and the composer together.
const wide = media("(min-width: 1100px)");
describe("layout", () => {
  it("lets the header scroll away on a phone, so the chrome never pins two thirds of the screen", () => {
    expect(decl(phone, ".page-header")).toMatch(/position:\s*static/);
  });
  it("keeps Send beside the text on a phone rather than a full-width row under it", () => {
    expect(phone).not.toMatch(/\.composer-write\s*{[^}]*flex-direction:\s*column/);
  });
  it("keeps the theme button and the connection on one line on a phone", () => {
    const m = decl(phone, ".dock-meta");
    expect(m).toMatch(/flex-wrap:\s*nowrap/);
    expect(decl(phone, ".dock-meta .theme-toggle")).toMatch(/white-space:\s*nowrap/);
    const s = decl(phone, ".page-dock .status");
    expect(s).toMatch(/text-overflow:\s*ellipsis/);
    expect(s).toMatch(/min-width:\s*0/);
  });
  it("aligns the lockup with the title's row, not the middle of the whole header", () => {
    expect(decl(outside, ".brand")).toMatch(/align-self:\s*start/);
  });
  it("lets the conversation's column grow with the window on a wide screen, the rail at its right edge", () => {
    expect(decl(outside, ":root")).toMatch(/--log-w:\s*72ch/);
    expect(decl(wide, ".room-body")).toMatch(/--log-w:\s*none/);
    expect(decl(wide, ".room-body.has-rail")).toMatch(/grid-template-columns:\s*minmax\(0,\s*1fr\)\s*300px/);
    expect(decl(wide, ".bubble .md, .handoff > .md, .notes")).toMatch(/max-width:\s*100ch/);
  });
  it("fills the window's height, the dock at the bottom of a short room", () => {
    expect(decl(outside, "#app")).toMatch(/min-height:\s*100dvh/);
    expect(decl(outside, "#app > .page-dock")).toMatch(/margin-top:\s*auto/);
  });
  it("keeps the whole rail reachable between the header and the dock: it scrolls on its own", () => {
    const d = decl(wide, ".room-body.has-rail > .rail");
    expect(d).toMatch(/top:\s*var\(--header-h/);
    expect(d).toMatch(/max-height:\s*calc\(100dvh\s*-\s*var\(--header-h[^)]*\)\s*-\s*var\(--dock-h/);
    expect(d).toMatch(/overflow-y:\s*auto/);
    expect(d).toMatch(/overscroll-behavior:\s*contain/);
    // The padding sits inside the height: content-box put the rail's last 8 px under the dock.
    expect(d).toMatch(/box-sizing:\s*border-box/);
  });
  it("shares the conversation's grid with the composer on a wide screen", () => {
    const d = decl(wide, ".page-dock");
    expect(d).toMatch(/grid-template-columns:\s*minmax\(0,\s*1fr\)\s*300px/);
    // 16 px of the main's padding past the measure, plus the room body's 16 px gap: the meta sits under the rail.
    expect(d).toMatch(/column-gap:\s*32px/);
  });
});

// The appearance pass (2026-11-06): hierarchy from size and opacity, one radius scale, the
// approval card's accent as a bar, the connection as a dot.
describe("appearance", () => {
  it("sets the room title bigger and lighter, smaller again on a phone", () => {
    expect(decl(phone, ".strip-title")).toMatch(/font-size:\s*22px/);
  });
  it("mutes the header's secondary facts and keeps the state chips at full strength", () => {
    expect(decl(outside, ".page-header .chip")).toMatch(/color:\s*color-mix\(in srgb, var\(--brand-header-foreground\) 68%, var\(--brand-header\)\)/);
    expect(decl(outside, ".page-header .chip-phase, .page-header .chip-run, .page-header .chip-verdict")).toMatch(/color:\s*var\(--brand-header-foreground\)/);
  });
  it("draws the approval card's accent as a bar on a raised card, not a full outline", () => {
    const a = decl(outside, ".approval");
    expect(a).not.toMatch(/border:\s*1px solid var\(--warning-border\)/);
    expect(a).toMatch(/box-shadow:\s*var\(--shadow-border\),\s*inset 3px 0 0 var\(--warning\)/);
    expect(a).toMatch(/padding:\s*8px 12px 8px 15px/);
  });
  it("draws deny as a ghost: no outline until hovered", () => {
    const d = decl(outside, "button.deny");
    expect(d).toMatch(/border-color:\s*transparent/);
    expect(d).toMatch(/background:\s*transparent/);
    expect(decl(outside, "button.deny:hover")).toMatch(/background:\s*color-mix\(in srgb, var\(--destructive\) 10%, transparent\)/);
  });
  it("says the connection with a dot and the UI face, the dot's colour by state", () => {
    const s = decl(outside, ".page-dock .status");
    expect(s).not.toMatch(/monospace/);
    expect(decl(outside, ".page-dock .status::before")).toMatch(/border-radius:\s*50%/);
    expect(decl(outside, '.page-dock[data-status="live"] .status::before')).toMatch(/background:\s*var\(--success\)/);
    expect(decl(outside, '.page-dock[data-status="reconnecting"] .status::before')).toMatch(/background:\s*var\(--warning\)/);
  });
  it("keeps one radius scale: controls, rows and cards", () => {
    const root = decl(outside, ":root");
    expect(root).toMatch(/--radius-control:\s*6px/);
    expect(root).toMatch(/--radius-row:\s*8px/);
    expect(root).toMatch(/--radius-card:\s*12px/);
    expect(decl(outside, ".cli-card")).toMatch(/border-radius:\s*var\(--radius-card\)/);
    expect(decl(outside, ".sk-msg")).toMatch(/border-radius:\s*var\(--radius-card\)/);
  });
});
