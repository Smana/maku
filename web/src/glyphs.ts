// SPDX-License-Identifier: Apache-2.0

// The clip's glyphs: a 24 grid centred on the origin, a 2 px stroke, built with createElementNS
// like icons.ts. Circles, lines and rects are paths here so the set is one element kind; a
// `solid` path fills in currentColor instead of stroking.

const NS = "http://www.w3.org/2000/svg";

type Part = readonly [d: string, solid?: true];

const dot = (cx: number, cy: number, r: number): string => `M${cx - r} ${cy}a${r} ${r} 0 1 0 ${2 * r} 0a${r} ${r} 0 1 0-${2 * r} 0`;

const parts = {
  scope: [[dot(0, 0, 8)], ["M0 -11V-5M0 5V11M-11 0H-5M5 0H11"], [dot(0, 0, 1.8), true]],
  search: [[dot(-2.5, -2.5, 7)], ["M2.5 2.5L10 10"]],
  checklist: [["M-10 -6.5L-7.5 -4L-4 -9.5M0 -7H10M-10 0.5L-7.5 3L-4 -2.5M0 0H10M-10 7.5L-7.5 10L-4 4.5M0 7H10"]],
  tools: [["M-9 9L3 -3"], ["M7.8 -2.2A4 4 0 1 1 2.2 -7.8"], ["M-9 -9L9 9"]],
  eye: [["M-11 0Q0 -11 11 0Q0 11 -11 0Z"], [dot(0, 0, 3.5), true]],
  branch: [[dot(-6, -7, 2.8)], [dot(-6, 7, 2.8)], [dot(6, -4, 2.8)], ["M-6 -4V4M6 -1Q6 4 -3 5"]],
  person: [[dot(0, -4, 4.5)], ["M-9 10Q-9 2 0 2Q9 2 9 10"]],
  github: [
    ["M-7 -8H7a3 3 0 0 1 3 3V5a3 3 0 0 1-3 3H-7a3 3 0 0 1-3-3V-5a3 3 0 0 1 3-3z"], ["M-10 -3H10"],
    [dot(-5, 2.5, 1.6), true], [dot(0, 2.5, 1.6), true], [dot(5, 2.5, 1.6), true],
  ],
  shield: [["M0 -10L9 -6Q9 6 0 10Q-9 6 -9 -6Z"], ["M-4 0L-1 3L4 -3"]],
  chat: [["M-8 -8H8a3 3 0 0 1 3 3V3a3 3 0 0 1-3 3H-1L-6 10V6H-8a3 3 0 0 1-3-3V-5a3 3 0 0 1 3-3z"]],
} as const satisfies Record<string, readonly Part[]>;

export type GlyphName = keyof typeof parts;

// glyph is decorative (hidden from assistive tech) unless it carries a label.
export function glyph(name: GlyphName, label?: string): SVGSVGElement {
  const svg = document.createElementNS(NS, "svg");
  for (const [k, v] of Object.entries({
    viewBox: "-12 -12 24 24", width: "24", height: "24", fill: "none", stroke: "currentColor",
    "stroke-width": "2", "stroke-linecap": "round", "stroke-linejoin": "round", class: `glyph glyph-${name}`,
  })) svg.setAttribute(k, v);
  if (label) {
    svg.setAttribute("role", "img");
    svg.setAttribute("aria-label", label);
  } else {
    svg.setAttribute("aria-hidden", "true");
  }
  for (const [d, solid] of parts[name] as readonly Part[]) {
    const p = document.createElementNS(NS, "path");
    p.setAttribute("d", d);
    if (solid) {
      p.setAttribute("fill", "currentColor");
      p.setAttribute("stroke", "none");
    }
    svg.append(p);
  }
  return svg;
}

const byPersona = new Map<string, GlyphName>([
  ["scoper", "scope"], ["triager", "search"], ["tester", "checklist"], ["implementer", "tools"], ["reviewer", "eye"],
]);
const byRole = new Map<string, GlyphName>([
  ["triager", "search"], ["tester", "checklist"], ["implementer", "tools"], ["reviewer", "eye"],
]);

// glyphFor is a node's glyph: its persona's, else its role's, else the tools.
export function glyphFor(persona: string, role: string): GlyphName {
  return byPersona.get(persona) ?? byRole.get(role) ?? "tools";
}
