// SPDX-License-Identifier: Apache-2.0

// The offline settings preview: the settings pages and approval memory as main.ts routes them, fed
// canned answers instead of the broker. `npm run preview`, then open /preview/settings.html
// (?path=/settings/merge-policies, ?path=/settings/approval-memory; ?theme=dark for the dark palette).

import { mountApprovalMemory } from "../src/approval-memory";
import { mountSettings, settingsFrame } from "../src/settings";
import { applyTheme, initTheme } from "../src/theme";

const params = new URLSearchParams(location.search);
const path = params.get("path") ?? "/settings";
const answers: Record<string, unknown> = {
  "/api/policy/repositories": { repositories: ["Smana/maku", "Smana/cloud-native-ref"] },
  "/api/approval-memory": { grants: [] },
};
const get = (async (url: string) => {
  const key = Object.keys(answers).find((k) => url.startsWith(k));
  return key ? new Response(JSON.stringify(answers[key])) : new Response(JSON.stringify({ error: "not_found" }), { status: 404 });
}) as unknown as typeof fetch;

const theme = initTheme();
const t = params.get("theme");
if (t === "light" || t === "dark") applyTheme(t);
const app = document.getElementById("app")!;
if (path === "/settings/approval-memory") mountApprovalMemory(settingsFrame(app, "Approval memory", "/settings/approval-memory", theme), get);
else void mountSettings(app, path, get, theme);
