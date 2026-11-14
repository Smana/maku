// SPDX-License-Identifier: Apache-2.0

// The offline settings preview: the settings pages and approval memory as main.ts routes them, fed
// canned answers instead of the broker. `npm run preview`, then open /preview/settings.html with
// ?path= set to one of: /settings, /settings/merge-policies (nine repositories, enough to filter),
// /settings/merge-policies/Smana/maku (bound, repository authority: the file holds the bindings),
// /settings/merge-policies/Smana/cloud-native-ref (bound, central authority, out of sync with its
// file, every evidence state), /settings/merge-policies/Smana/legacy (not migrated),
// /settings/merge-policies/policies/docs (a policy's page), /settings/evidence,
// /settings/approval-memory. ?theme=dark for the dark palette, ?q= to filter a list.

import { mountApprovalMemory } from "../src/approval-memory";
import { mountSettings, settingsFrame } from "../src/settings";
import { applyTheme, initTheme } from "../src/theme";
import { cnrPage, legacyPage, makuPage, policies, smallPages } from "./settings-fixtures";

const params = new URLSearchParams(location.search);
const path = params.get("path") ?? "/settings";
// Longest prefix first: "/api/policy/repositories" is a prefix of every repository's route.
const answers: Record<string, unknown> = {
  "/api/policy/repositories/Smana/maku": makuPage,
  "/api/policy/repositories/Smana/cloud-native-ref": cnrPage,
  "/api/policy/repositories/Smana/legacy": legacyPage,
  ...Object.fromEntries(Object.entries(smallPages).map(([r, p]) => [`/api/policy/repositories/${r}`, p])),
  "/api/policy/repositories": { repositories: ["Smana/maku", "Smana/cloud-native-ref", "Smana/legacy", ...Object.keys(smallPages)] },
  ...Object.fromEntries(policies.map((p) => [`/api/policy/policies/${p.name}`, p])),
  "/api/policy/policies": { policies },
  "/api/approval-memory": { grants: [] },
};
const get = (async (url: string) => {
  const key = Object.keys(answers).sort((a, b) => b.length - a.length).find((k) => url.startsWith(k));
  return key ? new Response(JSON.stringify(answers[key])) : new Response(JSON.stringify({ error: "not_found" }), { status: 404 });
}) as unknown as typeof fetch;

const theme = initTheme();
const t = params.get("theme");
if (t === "light" || t === "dark") applyTheme(t);
const app = document.getElementById("app")!;
if (path === "/settings/approval-memory") mountApprovalMemory(settingsFrame(app, "Approval memory", "/settings/approval-memory", theme), get);
else void mountSettings(app, path, get, theme);
