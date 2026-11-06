// SPDX-License-Identifier: Apache-2.0

// The page's icons: one outline set, a 1.5 px stroke on a 16 px grid, built with
// createElementNS so the strict CSP needs no markup. Each draws in currentColor: its
// state (hover, disabled, a status) comes from CSS, never from a second asset.

const NS = "http://www.w3.org/2000/svg";

const paths = {
  check: ["M3.5 8.5l3 3 6-7"],
  x: ["M4.5 4.5l7 7M11.5 4.5l-7 7"],
  ban: ["M8 1.75a6.25 6.25 0 1 0 0 12.5a6.25 6.25 0 1 0 0-12.5z", "M3.6 3.6l8.8 8.8"],
  spinner: ["M8 1.75a6.25 6.25 0 1 0 6.25 6.25"],
  chevron: ["M6 3.5l4.5 4.5-4.5 4.5"],
  send: ["M2.5 8h10", "M8.5 4l4 4-4 4"],
  // The run status's kinds (statusIcon). dot is filled by CSS (.icon-dot): a live run.
  clock: ["M8 1.75a6.25 6.25 0 1 0 0 12.5a6.25 6.25 0 1 0 0-12.5z", "M8 4.75V8l2.25 1.5"],
  dot: ["M8 4.5a3.5 3.5 0 1 0 0 7a3.5 3.5 0 1 0 0-7z"],
  circle: ["M8 3.25a4.75 4.75 0 1 0 0 9.5a4.75 4.75 0 1 0 0-9.5z"],
  square: ["M4.25 4.25h7.5v7.5h-7.5z"],
} as const;

export type IconName = keyof typeof paths;

// icon is one glyph: decorative (hidden from assistive tech) unless it carries a label,
// for an icon that is the only sign of a state.
export function icon(name: IconName, label?: string): SVGSVGElement {
  const svg = document.createElementNS(NS, "svg");
  for (const [k, v] of Object.entries({
    viewBox: "0 0 16 16", width: "16", height: "16", fill: "none", stroke: "currentColor",
    "stroke-width": "1.5", "stroke-linecap": "round", "stroke-linejoin": "round", class: `icon icon-${name}`,
  })) svg.setAttribute(k, v);
  if (label) {
    svg.setAttribute("role", "img");
    svg.setAttribute("aria-label", label);
  } else {
    svg.setAttribute("aria-hidden", "true");
  }
  for (const d of paths[name]) {
    const p = document.createElementNS(NS, "path");
    p.setAttribute("d", d);
    svg.append(p);
  }
  return svg;
}

// The run status's glyph by kind. The status carries a character too (the CLI prints it, and
// internal/summary mirrors it); the page draws this instead, so the line matches the icon set.
const STATUS: Record<string, IconName> = {
  running: "dot", queued: "circle", "waiting-you": "clock", "waiting-other": "clock", "waiting-pr": "clock",
  waiting: "clock", finished: "check", done: "check", failed: "x", stopped: "square",
};

// statusIcon is the decorative glyph for a run-status kind, null for a kind it does not know.
export function statusIcon(kind: string): SVGSVGElement | null {
  return Object.hasOwn(STATUS, kind) ? icon(STATUS[kind]) : null;
}
