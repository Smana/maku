// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { renderProfile } from "../src/profile";
import type { Summary } from "../src/summary";

type Profile = NonNullable<Summary["status"]["profile"]>;
const summary = (profile?: Profile): Summary => ({
  apiVersion: "summary/v1", room: "3kq7x2ma", url: "https://rooms.example/r/3kq7x2ma",
  status: { phase: "Done", run: null, budget: null, pr: null, issue: null, lastVerdict: null,
    ...(profile ? { profile } : {}) },
  needsYou: [], actions: [], notes: { untrusted: true, items: [] }, cursor: "seq:1",
});
const render = (s: Summary) => { const root = document.createElement("div"); renderProfile(root, s); return root; };

const frontier = (): Profile => ({ name: "review-frontier", source: "scoper", cohort: "treatment",
  recommended: "review-frontier", reason: "observe", why: "The change spans several files." });

describe("the Harness profile fold", () => {
  it("is absent without a profile", () => {
    expect(render(summary()).childElementCount).toBe(0);
  });

  it("names the active profile and shows every field", () => {
    const root = render(summary(frontier()));
    expect(root.querySelector("summary")!.textContent).toBe("Harness profile · review-frontier · treatment");
    expect(root.querySelector(".profile-source")!.textContent).toBe("Source: scoper");
    expect(root.querySelector(".profile-recommended")!.textContent).toBe("Recommended: review-frontier");
    expect(root.querySelector(".profile-reason")!.textContent).toBe("Reason: observe");
    expect(root.querySelector(".profile-why")!.textContent).toBe("Why: The change spans several files.");
  });

  it("distinguishes an observe-mode recommendation from the active floor", () => {
    const root = render(summary({ ...frontier(), name: "review-floor", source: "floor" }));
    expect(root.querySelector("summary")!.textContent).toBe("Harness profile · review-floor · treatment");
    expect(root.querySelector(".profile-source")!.textContent).toBe("Source: floor");
    expect(root.querySelector(".profile-recommended")!.textContent).toBe("Recommended: review-frontier");
    expect(root.querySelector(".profile-reason")!.textContent).toBe("Reason: observe");
  });

  it("renders the explanation as text", () => {
    const root = render(summary({ ...frontier(), why: '<img src="x" onerror="alert(1)">' }));
    expect(root.querySelector("img")).toBeNull();
    expect(root.textContent).toContain('<img src="x" onerror="alert(1)">');
  });

  it("stays open across a refresh once opened", () => {
    const root = render(summary(frontier()));
    root.querySelector("details")!.open = true;
    renderProfile(root, summary(frontier()));
    expect(root.querySelectorAll("details.profile")).toHaveLength(1);
    expect(root.querySelector("details")!.open).toBe(true);
  });
});
