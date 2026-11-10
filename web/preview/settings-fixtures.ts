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
