// SPDX-License-Identifier: Apache-2.0

// The settings preview's repositories. Smana/cloud-native-ref is bound to its Repository under
// central authority, out of sync with its .github/agent-factory.yaml, with every evidence state the
// page draws: one class eligible, one short of the minimum, one still being read, one open
// promotion and one approved exception. Smana/maku is bound under repository authority: its file
// holds the bindings. Smana/legacy has no Repository yet. smallPages are six more bound
// repositories, so the table has enough rows to filter. policies are the MergePolicies they bind.

import type { PolicyReport } from "../src/policy-page";
import type { BindingView, ClassReport, Condition, Evidence, RepositoryPage, RepositoryStatus } from "../src/settings";

const ev = (over: Partial<Evidence> = {}): Evidence => ({
  shadow: { mergedUnchanged: 14, changed: 1, reverted: 0, autoMerged: 0, drill: 1, reviewers: [{ model: "glm-5.3", mergedUnchanged: 14, changed: 1 }] },
  clean: 14, bad: 0, immature: 2, pending: 1, bound: 0.191, budget: 0.2, widen: false,
  ladder: [{ merges: 29, maxBad: 0, reached: false }, { merges: 46, maxBad: 1, reached: false }, { merges: 61, maxBad: 2, reached: false }],
  drill: "passed", minimum: 20, eligible: false, blockers: ["below_minimum"], ...over,
});

const you = { login: "Smana", maintainer: true, operator: true };
const guardrails = { protectedFiles: ["^\\.github/", "^\\.policy\\.yml$"], maxPatchFiles: 20, maxModifiedLines: 400, maxPerDay: 5 };
const base = { minimum: 20, readAt: "2026-11-10T08:00:00Z", evidenceAt: "2026-11-10T08:00:00Z", factory: "aws-0", defaultBranch: "main",
  harnessProfiles: ["adopted-none", "checked", "plain"], adoptedProfiles: ["adopted-none"], guardrails };
const cond = (type: string, status: string, reason: string, message = ""): Condition => ({ type, status, reason, message });
const accepted = cond("Accepted", "True", "Accepted");
const bindingsValid = cond("BindingsValid", "True", "Valid");

// binding is one accepted binding of a policy at generation, with what the repository supplies.
const binding = (name: string, mode: string, gen: number, params: Record<string, unknown> = {}): BindingView =>
  ({ name, mode, policyGeneration: gen, parameters: { name, mode, ...params }, accepted: true });

// manifest is the Repository object a bound repository's bindings are committed as.
const manifest = (name: string, bindings: BindingView[]) => `apiVersion: maku.ogenki.io/v1alpha1
kind: Repository
metadata:
  name: ${name}
  namespace: agent-system
spec:
  factoryRef: aws-0
  repository: Smana/${name}
  mergePolicies:
${bindings.map((b) => `  - name: ${b.name}\n    mode: ${b.mode}\n`).join("")}`;

// proposal is a shadow binding's promotion, as the factory proposes it in the Repository manifest.
const proposal = (name: string, m: string, cls: string) => ({
  path: `repository-${name}.yaml`, line: m.split("\n").findIndex((l) => l.endsWith(`name: ${cls}`)) + 2,
  file: m.replace(`name: ${cls}\n    mode: shadow`, `name: ${cls}\n    mode: live`), editURL: "",
});

const cnrBindings: BindingView[] = [
  binding("docs-links", "shadow", 2, { holdoutChecks: ["Check the documentation links"] }),
  binding("revert", "live", 1),
  binding("docs", "shadow", 4, { allowedFiles: ["^docs/.+\\.md$", "^README\\.md$"], maxPerDay: 2 }),
  { ...binding("deps-patch", "shadow", 1, { requiredLabel: "dependencies" }), accepted: false, reason: "MissingParameter" },
];
const cnrManifest = manifest("cloud-native-ref", cnrBindings);
const bound = (c: Omit<ClassReport, "source">): ClassReport => ({ source: "central", ...c });

