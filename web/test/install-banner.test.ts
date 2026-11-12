import { describe, expect, it } from "vitest";
import { mountInstallBanner, renderInstallBanner } from "../src/install-banner";

const answer = (status: number, body: unknown) => (async (path: string) => {
  expect(path).toBe("/api/install");
  return new Response(JSON.stringify(body), { status });
}) as unknown as typeof fetch;

describe("the install banner", () => {
  it("renders nothing for an empty list", () => {
    expect(renderInstallBanner([])).toBeNull();
    expect(renderInstallBanner(["not_a_code"])).toBeNull();
  });
  it("says the runs are not sandboxed under unsafeAllowRunc", () => {
    const b = renderInstallBanner(["unsafe_allow_runc"])!;
    expect(b.getAttribute("role")).toBe("alert");
    expect(b.textContent).toBe("Runs are not sandboxed: sandbox.unsafeAllowRunc is set. Use this install for demos only.");
  });
  it("names each unsafe setting once", () => {
    const b = renderInstallBanner(["unsafe_allow_runc", "network_policy_none", "shared_git_token"])!;
    expect(b.textContent).toContain("sandbox.unsafeAllowRunc");
    expect(b.textContent).toContain("network policies are off");
    expect(b.textContent).toContain("runController.unsafeSharedGitToken");
    expect(b.textContent!.endsWith("Use this install for demos only.")).toBe(true);
  });
  it("is mounted above the page from GET /api/install", async () => {
    document.body.innerHTML = `<div id="app"></div>`;
    const app = document.getElementById("app")!;
    await mountInstallBanner(app, answer(200, { unsafe: ["unsafe_allow_runc"] }));
    const b = document.body.firstElementChild!;
    expect(b.getAttribute("role")).toBe("alert");
    expect(b.nextElementSibling).toBe(app);
  });
  it("renders nothing when the list is empty or the fetch fails", async () => {
    for (const get of [answer(200, { unsafe: [] }), answer(503, {}), answer(401, {}), answer(200, { unsafe: "x" }),
      (async () => { throw new Error("offline"); }) as unknown as typeof fetch]) {
      document.body.innerHTML = `<div id="app"></div>`;
      await mountInstallBanner(document.getElementById("app")!, get);
      expect(document.querySelector("[role=alert]")).toBeNull();
    }
  });
});
