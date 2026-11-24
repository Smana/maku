// SPDX-License-Identifier: Apache-2.0

import { expect, test } from "vitest";
import { workflowCard } from "../src/profile";

test("the workflow card is open and says why and how much", () => {
  const card = workflowCard({
    name: "standard",
    source: "repository",
    cohort: "treatment",
    ceilingMicroUSD: 5_100_000,
    path: ["implementer", "reviewer", "CI", "merge gate"],
  }, { nodes: [] });
  expect(card.tagName).toBe("SECTION");
  expect(card.textContent).toContain("standard · this repository's default");
  expect(card.textContent).toContain("implementer → reviewer → CI → merge gate");
  expect(card.textContent).toContain("up to $5.10");
  expect(card.querySelector(":scope > .workflow-head")!.textContent).not.toMatch(/treatment|control|labeled|tier/);
  expect(card.querySelector("details.workflow-details")?.textContent).toContain("Cohort: treatment");
});

test("a refused pin is named on the card, not only in the details", () => {
  const card = workflowCard({
    name: "standard",
    source: "repository",
    cohort: "labeled",
    requested: "quick",
    refused: "profile_conflict",
  }, { nodes: [] });
  expect(card.querySelector(":scope > .workflow-head")!.textContent).toContain("quick was not applied");
  expect(card.querySelector(":scope > .workflow-head")!.textContent).toContain("workflow_conflict");
  expect(card.querySelector("details.workflow-details")?.textContent).not.toContain("Cohort:");
});
