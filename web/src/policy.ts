// SPDX-License-Identifier: Apache-2.0

// The room page's Merge policy fold (merge policy view D8): the class that governs the task, its
// mode and conditions, what it takes to merge, the gate's decision and the repository's classes,
// from summary/v1's status.policy. Read-only: the policy is the factory's GitOps config. Every
// string goes in as text.

import { el } from "./controls";
import type { Summary } from "./summary";

type Policy = NonNullable<Summary["status"]["policy"]>;
type Conditions = NonNullable<Policy["conditions"]>;

function conditionWords(c: Conditions | undefined, approve: boolean): string[] {
  const out: string[] = [];
  const list = (label: string, l?: string[]) => { if (l?.length) out.push(`${label} ${l.join(", ")}`); };
  if (c?.diffShape) out.push(`diff shape ${c.diffShape}`);
  if (c?.maxPatchFiles) out.push(`at most ${c.maxPatchFiles} files`);
  if (c?.maxModifiedLines) out.push(`at most ${c.maxModifiedLines} changed lines`);
  if (c?.modifyOnly) out.push("modifies only");
  list("allowed files", c?.allowedFiles);
  list("protected files", c?.protectedFiles);
  list("holdout checks", c?.holdoutChecks);
  if (c?.maxPerDay) out.push(`at most ${c.maxPerDay} merges a day`);
  list("adopts", c?.adoptLogins);
  if (c?.requiredLabel) out.push(`needs label ${c.requiredLabel}`);
  if (c?.oracle) out.push("oracle-backed");
  if (approve) out.push("approve to merge");
  return out;
}

function headline(p: Policy): string {
  return p.gateOff ? "Merge policy · gate off" : `Merge policy · ${p.class || "no class"} · ${p.mode}`;
}

// renderPolicy fills root with the fold, or empties it when the summary carries no policy.
export function renderPolicy(root: HTMLElement, s: Summary): void {
  const p = s.status.policy;
  if (!p) {
    root.replaceChildren();
    return;
  }
  // A summary refresh rebuilds the fold: keep it open for someone reading it.
  const d = el("details", { className: "policy", open: !!root.querySelector<HTMLDetailsElement>("details.policy")?.open });
  d.dataset.block = "policy";
  const words = conditionWords(p.conditions, !!p.approveToMerge);
  d.append(el("summary", { textContent: headline(p) }),
    el("p", { className: "policy-conditions", textContent: `Conditions: ${words.length ? words.join(" · ") : "none declared"}` }),
    el("p", { className: "policy-how", textContent: `To merge: ${p.how}` }));
  if (p.decision) d.append(el("p", { className: "policy-decision", textContent: `Decision: ${p.decision}` }));
  if (p.classes?.length) {
    const ul = el("ul", { className: "policy-classes" });
    for (const c of p.classes) ul.append(el("li", { textContent: [c.name, c.mode, c.adopted ? "adopted" : ""].filter(Boolean).join(" · ") }));
    d.append(el("p", { className: "hint", textContent: "Repository classes" }), ul);
  }
  if (p.truncated) d.append(el("p", { className: "policy-truncated hint", textContent: "Some entries were left out: the list is longer than the room carries." }));
  root.replaceChildren(d);
}