export const cnrPage: RepositoryPage = {
  report: {
    ...base, repository: "Smana/cloud-native-ref", file: "factory: aws-0\n", policy: { factory: "aws-0" },
    repositoryObject: "cloud-native-ref", repositoryManifest: cnrManifest, repositoryManifestPath: "repository-cloud-native-ref.yaml",
    repositoryStatus: { namespace: "agent-system", generation: 4, status: {
      observedGeneration: 4, authority: "central",
      conditions: [accepted, cond("BindingsValid", "False", "BindingsRefused", "refused: deps-patch (MissingParameter)"), cond("InSync", "False", "Diverges",
        "docs: maxPerDay only in the Repository; deps-patch: bound only in the Repository")],
      classes: [{ name: "docs-links", mode: "shadow", policyGeneration: 2, accepted: true, lastDecision: "would_merge:clean", lastDecisionAt: "2026-11-10T06:40:00Z" },
        { name: "revert", mode: "live", policyGeneration: 1, accepted: true }, { name: "docs", mode: "shadow", policyGeneration: 4, accepted: true },
        { name: "deps-patch", mode: "shadow", accepted: false, reason: "MissingParameter", message: "deps-patch needs allowedFiles" }],
      merge: { autoMergesPerDay: 2, mergerLogin: "ogenki-agent-merger[bot]" }, mergeDefaulted: ["autoMergesPerDay"],
    } },
    bindings: cnrBindings,
    classes: [
      bound({ class: "docs-links", mode: "shadow", evidence: ev({ clean: 21, eligible: true, blockers: [] }), proposal: proposal("cloud-native-ref", cnrManifest, "docs-links") }),
      bound({ class: "revert", mode: "live", unproposed: "not_shadow", evidence: ev({ clean: 40, eligible: true, blockers: [] }) }),
      bound({ class: "docs", mode: "shadow", evidence: ev(), proposal: proposal("cloud-native-ref", cnrManifest, "docs") }),
      bound({ class: "deps-patch", mode: "shadow", proposal: proposal("cloud-native-ref", cnrManifest, "deps-patch") }),
    ],
  },
  promotions: [{ id: "p1", repository: "Smana/cloud-native-ref", class: "docs-links", threshold: 20, requestedBy: "Smana",
    requestedAt: "2026-11-09T10:00:00Z", state: "pr_opened", pullRequest: 512 }],
  exceptions: [{ id: "e1", repository: "Smana/cloud-native-ref", class: "docs", minimum: 12, justification: "Docs-only class, reverted twice in a year, low blast radius.",
    requestedBy: "Smana", requestedAt: "2026-11-08T09:00:00Z", state: "approved", decidedBy: "ops-lead", expiresAt: "2026-12-08T09:00:00Z" }],
  you,
};

// Repository authority: the file binds the policies, and the Repository defers to it.
const makuFile = `factory: aws-0
mergePolicies:
  - name: docs-links
    mode: shadow
    holdoutChecks: ['lint']
  - name: revert
    mode: live
`;
export const makuPage: RepositoryPage = {
  report: {
    ...base, repository: "Smana/maku", file: makuFile,
    policy: { factory: "aws-0", mergePolicies: [{ name: "docs-links", mode: "shadow", holdoutChecks: ["lint"] }, { name: "revert", mode: "live" }] },
    repositoryObject: "maku", repositoryStatus: { namespace: "agent-system", generation: 2, status: { observedGeneration: 2, authority: "repository",
      conditions: [accepted, bindingsValid, cond("RepositoryFileValid", "True", "Valid"), cond("InSync", "True", "InSync")],
      classes: [{ name: "docs-links", mode: "shadow", policyGeneration: 2, accepted: true }, { name: "revert", mode: "live", policyGeneration: 1, accepted: true }] } },
    classes: [
      { class: "docs-links", source: "repository", mode: "shadow", evidence: ev({ clean: 9, immature: 1, pending: 0 }),
        proposal: { path: ".github/agent-factory.yaml", line: 4, file: makuFile.replace("mode: shadow", "mode: live"), editURL: "https://github.com/Smana/maku/edit/main/.github/agent-factory.yaml" } },
      { class: "revert", source: "repository", mode: "live", unproposed: "not_shadow", evidence: ev({ clean: 31, eligible: true, blockers: [] }) },
    ],
  },
  promotions: [], exceptions: [], you,
};

