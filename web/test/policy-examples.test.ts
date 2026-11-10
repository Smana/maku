// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { templates } from "../src/policy-editor";
import { EXAMPLES } from "../src/policy-examples";

describe("the example gallery", () => {
  it("has one entry per template and no entry without one", () => {
    expect(EXAMPLES.map((e) => e.name).sort()).toEqual(Object.keys(templates).sort());
  });
  it("has simple and advanced examples, each saying why", () => {
    expect(EXAMPLES.filter((e) => e.level === "simple").length).toBeGreaterThanOrEqual(3);
    expect(EXAMPLES.filter((e) => e.level === "advanced").length).toBeGreaterThanOrEqual(3);
    for (const e of EXAMPLES) expect(e.why.length, e.name).toBeGreaterThan(30);
  });
  it("gives every template a description of at most 280 characters", () => {
    for (const [name, make] of Object.entries(templates)) {
      const d = make().description ?? "";
      expect(d.length, name).toBeGreaterThan(20);
      expect(d.length, name).toBeLessThanOrEqual(280);
    }
  });
  it("keeps every example in shadow: a gallery never starts a class live", () => {
    for (const [name, make] of Object.entries(templates)) expect(make().mode, name).toBe("shadow");
  });
});
