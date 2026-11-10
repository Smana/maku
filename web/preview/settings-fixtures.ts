// SPDX-License-Identifier: Apache-2.0

// The settings preview's repositories. Smana/cloud-native-ref is served from a central MergePolicy
// that differs from its .github/agent-factory.yaml in two places, with every evidence state the
// page draws: one class eligible, one short of the minimum, one still being read, one open
// promotion and one approved exception. Smana/legacy has no MergePolicy yet. smallPages are six
// more MergePolicy-served repositories, so the table has enough rows to filter.

import type { PolicyFile } from "../src/policy-editor";
import type { ClassReport, Condition, Evidence, MergePolicyStatus, RepositoryPage } from "../src/settings";

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

const you = { login: "Smana", maintainer: true, operator: true };
const guardrails = { protectedFiles: ["^\\.github/", "^\\.policy\\.yml$"], maxPatchFiles: 20, maxModifiedLines: 400, maxPerDay: 5 };
const base = { minimum: 20, readAt: "2026-11-10T08:00:00Z", evidenceAt: "2026-11-10T08:00:00Z", factory: "aws-0", defaultBranch: "main",
  harnessProfiles: ["adopted-none", "checked", "plain"], adoptedProfiles: ["adopted-none"], guardrails };
const cond = (type: string, status: string, reason: string, message = ""): Condition => ({ type, status, reason, message });
const accepted = cond("Accepted", "True", "Accepted");
const fileValid = cond("RepositoryFileValid", "True", "Valid");

// The file's classes, as the MergePolicy serves them, each with what it is for but revert, whose
// card then leads with its summary line.
const served: PolicyFile = { factory: "aws-0", classes: {
  "docs-links": { description: "Fixes a broken link in the docs, and nothing else.", mode: "shadow", author: "agents", harness: "plain",
    conditions: { all: [{ holdoutChecks: ["Check the documentation links"] }] } },
  revert: { mode: "live", author: "agents", harness: "plain" },
  docs: { description: "Small documentation changes: Markdown under docs/ and the README, never the security pages.", mode: "shadow", author: "agents", harness: "plain",
    conditions: { all: [{ allowedFiles: ["^docs/.+\\.md$", "^README\\.md$"] }, { maxPatchFiles: 10 }, { maxModifiedLines: 200 }, { holdoutChecks: ["Check the documentation links"] }],
      deny: [{ protectedFiles: ["^docs/security/"] }] },
    requiresApproval: [{ changedFiles: ["^docs/architecture/"] }], limits: { maxPerDay: 2 } },
  "deps-patch": { mode: "shadow", author: "adopt", adopt: { logins: ["renovate[bot]"], branches: ["^renovate/"] }, harness: "adopted-none",
    conditions: { all: [{ diffShape: "semver-patch" }, { modifyOnly: true }, { releaseAgeCheck: "renovate/stability-days" }] } },
} };

const manifest = (name: string, classes: string) => `apiVersion: maku.ogenki.io/v1alpha1
kind: MergePolicy
metadata:
  name: ${name}
  namespace: agent-system
spec:
  repositoryRef: ${name}
  classes:
${classes}`;

const central = (c: Omit<ClassReport, "source">): ClassReport => ({ source: "central", unproposed: "not_in_repository_policy", ...c });

