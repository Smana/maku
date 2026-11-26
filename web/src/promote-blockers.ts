// SPDX-License-Identifier: Apache-2.0

// Why a policy cannot be promoted yet, one line per blocker the evidence names, each with its next
// step (correctness signals W6). The words are internal/factory/meaning's Blocker, said the same way.

import type { Evidence } from "./settings";

export interface BlockerLine { code: string; says: string; next?: string }

// The rules of the second plan the words may name: off until the factory enforces them.
export interface BlockerRules { uncheckedStaysShadow?: boolean; wrongLabel?: boolean }

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

export function blockerLine(code: string, e: Pick<Evidence, "clean" | "minimum" | "bad">, rules: BlockerRules = {}): BlockerLine {
  switch (code) {
    case "below_minimum":
      return { code, says: `Needs ${plural(Math.max(1, e.minimum - e.clean), "more decision", "more decisions")} you agree with.`,
        next: "Merge unchanged the pull requests it would have merged." };
    case "bad_outcome":
      return { code, says: `${plural(Math.max(1, e.bad), "wrong call", "wrong calls")} among its recent merges.`,
        next: "Read each one in the decisions list: a wrong call blocks promotion." };
    case "no_drill":
    case "drill_not_passed":
      return { code, says: "No rollback drill on this repository yet.",
        next: "Run `maku policy drill owner/name#PR` on a merged pull request, then merge the revert it opens." };
    case "Unchecked":
      if (rules.uncheckedStaysShadow) return { code, says: "Nothing checks what these changes mean, so this policy cannot merge alone.",
        next: "Add a signal: require a plan and tests, or a shape." };
      break;
    case "candidates_open":
      if (rules.wrongLabel) return { code, says: "Later fixes may have corrected a merge of this policy. Answer them first.", next: "Answer each candidate." };
      break;
  }
  return { code, says: `${code.replaceAll("_", " ")}.` };
}

export function blockerLines(e: Evidence, rules: BlockerRules = {}): BlockerLine[] {
  return (e.blockers ?? []).map((code) => blockerLine(code, e, rules));
}

// blockerList is the lines as a list, each next step after its blocker.
export function blockerList(lines: BlockerLine[]): HTMLUListElement {
  const ul = document.createElement("ul");
  ul.className = "promote-blockers";
  for (const l of lines) {
    const li = document.createElement("li");
    li.dataset.code = l.code;
    const says = document.createElement("span");
    says.className = "blocker-says";
    says.textContent = l.says;
    li.append(says);
    if (l.next) {
      const next = document.createElement("span");
      next.className = "blocker-next";
      next.textContent = ` ${l.next}`;
      li.append(next);
    }
    ul.append(li);
  }
  return ul;
}
