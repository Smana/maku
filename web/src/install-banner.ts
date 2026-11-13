// SPDX-License-Identifier: Apache-2.0

// The install's unsafe settings (GET /api/install, v4 D18) and the factory's private-code warning
// (v4 D3): a persistent banner above the rooms list and every settings page while the broker lists
// any. The codes are bounded; an unknown one is ignored. A failed read shows nothing: the page works
// without it, and its own reads sign in again.

const SAYS: Record<string, string> = {
  unsafe_allow_runc: "Runs are not sandboxed: sandbox.unsafeAllowRunc is set.",
  network_policy_none: "Nothing confines a run's network: network policies are off.",
  shared_git_token: "Every role shares one write token: runController.unsafeSharedGitToken is set.",
  model_key_in_sandbox: "The model provider's key sits in every run's sandbox: runController.model.mode is secret.",
  private_code_unacknowledged: "A private repository is served without spec.privateCode.acknowledged: true. " +
    "That field states the code is sent to the configured models, which are usually SaaS. " +
    "Set it on the Repository: the next release refuses private code without it.",
};

// DEMOS_ONLY are the codes that weaken a run's isolation: the banner then says the install is for
// demos only. Private code is a choice to acknowledge, not an isolation setting.
const DEMOS_ONLY = new Set(["unsafe_allow_runc", "network_policy_none", "shared_git_token", "model_key_in_sandbox"]);

// renderInstallBanner is the banner for these codes, or null when none is known.
export function renderInstallBanner(unsafe: string[]): HTMLElement | null {
  const codes = [...new Set(unsafe)].filter((c) => SAYS[c] !== undefined);
  if (codes.length === 0) return null;
  const lines = codes.map((c) => SAYS[c] ?? "");
  if (codes.some((c) => DEMOS_ONLY.has(c))) lines.push("Use this install for demos only.");
  const b = document.createElement("div");
  b.className = "banner banner-install";
  b.setAttribute("role", "alert");
  b.textContent = lines.join(" ");
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