// Not migrated: the factory's config file serves the classes; the page shows them read-only.
export const legacyPage: RepositoryPage = {
  report: {
    ...base, repository: "Smana/legacy", file: "factory: aws-0\n", policy: { factory: "aws-0" },
    classes: [
      { class: "docs-links", source: "central", mode: "shadow", unproposed: "not_in_repository_policy", evidence: ev({ clean: 9, immature: 1, pending: 0 }) },
      { class: "revert", source: "central", mode: "live", unproposed: "not_shadow", evidence: ev({ clean: 31, eligible: true, blockers: [] }) },
    ],
  },
  promotions: [], exceptions: [], you,
};

// smallPage is a bound repository with its two required bindings; sync says what its InSync
// condition reads, or that the Repository has no status yet.
const smallPage = (repository: string, sync: "in-sync" | "diverges" | "no-status"): RepositoryPage => {
  const name = repository.split("/")[1];
  const bindings = [binding("docs-links", "shadow", 2), binding("revert", "live", 1)];
  const m = manifest(name, bindings);
  const status: RepositoryStatus | undefined = sync === "no-status" ? undefined : { namespace: "agent-system", generation: 1, status: {
    observedGeneration: 1, authority: "central", conditions: [accepted, bindingsValid, sync === "in-sync" ? cond("InSync", "True", "InSync")
      : cond("InSync", "False", "Diverges", "docs-links: bound only in the Repository")] } };
  return {
    report: { ...base, repository, policy: { factory: "aws-0" }, repositoryObject: name, repositoryManifest: m,
      repositoryManifestPath: `repository-${name}.yaml`, repositoryStatus: status, bindings,
      classes: [bound({ class: "docs-links", mode: "shadow", evidence: ev({ clean: 6 }), proposal: proposal(name, m, "docs-links") }),
        bound({ class: "revert", mode: "live", unproposed: "not_shadow" })] },
    promotions: [], exceptions: [], you: { ...you, operator: false },
  };
};

export const smallPages: Record<string, RepositoryPage> = Object.fromEntries(([
  ["Smana/cloud-tools", "in-sync"], ["Smana/homelab", "in-sync"], ["Smana/blog", "diverges"],
  ["Smana/dotfiles", "in-sync"], ["Smana/cnd-website", "in-sync"], ["Smana/ogenki-charts", "no-status"],
] as const).map(([r, s]) => [r, smallPage(r, s)]));

// policy is a MergePolicy as /api/policy/policies lists it.
const policy = (name: string, generation: number, spec: PolicyReport["spec"], consumers: number, current: number): PolicyReport => ({
  name, generation, spec, manifest: "", manifestPath: `mergepolicy-${name}.yaml`,
  status: { observedGeneration: generation, consumerCount: consumers, currentCount: current },
});
export const policies: PolicyReport[] = [
  policy("docs-links", 2, { description: "Fixes a broken link in the docs, and nothing else.", author: "agents", harness: "plain", checks: ["Check the documentation links"] }, 8, 8),
  policy("revert", 1, { description: "Reverts a merge the factory made.", author: "agents", harness: "plain" }, 8, 8),
  policy("docs", 4, { description: "Small documentation changes: Markdown under docs/ and the README, never the security pages.", author: "agents", harness: "plain", requires: ["allowedFiles"] }, 1, 1),
  policy("deps-patch", 1, { description: "A dependency's patch bump.", author: "adopt", harness: "adopted-none", diffShape: "semver-patch", modifyOnly: true, requires: ["allowedFiles"] }, 1, 0),
];
