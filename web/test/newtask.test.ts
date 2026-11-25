// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from "vitest";
import { newTaskForm } from "../src/newtask";

function jsonResp(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("newTaskForm", () => {
  it("the new task form shows the card before it files", async () => {
    const calls: { url: string; body: Record<string, unknown> }[] = [];
    const hash1 = "sha256:" + "a".repeat(64);
    const hash2 = "sha256:" + "b".repeat(64);

    const get = vi.fn(async (url: string, init?: RequestInit) => {
      const body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : {};
      calls.push({ url, body });
      if (url === "/api/handoffs") {
        return jsonResp({
          id: "h1234567890",
          ready: true,
          workflow: "standard",
          budget: 5.1,
          repository: "Smana/maku",
          title: "docs: fix dead install link",
          body: "## What is wrong\nDead link.",
        });
      }
      if (url === "/api/handoffs/h1234567890/check") {
        const wf = typeof body.workflow === "string" ? body.workflow : "standard";
        const outcome = typeof body.outcome === "string" ? body.outcome : "change";
        const careful = wf === "careful";
        return jsonResp({
          id: "h1234567890",
          hash: careful || outcome === "plan-first" ? hash2 : hash1,
          canStart: true,
          linked: true,
          maintainer: true,
          workflow: wf,
          repository: "Smana/maku",
          title: "docs: fix dead install link",
          body: "## What is wrong\nDead link.",
          defaultChoice: {
            name: "standard",
            description: "Default workflow",
            ceilingUSD: 5.1,
            path: ["implementer", "reviewer", "CI", "merge gate"],
          },
          activeChoice: careful
            ? {
                name: "careful",
                description: "Careful multi-file workflow",
                ceilingUSD: 13.6,
                path: ["implementer", "tester", "reviewer", "CI", "merge gate"],
              }
            : {
                name: "standard",
                description: "Default workflow",
                ceilingUSD: 5.1,
                path: ["implementer", "reviewer", "CI", "merge gate"],
              },
          workflowChoices: [
            { name: "standard", description: "Default workflow", ceilingUSD: 5.1, path: ["implementer", "reviewer", "CI", "merge gate"] },
            { name: "careful", description: "Careful multi-file workflow", ceilingUSD: 13.6, path: ["implementer", "tester", "reviewer", "CI", "merge gate"] },
          ],
          plan: {
            repository: "Smana/maku",
            budget: { ceilingUSD: careful ? 13.6 : 5.1 },
            issues: [{
              title: "docs: fix dead install link",
              body: "## What is wrong\nDead link.",
              profile: careful ? "careful" : "",
              outcome,
            }],
          },
        });
      }
      if (url === "/api/handoffs/h1234567890/start") {
        return jsonResp({
          repository: "Smana/maku",
          issue: 42,
          url: "https://github.com/Smana/maku/issues/42",
          labels: ["workflow:careful", "plan-first", "factory/ready"],
          viewer: "dev",
        }, 201);
      }
      return jsonResp({ error: "not_found" }, 404);
    });

    const root = newTaskForm(get as unknown as typeof fetch, ["Smana/maku", "Smana/ai-native-ref"]);
    const textarea = root.querySelector('textarea[name="text"]') as HTMLTextAreaElement;
    const repoSelect = root.querySelector('select[name="repository"]') as HTMLSelectElement;
    const form = root.querySelector("form.new-task-form") as HTMLFormElement;

    expect(textarea.getAttribute("aria-label")).toBe("What should change?");
    expect([...repoSelect.options].map((o) => o.value)).toEqual(["", "Smana/ai-native-ref", "Smana/maku"]);

    textarea.value = "fix the dead install link";
    repoSelect.value = "Smana/maku";
    form.dispatchEvent(new Event("submit", { cancelable: true }));

    await vi.waitFor(() => expect(root.querySelector(".workflow-card")).not.toBeNull());
    // Nothing has been filed yet: only plan + check were called.
    expect(calls.map((c) => c.url)).toEqual(["/api/handoffs", "/api/handoffs/h1234567890/check"]);

    const card = root.querySelector(".workflow-card")!;
    expect(card.textContent).toContain("a pull request");
    expect(card.textContent).toContain("standard · this repository's default");
    expect(card.textContent).toContain("implementer → reviewer → CI → merge gate");
    expect(card.textContent).toContain("up to $5.10");
    expect(root.querySelector(".new-task-title")?.textContent).toBe("docs: fix dead install link");
    expect(root.querySelector(".new-task-body")?.textContent).toContain("Dead link.");

    // Maintainer gets a described workflow select and Plan first checkbox.
    const wfSelect = root.querySelector('select[name="workflow"]') as HTMLSelectElement;
    expect(wfSelect).not.toBeNull();
    expect(wfSelect.options[1].textContent).toContain("careful");
    expect(wfSelect.options[1].textContent).toContain("Careful multi-file workflow");
    expect(wfSelect.options[1].textContent).toContain("up to $13.60");

    // Toggle Plan first -> re-checks with outcome: "plan-first".
    const planFirst = root.querySelector('input[name="planFirst"]') as HTMLInputElement;
    expect(planFirst.checked).toBe(false);
    planFirst.checked = true;
    planFirst.dispatchEvent(new Event("change"));
    await vi.waitFor(() => expect(root.querySelector(".workflow-card")?.textContent).toContain("a plan to approve, then a pull request"));

    // Clicking Start now files the issue through POST /api/handoffs/{id}/start.
    const startBtn = root.querySelector('button[data-act="start-task"]') as HTMLButtonElement;
    expect(startBtn).not.toBeNull();
    startBtn.click();
    await vi.waitFor(() => expect(root.querySelector(".new-task-filed")?.textContent).toContain("#42"));
    expect(calls.at(-1)?.url).toBe("/api/handoffs/h1234567890/start");
  });

  it("a viewer with no GitHub link cannot start", async () => {
    const calls: string[] = [];
    const get = vi.fn(async (url: string) => {
      calls.push(url);
      if (url === "/api/handoffs") {
        return jsonResp({
          id: "h-unlinked",
          ready: true,
          workflow: "standard",
          budget: 2.0,
          repository: "Smana/maku",
          title: "docs: fix typo",
          body: "## What is wrong\nTypo.",
        });
      }
      if (url === "/api/handoffs/h-unlinked/check") {
        return jsonResp({
          id: "h-unlinked",
          hash: "sha256:" + "c".repeat(64),
          canStart: false,
          canStartWhy: "not_linked",
          linked: false,
          workflow: "standard",
          repository: "Smana/maku",
          title: "docs: fix typo",
          body: "## What is wrong\nTypo.",
          plan: {
            repository: "Smana/maku",
            budget: { ceilingUSD: 2.0 },
            issues: [{ title: "docs: fix typo", body: "## What is wrong\nTypo." }],
          },
        });
      }
      return jsonResp({ error: "not_found" }, 404);
    });

    const root = newTaskForm(get as unknown as typeof fetch, ["Smana/maku"]);
    (root.querySelector('textarea[name="text"]') as HTMLTextAreaElement).value = "fix typo";
    (root.querySelector("form.new-task-form") as HTMLFormElement).dispatchEvent(new Event("submit", { cancelable: true }));

    await vi.waitFor(() => expect(root.querySelector(".new-task-link-notice")).not.toBeNull());
    expect(root.textContent).toContain("Link your GitHub account");
    expect(root.querySelector('a[href="/link/github"]')).not.toBeNull();
    expect(root.querySelector('button[data-act="start-task"]')).toBeNull();
    expect(calls).not.toContain("/api/handoffs/h-unlinked/start");
  });

  it("shows the planner's one question and continues once answered", async () => {
    let step = 0;
    const get = vi.fn(async (url: string) => {
      if (url === "/api/handoffs") {
        step++;
        if (step === 1) {
          return jsonResp({
            id: "h-question",
            ready: false,
            question: { text: "Which install URL?", recommended: "https://example.test/install" },
            repository: "Smana/maku",
            title: "docs: fix install link",
            body: "## What is wrong\nBroken link.",
          });
        }
        return jsonResp({ id: "h-question", ready: true });
      }
      if (url === "/api/handoffs/h-question/check") {
        return jsonResp({
          id: "h-question",
          hash: "sha256:" + "d".repeat(64),
          canStart: true,
          linked: true,
          workflow: "standard",
          repository: "Smana/maku",
          title: "docs: fix install link",
          body: "## What is wrong\nBroken link.",
          plan: {
            repository: "Smana/maku",
            budget: { ceilingUSD: 2.0 },
            issues: [{ title: "docs: fix install link", body: "## What is wrong\nBroken link." }],
          },
        });
      }
      return jsonResp({}, 404);
    });

    const root = newTaskForm(get as unknown as typeof fetch, ["Smana/maku"]);
    (root.querySelector('textarea[name="text"]') as HTMLTextAreaElement).value = "install link is broken";
    (root.querySelector("form.new-task-form") as HTMLFormElement).dispatchEvent(new Event("submit", { cancelable: true }));

    await vi.waitFor(() => expect(root.querySelector(".new-task-question")).not.toBeNull());
    expect(root.querySelector(".new-task-q-text")?.textContent).toBe("Which install URL?");
    expect(root.querySelector('button[data-act="start-task"]')).toBeNull();

    (root.querySelector('button[data-act="accept-rest"]') as HTMLButtonElement).click();
    await vi.waitFor(() => expect(root.querySelector('button[data-act="start-task"]')).not.toBeNull());
  });
});
