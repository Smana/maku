// SPDX-License-Identifier: Apache-2.0

// The install's unsafe settings (GET /api/install, v4 D18): a persistent banner above the rooms list
// and every settings page while the broker lists any. The codes are bounded; an unknown one is
// ignored. A failed read shows nothing: the page works without it, and its own reads sign in again.

const SAYS: Record<string, string> = {
  unsafe_allow_runc: "Runs are not sandboxed: sandbox.unsafeAllowRunc is set.",
  network_policy_none: "Nothing confines a run's network: network policies are off.",
  shared_git_token: "Every role shares one write token: runController.unsafeSharedGitToken is set.",
};

// renderInstallBanner is the banner for these codes, or null when none is known.
export function renderInstallBanner(unsafe: string[]): HTMLElement | null {
  const lines = [...new Set(unsafe)].map((c) => SAYS[c]).filter((s): s is string => s !== undefined);
  if (lines.length === 0) return null;
  const b = document.createElement("div");
  b.className = "banner banner-install";
  b.setAttribute("role", "alert");
  b.textContent = [...lines, "Use this install for demos only."].join(" ");
  return b;
}

// mountInstallBanner reads GET /api/install and puts the banner before el, outside the part of the
// page each view redraws.
export async function mountInstallBanner(el: HTMLElement, get: typeof fetch = fetch): Promise<void> {
  try {
    const r = await get("/api/install", { credentials: "same-origin" });
    if (!r.ok) return;
    const body = (await r.json()) as { unsafe?: unknown };
    if (!Array.isArray(body.unsafe)) return;
    const b = renderInstallBanner(body.unsafe.filter((c): c is string => typeof c === "string"));
    if (b) el.before(b);
  } catch { /* unreadable: no banner */ }
}
