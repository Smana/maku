# UI review, PR 1: Settings, implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the settings pages understandable: overview cards, a repository table, class cards that read as sentences, an editor grouped by question with help on every field, and a gallery of simple and advanced examples.

**Architecture:** Pure functions carry the meaning (`describeClass`, the field table, the example table, the mode counts), each unit-tested; `settings.ts` and `policy-editor.ts` render them. The broker API, the factory's check and the YAML round trip stay as they are: the page still writes nothing, and "Check, compare and simulate" is still the only judge.

**Tech Stack:** TypeScript (DOM, no framework), esbuild, vitest with jsdom, plain CSS in `web/src/app.css`. One Go test (`internal/factory/mergepolicy`) checks the example templates.

**Spec:** `docs/superpowers/specs/2026-11-10-ui-review-design.md`, Part 1. PR 2 (Theme) and PR 3 (Rail) get their own plans after this one merges.

## Global Constraints

- Every `.go` file's first line is `// SPDX-License-Identifier: Apache-2.0`; `.ts` files too, as the existing ones do.
- stdlib-style tests only: vitest `describe`/`it`/`expect`, hand-written fakes (`fakeFetch` in `web/test/settings.test.ts`). No new dependency in `web/package.json`.
- Every value from the broker is written as text (`textContent`), never `innerHTML`.
- No emoji in UI text (Ogenki rule). A status is a word plus a colour, never a colour alone.
- Copy: sentence case, plain words, the YAML key shown in mono beside its label.
- Headings: h2 per page section, h3 per editor group; no level skipped.
- Under 720 px nothing scrolls sideways; labels stack above fields.
- Motion (skills `micro-interaction`, `accessible-animation`): opacity/transform only, 150–200 ms ease-out, none under `prefers-reduced-motion: reduce`.
- Before each UI edit, read `/home/smana/.claude/skills/impeccable/reference/craft-floor.md`.
- `task check` exits 0 before the PR, `ui:check` included: rebuild the bundle with `task ui:build` and commit `internal/humanapi/ui/dist/`.
- Commit messages in English, conventional style (`feat(ui): …`). No `Co-Authored-By` line (owner's rule).
- Lint TypeScript through `npm --prefix web run typecheck`; run tests with `npm --prefix web test -- <file>`.

## File structure

| File | Responsibility | Status |
|---|---|---|
| `web/preview/settings.ts` | Offline settings preview: routes answered in the right order, two repositories | Modify |
| `web/preview/settings-fixtures.ts` | The second repository's fixture (repository file, every evidence state) | Create |
| `web/src/policy-describe.ts` | `describeClass`, `describeGuardrails`, `modeMeaning`: a class and the guardrails in words | Create |
| `web/src/policy-fields.ts` | The editor's field table: key, label, help, docs anchor, group, when shown | Create |
| `web/src/policy-examples.ts` | Each template's level and "why"; the gallery's order | Create |
| `web/src/policy-templates.json` | Two new templates, `docs-or-tests` and `infra-with-approval` | Modify |
| `web/src/settings.ts` | Pages: home cards, repository table, repository page (class cards), timeline | Modify |
| `web/src/class-card.ts` | One class card: badge, sentence, evidence bar, ladder, promotion and exception inside | Create (logic moved out of `settings.ts` `classesTable`) |
| `web/src/policy-editor.ts` | `classForm` regrouped by question, folded groups, live sentence; `exampleGallery` | Modify |
| `web/src/app.css` | Cards, table, class card, editor groups, gallery, sticky actions, 720 px stacking | Modify |
| `web/test/policy-describe.test.ts`, `web/test/policy-fields.test.ts`, `web/test/policy-examples.test.ts`, `web/test/class-card.test.ts` | New unit tests | Create |
| `web/test/settings.test.ts` | Existing tests moved to the new structure (data-key selectors, cards) | Modify |
| `docs/web-ui.md`, `docs/merge-policies.md`, `docs/development.md` | Settings rows, the settings editor section, preview paths | Modify |

---

### Task 1: Preview fixtures for every settings page

**Files:**
- Modify: `web/preview/settings.ts`
- Create: `web/preview/settings-fixtures.ts`
- Modify: `docs/development.md` (the "UI preview" section)

**Interfaces:**
- Produces: `cnrPage: RepositoryPage` exported from `web/preview/settings-fixtures.ts`; preview URLs `?path=/settings`, `/settings/merge-policies`, `/settings/merge-policies/Smana/maku`, `/settings/merge-policies/Smana/cloud-native-ref`, `/settings/evidence`, `/settings/approval-memory`.

- [ ] **Step 1: Write the fixture**

`web/preview/settings-fixtures.ts`:

```ts
// SPDX-License-Identifier: Apache-2.0

// A repository whose classes come from its own .github/agent-factory.yaml, with every evidence
// state the page draws: one class eligible, one short of the minimum, one still being read, one
// open promotion and one approved exception.

import type { Evidence, RepositoryPage } from "../src/settings";

const ev = (over: Partial<Evidence> = {}): Evidence => ({
  shadow: { mergedUnchanged: 14, changed: 1, reverted: 0, autoMerged: 0, drill: 1, reviewers: [{ model: "glm-5.3", mergedUnchanged: 14, changed: 1 }] },
  clean: 14, bad: 0, immature: 2, pending: 1, bound: 0.191, budget: 0.2, widen: false,
  ladder: [{ merges: 29, maxBad: 0, reached: false }, { merges: 46, maxBad: 1, reached: false }, { merges: 61, maxBad: 2, reached: false }],
  drill: "passed", minimum: 20, eligible: false, blockers: ["below_minimum"], ...over,
});

const file = `factory: aws-0
classes:
  docs-links:
    mode: shadow
    author: agents
    harness: plain
    conditions:
      all:
        - holdoutChecks: ['Check the documentation links']
  revert:
    mode: live
    author: agents
    harness: plain
  docs:
    mode: shadow
    author: agents
    harness: plain
    conditions:
      all:
        - allowedFiles: ['^docs/.+\\.md$', '^README\\.md$']
        - maxPatchFiles: 10
        - maxModifiedLines: 200
        - holdoutChecks: ['Check the documentation links']
      deny:
        - protectedFiles: ['^docs/security/']
    requiresApproval:
      - changedFiles: ['^docs/architecture/']
    limits:
      maxPerDay: 2
  deps-patch:
    mode: shadow
    author: adopt
    adopt:
      logins: ['renovate[bot]']
      branches: ['^renovate/']
    harness: adopted-none
    conditions:
      all:
        - diffShape: semver-patch
        - modifyOnly: true
        - releaseAgeCheck: renovate/stability-days
`;

export const cnrPage: RepositoryPage = {
  report: {
    repository: "Smana/cloud-native-ref", minimum: 20, readAt: "2026-11-10T08:00:00Z", evidenceAt: "2026-11-10T08:00:00Z",
    factory: "aws-0", defaultBranch: "main", harnessProfiles: ["adopted-none", "checked", "plain"], adoptedProfiles: ["adopted-none"],
    guardrails: { protectedFiles: ["^\\.github/", "^\\.policy\\.yml$"], maxPatchFiles: 20, maxModifiedLines: 400, maxPerDay: 5 },
    file,
    policy: { factory: "aws-0", classes: {
      "docs-links": { mode: "shadow", author: "agents", harness: "plain", conditions: { all: [{ holdoutChecks: ["Check the documentation links"] }] } },
      revert: { mode: "live", author: "agents", harness: "plain" },
      docs: { mode: "shadow", author: "agents", harness: "plain",
        conditions: { all: [{ allowedFiles: ["^docs/.+\\.md$", "^README\\.md$"] }, { maxPatchFiles: 10 }, { maxModifiedLines: 200 }, { holdoutChecks: ["Check the documentation links"] }],
          deny: [{ protectedFiles: ["^docs/security/"] }] },
        requiresApproval: [{ changedFiles: ["^docs/architecture/"] }], limits: { maxPerDay: 2 } },
      "deps-patch": { mode: "shadow", author: "adopt", adopt: { logins: ["renovate[bot]"], branches: ["^renovate/"] }, harness: "adopted-none",
        conditions: { all: [{ diffShape: "semver-patch" }, { modifyOnly: true }, { releaseAgeCheck: "renovate/stability-days" }] } },
    } },
    classes: [
      { class: "docs-links", source: "repository", mode: "shadow", evidence: ev({ clean: 21, eligible: true, blockers: [] }),
        proposal: { path: ".github/agent-factory.yaml", line: 4, file, editURL: "https://github.com/Smana/cloud-native-ref/edit/main/.github/agent-factory.yaml" } },
      { class: "revert", source: "repository", mode: "live", evidence: ev({ clean: 40, eligible: true, blockers: [] }), unproposed: "not_shadow" },
      { class: "docs", source: "repository", mode: "shadow", evidence: ev(),
        proposal: { path: ".github/agent-factory.yaml", line: 14, file, editURL: "https://github.com/Smana/cloud-native-ref/edit/main/.github/agent-factory.yaml" } },
      { class: "deps-patch", source: "repository", mode: "shadow" },
    ],
  },
  promotions: [{ id: "p1", repository: "Smana/cloud-native-ref", class: "docs-links", threshold: 20, requestedBy: "Smana",
    requestedAt: "2026-11-09T10:00:00Z", state: "pr_opened", pullRequest: 512 }],
  exceptions: [{ id: "e1", repository: "Smana/cloud-native-ref", class: "docs", minimum: 12, justification: "Docs-only class, reverted twice in a year, low blast radius.",
    requestedBy: "Smana", requestedAt: "2026-11-08T09:00:00Z", state: "approved", decidedBy: "ops-lead", expiresAt: "2026-12-08T09:00:00Z" }],
  you: { login: "Smana", maintainer: true, operator: true },
};
```

- [ ] **Step 2: Route the repository pages before the list**

In `web/preview/settings.ts`, replace the `answers` object and the `get` lookup with:

```ts
import golden from "../../internal/factory/policyview/testdata/ui/merge-policy.json";
import { cnrPage } from "./settings-fixtures";

// Longest prefix first: "/api/policy/repositories" is a prefix of every repository's route.
const answers: Record<string, unknown> = {
  "/api/policy/repositories/Smana/maku": { report: golden.report, promotions: [], exceptions: [], you: { login: "Smana", maintainer: true, operator: false } },
  "/api/policy/repositories/Smana/cloud-native-ref": cnrPage,
  "/api/policy/repositories": { repositories: ["Smana/maku", "Smana/cloud-native-ref"] },
  "/api/approval-memory": { grants: [] },
};
const get = (async (url: string) => {
  const key = Object.keys(answers).sort((a, b) => b.length - a.length).find((k) => url.startsWith(k));
  return key ? new Response(JSON.stringify(answers[key])) : new Response(JSON.stringify({ error: "not_found" }), { status: 404 });
}) as unknown as typeof fetch;
```

Update the header comment to list every `?path=` above.

- [ ] **Step 3: Build and look**

Run: `npm --prefix web run preview:build && (node web/preview/serve.mjs &) && sleep 1`
Then capture the two repository pages:
`chromium --headless=new --disable-gpu --hide-scrollbars --window-size=1440,3600 --virtual-time-budget=5000 --screenshot=/tmp/cnr.png "http://127.0.0.1:4180/preview/settings.html?theme=dark&path=/settings/merge-policies/Smana/cloud-native-ref"`
Expected: the classes table shows `docs-links` eligible, `docs` "14/20 clean", `deps-patch` pending; promotion and exception rows present. Same for `Smana/maku` (served from a MergePolicy).

- [ ] **Step 4: Document the paths**

In `docs/development.md`, under "## The UI preview", add after the `full.html` bullet:

```markdown
- `http://127.0.0.1:4180/preview/settings.html?path=/settings/merge-policies/Smana/cloud-native-ref`
  (a repository's own policy file, every evidence state) and `…/Smana/maku` (served from a
  MergePolicy, the factory's golden); `?path=/settings`, `/settings/merge-policies`,
  `/settings/evidence` and `/settings/approval-memory` for the others
```

- [ ] **Step 5: Commit**

```bash
git add web/preview/settings.ts web/preview/settings-fixtures.ts docs/development.md
git commit -m "test(ui): preview every settings page from fixtures"
```

---

### Task 2: `describeClass`, a class in words

**Files:**
- Create: `web/src/policy-describe.ts`
- Test: `web/test/policy-describe.test.ts`

**Interfaces:**
- Consumes: `ClassPolicy`, `Leaf` from `web/src/policy-editor.ts`.
- Produces:
  ```ts
  export interface ClassStory { lead: string; when: string[]; also: string[] }
  export function describeClass(c: ClassPolicy, guardrails?: Record<string, unknown>): ClassStory
  export function describeGuardrails(g: Record<string, unknown>): string[]
  export function modeMeaning(mode: string): { label: string; meaning: string }
  export function joinOr(xs: string[], word?: "or" | "and"): string
  ```

- [ ] **Step 1: Write the failing tests**

`web/test/policy-describe.test.ts`:

```ts
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { describeClass, describeGuardrails, joinOr, modeMeaning } from "../src/policy-describe";
import { templates } from "../src/policy-editor";

describe("describeClass", () => {
  it("reads a docs class as its conditions, deny, approval and limit", () => {
    const s = describeClass({ mode: "shadow", author: "agents", harness: "plain",
      conditions: { all: [{ allowedFiles: ["^docs/.+\\.md$", "^README\\.md$"] }, { maxPatchFiles: 10 }, { maxModifiedLines: 200 },
        { holdoutChecks: ["Check the documentation links"] }], deny: [{ protectedFiles: ["^docs/security/"] }] },
      requiresApproval: [{ changedFiles: ["^docs/architecture/"] }], limits: { maxPerDay: 2 }, monitor: { followUpWindow: "168h" } });
    expect(s.lead).toBe("Would merge an agent's pull request on its own when all of these hold (shadow: it records the decision and merges nothing):");
    expect(s.when).toEqual([
      "it only changes files matching ^docs/.+\\.md$ or ^README\\.md$",
      "it changes at most 10 files",
      "it changes at most 200 lines",
      "the check “Check the documentation links” passes",
    ]);
    expect(s.also).toEqual([
      "Never if it touches ^docs/security/.",
      "A maintainer approves it first if it changes ^docs/architecture/.",
      "At most 2 a day.",
      "Watched for 168h after it merges.",
    ]);
  });

  it("names an adopted bot and its branches", () => {
    const s = describeClass({ mode: "live", author: "adopt", harness: "adopted-none", adopt: { logins: ["renovate[bot]"], branches: ["^renovate/"] },
      conditions: { all: [{ diffShape: "semver-patch" }, { modifyOnly: true }, { releaseAgeCheck: "renovate/stability-days" }] } });
    expect(s.lead).toBe("Merges a pull request from renovate[bot] on a branch matching ^renovate/ on its own when all of these hold:");
    expect(s.when).toEqual(["the diff is a patch version bump", "it only modifies files, adding or deleting none",
      "the status “renovate/stability-days” is green (the release is old enough)"]);
  });

  it("says an off class decides nothing, and what it would check", () => {
    expect(describeClass({ mode: "off", author: "agents", harness: "plain" }).lead)
      .toBe("Off: decides nothing. If turned on, it would merge an agent's pull request on its own when all of these hold:");
  });

  it("says when a class has no condition of its own", () => {
    expect(describeClass({ mode: "shadow", author: "agents", harness: "plain" }).when)
      .toEqual(["the factory's base checks pass (the class adds no condition of its own)"]);
  });

  it("falls back to the factory's caps when the class sets none", () => {
    const s = describeClass({ mode: "shadow", author: "agents", harness: "plain", conditions: { all: [{ allowedFiles: ["^a$"] }] } },
      { maxPatchFiles: 20, maxModifiedLines: 400, maxPerDay: 5 });
    expect(s.when).toContain("it changes at most 20 files (the factory's cap)");
    expect(s.when).toContain("it changes at most 400 lines (the factory's cap)");
    expect(s.also).toContain("At most 5 a day (the factory's cap).");
  });

  it("reads an any as one of its branches", () => {
    const s = describeClass({ mode: "shadow", author: "agents", harness: "plain",
      conditions: { any: [[{ allowedFiles: ["^docs/"] }], [{ allowedFiles: ["^test/"] }, { maxPatchFiles: 3 }]] } });
    expect(s.when).toEqual(["one of: (it only changes files matching ^docs/) or (it only changes files matching ^test/ and it changes at most 3 files)"]);
  });

  it("names a leaf it has no words for, never drops it", () => {
    expect(describeClass({ mode: "shadow", author: "agents", harness: "plain", conditions: { all: [{ brandNew: 1 }] } }).when)
      .toEqual(["a condition this page does not describe (brandNew)"]);
  });

  it("has words for every leaf of every template", () => {
    for (const [name, make] of Object.entries(templates)) {
      const s = describeClass(make());
      expect(s.when.join(" "), name).not.toContain("does not describe");
    }
  });
});

describe("the guardrails and modes", () => {
  it("writes the guardrails as sentences, never as JSON", () => {
    expect(describeGuardrails({ protectedFiles: ["^\\.github/"], mandatoryChecks: ["lint"], maxPatchFiles: 20, maxModifiedLines: 400, maxPerDay: 5 })).toEqual([
      "Every class refuses a pull request that touches ^\\.github/.",
      "Every class also needs the check “lint”.",
      "No class may allow more than 20 files, 400 lines or 5 merges a day.",
    ]);
    expect(describeGuardrails({})).toEqual(["The factory sets no guardrail beyond its base checks."]);
  });
  it("gives every mode its meaning", () => {
    expect(modeMeaning("live")).toEqual({ label: "Live", meaning: "merges on its own" });
    expect(modeMeaning("shadow")).toEqual({ label: "Shadow", meaning: "records what it would do, merges nothing" });
    expect(modeMeaning("off")).toEqual({ label: "Off", meaning: "decides nothing" });
    expect(modeMeaning("weird")).toEqual({ label: "weird", meaning: "a mode this page does not know" });
  });
  it("joins with or, and with and", () => {
    expect(joinOr(["a"])).toBe("a");
    expect(joinOr(["a", "b", "c"])).toBe("a, b or c");
    expect(joinOr(["a", "b"], "and")).toBe("a and b");
  });
});
```

- [ ] **Step 2: Run them to see them fail**

Run: `npm --prefix web test -- policy-describe`
Expected: FAIL, `Cannot find module '../src/policy-describe'`.

- [ ] **Step 3: Implement**

`web/src/policy-describe.ts`:

```ts
// SPDX-License-Identifier: Apache-2.0

// A merge class in words: what the class card and the editor's live sentence say. Pure: one class
// (and the factory's guardrails, for the caps a class inherits) in, sentences out. Every leaf of
// the closed vocabulary has a phrase; one it does not know is named, never dropped, so the page
// never reads a class as looser than it is. Patterns and check names are the policy's own text,
// written as text by the caller.

import type { ClassPolicy, Leaf } from "./policy-editor";

export interface ClassStory { lead: string; when: string[]; also: string[] }

export function joinOr(xs: string[], word: "or" | "and" = "or"): string {
  if (xs.length <= 1) return xs[0] ?? "";
  return `${xs.slice(0, -1).join(", ")} ${word} ${xs.at(-1)}`;
}

const quoted = (xs: string[]) => xs.map((x) => `“${x}”`);

const shapes: Record<string, string> = {
  "links-only": "the diff only changes links",
  "semver-patch": "the diff is a patch version bump",
  "semver-minor": "the diff is a minor version bump",
  "added-entry": "the diff only adds an entry",
};

function leafPhrase(l: Leaf, g: Record<string, unknown>): string {
  const [k, v] = Object.entries(l)[0] ?? ["", undefined];
  switch (k) {
    case "allowedFiles": return `it only changes files matching ${joinOr(v as string[])}`;
    case "companionFiles": return `its other files match ${joinOr(v as string[])}`;
    case "maxPatchFiles": return `it changes at most ${v} files`;
    case "maxModifiedLines": return `it changes at most ${v} lines`;
    case "modifyOnly": return "it only modifies files, adding or deleting none";
    case "diffShape": return shapes[String(v)] ?? `the diff has the shape ${String(v)}`;
    case "holdoutChecks": {
      const c = v as string[];
      return `the check${c.length > 1 ? "s" : ""} ${joinOr(quoted(c), "and")} pass${c.length > 1 ? "" : "es"}`;
    }
    case "releaseAgeCheck": return `the status “${String(v)}” is green (the release is old enough)`;
    case "requiredLabel": return `it carries the label “${String(v)}”`;
    case "oracle": return "the dead-links oracle has no finding open";
  }
  void g;
  return `a condition this page does not describe (${k})`;
}

function who(c: ClassPolicy): string {
  if (c.author !== "adopt") return "an agent's pull request";
  const logins = c.adopt?.logins ?? [];
  const branches = c.adopt?.branches ?? [];
  return `a pull request from ${joinOr(logins) || "an adopted bot"}${branches.length ? ` on a branch matching ${joinOr(branches)}` : ""}`;
}

export function describeClass(c: ClassPolicy, guardrails: Record<string, unknown> = {}): ClassStory {
  const all = c.conditions?.all ?? [];
  const has = (k: string) => all.some((l) => k in l);
  const tail = "on its own when all of these hold";
  const lead = c.mode === "live" ? `Merges ${who(c)} ${tail}:`
    : c.mode === "shadow" ? `Would merge ${who(c)} ${tail} (shadow: it records the decision and merges nothing):`
    : `Off: decides nothing. If turned on, it would merge ${who(c)} ${tail}:`;
  const when = all.map((l) => leafPhrase(l, guardrails));
  for (const [k, unit] of [["maxPatchFiles", "files"], ["maxModifiedLines", "lines"]] as const) {
    if (!has(k) && typeof guardrails[k] === "number" && (guardrails[k] as number) > 0) when.push(`it changes at most ${guardrails[k]} ${unit} (the factory's cap)`);
  }
  const any = c.conditions?.any ?? [];
  if (any.length) when.push(`one of: ${any.map((b) => `(${joinOr(b.map((l) => leafPhrase(l, guardrails)), "and")})`).join(" or ")}`);
  if (when.length === 0) when.push("the factory's base checks pass (the class adds no condition of its own)");

  const also: string[] = [];
  const deny = (c.conditions?.deny ?? []).flatMap((d) => d.protectedFiles);
  if (deny.length) also.push(`Never if it touches ${joinOr(deny)}.`);
  const ask = (c.requiresApproval ?? []).flatMap((a) => a.changedFiles);
  if (ask.length) also.push(`A maintainer approves it first if it changes ${joinOr(ask)}.`);
  if (c.limits?.maxPerDay) also.push(`At most ${c.limits.maxPerDay} a day.`);
  else if (typeof guardrails.maxPerDay === "number" && guardrails.maxPerDay > 0) also.push(`At most ${guardrails.maxPerDay} a day (the factory's cap).`);
  const fw = c.monitor?.followUpWindow;
  if (typeof fw === "string" && fw) also.push(`Watched for ${fw} after it merges.`);
  return { lead, when, also };
}

export function describeGuardrails(g: Record<string, unknown>): string[] {
  const out: string[] = [];
  const list = (k: string) => (Array.isArray(g[k]) ? (g[k] as string[]) : []);
  if (list("protectedFiles").length) out.push(`Every class refuses a pull request that touches ${joinOr(list("protectedFiles"))}.`);
  if (list("mandatoryChecks").length) out.push(`Every class also needs the check${list("mandatoryChecks").length > 1 ? "s" : ""} ${joinOr(quoted(list("mandatoryChecks")), "and")}.`);
  const caps = ([["maxPatchFiles", "files"], ["maxModifiedLines", "lines"], ["maxPerDay", "merges a day"]] as const)
    .filter(([k]) => typeof g[k] === "number" && (g[k] as number) > 0).map(([k, unit]) => `${g[k]} ${unit}`);
  if (caps.length) out.push(`No class may allow more than ${joinOr(caps)}.`);
  return out.length ? out : ["The factory sets no guardrail beyond its base checks."];
}

const modes: Record<string, { label: string; meaning: string }> = {
  live: { label: "Live", meaning: "merges on its own" },
  shadow: { label: "Shadow", meaning: "records what it would do, merges nothing" },
  off: { label: "Off", meaning: "decides nothing" },
};

export function modeMeaning(mode: string): { label: string; meaning: string } {
  return modes[mode] ?? { label: mode, meaning: "a mode this page does not know" };
}
```

- [ ] **Step 4: Run the tests**

Run: `npm --prefix web test -- policy-describe && npm --prefix web run typecheck`
Expected: PASS, no type error. If a template carries a leaf without a phrase, add the phrase to `leafPhrase` (never loosen the test).

- [ ] **Step 5: Commit**

```bash
git add web/src/policy-describe.ts web/test/policy-describe.test.ts
git commit -m "feat(ui): describe a merge class and the guardrails in words"
```

---

### Task 3: The field table and the examples

**Files:**
- Create: `web/src/policy-fields.ts`, `web/src/policy-examples.ts`
- Modify: `web/src/policy-templates.json` (two new templates)
- Test: `web/test/policy-fields.test.ts`, `web/test/policy-examples.test.ts`; Go: `internal/factory/mergepolicy/templates_test.go` (unchanged, it reads the JSON)

**Interfaces:**
- Produces:
  ```ts
  export type Group = "who" | "touch" | "size" | "pass" | "ask" | "limits";
  export interface Field { key: string; label: string; help: string; anchor: string; group: Group; kind: "list" | "number" | "text" | "bool" | "shape";
    shown(c: ClassPolicy): boolean }
  export const GROUPS: { id: Group; title: string; add: string }[]
  export const FIELDS: Field[]
  export const DOCS: string  // base URL of docs/merge-policies.md
  export function docsLink(anchor: string): string
  // policy-examples.ts
  export interface Example { name: string; level: "simple" | "advanced"; why: string }
  export const EXAMPLES: Example[]
  ```

- [ ] **Step 1: Write the failing tests**

`web/test/policy-fields.test.ts`:

```ts
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { docsLink, FIELDS, GROUPS } from "../src/policy-fields";

const agents = { mode: "shadow", author: "agents", harness: "plain" };
const adopt = { ...agents, author: "adopt", adopt: { logins: [], branches: [] } };
const shown = (c: object) => FIELDS.filter((f) => f.shown(c as never)).map((f) => f.key);

describe("the editor's fields", () => {
  it("gives every field a plain label, one help line and a group the page draws", () => {
    const groups = new Set(GROUPS.map((g) => g.id));
    for (const f of FIELDS) {
      expect(f.label, f.key).not.toMatch(/[A-Z]{2}|\./); // a sentence, not a key
      expect(f.help.length, f.key).toBeGreaterThan(20);
      expect(f.help.length, f.key).toBeLessThan(160);
      expect(groups.has(f.group), f.key).toBe(true);
    }
  });
  it("shows the adopted leaves to an adopted class only, and the off-only keys to an off class only", () => {
    expect(shown(agents)).not.toContain("releaseAgeCheck");
    expect(shown(agents)).not.toContain("requiredLabel");
    expect(shown(adopt)).toContain("releaseAgeCheck");
    expect(shown(adopt)).toContain("adopt.logins");
    expect(shown(agents)).not.toContain("harnessAllowed");
    expect(shown({ ...agents, mode: "off" })).toContain("harnessAllowed");
    expect(shown({ ...agents, mode: "off" })).toContain("escalation.notConverged");
  });
  it("shows companionFiles only with an added-entry shape", () => {
    expect(shown(adopt)).not.toContain("companionFiles");
    expect(shown({ ...adopt, conditions: { all: [{ diffShape: "added-entry" }] } })).toContain("companionFiles");
  });
  it("links each field to its section of the merge-policy guide", () => {
    expect(docsLink("leaves")).toMatch(/\/docs\/merge-policies\.md#leaves$/);
  });
});
```

`web/test/policy-examples.test.ts`:

```ts
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { templates } from "../src/policy-editor";
import { EXAMPLES } from "../src/policy-examples";

describe("the example gallery", () => {
  it("has one entry per template and no entry without one", () => {
    expect(EXAMPLES.map((e) => e.name).sort()).toEqual(Object.keys(templates).sort());
  });
  it("has simple and advanced examples, each saying why", () => {
    expect(EXAMPLES.filter((e) => e.level === "simple").length).toBeGreaterThanOrEqual(3);
    expect(EXAMPLES.filter((e) => e.level === "advanced").length).toBeGreaterThanOrEqual(3);
    for (const e of EXAMPLES) expect(e.why.length, e.name).toBeGreaterThan(30);
  });
  it("keeps every example in shadow: a gallery never starts a class live", () => {
    for (const [name, make] of Object.entries(templates)) expect(make().mode, name).toBe("shadow");
  });
});
```

- [ ] **Step 2: Run them to see them fail**

Run: `npm --prefix web test -- policy-fields policy-examples`
Expected: FAIL, modules not found.

- [ ] **Step 3: Add the two templates**

Append to `web/src/policy-templates.json` (inside the top-level object):

```json
  "docs-or-tests": {
    "mode": "shadow",
    "author": "agents",
    "harness": "",
    "conditions": {
      "all": [
        { "maxPatchFiles": 10 },
        { "maxModifiedLines": 200 }
      ],
      "any": [
        [ { "allowedFiles": ["^docs/.+\\.md$"] }, { "holdoutChecks": ["Check the documentation links 🔗"] } ],
        [ { "allowedFiles": ["^test/.+$", "^.+_test\\.go$"] }, { "holdoutChecks": ["Tests"] } ]
      ]
    },
    "limits": { "maxPerDay": 2 },
    "monitor": { "followUpWindow": "168h" }
  },
  "infra-with-approval": {
    "mode": "shadow",
    "author": "agents",
    "harness": "",
    "conditions": {
      "all": [
        { "allowedFiles": ["^infrastructure/.+\\.(tf|yaml)$"] },
        { "maxPatchFiles": 5 },
        { "maxModifiedLines": 80 },
        { "holdoutChecks": ["Validate infrastructure"] }
      ],
      "deny": [ { "protectedFiles": ["^infrastructure/(prod|security)/"] } ]
    },
    "requiresApproval": [ { "changedFiles": ["^infrastructure/network/.+$"] } ],
    "limits": { "maxPerDay": 1 },
    "monitor": { "followUpWindow": "168h" }
  }
```

Run: `go test ./internal/factory/mergepolicy -run TestEveryEditorTemplatePassesCheck -count=1`
Expected: PASS. If the factory refuses a template, change the template to what the error asks (for example a key the `any` branch may not repeat), never the Go test.

- [ ] **Step 4: Implement the field table**

`web/src/policy-fields.ts`:

```ts
// SPDX-License-Identifier: Apache-2.0

// The class editor's fields, grouped by the question each answers. A field is shown only where the
// factory accepts it (docs/merge-policies.md, Reference): the adopted leaves on an adopted class,
// companionFiles with an added-entry shape, harnessAllowed and escalation on an off class. Labels
// are words; the key stays beside them in mono, so a reader of the YAML finds it.

import type { ClassPolicy } from "./policy-editor";

export type Group = "who" | "touch" | "size" | "pass" | "ask" | "limits";
export interface Field {
  key: string; label: string; help: string; anchor: string; group: Group;
  kind: "list" | "number" | "text" | "bool" | "shape";
  shown(c: ClassPolicy): boolean;
}

export const DOCS = "https://github.com/Smana/maku/blob/main/docs/merge-policies.md";
export const docsLink = (anchor: string) => `${DOCS}#${anchor}`;

export const GROUPS: { id: Group; title: string; add: string }[] = [
  { id: "who", title: "Who writes it", add: "" },
  { id: "touch", title: "What it may touch", add: "Add file rules" },
  { id: "size", title: "How big it may be", add: "Add a size limit" },
  { id: "pass", title: "What must pass", add: "Add a check" },
  { id: "ask", title: "When to ask a maintainer", add: "Add an approval rule" },
  { id: "limits", title: "Limits and follow-up", add: "Add a daily cap or a follow-up" },
];

const always = () => true;
const adopted = (c: ClassPolicy) => c.author === "adopt";
const off = (c: ClassPolicy) => c.mode === "off";
const shape = (c: ClassPolicy) => c.conditions?.all?.find((l) => "diffShape" in l)?.diffShape;

export const FIELDS: Field[] = [
  { key: "adopt.logins", label: "Bot accounts it adopts", group: "who", kind: "list", anchor: "a-class", shown: adopted,
    help: "The GitHub App logins whose pull requests this class decides, such as renovate[bot]. One per line." },
  { key: "adopt.branches", label: "Their branch prefixes", group: "who", kind: "list", anchor: "a-class", shown: adopted,
    help: "^ plus a literal prefix, such as ^renovate/. A pull request on another branch is not this class's." },
  { key: "harnessAllowed", label: "Other harness profiles a run may use", group: "who", kind: "list", anchor: "central-config", shown: off,
    help: "Profiles the scoper may pick for this class's runs. Only an off or review class takes them." },
  { key: "escalation.outOfBudget", label: "Profile when a run runs out of budget", group: "who", kind: "text", anchor: "central-config", shown: off,
    help: "One of the profiles above. Empty: the task goes to a human." },
  { key: "escalation.notConverged", label: "Profile when review does not converge", group: "who", kind: "text", anchor: "central-config", shown: off,
    help: "One of the profiles above. Empty: the task goes to a human." },
  { key: "allowedFiles", label: "Files it may change", group: "touch", kind: "list", anchor: "leaves", shown: always,
    help: "Regular expressions, one per line, from ^ to $. A pull request that changes any other file is not this class's." },
  { key: "companionFiles", label: "Files that may change with the entry", group: "touch", kind: "list", anchor: "leaves", shown: (c) => shape(c) === "added-entry",
    help: "Regular expressions for the files an added entry may also touch, anchored like the allowed files." },
  { key: "protectedFiles", label: "Files it must never touch", group: "touch", kind: "list", anchor: "leaves", shown: always,
    help: "Regular expressions, one per line. A match refuses the pull request, on top of the factory's own protected paths." },
  { key: "maxPatchFiles", label: "Most files changed", group: "size", kind: "number", anchor: "leaves", shown: always,
    help: "At most the factory's cap. Empty: the cap applies." },
  { key: "maxModifiedLines", label: "Most lines changed", group: "size", kind: "number", anchor: "leaves", shown: always,
    help: "Additions plus deletions, as GitHub counts them. At most the factory's cap. Empty: the cap applies." },
  { key: "modifyOnly", label: "Only modify existing files", group: "size", kind: "bool", anchor: "leaves", shown: always,
    help: "Refuses a pull request that adds, deletes or renames a file." },
  { key: "diffShape", label: "Shape of the change", group: "size", kind: "shape", anchor: "leaves", shown: always,
    help: "links-only for an agents class; the version bumps and added-entry for an adopted one." },
  { key: "holdoutChecks", label: "Checks that must pass", group: "pass", kind: "list", anchor: "leaves", shown: always,
    help: "GitHub Actions check names, one per line, green on the pull request's head. Their workflow is a protected path." },
  { key: "releaseAgeCheck", label: "Release-age status", group: "pass", kind: "text", anchor: "leaves", shown: adopted,
    help: "A commit status the adopted bot posts once a release is old enough, such as renovate/stability-days." },
  { key: "requiredLabel", label: "Label it must carry", group: "pass", kind: "text", anchor: "leaves", shown: adopted,
    help: "A label the adopted bot sets, 1 to 50 characters." },
  { key: "changedFiles", label: "Changes that need a maintainer's approval", group: "ask", kind: "list", anchor: "a-class", shown: always,
    help: "Regular expressions, one per line. A match sends the pull request to a maintainer, even in a live class." },
  { key: "maxPerDay", label: "Most merges a day", group: "limits", kind: "number", anchor: "a-class", shown: always,
    help: "This class's own daily cap, at most the factory's. Empty: the factory's cap." },
  { key: "followUpWindow", label: "Watch after merge for", group: "limits", kind: "text", anchor: "a-class", shown: always,
    help: "How long the post-merge monitor follows a merge, 1h to 720h, such as 168h. Empty: no monitor." },
];
```

Note: `kind: "shape"` options come from `diffShapes` in `policy-editor.ts`, filtered in Task 6 to `["", "links-only"]` for an agents class.

- [ ] **Step 5: Implement the examples**

`web/src/policy-examples.ts`:

```ts
// SPDX-License-Identifier: Apache-2.0

// The gallery's words for each template in policy-templates.json: its level and why a maintainer
// would start from it. The templates themselves stay in the JSON, so the factory's own check
// (mergepolicy.TestEveryEditorTemplatePassesCheck) keeps every example valid.

export interface Example { name: string; level: "simple" | "advanced"; why: string }

export const EXAMPLES: Example[] = [
  { name: "docs-broad", level: "simple",
    why: "Let agents fix documentation without a human merge: small, docs-only changes whose links still resolve, with sensitive pages kept out." },
  { name: "deps-patch", level: "simple",
    why: "Let Renovate's patch bumps merge on their own once the release is old enough, as long as nothing is added or deleted." },
  { name: "generated-files", level: "simple",
    why: "Let a regeneration land when it only rewrites generated files and regenerating them again gives the same result." },
  { name: "deps-minor", level: "advanced",
    why: "Watch Renovate's minor bumps in shadow to learn how often they are safe. The factory keeps this shape in shadow only." },
  { name: "docs-or-tests", level: "advanced",
    why: "One class for two kinds of small change, docs or tests, each with its own check, using an any of two branches." },
  { name: "infra-with-approval", level: "advanced",
    why: "Small infrastructure changes that pass validation, never production or security paths, and a maintainer's yes for network changes." },
];
```

- [ ] **Step 6: Run the tests**

Run: `npm --prefix web test -- policy-fields policy-examples policy-describe && npm --prefix web run typecheck`
Expected: PASS (the describe test's "every template" case now covers the two new ones).

- [ ] **Step 7: Commit**

```bash
git add web/src/policy-fields.ts web/src/policy-examples.ts web/src/policy-templates.json web/test/policy-fields.test.ts web/test/policy-examples.test.ts
git commit -m "feat(ui): label, explain and group every merge-class field; simple and advanced examples"
```

---

### Task 4: Settings home and repository table

**Files:**
- Modify: `web/src/settings.ts` (the home branch of `mountSettings`, `repositories`, new `modeCounts`)
- Test: `web/test/settings.test.ts` ("the index and evidence pages")

**Interfaces:**
- Consumes: `RepositoryPage`, `read`, `settingsFrame`, `centralManifest` (all in `settings.ts`); `modeMeaning` (Task 2).
- Produces: `export function modeCounts(r: Report): Record<string, number>`; `export async function readAll(repos: string[], get: typeof fetch, limit = 4): Promise<(RepositoryPage | Error)[]>`; DOM: `.settings-cards > a.settings-card`, `table.repo-table`.

- [ ] **Step 1: Write the failing tests**

Replace the `describe("the index and evidence pages", …)` block's first test with:

```ts
describe("the index and evidence pages", () => {
  const two = () => fakeFetch({ "/api/policy/repositories": { repositories: ["Smana/cloud-native-ref", "Smana/broken"] },
    "/api/policy/repositories/Smana/cloud-native-ref": page(), "/api/approval-memory": { grants: [{}, {}] } });

  it("opens settings on one card per section, each the link, with its counts", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings", two());
    const cards = [...app.querySelectorAll("a.settings-card")];
    expect(cards.map((a) => a.getAttribute("href"))).toEqual(["/settings/merge-policies", "/settings/evidence", "/settings/approval-memory"]);
    expect(cards[0].textContent).toContain("2 repositories");
    expect(cards[0].textContent).toContain("1 live · 1 shadow");
    expect(cards[1].textContent).toContain("1 class eligible to promote");
    expect(cards[2].textContent).toContain("2 saved grants");
    expect(app.querySelector("main ul")).toBeNull(); // no bullet list
  });

  it("lists each repository with where its classes come from and its classes by mode", async () => {
    const app = document.createElement("div");
    await mountSettings(app, "/settings/merge-policies", two());
    const rows = [...app.querySelectorAll("table.repo-table tbody tr")].map((tr) => [...tr.children].map((td) => td.textContent));
    expect(rows[0]).toEqual(["Smana/cloud-native-ref", "its own .github/agent-factory.yaml", "1 live · 1 shadow", "docs-links 20/20"]);
    expect(rows[1][0]).toBe("Smana/broken");
    expect(rows[1][1]).toMatch(/^Not readable: /);
    expect(app.querySelector('table.repo-table a[href="/settings/merge-policies/Smana/cloud-native-ref"]')).not.toBeNull();
  });

  it("counts a report's classes by mode", () => {
    expect(modeCounts(page().report)).toEqual({ live: 1, shadow: 1 });
  });
```

Keep the existing evidence assertions as their own `it("lists every class's evidence", …)`. Add `modeCounts` to the test's import line.

- [ ] **Step 2: Run them to see them fail**

Run: `npm --prefix web test -- settings`
Expected: FAIL (no `a.settings-card`, no `modeCounts`).

- [ ] **Step 3: Implement**

In `web/src/settings.ts`:

```ts
// modeCounts is a report's classes by mode, the words the list and the home card show.
export function modeCounts(r: Report): Record<string, number> {
  const out: Record<string, number> = {};
  for (const c of r.classes) out[c.mode] = (out[c.mode] ?? 0) + 1;
  return out;
}

const countsText = (n: Record<string, number>) =>
  ["live", "shadow", "off"].filter((m) => n[m]).map((m) => `${n[m]} ${m}`).join(" · ") || "no class";

// readAll reads each repository's page, at most limit at a time; a failed read is its Error, in place.
export async function readAll(repos: string[], get: typeof fetch, limit = 4): Promise<(RepositoryPage | Error)[]> {
  const out: (RepositoryPage | Error)[] = new Array(repos.length);
  let next = 0;
  const worker = async () => {
    while (next < repos.length) {
      const i = next++;
      try { out[i] = await read<RepositoryPage>(`/api/policy/repositories/${repos[i]}`, get); }
      catch (e) { out[i] = e instanceof Error ? e : new Error(String(e)); }
    }
  };
  await Promise.all(Array.from({ length: Math.min(limit, repos.length) }, worker));
  return out;
}

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

function card(href: string, title: string, purpose: string, counts: string): HTMLAnchorElement {
  const a = link(href, "");
  a.className = "settings-card";
  a.append(el("h2", title), el("p", purpose), el("p", counts, "settings-card-counts"));
  return a;
}

async function home(app: HTMLElement, get: typeof fetch, theme?: ThemeController) {
  const main = settingsFrame(app, "Settings", "/settings", theme);
  const box = el("div", undefined, "settings-cards");
  main.append(box);
  let repos: string[] = [];
  let pages: (RepositoryPage | Error)[] = [];
  let grants: number | undefined;
  try { ({ repositories: repos } = await read<{ repositories: string[] }>("/api/policy/repositories", get)); pages = await readAll(repos, get); } catch { /* counts unknown */ }
  try { grants = (await read<{ grants: unknown[] }>("/api/approval-memory", get)).grants.length; } catch { /* counts unknown */ }
  const ok = pages.filter((p): p is RepositoryPage => !(p instanceof Error));
  const modes: Record<string, number> = {};
  for (const p of ok) for (const [m, n] of Object.entries(modeCounts(p.report))) modes[m] = (modes[m] ?? 0) + n;
  const eligible = ok.flatMap((p) => p.report.classes).filter((c) => c.mode === "shadow" && c.evidence?.eligible).length;
  const unknown = "Counts unavailable right now.";
  box.append(
    card("/settings/merge-policies", "Merge policies", "Which agent pull requests may merge on their own, per repository.",
      repos.length ? `${plural(repos.length, "repository", "repositories")} · ${countsText(modes)}` : unknown),
    card("/settings/evidence", "Evidence", "How close each shadow class is to being promoted to live.",
      ok.length ? `${plural(eligible, "class", "classes")} eligible to promote` : unknown),
    card("/settings/approval-memory", "Approval memory", "Approvals you saved so a later, similar pull request can merge without asking again.",
      grants === undefined ? unknown : plural(grants, "saved grant")));
}
```

Replace the home branch at the end of `mountSettings` with `return home(app, get, theme);` and delete the old `ul` loop.

Replace `repositories` with:

```ts
async function repositories(app: HTMLElement, get: typeof fetch, theme?: ThemeController) {
  const main = settingsFrame(app, "Merge policies", "/settings/merge-policies", theme);
  try {
    const { repositories } = await read<{ repositories: string[] }>("/api/policy/repositories", get);
    if (repositories.length === 0) {
      main.append(el("p", "No repository you can read is configured."));
      return;
    }
    const pages = await readAll(repositories, get);
    main.append(table(["Repository", "Classes served from", "Classes", "Closest to promotion"], repositories.map((repo, i) => {
      const p = pages[i];
      if (p instanceof Error) return [repo, `Not readable: ${explain(p.message)}`, "-", "-"];
      const r = p.report;
      const from = r.mergePolicy ? `MergePolicy ${r.mergePolicy}` : r.classes.some((c) => c.source === "repository") ? "its own .github/agent-factory.yaml" : "the factory's central config";
      const best = r.classes.filter((c) => c.mode === "shadow" && c.evidence).sort((a, b) => b.evidence!.clean - a.evidence!.clean)[0];
      return [link(`/settings/merge-policies/${repo}`, repo), from, countsText(modeCounts(r)), best ? `${best.class} ${best.evidence!.clean}/${best.evidence!.minimum}` : "-"];
    }), "repo-table"));
  } catch (e) { failed(main, e); }
}
```

and give `table` an optional class: `function table(head: string[], rows: (string | Node)[][], cls = ""): HTMLTableElement` with `el("table", undefined, \`rooms settings-table ${cls}\`.trim())`.

Make `evidencePage` use `readAll` instead of its sequential loop, skipping `Error` entries with one row "Not readable".

- [ ] **Step 4: Run the tests**

Run: `npm --prefix web test -- settings && npm --prefix web run typecheck`
Expected: PASS. The "marks the page's section as the current tab" test still passes (it answers `/api/policy/repositories`; the home's approval-memory read gets a 404 and shows "Counts unavailable").

- [ ] **Step 5: Commit**

```bash
git add web/src/settings.ts web/test/settings.test.ts
git commit -m "feat(ui): settings opens on section cards; repositories as a table with their classes by mode"
```

---

### Task 5: Class cards on the repository page

**Files:**
- Create: `web/src/class-card.ts` (the logic of `classesTable`, `promotionActions`, `exceptionForm` moved here)
- Modify: `web/src/settings.ts` (`repositoryPage`: cards instead of the classes, promotions and exceptions tables; guardrails as sentences; intro paragraph split and bounded)
- Test: `web/test/class-card.test.ts`; `web/test/settings.test.ts` (tests that select `.policy-classes tbody tr` move to `.class-card`)

**Interfaces:**
- Consumes: `describeClass`, `describeGuardrails`, `modeMeaning` (Task 2); `evidenceLine`, `ladderLine`, `explain`, `post`, `showProposal`, `Evidence`, `RepositoryPage`, `ClassReport` from `settings.ts` (export `post` and `explain` if not yet exported).
- Produces:
  ```ts
  export interface CardContext { page: RepositoryPage; get: typeof fetch; say: HTMLElement; reload: () => void; main: HTMLElement; policy?: PolicyFile }
  export function classCard(c: ClassReport, ctx: CardContext): HTMLElement   // <article class="class-card" id="class-<name>">
  export function evidenceBar(e: Evidence): HTMLElement                       // <div class="evidence-bar" role="meter">
  export const READING = "Reading evidence from GitHub…";
  ```

- [ ] **Step 1: Write the failing tests**

`web/test/class-card.test.ts`:

```ts
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { classCard, evidenceBar, READING } from "../src/class-card";
import type { Evidence, RepositoryPage } from "../src/settings";

const ev = (over: Partial<Evidence> = {}): Evidence => ({
  shadow: { mergedUnchanged: 14, changed: 1, reverted: 0, autoMerged: 0, drill: 1, reviewers: [] },
  clean: 14, bad: 0, immature: 0, pending: 0, bound: 0.1, budget: 0.2, widen: false,
  ladder: [{ merges: 29, maxBad: 0, reached: false }, { merges: 46, maxBad: 1, reached: false }, { merges: 61, maxBad: 2, reached: false }],
  drill: "passed", minimum: 20, eligible: false, blockers: ["below_minimum"], ...over,
});
const page = (): RepositoryPage => ({
  report: { repository: "o/r", minimum: 20, readAt: "", factory: "aws-0", defaultBranch: "main", harnessProfiles: ["plain"], guardrails: {},
    policy: { factory: "aws-0", classes: { docs: { mode: "shadow", author: "agents", harness: "plain", conditions: { all: [{ maxPatchFiles: 10 }] } } } },
    classes: [{ class: "docs", source: "repository", mode: "shadow", evidence: ev() }] },
  promotions: [], exceptions: [], you: { login: "me", maintainer: true, operator: false },
});
const ctx = (p = page()) => ({ page: p, get: fetch, say: document.createElement("p"), reload: () => {}, main: document.createElement("main"), policy: p.report.policy });

describe("a class card", () => {
  it("names the class, its mode and what the mode means, then the class as sentences", () => {
    const c = classCard(page().report.classes[0], ctx());
    expect(c.id).toBe("class-docs");
    expect(c.querySelector("h3")?.textContent).toBe("docs");
    expect(c.querySelector(".mode-badge")?.textContent).toBe("Shadow · records what it would do, merges nothing");
    expect(c.querySelector(".mode-badge")?.getAttribute("data-mode")).toBe("shadow");
    expect(c.textContent).toContain("it changes at most 10 files");
  });
  it("says a class set by the factory is set there, not a bare (central)", () => {
    const p = page();
    p.report.classes[0].source = "central";
    expect(classCard(p.report.classes[0], ctx(p)).textContent).toContain("Set by the factory's central config");
  });
  it("shows the evidence as progress toward the minimum, with the count in words", () => {
    const bar = evidenceBar(ev());
    expect(bar.getAttribute("role")).toBe("meter");
    expect(bar.getAttribute("aria-valuenow")).toBe("14");
    expect(bar.getAttribute("aria-valuemax")).toBe("20");
    expect(bar.textContent).toContain("14 of 20 clean decisions to promote");
  });
  it("says once that evidence is being read, and offers no promotion meanwhile", () => {
    const p = page();
    delete p.report.classes[0].evidence;
    const c = classCard(p.report.classes[0], ctx(p));
    expect(c.textContent).toContain(READING);
    expect([...c.querySelectorAll("button")].some((b) => b.textContent === "Promote to live")).toBe(false);
  });
  it("says there is no promotion request in one line, not an empty table", () => {
    const c = classCard(page().report.classes[0], ctx());
    expect(c.querySelector("table")).toBeNull();
    expect(c.textContent).toContain("No promotion requested.");
  });
  it("lists the class's own promotion and exception inside it", () => {
    const p = page();
    p.promotions = [{ id: "p1", repository: "o/r", class: "docs", threshold: 20, requestedBy: "me", requestedAt: "2026-11-09", state: "pr_opened", pullRequest: 7 }];
    p.exceptions = [{ id: "e1", repository: "o/r", class: "docs", minimum: 12, justification: "low blast radius, docs only", requestedBy: "me", requestedAt: "2026-11-08", state: "approved" }];
    const c = classCard(p.report.classes[0], ctx(p));
    expect(c.textContent).toContain("Promotion requested by me on 2026-11-09: pull request opened");
    expect(c.querySelector('a[href="https://github.com/o/r/pull/7"]')).not.toBeNull();
    expect(c.textContent).toContain("Exception: 12 clean decisions instead of 20, approved");
  });
});
```

- [ ] **Step 2: Run them to see them fail**

Run: `npm --prefix web test -- class-card`
Expected: FAIL, module not found.

- [ ] **Step 3: Implement `class-card.ts`**

Move `classesTable`'s per-class logic, `promotionActions` and `exceptionForm` from `settings.ts` into `web/src/class-card.ts`, unchanged in behaviour (same POST bodies, same buttons, same eligibility rule), and render them as a card:

```ts
// SPDX-License-Identifier: Apache-2.0

// One merge class on its repository's page: its name and mode (with what the mode means), the
// class in words, its evidence toward promotion, and its own promotion request and exception. The
// broker decides every write: a button here is only a convenience it would also refuse.

import { describeClass, modeMeaning } from "./policy-describe";
import type { PolicyFile } from "./policy-editor";
import { evidenceLine, explain, ladderLine, post, showProposal, type ClassReport, type Evidence, type Exception, type Promotion, type Proposal, type RepositoryPage } from "./settings";

export const READING = "Reading evidence from GitHub…";

export interface CardContext { page: RepositoryPage; get: typeof fetch; say: HTMLElement; reload: () => void; main: HTMLElement; policy?: PolicyFile }

function el<K extends keyof HTMLElementTagNameMap>(tag: K, text?: string, cls?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (text !== undefined) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

const sources: Record<string, string> = {
  repository: "Declared in the repository's .github/agent-factory.yaml",
  central: "Set by the factory's central config",
  mergepolicy: "Served from a MergePolicy",
};

export function evidenceBar(e: Evidence): HTMLElement {
  const box = el("div", undefined, "evidence-bar");
  box.setAttribute("role", "meter");
  box.setAttribute("aria-valuemin", "0");
  box.setAttribute("aria-valuenow", String(Math.min(e.clean, e.minimum)));
  box.setAttribute("aria-valuemax", String(e.minimum));
  box.setAttribute("aria-label", "clean decisions toward promotion");
  const fill = el("span", undefined, "evidence-fill");
  fill.style.width = `${Math.min(100, Math.round((100 * e.clean) / Math.max(1, e.minimum)))}%`;
  const track = el("span", undefined, "evidence-track");
  track.append(fill);
  box.append(track, el("span", `${e.clean} of ${e.minimum} clean decisions to promote`, "evidence-count"));
  return box;
}

const ladder = (e: Evidence) => {
  const ol = el("ol", undefined, "evidence-ladder");
  ol.setAttribute("aria-label", "widening steps");
  for (const s of e.ladder) {
    const li = el("li", `${s.merges} merges${s.reached ? ", reached" : ""}`);
    li.dataset.reached = String(s.reached);
    ol.append(li);
  }
  return ol;
};

export function classCard(c: ClassReport, ctx: CardContext): HTMLElement {
  const card = el("article", undefined, "class-card");
  card.id = `class-${c.class}`;
  const head = el("header", undefined, "class-card-head");
  const m = modeMeaning(c.mode);
  const badge = el("span", `${m.label} · ${m.meaning}`, "mode-badge");
  badge.dataset.mode = c.mode;
  head.append(el("h3", c.class), badge);
  card.append(head, el("p", sources[c.source] ?? c.source, "class-source"));

  const policy = ctx.policy?.classes?.[c.class];
  if (policy) {
    const s = describeClass(policy, ctx.page.report.guardrails);
    const when = el("ul", undefined, "class-when");
    for (const w of s.when) when.append(el("li", w));
    card.append(el("p", s.lead, "class-lead"), when, ...s.also.map((a) => el("p", a, "class-also")));
  }

  const e = c.evidence;
  const act = el("div", undefined, "class-actions");
  if (!e) card.append(el("p", READING, "evidence-reading"));
  else {
    card.append(evidenceBar(e), ladder(e), el("p", evidenceLine(e), "evidence-detail"));
    // (moved verbatim from classesTable: reviewers line, the exception-aware eligibility, the
    // "Promote to live" and "Ask an operator for an exception" buttons, appended to act)
  }
  card.append(act);
  card.append(promotionsOf(c.class, ctx), exceptionsOf(c.class, ctx));
  return card;
}

function promotionsOf(cls: string, ctx: CardContext): HTMLElement {
  const box = el("div", undefined, "class-promotions");
  const ps = ctx.page.promotions.filter((p) => p.class === cls);
  if (ps.length === 0) { box.append(el("p", "No promotion requested.", "class-empty")); return box; }
  for (const p of ps) {
    const line = el("p", `Promotion requested by ${p.requestedBy} on ${p.requestedAt}: ${p.state === "pr_opened" ? "pull request opened" : p.state.replaceAll("_", " ")}`);
    if (p.pullRequest) {
      const a = el("a", ` #${p.pullRequest}`);
      a.href = `https://github.com/${p.repository}/pull/${p.pullRequest}`;
      line.append(a);
    }
    const act = el("span", undefined, "class-actions");
    if (ctx.page.you.maintainer && (p.state === "requested" || p.state === "pr_opened")) promotionActions(act, p, ctx);
    box.append(line, act);
  }
  return box;
}

function exceptionsOf(cls: string, ctx: CardContext): HTMLElement {
  const box = el("div", undefined, "class-exceptions");
  for (const x of ctx.page.exceptions.filter((y) => y.class === cls)) {
    box.append(el("p", `Exception: ${x.minimum} clean decisions instead of ${ctx.page.report.minimum}, ${x.state}` +
      (x.decidedBy ? ` by ${x.decidedBy}` : "") + (x.expiresAt ? ` until ${x.expiresAt}` : "") + `. “${x.justification}”`));
    // (operator Approve/Refuse buttons moved verbatim from repositoryPage's exceptions table)
  }
  return box;
}

// promotionActions and exceptionForm: moved verbatim from settings.ts, taking ctx for get, say and reload.
```

The two "(moved verbatim …)" notes above are the existing code bodies from `settings.ts` lines in `classesTable` (the `reviewers`/`eligible`/button branch) and the exceptions table's Approve/Refuse loop; move them, do not rewrite them. Unused imports must be removed so `typecheck` passes.

- [ ] **Step 4: Use the cards on the page**

In `repositoryPage` (`settings.ts`):

- Replace the one long intro paragraph with two short ones (`p.lead`, each under 80 characters a line by CSS `max-width: 72ch`): what serves the classes, then how a change is made.
- Replace `h2 Classes` + `classesTable` + the promotions and exceptions tables with:

```ts
main.append(el("h2", "Classes"));
const guard = el("ul", undefined, "guardrails");
for (const g of describeGuardrails(r.guardrails)) guard.append(el("li", g));
main.append(el("p", "The factory's guardrails, which every class inherits:", "guardrails-lead"), guard);
const classes = el("section", undefined, "policy-classes");
main.append(classes);
const policyOf = (p: RepositoryPage) => (centralManifest(p.report) ? p.report.served : p.report.policy);
const draw = (p: RepositoryPage) => classes.replaceChildren(
  ...(pending(p.report) ? [el("p", READING, "evidence-age")] : [el("p", `Evidence ${evidenceAge(p.report)}.`, "evidence-age")]),
  ...p.report.classes.map((c) => classCard(c, { page: p, get, say, reload, main, policy: policyOf(p) })));
draw(page);
```

and call `draw(cur)` inside the existing polling loop instead of `classesTable(...)`. Delete `classesTable`, `promotionActions`, `exceptionForm` from `settings.ts`. Change `PENDING` to `READING` in `evidenceAge`, and re-export: `export { READING as PENDING } from "./class-card";` so existing imports keep compiling, then replace the remaining uses.

- [ ] **Step 5: Move the existing tests**

In `web/test/settings.test.ts`:
- `renders each class as computing…`: replace the row loop with
  ```ts
  const cards = [...app.querySelectorAll(".policy-classes .class-card")];
  expect(cards).toHaveLength(2);
  expect(app.querySelectorAll(".policy-classes .evidence-reading")).toHaveLength(2);
  expect(app.querySelector(".policy-classes .evidence-age")?.textContent).toBe(READING);
  ```
- `fills the classes in place…`: `toContain("20/20 clean")` stays (it is `evidenceLine`'s text, now in `.evidence-detail`).
- `offers a maintainer an exception…`: find the buttons inside `#class-<name>` instead of table rows.
- Import `READING` from `../src/class-card`.

- [ ] **Step 6: Run the tests**

Run: `npm --prefix web test && npm --prefix web run typecheck`
Expected: PASS, all files.

- [ ] **Step 7: Commit**

```bash
git add web/src/class-card.ts web/src/settings.ts web/test/class-card.test.ts web/test/settings.test.ts
git commit -m "feat(ui): each merge class as a card that reads as sentences, with its evidence, promotion and exception"
```

---

### Task 6: The editor grouped by question

**Files:**
- Modify: `web/src/policy-editor.ts` (`classForm`)
- Modify: `web/src/settings.ts` (`editor`: picker without templates, legend, live sentence, form/YAML tabs, sticky actions)
- Test: `web/test/settings.test.ts` ("the policy editor")

**Interfaces:**
- Consumes: `FIELDS`, `GROUPS`, `docsLink` (Task 3); `describeClass` (Task 2); existing `leaf`, `setLeaf`, `lines`, `diffShapes`, `profilesFor`.
- Produces: `classForm(cls, profiles, change, adopted?, guardrails?)`: same signature plus an optional `guardrails`; DOM: `fieldset.editor-form > legend` naming the class, `section.editor-group[data-group]` with `h3`, `label.editor-field[data-key]` containing `span.field-label`, `code.field-key`, `p.field-help`, `a.field-docs`; a folded group is `button.group-add`; `p.class-story` the live sentence; tabs `button[role=tab]` "Form" and "YAML (expert)"; `div.editor-actions` holding the check button.

- [ ] **Step 1: Write the failing tests**

Add to `describe("the policy editor", …)`:

```ts
const field = (app: HTMLElement, key: string) => app.querySelector(`.editor-field[data-key="${key}"]`) as HTMLLabelElement;

it("labels every field in words, with its key, one help line and a link to the guide", async () => {
  const app = document.createElement("div");
  await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
  const f = field(app, "maxPatchFiles");
  expect(f.querySelector(".field-label")?.textContent).toBe("Most files changed");
  expect(f.querySelector("code.field-key")?.textContent).toBe("maxPatchFiles");
  expect(f.querySelector(".field-help")?.textContent).toMatch(/factory's cap/);
  expect(f.querySelector("a.field-docs")?.getAttribute("href")).toMatch(/merge-policies\.md#leaves$/);
  expect(app.querySelector(".editor-form legend")?.textContent).toBe("Editing docs-links");
});

it("groups the fields by question under h3s, and folds a group with nothing set", async () => {
  const app = document.createElement("div");
  await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
  expect([...app.querySelectorAll(".editor-group h3")].map((h) => h.textContent))
    .toEqual(["Who writes it", "What it may touch", "How big it may be", "What must pass", "When to ask a maintainer", "Limits and follow-up"]);
  const ask = app.querySelector('.editor-group[data-group="ask"]')!;
  const add = ask.querySelector("button.group-add") as HTMLButtonElement;
  expect(add.textContent).toBe("Add an approval rule");
  expect(ask.querySelector(".editor-field")).toBeNull();
  add.click();
  expect(ask.querySelector('.editor-field[data-key="changedFiles"]')).not.toBeNull();
});

it("rewrites the class's sentence as the form changes", async () => {
  const app = document.createElement("div");
  await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
  const cap = field(app, "maxPatchFiles").querySelector("input")!;
  cap.value = "4";
  cap.dispatchEvent(new Event("input"));
  expect(app.querySelector(".class-story")?.textContent).toContain("it changes at most 4 files");
});

it("lists only classes in the picker; examples come from the gallery", async () => {
  const app = document.createElement("div");
  await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
  const pick = app.querySelector('select[aria-label="class to edit"]') as HTMLSelectElement;
  expect([...pick.options].map((o) => o.value)).toEqual(["docs-links"]);
});

it("keeps the expert YAML in a tab beside the form, the check below both", async () => {
  const app = document.createElement("div");
  await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
  const tabs = [...app.querySelectorAll('.policy-editor [role="tab"]')] as HTMLButtonElement[];
  expect(tabs.map((t) => t.textContent)).toEqual(["Form", "YAML (expert)"]);
  expect(tabs[0].getAttribute("aria-selected")).toBe("true");
  expect((app.querySelector(".yaml-editor")!.closest('[role="tabpanel"]') as HTMLElement).hidden).toBe(true);
  tabs[1].click();
  expect((app.querySelector(".yaml-editor")!.closest('[role="tabpanel"]') as HTMLElement).hidden).toBe(false);
  expect(app.querySelector(".editor-actions button")?.textContent).toBe("Check, compare and simulate");
});
```

Move the existing editor tests to the new selectors:
- `edits a class in the form…`: `const cap = field(app, "maxPatchFiles").querySelector("input")!;`
- `starts a new class from a template…` and `gives a template a profile…`: start through the gallery (Task 7) with `startFrom(app, "deps-patch")`, a helper that clicks `.example-card[data-example="deps-patch"] button`. Select the mode with `field(app, "mode").querySelector("select")` and the harness with `field(app, "harness").querySelector("select")` instead of positional `[2]`.

- [ ] **Step 2: Run them to see them fail**

Run: `npm --prefix web test -- settings`
Expected: FAIL on the new tests (no `data-key`, no groups, no tabs).

- [ ] **Step 3: Rewrite `classForm`**

In `web/src/policy-editor.ts`, keep `classForm`'s state handling (`c`, `emit`, `setLeaf` writes, the author/harness coupling) and replace the rendering:

```ts
export function classForm(cls: ClassPolicy, profiles: string[], change: (c: ClassPolicy) => void, adopted?: string[] | null,
  guardrails: Record<string, unknown> = {}, name = ""): HTMLElement {
  const c: ClassPolicy = structuredClone(cls);
  c.conditions ??= {};
  const form = el("fieldset", undefined, "editor-form");
  form.append(el("legend", name ? `Editing ${name}` : "New class"));
  const story = el("div", undefined, "class-story");
  story.setAttribute("aria-live", "polite");
  const tell = () => {
    const s = describeClass(c, guardrails);
    const ul = el("ul");
    for (const w of s.when) ul.append(el("li", w));
    story.replaceChildren(el("p", s.lead), ul, ...s.also.map((a) => el("p", a)));
  };
  const emit = () => { tell(); change(structuredClone(c)); };
  form.append(story);

  // field wraps one control in its label, key, help and guide link; data-key is the selector tests use.
  const field = (key: string, label: string, help: string, anchor: string, input: HTMLElement) => {
    const l = el("label", undefined, "editor-field");
    l.dataset.key = key;
    const top = el("span", undefined, "field-top");
    top.append(el("span", label, "field-label"), el("code", key, "field-key"));
    const docs = el("a", "Learn more", "field-docs");
    docs.href = docsLink(anchor);
    docs.target = "_blank";
    docs.rel = "noopener";
    const h = el("p", help, "field-help");
    h.append(" ", docs);
    l.append(top, input, h);
    return l;
  };
  // (the existing select/text/number helpers now return the control instead of appending it)

  // Always shown, above the groups: mode, author, harness profile.
  form.append(field("mode", "Mode", "Live merges, shadow records what it would do and merges nothing, off decides nothing.", "shadow-and-live",
    selectOf(["shadow", "live", "off"], c.mode, (v) => { c.mode = v; redraw(); })));
  form.append(field("author", "Written by", "Agents: the factory's own runs. Adopt: another App's pull requests, such as Renovate's.", "a-class",
    selectOf(["agents", "adopt"], c.author, (v) => {
      c.author = v;
      if (v === "adopt") c.adopt ??= { logins: [], branches: [] }; else delete c.adopt;
      if (!profilesFor(v, profiles, adopted).includes(c.harness)) c.harness = profilesFor(v, profiles, adopted)[0] ?? "";
      fillHarness(); // the old fill(): refills the harness select with the profiles this author may take
      redraw();
    })));
  form.append(field("harness", "Harness profile", "How the factory runs this class's tasks, one of the profiles the factory defines.", "central-config", harnessSelect()));

  const groupsAt = el("div", undefined, "editor-groups");
  form.append(groupsAt);
  const open = new Set<Group>();
  const redraw = () => {
    groupsAt.replaceChildren(...GROUPS.map((g) => {
      const sec = el("section", undefined, "editor-group");
      sec.dataset.group = g.id;
      sec.append(el("h3", g.title));
      const fs = FIELDS.filter((f) => f.group === g.id && f.shown(c));
      const set = fs.some((f) => hasValue(c, f.key));
      if (g.add && !set && !open.has(g.id)) {
        const b = el("button", g.add, "group-add");
        b.type = "button";
        b.onclick = () => { open.add(g.id); redraw(); (groupsAt.querySelector(`[data-group="${g.id}"] input, [data-group="${g.id}"] textarea`) as HTMLElement | null)?.focus(); };
        sec.append(b);
        return sec;
      }
      for (const f of fs) sec.append(field(f.key, f.label, f.help, f.anchor, control(f)));
      if (g.id === "touch" && (c.conditions!.any ?? []).length) sec.append(el("p", "This class also has an any of several branches: edit it in the YAML view.", "notice"));
      return sec;
    }));
  };
  redraw();
  tell();
  return form;
}
```

with, in the same file:

```ts
// hasValue reports whether c sets key, the field table's key (dotted for nested keys).
function hasValue(c: ClassPolicy, key: string): boolean {
  const all = c.conditions?.all ?? [];
  switch (key) {
    case "adopt.logins": return !!c.adopt?.logins.length;
    case "adopt.branches": return !!c.adopt?.branches.length;
    case "harnessAllowed": return !!c.harnessAllowed?.length;
    case "escalation.outOfBudget": return !!c.escalation?.outOfBudget;
    case "escalation.notConverged": return !!c.escalation?.notConverged;
    case "protectedFiles": return !!c.conditions?.deny?.length;
    case "changedFiles": return !!c.requiresApproval?.length;
    case "maxPerDay": return !!c.limits?.maxPerDay;
    case "followUpWindow": return !!c.monitor?.followUpWindow;
    default: return all.some((l) => key in l);
  }
}
```

and `control(f: Field): HTMLElement` building, per `f.kind`, the same input the old form built for that key (textarea of lines for `list`, `type=number` with `min=1` and, when the guardrail sets one, `max` for `number`, checkbox for `bool`, select of `diffShapes` for `shape`, filtered to `["", "links-only"]` when `c.author !== "adopt"`), writing through the same setters the old form used (`setLeaf` for leaves, `c.conditions.deny`, `c.requiresApproval`, `c.limits`, `c.monitor`, `c.adopt`, and new: `c.harnessAllowed = lines(v)` / `delete` when empty, `c.escalation = { ...c.escalation, outOfBudget: v }` with empty keys deleted). A list textarea marks a line that is not a valid `RegExp` (`try { new RegExp(x) } catch`) by setting `aria-invalid="true"` and a `p.field-error` "Line N is not a regular expression".

Import `describeClass` from `./policy-describe` and `FIELDS`, `GROUPS`, `docsLink`, `type Field`, `type Group` from `./policy-fields`.

- [ ] **Step 4: Rework `editor` in `settings.ts`**

- The picker lists `Object.keys(file.classes)` only. Add `button.example-open` "New class from an example" that reveals the gallery (Task 7).
- `classForm(cls, profiles, change, r.adoptedProfiles, r.guardrails, key())`.
- Replace the line `factory: … Guardrails: ${JSON.stringify(…)} …` with `p.editor-meta`: "This file belongs to the factory aws-0 (read-only). Harness profiles: plain, checked." (guardrails are already on the page as sentences).
- Wrap the form and the YAML view in two tab panels:

```ts
const tabs = el("div", undefined, "editor-tabs");
tabs.setAttribute("role", "tablist");
const panes: HTMLElement[] = [];
const tab = (label: string, pane: HTMLElement, on: boolean) => {
  const b = el("button", label);
  b.type = "button";
  b.setAttribute("role", "tab");
  b.setAttribute("aria-selected", String(on));
  pane.setAttribute("role", "tabpanel");
  pane.hidden = !on;
  b.onclick = () => {
    for (const t of tabs.children) t.setAttribute("aria-selected", String(t === b));
    for (const p of panes) p.hidden = p !== pane;
  };
  panes.push(pane);
  tabs.append(b);
};
const formPane = el("div", undefined, "editor-pane");
formPane.append(labelled2("Class", pick), name, warning, formAt);
const yamlPane = el("div", undefined, "editor-pane");
yamlPane.append(el("p", "The form rewrites the whole file without comments; edits here are checked exactly as written."), view.root);
tab("Form", formPane, true);
tab("YAML (expert)", yamlPane, false);
box.append(tabs, formPane, yamlPane);
```

- Put `out` (the check's answer) before a `div.editor-actions` holding `check` and the reader notice, appended last.
- The `h3 "Expert YAML view"` goes away (the tab names it).

- [ ] **Step 5: Run the tests**

Run: `npm --prefix web test && npm --prefix web run typecheck`
Expected: PASS, all files. A failing old test that asserted the old structure is moved to the new selector, never deleted, unless its behaviour is gone by the spec (the template options in the picker; that one is replaced by the picker test above).

- [ ] **Step 6: Commit**

```bash
git add web/src/policy-editor.ts web/src/settings.ts web/test/settings.test.ts
git commit -m "feat(ui): the class editor grouped by question, every field labelled and explained, with a live sentence"
```

---

### Task 7: The example gallery

**Files:**
- Modify: `web/src/policy-editor.ts` (new `exampleGallery`)
- Modify: `web/src/settings.ts` (`editor`: the gallery behind "New class from an example", starting the form from a template)
- Test: `web/test/settings.test.ts`

**Interfaces:**
- Consumes: `EXAMPLES` (Task 3), `templates`, `describeClass`, `toYAML`, `highlight`.
- Produces: `export function exampleGallery(start: (name: string) => void, guardrails?: Record<string, unknown>): HTMLElement`; DOM `section.example-gallery > h3` ("Simple", "Advanced") and `article.example-card[data-example]` with the sentence, `p.example-why`, `details > pre` (YAML) and `button` "Start from this".

- [ ] **Step 1: Write the failing test**

```ts
it("offers simple and advanced examples, each explained, and starts the form from one", async () => {
  const app = document.createElement("div");
  await mountSettings(app, "/settings/merge-policies/Smana/cloud-native-ref", fakeFetch({ "/api/policy/repositories/Smana/cloud-native-ref": page() }));
  (app.querySelector("button.example-open") as HTMLButtonElement).click();
  const g = app.querySelector("section.example-gallery") as HTMLElement;
  expect(g.hidden).toBe(false);
  expect([...g.querySelectorAll("h3")].map((h) => h.textContent)).toEqual(["Simple", "Advanced"]);
  const card = g.querySelector('.example-card[data-example="infra-with-approval"]')!;
  expect(card.querySelector(".example-why")?.textContent).toMatch(/network/);
  expect(card.textContent).toContain("A maintainer approves it first if it changes ^infrastructure/network/.+$.");
  (card.querySelector("button") as HTMLButtonElement).click();
  expect(app.querySelector(".editor-form legend")?.textContent).toBe("New class from infra-with-approval");
  expect((app.querySelector('input[aria-label="new class name"]') as HTMLInputElement).hidden).toBe(false);
});
```

and the `startFrom` helper used by the moved tests of Task 6:

```ts
const startFrom = (app: HTMLElement, name: string) => {
  (app.querySelector("button.example-open") as HTMLButtonElement).click();
  (app.querySelector(`.example-card[data-example="${name}"] button`) as HTMLButtonElement).click();
};
```

- [ ] **Step 2: Run it to see it fail**

Run: `npm --prefix web test -- settings`
Expected: FAIL, no `button.example-open` gallery.

- [ ] **Step 3: Implement**

In `policy-editor.ts`:

```ts
// exampleGallery shows each template as an example: its level, why one would start from it, what
// it does in words and its YAML; start(name) begins a new class from it.
export function exampleGallery(start: (name: string) => void, guardrails: Record<string, unknown> = {}): HTMLElement {
  const box = el("section", undefined, "example-gallery");
  box.hidden = true;
  for (const level of ["simple", "advanced"] as const) {
    box.append(el("h3", level === "simple" ? "Simple" : "Advanced"));
    const row = el("div", undefined, "example-row");
    for (const ex of EXAMPLES.filter((e) => e.level === level)) {
      const t = templates[ex.name]();
      const s = describeClass(t, guardrails);
      const card = el("article", undefined, "example-card");
      card.dataset.example = ex.name;
      const ul = el("ul");
      for (const w of s.when) ul.append(el("li", w));
      const yaml = el("details");
      const pre = el("pre", undefined, "example-yaml");
      pre.append(highlight(toYAML({ [ex.name]: t })));
      yaml.append(el("summary", "YAML"), pre);
      const go = el("button", "Start from this");
      go.type = "button";
      go.onclick = () => start(ex.name);
      card.append(el("h4", ex.name), el("p", ex.why, "example-why"), el("p", s.lead), ul, ...s.also.map((a) => el("p", a)), yaml, go);
      row.append(card);
    }
    box.append(row);
  }
  return box;
}
```

The gallery's h3 sits under the editor's h2 "Policy editor", its cards' h4 under the h3: no level skipped.

In `editor` (`settings.ts`): replace the `template:` branch of `show` with a `source` variable (`{ kind: "class"; name } | { kind: "template"; name }`). `start(name)` sets `source = { kind: "template", name }`, shows the name input (placeholder "new class name", defaulting to the template's name), hides the gallery and calls `show()`; the legend reads `New class from ${name}`. The picker's `onchange` sets `source = { kind: "class", name: pick.value }`. `button.example-open` toggles `gallery.hidden` and sets its own `aria-expanded`.

- [ ] **Step 4: Run the tests**

Run: `npm --prefix web test && npm --prefix web run typecheck`
Expected: PASS, the moved template tests included.

- [ ] **Step 5: Commit**

```bash
git add web/src/policy-editor.ts web/src/settings.ts web/test/settings.test.ts
git commit -m "feat(ui): a gallery of simple and advanced merge-class examples, each explained"
```

---

### Task 8: Styles, mobile and motion

**Files:**
- Modify: `web/src/app.css` (the settings block, around lines 1040–1100)
- Test: `web/test/overflow.test.ts` (extend if it covers settings), screenshots

**Interfaces:** none new; classes from Tasks 4–7.

- [ ] **Step 1: Read the floor**

Read `/home/smana/.claude/skills/impeccable/reference/craft-floor.md`, and the `micro-interaction` and `accessible-animation` skills' SKILL.md.

- [ ] **Step 2: Write the styles**

Add, using the existing tokens only (PR 2 swaps their values):

```css
/* Settings home: one card per section, the whole card the link. */
.settings-cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 16px; margin: 8px 0 24px; }
.settings-card { display: block; padding: 16px; border-radius: var(--radius-card); background: var(--card); box-shadow: var(--shadow-border);
  color: var(--card-foreground); text-decoration: none; transition: box-shadow 150ms ease-out, transform 150ms ease-out; }
.settings-card:hover { box-shadow: var(--shadow-border-hover); transform: translateY(-1px); }
.settings-card:focus-visible { outline: 2px solid var(--ring); outline-offset: 2px; }
.settings-card h2 { margin: 0 0 4px; font-size: 18px; }
.settings-card p { margin: 0 0 8px; color: var(--muted-foreground); max-width: 60ch; }
.settings-card .settings-card-counts { margin: 0; color: var(--card-foreground); font-variant-numeric: tabular-nums; }

/* Prose on settings pages stays readable: no line past 72 characters. */
.settings > p, .settings .guardrails, .settings .class-card p { max-width: 72ch; }
.settings h2 { font-size: 20px; margin: 28px 0 8px; }
.settings h3 { font-size: 16px; margin: 0; }

/* A class: name, mode in words, the class as sentences, its evidence. */
.policy-classes { display: grid; gap: 12px; }
.class-card { padding: 16px; border-radius: var(--radius-card); background: var(--card); box-shadow: var(--shadow-border); }
.class-card:target { box-shadow: 0 0 0 2px var(--ring); }
.class-card-head { display: flex; flex-wrap: wrap; gap: 8px; align-items: baseline; justify-content: space-between; }
.mode-badge { padding: 2px 10px; border-radius: 999px; font-size: 13px; border: 1px solid var(--border); }
.mode-badge[data-mode="live"] { border-color: var(--success); color: var(--success-text); }
.mode-badge[data-mode="shadow"] { border-color: var(--primary); color: var(--link); }
.mode-badge[data-mode="off"] { color: var(--muted-foreground); }
.class-source, .evidence-detail, .class-empty { color: var(--muted-foreground); font-size: 14px; }
.class-when { margin: 4px 0 8px; padding-left: 20px; }
.evidence-bar { display: flex; align-items: center; gap: 10px; margin: 8px 0 4px; }
.evidence-track { flex: 1; max-width: 320px; height: 6px; border-radius: 999px; background: var(--muted); overflow: hidden; }
.evidence-fill { display: block; height: 100%; background: var(--primary); }
.evidence-count { font-size: 14px; font-variant-numeric: tabular-nums; }
.evidence-ladder { display: flex; gap: 12px; list-style: none; padding: 0; margin: 0 0 4px; font-size: 13px; color: var(--muted-foreground); }
.evidence-ladder li[data-reached="true"] { color: var(--success-text); }

/* The editor: groups by question, each field its label, key, control and help. */
.editor-tabs { display: flex; gap: 4px; margin: 8px 0; border-bottom: 1px solid var(--border); }
.editor-tabs [role="tab"] { border: 0; background: none; padding: 8px 12px; color: var(--muted-foreground); min-height: var(--hit); }
.editor-tabs [role="tab"][aria-selected="true"] { color: var(--foreground); box-shadow: inset 0 -2px 0 var(--primary); }
.class-story { padding: 12px 16px; margin: 0 0 12px; border-radius: var(--radius-row); background: var(--muted); }
.editor-group { padding: 12px 0; border-top: 1px solid var(--border); }
.settings .editor-field { display: grid; grid-template-columns: minmax(12em, 18em) minmax(0, 1fr); gap: 4px 16px; align-items: start; margin: 8px 0; }
.field-top { display: flex; flex-direction: column; gap: 2px; }
.field-key { font-size: 12px; color: var(--muted-foreground); }
.field-help { grid-column: 2; margin: 0; font-size: 13px; color: var(--muted-foreground); }
.field-error { grid-column: 2; margin: 0; font-size: 13px; color: var(--destructive-text); }
.group-add { margin-top: 4px; }
.editor-actions { position: sticky; bottom: 0; display: flex; gap: 12px; align-items: center; padding: 12px 0; background: var(--muted); border-top: 1px solid var(--border); }

/* The gallery: simple, then advanced. */
.example-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 12px; margin: 8px 0 16px; }
.example-card { padding: 14px 16px; border-radius: var(--radius-card); background: var(--card); box-shadow: var(--shadow-border); display: flex; flex-direction: column; gap: 6px; }
.example-card h4 { margin: 0; font-family: ui-monospace, monospace; font-size: 14px; }
.example-why { color: var(--muted-foreground); margin: 0; }
.example-card button { align-self: flex-start; margin-top: auto; }
.example-yaml { font-size: 12px; overflow-x: auto; }

/* A new group or a revealed gallery fades in; nothing moves under reduced motion. */
.editor-group, .example-gallery:not([hidden]) { animation: settle-in 180ms ease-out; }
@keyframes settle-in { from { opacity: 0; transform: translateY(4px); } to { opacity: 1; transform: none; } }
@media (prefers-reduced-motion: reduce) {
  .editor-group, .example-gallery:not([hidden]) { animation: none; }
  .settings-card { transition: none; }
  .settings-card:hover { transform: none; }
}

@media (max-width: 720px) {
  .settings .editor-field { grid-template-columns: minmax(0, 1fr); }
  .field-help, .field-error { grid-column: 1; }
  .settings .settings-table { display: block; overflow-x: auto; }
}
```

Delete the old `.settings .editor-field { … 18em 1fr … }` rule and `.settings h2 { font-size: 16px … }`.

- [ ] **Step 3: Check it in the preview**

Run: `npm --prefix web run preview:build` (server from Task 1 still running), then for each of `/settings`, `/settings/merge-policies`, `/settings/merge-policies/Smana/cloud-native-ref`, `/settings/merge-policies/Smana/maku`, in `theme=dark` and `theme=light`, at `1440,3600` and `390,2400`:
`chromium --headless=new --disable-gpu --hide-scrollbars --window-size=W,H --virtual-time-budget=5000 --screenshot=<scratch>/<page>-<theme>-<w>.png "<url>"`
Look at each. Expected: cards and the table on a surface, no line past 72 characters, labels stacked at 390 px, no sideways scroll (`document.documentElement.scrollWidth <= innerWidth`: run `chromium --headless=new --dump-dom` is not enough; use `npx vitest run web/test/overflow.test.ts` if it covers settings, else note the visual check).

- [ ] **Step 4: Run the UI tests**

Run: `npm --prefix web test && npm --prefix web run typecheck`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/app.css
git commit -m "feat(ui): settings cards, class cards, editor groups and gallery styled; stacked under 720 px"
```

---

### Task 9: Docs, bundle, gate, review, PR

**Files:**
- Modify: `docs/web-ui.md` (the settings rows, around line 196), `docs/merge-policies.md` ("### The settings editor", line 696)
- Modify: `internal/humanapi/ui/dist/` (rebuilt)

- [ ] **Step 1: Update the docs**

- `docs/web-ui.md`: the `/settings` row reads "Section cards with live counts"; `/settings/merge-policies` reads "One row per repository: where its classes come from, its classes by mode, the class closest to promotion"; the repository row reads "One card per class: its mode and what it means, the class in words, evidence toward promotion with the widening steps, its promotion and exception; the factory's guardrails in words".
- `docs/merge-policies.md` "The settings editor": the form is grouped by question (list the six groups), each field labelled with its key and a help line, a live sentence, the gallery's simple and advanced examples (name the six), the YAML view as a tab, the check below both. Keep the existing facts (nothing is written; Check is the factory's).

Run: `bash hack/docs_test.sh && bash hack/docs.sh`
Expected: exit 0 (no dead anchor; the docs links in `policy-fields.ts` point at `#leaves`, `#a-class`, `#central-config`, `#shadow-and-live`, which exist in `docs/merge-policies.md`: verify with `grep -n '^### Shadow and live\|^#### Leaves\|^#### A class\|^### Central config' docs/merge-policies.md`).

- [ ] **Step 2: Rebuild the bundle and run the gate**

Run: `task ui:build && task check`
Expected: exit 0. `ui:check` passes because `internal/humanapi/ui/dist/` is the fresh build.

- [ ] **Step 3: Design review**

Run impeccable `polish` on the settings pages, then `audit`, then:
`/home/smana/.claude/skills/impeccable/scripts/impeccable detect --json "http://127.0.0.1:4180/preview/settings.html?theme=dark&path=/settings/merge-policies/Smana/cloud-native-ref"` (and the three other settings paths).
Expected: no `skipped-heading`, no `line-length` on settings; fix anything else it finds or list it as intentional in the PR body.

- [ ] **Step 4: Commit and open the PR**

```bash
git add docs/web-ui.md docs/merge-policies.md internal/humanapi/ui/dist
git commit -m "docs(ui): the settings pages and the grouped editor"
git push -u origin HEAD
gh pr create --title "feat(ui): settings that explain merge policies" --body-file <scratch>/pr-body.md
```

PR body: what changed per page, before/after screenshots (dark and light, 1440 and 390), the preview paths to check locally, the impeccable detect result, and the closing line `🤖 Generated with [Claude Code](https://claude.com/claude-code)`. **Do not merge:** the owner checks the result locally first.
