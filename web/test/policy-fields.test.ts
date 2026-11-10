// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { docsLink, FIELDS, GROUPS } from "../src/policy-fields";

const agents = { mode: "shadow", author: "agents", harness: "plain" };
const adopt = { ...agents, author: "adopt", adopt: { logins: [], branches: [] } };
const shown = (c: object) => FIELDS.filter((f) => f.shown(c as never)).map((f) => f.key);

describe("the editor's fields", () => {
  it("gives every field a plain label, one help line and a group the page draws", () => {
    const groups = new Set(GROUPS.map((g) => g.id));
    for (const f of FIELDS) {
      expect(f.label, f.key).not.toMatch(/[A-Z]{2}|\./); // a sentence, not a key
      expect(f.help.length, f.key).toBeGreaterThan(20);
      expect(f.help.length, f.key).toBeLessThan(160);
      expect(groups.has(f.group), f.key).toBe(true);
    }
  });
  it("shows the adopted leaves to an adopted class only, and the off-only keys to an off class only", () => {
    expect(shown(agents)).not.toContain("releaseAgeCheck");
    expect(shown(agents)).not.toContain("requiredLabel");
    expect(shown(adopt)).toContain("releaseAgeCheck");
    expect(shown(adopt)).toContain("adopt.logins");
    expect(shown(agents)).not.toContain("harnessAllowed");
    expect(shown({ ...agents, mode: "off" })).toContain("harnessAllowed");
    expect(shown({ ...agents, mode: "off" })).toContain("escalation.notConverged");
  });
  it("shows companionFiles only with an added-entry shape", () => {
    expect(shown(adopt)).not.toContain("companionFiles");
    expect(shown({ ...adopt, conditions: { all: [{ diffShape: "added-entry" }] } })).toContain("companionFiles");
  });
  it("links each field to its section of the merge-policy guide", () => {
    expect(docsLink("leaves")).toMatch(/\/docs\/merge-policies\.md#leaves$/);
  });
});
