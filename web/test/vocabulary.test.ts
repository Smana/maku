// SPDX-License-Identifier: Apache-2.0

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test } from "vitest";

const TIER_RE = /\btier (light|standard|frontier)\b/i;
const TRIAGER_RE = /\(triager\)/;
const PROFILE_RE = /\bprofile\b/i;

// Non-user-visible literals (DOM class names, dataset keys, JSON/API keys, CSS selectors).
const ALLOWED = new Set([
  "profile",
  "details.profile",
  "profile-source",
  "profile-cohort",
  "profile-requested",
  "profile-refused",
  "profile-recommended",
  "profile-reason",
  "profile-why",
  "class-profiles",
  "class-profile-list",
  "profiles",
  "harnessProfiles",
  "./profile",
]);

function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/(^|[^:\\])\/\/.*$/gm, "$1");
}

function stringLiterals(src: string): string[] {
  const out: string[] = [];
  const re = /(["'`])((?:\\.|(?!\1)[^\\])*)\1/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(src)) !== null) {
    // Strip JS template interpolations (${...}) so identifier names are not mistaken for UI copy.
    const text = m[1] === "`" ? m[2].replace(/\$\{[^}]*\}/g, "") : m[2];
    out.push(text);
  }
  return out;
}

test("web/src user-visible strings use workflow, never tier <name>, (triager), or profile", () => {
  const dir = join(__dirname, "..", "src");
  const bad: string[] = [];
  for (const name of readdirSync(dir).filter((f) => f.endsWith(".ts")).sort()) {
    const src = stripComments(readFileSync(join(dir, name), "utf8"));
    for (const lit of stringLiterals(src)) {
      if (ALLOWED.has(lit)) continue;
      if (TIER_RE.test(lit)) bad.push(`${name}: tier literal ${JSON.stringify(lit)}`);
      if (TRIAGER_RE.test(lit)) bad.push(`${name}: (triager) literal ${JSON.stringify(lit)}`);
      if (PROFILE_RE.test(lit)) {
        const cleaned = lit.replace(/profile_[a-z_]+/g, "");
        if (PROFILE_RE.test(cleaned)) {
          bad.push(`${name}: profile literal ${JSON.stringify(lit)}`);
        }
      }
    }
  }
  expect(bad).toEqual([]);
});

// Correctness signals W5: the merge-policy pages say agreed and quiet, never clean or healthy, and
// never claim a change was right, correct, safe or successful. Wire names are sent or read, never shown.
const OLD_RE = /\b(clean|healthy)\b/i;
const NEVER_RE = /\b(right|correct|safe|successful)\b/i;
const WIRE = new Set(["clean", "healthy", "mergedUnchanged"]);
const POLICY_PAGES = ["class-card.ts", "settings-api.ts", "policy-page.ts", "policy-help.ts", "policy-words.ts", "sentence.ts",
  "decisions.ts", "binding-editor.ts", "policy-describe.ts", "backtest.ts", "settings.ts"];

test("merge-policy pages say agreed and quiet (W5)", () => {
  const dir = join(__dirname, "..", "src");
  const bad: string[] = [];
  for (const name of POLICY_PAGES.filter((f) => readdirSync(dir).includes(f))) {
    const src = stripComments(readFileSync(join(dir, name), "utf8"));
    for (const lit of stringLiterals(src)) {
      if (WIRE.has(lit)) continue;
      if (OLD_RE.test(lit)) bad.push(`${name}: ${JSON.stringify(lit)}`);
      if (NEVER_RE.test(lit) && !/(quiet, not right|not that the change is right|right now)/.test(lit)) bad.push(`${name}: ${JSON.stringify(lit)}`);
    }
  }
  expect(bad).toEqual([]);
});
