// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { mountCoordinationPanel, renderCoordinationNode } from "../src/coordination";
import type { CoordinationConfirmation, CoordinationFlow, CoordinationNode, CoordinationView } from "../src/summary";

const child = (o: Partial<CoordinationNode>): CoordinationNode => ({
  id: "contract", kind: "child", childID: "contract", repository: "acme/api", roles: ["implementer", "reviewer"], disabled: true, ...o,
});

const planned: CoordinationFlow = {
  planVersion: 2, planHash: "0123456789abcdef", restrictedChildren: 0,
  nodes: [
    child({ requiredConfirmation: "parent" }),
    child({ id: "consumer", childID: "consumer", repository: "acme/web", requiredConfirmation: "parent", reason: "artifact_pending" }),
    { id: "integration:beforeDependentMerge", kind: "integration", roles: ["triager"], disabled: true },
    { id: "integration:final", kind: "integration", roles: ["triager"], disabled: true },
  ],
  edges: [
    { from: "contract", to: "consumer", kind: "artifact", name: "contract/consumer-v1-schema", state: "pending" },
    { from: "integration:beforeDependentMerge", to: "consumer", kind: "integration", state: "pending" },
  ],
};

const filed: CoordinationFlow = {
  ...planned,
  nodes: [
    child({ disabled: false, phase: "Merged", reason: "merged", room: "abcdefgh", issueURL: "https://github.com/acme/api/issues/1", prURL: "https://github.com/acme/api/pull/2" }),
    child({ id: "consumer", childID: "consumer", repository: "acme/web", disabled: false, phase: "AwaitingHuman", room: "bcdefghi", prURL: "https://github.com/acme/web/pull/4" }),
    { id: "integration:beforeDependentMerge", kind: "integration", roles: ["triager"], disabled: false, phase: "passed" },
    { id: "integration:final", kind: "integration", roles: ["triager"], disabled: true },
  ],
  edges: [
    { from: "contract", to: "consumer", kind: "artifact", name: "contract/consumer-v1-schema", state: "landed" },
    { from: "integration:beforeDependentMerge", to: "consumer", kind: "integration", state: "released" },
  ],
};

function mount() {
  const root = document.createElement("div");
  document.body.replaceChildren(root);
  return { root, panel: mountCoordinationPanel(root) };
}

describe("coordination panel", () => {
  it("draws a distinct plan and live coordination diagram", () => {
    const { root, panel } = mount();
    panel.set(planned);
    const nodes = [...root.querySelectorAll<HTMLElement>(".rail-coordination-node")];
    expect(nodes.map((n) => n.dataset.node)).toEqual(["contract", "consumer", "integration:beforeDependentMerge", "integration:final"]);
    for (const n of nodes) expect(n.querySelector("button")?.disabled).toBe(true);
    expect(root.textContent).toContain("contract/consumer-v1-schema");
    expect(root.textContent).toContain("awaiting parent confirmation");
    expect(root.querySelector("a")).toBeNull();

    panel.set(filed);
    const links = [...root.querySelectorAll<HTMLAnchorElement>("a")].map((a) => a.getAttribute("href"));
    expect(links).toContain("/r/abcdefgh");
    expect(links).toContain("https://github.com/acme/api/pull/2");
    expect(root.querySelector('.rail-coordination-edge[data-kind="artifact"]')?.getAttribute("data-state")).toBe("landed");
    expect(root.textContent).toContain("released");
    // No parent run or merge node, and nothing of the splitter's vocabulary.
    expect(root.textContent).not.toMatch(/\bmerge node\b|parent run/);
    expect(root.querySelector(".rail-part, .rail-parent, .rail-chip")).toBeNull();
    expect(root.querySelector('[data-node="parent"]')).toBeNull();
  });

  it("renders only the restricted count for a denied child", () => {
    const { root, panel } = mount();
    panel.set({ ...filed, restrictedChildren: 1, nodes: filed.nodes.slice(0, 1), edges: [] });
    expect(root.querySelectorAll(".rail-coordination-node")).toHaveLength(1);
    expect(root.querySelector(".rail-coordination-restricted")?.textContent).toBe("1 child you cannot read");
    expect(root.innerHTML).not.toContain("acme/web");
    expect(root.innerHTML).not.toContain("consumer");
  });

  it("hides itself without a family", () => {
    const { root, panel } = mount();
    panel.set(planned);
    panel.set(undefined);
    expect(root.querySelector<HTMLElement>(".rail-coordination")!.hidden).toBe(true);
  });

  it("never links a room id or URL it did not validate", () => {
    const li = renderCoordinationNode(child({ disabled: false, room: "../../x", prURL: "javascript:alert(1)", phase: "<img src=x>" }));
    expect(li.querySelector("a")).toBeNull();
    expect(li.querySelector("img")).toBeNull();
    expect(li.textContent).toContain("<img src=x>");
  });
});

