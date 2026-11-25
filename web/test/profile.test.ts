// SPDX-License-Identifier: Apache-2.0

import { expect, test } from "vitest";
import { costWords, workflowCard } from "../src/profile";

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

test("the Cost row says a confirmed cost caps the task, and why a step-up stays within it", () => {
  const base = { source: "label", cohort: "labeled" };
  expect(costWords({ ...base, name: "careful", ceilingMicroUSD: 5_100_000, confirmedMicroUSD: 5_100_000, workflowCeilingMicroUSD: 13_600_000 }, 5_100_000))
    .toBe("capped at your confirmed $5.10 (careful's ceiling is $13.60)");
  expect(costWords({ ...base, name: "standard", confirmedMicroUSD: 5_100_000, workflowCeilingMicroUSD: 5_100_000, stepUpCeilingMicroUSD: 5_100_000 }, 5_100_000))
    .toBe("up to $5.10 · a step-up stays within your $5.10");
  expect(costWords({ ...base, name: "standard", stepUpCeilingMicroUSD: 5_100_000 }, 5_100_000)).toBe("up to $5.10");
  expect(costWords({ ...base, name: "standard", stepUpCeilingMicroUSD: 13_600_000 }, 5_100_000)).toBe("up to $5.10 · up to $13.60 if it steps up");
  const card = workflowCard({ ...base, name: "careful", ceilingMicroUSD: 5_100_000, confirmedMicroUSD: 5_100_000, workflowCeilingMicroUSD: 13_600_000 }, { nodes: [] });
  expect(card.querySelector(".workflow-head")!.textContent).toContain("capped at your confirmed $5.10 (careful's ceiling is $13.60)");
});