export const cnrPage: RepositoryPage = {
  report: {
    ...base, repository: "Smana/cloud-native-ref", file, policy: served,
    mergePolicy: "cloud-native-ref", manifestPath: "mergepolicy-cloud-native-ref.yaml", served,
    manifest: manifest("cloud-native-ref", `    docs-links:
      description: Fixes a broken link in the docs, and nothing else.
      shadow: true
      conditions:
        all:
        - holdoutChecks: [Check the documentation links]
    revert: {}
    docs:
      description: "Small documentation changes: Markdown under docs/ and the README, never the security pages."
      shadow: true
      limits:
        maxPerDay: 2
    deps-patch:
      author: adopt
      shadow: true
`),
    mergePolicyStatus: { namespace: "agent-system", generation: 4, status: {
      observedGeneration: 4, authority: "central", classCount: 4, liveCount: 1,
      conditions: [accepted, fileValid, cond("InSync", "False", "Diverges",
        "docs: description only in the central config; deps-patch: conditions differ from .github/agent-factory.yaml")],
      classes: [{ name: "docs-links", mode: "shadow", lastDecision: "would_merge:clean", lastDecisionAt: "2026-11-10T06:40:00Z" },
        { name: "revert", mode: "live" }, { name: "docs", mode: "shadow" }, { name: "deps-patch", mode: "shadow" }],
    } as MergePolicyStatus["status"] },
    classes: [
      central({ class: "docs-links", mode: "shadow", evidence: ev({ clean: 21, eligible: true, blockers: [] }) }),
      central({ class: "revert", mode: "live", evidence: ev({ clean: 40, eligible: true, blockers: [] }) }),
      central({ class: "docs", mode: "shadow", evidence: ev() }),
      central({ class: "deps-patch", mode: "shadow" }),
    ],
  },
  promotions: [{ id: "p1", repository: "Smana/cloud-native-ref", class: "docs-links", threshold: 20, requestedBy: "Smana",
    requestedAt: "2026-11-09T10:00:00Z", state: "pr_opened", pullRequest: 512 }],
  exceptions: [{ id: "e1", repository: "Smana/cloud-native-ref", class: "docs", minimum: 12, justification: "Docs-only class, reverted twice in a year, low blast radius.",
    requestedBy: "Smana", requestedAt: "2026-11-08T09:00:00Z", state: "approved", decidedBy: "ops-lead", expiresAt: "2026-12-08T09:00:00Z" }],
  you,
};

// Not migrated: the classes come from the repository's own file, read-only here.
const legacyFile = `factory: aws-0
classes:
  docs-links:
    mode: shadow
    author: agents
    harness: plain
  revert:
    mode: live
    author: agents
    harness: plain
`;
export const legacyPage: RepositoryPage = {
  report: {
    ...base, repository: "Smana/legacy", file: legacyFile,
    policy: { factory: "aws-0", classes: {
      "docs-links": { mode: "shadow", author: "agents", harness: "plain" },
      revert: { mode: "live", author: "agents", harness: "plain" },
    } },
    classes: [
      { class: "docs-links", source: "repository", mode: "shadow", evidence: ev({ clean: 9, immature: 1, pending: 0 }),
        proposal: { path: ".github/agent-factory.yaml", line: 3, file: legacyFile, editURL: "https://github.com/Smana/legacy/edit/main/.github/agent-factory.yaml" } },
      { class: "revert", source: "repository", mode: "live", unproposed: "not_shadow", evidence: ev({ clean: 31, eligible: true, blockers: [] }) },
    ],
  },
  promotions: [], exceptions: [], you,
};

// smallPage is a MergePolicy-served repository with its two required classes; sync says what its
// InSync condition reads, or that the MergePolicy has no status yet.
const smallPage = (repository: string, sync: "in-sync" | "diverges" | "no-status"): RepositoryPage => {
  const name = repository.split("/")[1];
  const classes: PolicyFile = { factory: "aws-0", classes: {
    "docs-links": { mode: "shadow", author: "agents", harness: "plain" }, revert: { mode: "live", author: "agents", harness: "plain" } } };
  const status: MergePolicyStatus | undefined = sync === "no-status" ? undefined : { namespace: "agent-system", generation: 1, status: {
    observedGeneration: 1, authority: "central", conditions: [accepted, fileValid, sync === "in-sync" ? cond("InSync", "True", "InSync")
      : cond("InSync", "False", "Diverges", "docs-links: only in the central config")] } };
  return {
    report: { ...base, repository, policy: classes, served: classes, mergePolicy: name, manifestPath: `mergepolicy-${name}.yaml`,
      manifest: manifest(name, "    docs-links:\n      shadow: true\n    revert: {}\n"), mergePolicyStatus: status,
      classes: [central({ class: "docs-links", mode: "shadow", evidence: ev({ clean: 6 }) }), central({ class: "revert", mode: "live" })] },
    promotions: [], exceptions: [], you: { ...you, operator: false },
  };
};

export const smallPages: Record<string, RepositoryPage> = Object.fromEntries(([
  ["Smana/cloud-tools", "in-sync"], ["Smana/homelab", "in-sync"], ["Smana/blog", "diverges"],
  ["Smana/dotfiles", "in-sync"], ["Smana/cnd-website", "in-sync"], ["Smana/ogenki-charts", "no-status"],
] as const).map(([r, s]) => [r, smallPage(r, s)]));
