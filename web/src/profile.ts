// SPDX-License-Identifier: Apache-2.0

// The room's Harness profile fold: the active profile and cohort from summary/v1, with the
// scoper's recommendation kept distinct when observe mode leaves the floor active.

import { el } from "./controls";
import type { Summary } from "./summary";

// renderProfile fills root with the fold, or empties it when the summary predates profile facts.
export function renderProfile(root: HTMLElement, s: Summary): void {
  const p = s.status.profile;
  if (!p) {
    root.replaceChildren();
    return;
  }
  const d = el("details", { className: "profile", open: !!root.querySelector<HTMLDetailsElement>("details.profile")?.open });
  d.dataset.block = "profile";
  d.append(el("summary", { textContent: `Harness profile · ${p.name} · ${p.cohort}` }),
    el("p", { className: "profile-source", textContent: `Source: ${p.source}` }));
  if (p.recommended) d.append(el("p", { className: "profile-recommended", textContent: `Recommended: ${p.recommended}` }));
  if (p.reason) d.append(el("p", { className: "profile-reason", textContent: `Reason: ${p.reason}` }));
  if (p.why) d.append(el("p", { className: "profile-why", textContent: `Why: ${p.why}` }));
  root.replaceChildren(d);
}