describe("coordination controls", () => {
  const view = (restricted = 0): CoordinationView => ({
    parentTask: "pppppppp", phase: "Planned", planHash: "0123456789abcdef", restrictedChildren: restricted,
    plan: { id: "pppppppp", version: 2, title: "t", children: [{ id: "contract", repository: "acme/api" }] },
    children: [{ id: "contract", repository: "acme/api", fingerprint: "fp-contract" }],
    budget: { planningReservedMicroUSD: 7, planningSettledMicroUSD: 3 },
  });
  const childFlow: CoordinationFlow = { ...planned, nodes: [child({ requiredConfirmation: "child" })], edges: [] };

  it("binds the parent confirmation to the exact plan and hash", () => {
    const sent: CoordinationConfirmation[] = [];
    const { root, panel } = mountWith((c) => { sent.push(c); return Promise.resolve(); });
    panel.set(planned, view());
    const b = root.querySelector<HTMLButtonElement>('button[data-act="coordination-confirm"]')!;
    expect(b.textContent).toBe("Confirm plan v2");
    b.click();
    expect(sent).toHaveLength(1);
    expect(sent[0].planHash).toBe("0123456789abcdef");
    expect(sent[0].plan.version).toBe(2);
    expect(sent[0].parent).toEqual({ planHash: "0123456789abcdef" });
    expect(sent[0].child).toBeUndefined();
    expect(sent[0].planning).toEqual({ attemptID: "", effectiveRequester: "", reservedMicroUSD: 7, settledMicroUSD: 3 });
  });

  it("binds a child authorization to its fingerprint and version", () => {
    const sent: CoordinationConfirmation[] = [];
    const { root, panel } = mountWith((c) => { sent.push(c); return Promise.resolve(); });
    panel.set(childFlow, view());
    root.querySelector<HTMLButtonElement>('[data-node="contract"] button[data-act="coordination-authorize"]')!.click();
    expect(sent[0].child).toEqual({ childID: "contract", childFingerprint: "fp-contract", planVersion: 2 });
    expect(sent[0].parent).toBeUndefined();
  });

  it("offers no decision while a child is restricted, or without a view", () => {
    const { root, panel } = mountWith(() => Promise.resolve());
    panel.set(planned, view(1));
    expect(root.querySelector('button[data-act^="coordination-"]')).toBeNull();
    panel.set(planned, undefined);
    expect(root.querySelector('button[data-act^="coordination-"]')).toBeNull();
  });

  it("says a refusal and asks for a fresh summary", async () => {
    let refreshed = 0;
    const { root, panel } = mountWith(() => Promise.reject(new Error("stale_plan")), () => { refreshed++; });
    panel.set(planned, view());
    root.querySelector<HTMLButtonElement>('button[data-act="coordination-confirm"]')!.click();
    await Promise.resolve(); await Promise.resolve();
    expect(root.querySelector(".rail-coordination-status")?.textContent).toContain("stale_plan");
    expect(refreshed).toBe(1);
  });
});

function mountWith(confirm: (c: CoordinationConfirmation) => Promise<void>, refresh: () => void = () => {}) {
  const root = document.createElement("div");
  document.body.replaceChildren(root);
  return { root, panel: mountCoordinationPanel(root, { confirm, refresh }) };
}

describe("coordination recovery", () => {
  const view: CoordinationView = {
    parentTask: "pppppppp", phase: "Coordinating", planHash: "0123456789abcdef", restrictedChildren: 0,
    plan: { id: "pppppppp", version: 3, title: "t" }, children: [], budget: {},
    recovery: { code: "retry_integration", planVersion: 3 },
  };
  it("offers the one proposed recovery, bound to its code and version", () => {
    const sent: { code: string; planVersion: number }[] = [];
    const root = document.createElement("div");
    document.body.replaceChildren(root);
    const panel = mountCoordinationPanel(root, { confirm: () => Promise.resolve(), recover: (r) => { sent.push(r); return Promise.resolve(); }, refresh: () => {} });
    panel.set(filed, view);
    const b = root.querySelector<HTMLButtonElement>('button[data-act="coordination-recover"]')!;
    expect(b.textContent).toBe("Recover: retry_integration");
    b.click();
    expect(sent).toEqual([{ code: "retry_integration", planVersion: 3 }]);
  });
  it("offers none without a proposal or with a child withheld", () => {
    const root = document.createElement("div");
    const panel = mountCoordinationPanel(root, { confirm: () => Promise.resolve(), recover: () => Promise.resolve(), refresh: () => {} });
    panel.set(filed, { ...view, recovery: undefined });
    expect(root.querySelector('button[data-act="coordination-recover"]')).toBeNull();
    panel.set({ ...filed, restrictedChildren: 1 }, { ...view, restrictedChildren: 1 });
    expect(root.querySelector('button[data-act="coordination-recover"]')).toBeNull();
  });
});
