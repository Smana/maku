// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { blockerLine } from "../src/promote-blockers";

describe("why Promote is disabled (correctness signals W6)", () => {
  const e = { clean: 7, minimum: 20, bad: 2 };
  it("says each blocker of the table with its next step", () => {
    expect(blockerLine("below_minimum", e)).toEqual({ code: "below_minimum", says: "Needs 13 more decisions you agree with.", next: "Merge unchanged the pull requests it would have merged." });
    expect(blockerLine("bad_outcome", e).says).toBe("2 wrong calls among its recent merges.");
    expect(blockerLine("no_drill", e).next).toContain("maku policy drill");
    expect(blockerLine("drill_not_passed", e).says).toBe("No rollback drill on this repository yet.");
  });
  it("names the second plan's blockers only under the rules that emit them", () => {
    expect(blockerLine("Unchecked", e)).toEqual({ code: "Unchecked", says: "Unchecked." });
    expect(blockerLine("Unchecked", e, { uncheckedStaysShadow: true }).says).toBe("Nothing checks what these changes mean, so this policy cannot merge alone.");
    expect(blockerLine("candidates_open", e, { wrongLabel: true }).says).toContain("Answer them first.");
    expect(blockerLine("x_y", e)).toEqual({ code: "x_y", says: "x y." });
  });
});
