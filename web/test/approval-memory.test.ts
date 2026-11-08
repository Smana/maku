// SPDX-License-Identifier: Apache-2.0

import { afterEach, describe, expect, it, vi } from "vitest";
import { actionsFor, fetchGrants, type Grant, mountApprovalMemory, moveGrant, predicate } from "../src/approval-memory";

const grant: Grant = {
  id: "aaaaaaaaaaaaaaaaaaaaaaaaaa", version: 2, repository: "smana/cloud-native-ref", class: "docs-broad",
  files: ["docs/security/a\\.md"], policyHash: "0123456789ab", originPR: 42, createdBy: "smana",
  expiresAt: "2027-02-01T00:00:00Z", status: "active",
};

type Call = { path: string; init?: RequestInit };

// fake answers each path with its body and records the calls.
function fake(answers: Record<string, { status: number; body: unknown }>, calls: Call[] = []): typeof fetch {
  return (async (path: string, init?: RequestInit) => {
    calls.push({ path, init });
    const key = Object.keys(answers).find((k) => path.startsWith(k));
    const a = key ? answers[key] : { status: 404, body: {} };
    return new Response(JSON.stringify(a.body), { status: a.status });
  }) as unknown as typeof fetch;
}

const flush = () => new Promise((r) => setTimeout(r, 0));

afterEach(() => { vi.restoreAllMocks(); });

describe("approval memory", () => {
  it("offers what each status allows: revoked and disabled are for good", () => {
    expect(actionsFor("active")).toEqual(["revoke", "renew"]);
    expect(actionsFor("suspended")).toEqual(["reconfirm", "revoke"]);
    expect(actionsFor("expired")).toEqual(["renew", "revoke"]);
    expect(actionsFor("revoked")).toEqual([]);
    expect(actionsFor("disabled")).toEqual([]);
  });
  it("shows a grant's exact predicate", () => {
    expect(predicate(grant)).toBe("smana/cloud-native-ref · docs-broad · every caught file matches docs/security/a\\.md · policy 0123456789ab");
    expect(predicate({ ...grant, diffShape: "links-only" })).toContain(", diff shape links-only");
  });
  it("lists with the search and the status as the query", async () => {
    const calls: Call[] = [];
    const l = await fetchGrants("security", "active", fake({ "/api/approval-memory/grants": { status: 200, body: { grants: [grant] } } }, calls));
    expect(calls[0].path).toBe("/api/approval-memory/grants?q=security&status=active");
    expect(l.grants.map((g) => g.id)).toEqual([grant.id]);
    await fetchGrants("", "", fake({ "/api/approval-memory/grants": { status: 200, body: {} } }, calls));
    expect(calls[1].path).toBe("/api/approval-memory/grants");
  });
  it("names a refused action by the broker's bounded code, never its free text", async () => {
    const refused = fake({ "/api/approval-memory/grants/": { status: 403, body: { error: "repository_permission" } } });
    await expect(moveGrant(grant.id, "revoke", {}, refused)).rejects.toThrow("repository_permission");
    const odd = fake({ "/api/approval-memory/grants/": { status: 409, body: { error: "<b>not a code</b>" } } });
    await expect(moveGrant(grant.id, "renew", {}, odd)).rejects.toThrow("HTTP 409");
  });
  it("renders each grant as text, with its actions, and revokes on confirmation", async () => {
    const calls: Call[] = [];
    const evil = { ...grant, id: "bbbbbbbbbbbbbbbbbbbbbbbbbb", createdBy: "<img src=x onerror=alert(1)>", status: "suspended" };
    const get = fake({
      "/api/approval-memory/grants/": { status: 200, body: { ...grant, status: "revoked" } },
      "/api/approval-memory/grants": { status: 200, body: { grants: [grant, evil] } },
    }, calls);
    const root = document.createElement("div");
    mountApprovalMemory(root, get);
    await flush();
    const rows = root.querySelectorAll<HTMLLIElement>("li.grant");
    expect(rows.length).toBe(2);
    expect(root.querySelector("img")).toBeNull();
    expect(rows[1].textContent).toContain("<img src=x onerror=alert(1)>");
    expect([...rows[0].querySelectorAll("button")].map((b) => b.dataset.act)).toEqual(["revoke", "renew"]);
    expect([...rows[1].querySelectorAll("button")].map((b) => b.dataset.act)).toEqual(["reconfirm", "revoke"]);
    expect(root.querySelector<HTMLAnchorElement>("a")!.getAttribute("href")).toBe("/api/approval-memory/export");
    vi.stubGlobal("confirm", () => false);
    rows[0].querySelector<HTMLButtonElement>("button[data-act=revoke]")!.click();
    await flush();
    expect(calls.some((c) => c.init?.method === "POST")).toBe(false);
    vi.stubGlobal("confirm", () => true);
    rows[0].querySelector<HTMLButtonElement>("button[data-act=revoke]")!.click();
    await flush();
    await flush();
    const post = calls.find((c) => c.init?.method === "POST")!;
    expect(post.path).toBe(`/api/approval-memory/grants/${grant.id}/revoke`);
    expect(JSON.parse(String(post.init!.body))).toEqual({ version: 2 });
    expect(calls.filter((c) => c.path.startsWith("/api/approval-memory/grants?") || c.path === "/api/approval-memory/grants").length).toBe(2);
  });
  it("says a list may be short when access could not be verified, and says a refusal", async () => {
    const root = document.createElement("div");
    mountApprovalMemory(root, fake({ "/api/approval-memory/grants": { status: 200, body: { grants: [], unverified: true } } }));
    await flush();
    expect(root.querySelector(".grants-note")!.textContent).toContain("could not be verified");
    const down = document.createElement("div");
    mountApprovalMemory(down, fake({ "/api/approval-memory/grants": { status: 503, body: {} } }));
    await flush();
    expect(down.querySelector(".grants-note")!.textContent).toBe("The grants are unavailable: 503.");
  });
});
