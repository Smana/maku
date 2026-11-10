// SPDX-License-Identifier: Apache-2.0

// The Settings entry point every page header carries beside the brand: a gear and a label, so the
// settings pages are found without knowing their URL.

import { api } from "./api";

const NS = "http://www.w3.org/2000/svg";

function gear(): SVGSVGElement {
  const svg = document.createElementNS(NS, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("width", "16");
  svg.setAttribute("height", "16");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "2");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  const circle = document.createElementNS(NS, "circle");
  circle.setAttribute("cx", "12");
  circle.setAttribute("cy", "12");
  circle.setAttribute("r", "3");
  const path = document.createElementNS(NS, "path");
  path.setAttribute("d", "M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z");
  svg.append(circle, path);
  return svg;
}

// mountSettingsLink appends the link to host, hidden until the settings API proves the feature is on.
export function mountSettingsLink(host: HTMLElement, href = "/settings", get: typeof fetch = fetch): HTMLAnchorElement {
  const a = document.createElement("a");
  a.className = "settings-link";
  a.href = href;
  a.hidden = true;
  const label = document.createElement("span");
  label.textContent = "Settings";
  a.append(gear(), label);
  host.append(a);
  void api("/api/policy/repositories", {}, get).then((r) => { a.hidden = !r.ok; }).catch(() => { a.hidden = true; });
  return a;
}
