// SPDX-License-Identifier: Apache-2.0

import { afterEach, describe, expect, it, vi } from "vitest";
import { actionsFor, fetchGrants, type Grant, history, mountApprovalMemory, moveGrant, predicate, type Proposal,
  proposalView, ruleFromQuery } from "../src/approval-memory";

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
    expect([...rows[0].querySelectorAll("button")].map((b) => b.dataset.act)).toEqual(["revoke", "renew", "history"]);
    expect([...rows[1].querySelectorAll("button")].map((b) => b.dataset.act)).toEqual(["reconfirm", "revoke", "history"]);
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
  it("splits a grant's audit into its usage and its outcome", () => {
    const box = history({ grant, outside: [], events: [
      { event: "created", version: 1, actor: "smana", at: "2026-11-01T10:00:00Z" },
      { event: "used", version: 1, actor: "system:factory", pr: 51, head: "0123456789abcdef", at: "2026-11-02T10:00:00Z" },
      { event: "disabled", version: 1, actor: "system:factory", reason: "main_red", pr: 51, at: "2026-11-03T10:00:00Z" },
    ] });
    const [usage, outcome] = [...box.querySelectorAll("ul")].map((u) => u.textContent);
    expect(usage).toContain("used (v1) by system:factory on #51 at 0123456");
    expect(usage).not.toContain("disabled");
    expect(outcome).toContain("disabled (v1) by system:factory on #51: main_red");
    expect(history({ grant, outside: [], events: [] }).textContent).toContain("Never used.");
  });
  it("opens a grant's history on demand", async () => {
    const get = fake({
      [`/api/approval-memory/grants/${grant.id}`]: { status: 200, body: { grant, outside: [], events: [
        { event: "used", version: 2, actor: "system:factory", pr: 7, at: "2026-11-02T10:00:00Z" }] } },
      "/api/approval-memory/grants": { status: 200, body: { grants: [grant] } },
    });
    const root = document.createElement("div");
    mountApprovalMemory(root, get);
    await flush();
    root.querySelector<HTMLButtonElement>("button[data-act=history]")!.click();
    await flush();
    await flush();
    expect(root.querySelector(".grant-history")!.textContent).toContain("used (v2) by system:factory on #7");
  });
  it("prefills merge and create rule from a decision's link", () => {
    expect(ruleFromQuery("?repository=smana/x&class=docs-broad&pr=12&policy=0123456789ab&file=a.md&file=b.md")).toEqual({
      repository: "smana/x", class: "docs-broad", pullRequest: 12, policyHash: "0123456789ab", caughtFiles: ["a.md", "b.md"] });
    expect(ruleFromQuery("?pr=-3").pullRequest).toBe(0);
  });
  it("shows the exact rule, the future pull requests it matches, what stays outside, expiry, revocation and merge once", () => {
    const p: Proposal = { predicate: { repository: "smana/x", class: "docs-broad", files: ["docs/a\\.md"], policyHash: "0123456789ab" },
      matches: "a future pull request of smana/x …", outside: ["the breaker and the daily caps"], expiresAt: "2027-02-06T00:00:00Z",
      revoke: "POST /api/approval-memory/grants/<id>/revoke, once saved", mergeOnce: "approve the pull request's head on GitHub" };
    const t = proposalView(p).textContent!;
    for (const want of ["every caught file matches docs/a\\.md", "Future pull requests it would match", "a future pull request of smana/x",
      "the breaker and the daily caps", "Expires 2027-02-06", "/revoke, once saved", "Merge once instead: approve"]) expect(t).toContain(want);
  });
  it("previews before it saves, then saves the previewed rule and lists it", async () => {
    const calls: Call[] = [];
    const prop = { predicate: { repository: "smana/x", class: "docs-broad", files: ["a\\.md"], policyHash: "0123456789ab" },
      matches: "m", outside: ["o"], expiresAt: "2027-02-06T00:00:00Z", revoke: "r", mergeOnce: "approve" };
    const get = fake({
      "/api/approval-memory/preview": { status: 200, body: prop },
      "/api/approval-memory/grants?": { status: 200, body: { grants: [] } },
      "/api/approval-memory/grants": { status: 201, body: { ...prop, grant } },
    }, calls);
    const root = document.createElement("div");
    mountApprovalMemory(root, get);
    await flush();
    const set = (name: string, v: string) => { root.querySelector<HTMLInputElement>(`[name=${name}]`)!.value = v; };
    set("repository", "Smana/X"); set("class", "docs-broad"); set("pr", "12"); set("policy", "0123456789ab"); set("files", "a.md\n\n");
    const create = root.querySelector<HTMLButtonElement>("button[data-act=create]")!;
    expect(create.hidden).toBe(true);
    root.querySelector<HTMLButtonElement>("button[data-act=preview]")!.click();
    await flush();
    const preview = calls.find((c) => c.path === "/api/approval-memory/preview")!;
    expect(JSON.parse(String(preview.init!.body))).toEqual({ repository: "smana/x", class: "docs-broad", policyHash: "0123456789ab",
      pullRequest: 12, caughtFiles: ["a.md"], lifetimeDays: 90 });
    expect(root.querySelector(".rule-preview")!.textContent).toContain("before it is saved");
    expect(create.hidden).toBe(false);
    create.click();
    await flush();
    await flush();
    expect(calls.some((c) => c.path === "/api/approval-memory/grants" && c.init?.method === "POST")).toBe(true);
    expect(root.querySelector(".rule-preview")!.textContent).toContain("Rule saved: aaaaaaaa");
  });
  it("says why a preview was refused, and offers no save", async () => {
    const root = document.createElement("div");
    mountApprovalMemory(root, fake({ "/api/approval-memory/preview": { status: 403, body: { error: "repository_permission" } },
      "/api/approval-memory/grants": { status: 200, body: { grants: [] } } }));
    await flush();
    root.querySelector<HTMLButtonElement>("button[data-act=preview]")!.click();
    await flush();
    expect(root.querySelector(".rule-out")!.textContent).toBe("No preview: repository_permission");
    expect(root.querySelector<HTMLButtonElement>("button[data-act=create]")!.hidden).toBe(true);
  });
});
