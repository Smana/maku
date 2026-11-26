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

test("the Cost row says a confirmed cost caps the task, and whether a step-up runs within it or is offered", () => {
  const base = { source: "label", cohort: "labeled" };
  expect(costWords({ ...base, name: "careful", ceilingMicroUSD: 5_100_000, confirmedMicroUSD: 5_100_000, workflowCeilingMicroUSD: 13_600_000 }, 5_100_000))
    .toBe("capped at your confirmed $5.10 (careful's ceiling is $13.60)");
  expect(costWords({ ...base, name: "standard", confirmedMicroUSD: 5_100_000, workflowCeilingMicroUSD: 5_100_000, stepUpCeilingMicroUSD: 5_100_000 }, 5_100_000))
    .toBe("up to $5.10 · a step-up stays within your $5.10");
  expect(costWords({ ...base, name: "standard", confirmedMicroUSD: 5_100_000, stepUpCeilingMicroUSD: 13_600_000, stepUpTo: "careful" }, 5_100_000))
    .toBe("up to $5.10 · a step-up to careful ($13.60) is offered, not run");
  expect(costWords({ ...base, name: "standard", confirmedMicroUSD: 20_000_000, stepUpCeilingMicroUSD: 13_600_000 }, 5_100_000))
    .toBe("up to $5.10 · up to $13.60 if it steps up, within your $20.00");
  expect(costWords({ ...base, name: "standard", stepUpCeilingMicroUSD: 5_100_000 }, 5_100_000)).toBe("up to $5.10");
  expect(costWords({ ...base, name: "standard", stepUpCeilingMicroUSD: 13_600_000 }, 5_100_000)).toBe("up to $5.10 · up to $13.60 if it steps up");
  const card = workflowCard({ ...base, name: "careful", ceilingMicroUSD: 5_100_000, confirmedMicroUSD: 5_100_000, workflowCeilingMicroUSD: 13_600_000 }, { nodes: [] });
  expect(card.querySelector(".workflow-head")!.textContent).toContain("capped at your confirmed $5.10 (careful's ceiling is $13.60)");
});

// testdata/progress.json is the Go side's table too (internal/roomview progress_test.go): the room
// page's Will run row words the stages that already ran as maku task and Slack do.
test("Will run marks the stages that ran, as roomview.PathLine does", async () => {
  const { progress, pathWords } = await import("../src/profile");
  const { default: cases } = await import("../../internal/roomview/testdata/progress.json");
  for (const c of cases as { name: string; path: string[]; runs: { persona?: string; role?: string; outcome?: string; verdict?: string }[]; want: string }[]) {
    const runs = c.runs.map((r) => ({ persona: r.persona ?? "", role: r.role ?? "", outcome: r.outcome, verdict: r.verdict }));
    expect(pathWords(progress(c.path, runs)), c.name).toBe(c.want);
  }
});

test("after a step-up the card shows the scoper done, not dropped or run again", () => {
  const node = (persona: string, outcome?: string) =>
    ({ run: persona, persona, role: persona, round: 1, fromSeq: 0, toSeq: 0, outcome, tools: { ok: 0, failed: 0 } });
  const card = workflowCard({ name: "careful", source: "escalation", cohort: "labeled", reason: "stalled" },
    { plan: ["implementer", "reviewer"], nodes: [node("scoper", "succeeded"), node("implementer", "failed")] });
  const row = [...card.querySelectorAll(".workflow-row")].find((r) => r.textContent?.startsWith("Will run"));
  expect(row?.querySelector(".workflow-value")?.textContent).toBe("scoper ✓ · implementer → reviewer → CI → merge gate");
});
