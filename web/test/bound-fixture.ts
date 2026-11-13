// SPDX-License-Identifier: Apache-2.0

// A repository served from its Repository's bindings (B3, B16), shared by the bound page's tests.

import type { Evidence, RepositoryPage } from "../src/settings";

export const ev = (over: Partial<Evidence> = {}): Evidence => ({
  shadow: { mergedUnchanged: 32, changed: 0, reverted: 0, autoMerged: 0, drill: 1, reviewers: [],
    byGeneration: [{ generation: 4, mergedUnchanged: 3, changed: 0 }, { generation: 3, mergedUnchanged: 17, changed: 1 }, { before: true, mergedUnchanged: 12, changed: 0 }] },
  clean: 32, bad: 0, immature: 0, pending: 0, bound: 0.09, budget: 0.1, widen: false,
  ladder: [{ merges: 29, maxBad: 0, reached: true }], drill: "passed", minimum: 20, eligible: true, ...over,
});

export const MANIFEST = `apiVersion: maku.ogenki.io/v1alpha1
kind: Repository
metadata:
  name: ai-native-ref
  namespace: agent-system
spec:
  factoryRef: aws-0
  mergePolicies:
  - holdoutChecks:
    - lint
    mode: shadow
    name: docs-links
  - allowedFiles:
    - ^docs/
    mode: shadow
    name: dead-links
  repository: Smana/ai-native-ref
`;

// boundPage is ai-native-ref served from its Repository's bindings: dead-links refused at
// generation 4 (MissingParameter) and still serving generation 3, docs-links accepted.
export const boundPage = (): RepositoryPage => ({
  report: {
    repository: "Smana/ai-native-ref", minimum: 20, readAt: "2026-11-08T00:00:00Z", evidenceAt: "2026-11-08T00:00:00Z", factory: "aws-0", defaultBranch: "main",
    harnessProfiles: [], guardrails: {},
    repositoryObject: "ai-native-ref", repositoryManifest: MANIFEST, repositoryManifestPath: "repository-ai-native-ref.yaml",
    repositoryStatus: { namespace: "agent-system", generation: 7, status: { observedGeneration: 7, authority: "central",
      conditions: [{ type: "Accepted", status: "True" }, { type: "BindingsValid", status: "False", reason: "BindingsRefused", message: "refused: dead-links (MissingParameter)" }],
      classes: [{ name: "dead-links", mode: "shadow", policyGeneration: 3, accepted: false, reason: "MissingParameter", message: "monitor.flux needs Repository.spec.merge.fluxSource" },
        { name: "docs-links", mode: "shadow", policyGeneration: 1, accepted: true }],
      merge: { autoMergesPerDay: 2, mergerLogin: "ogenki-agent-merger[bot]" }, mergeDefaulted: ["autoMergesPerDay"] } },
    bindings: [
      { name: "docs-links", mode: "shadow", policyGeneration: 1, parameters: { name: "docs-links", mode: "shadow", holdoutChecks: ["lint"] }, accepted: true },
      { name: "dead-links", mode: "shadow", policyGeneration: 3, parameters: { name: "dead-links", mode: "shadow", allowedFiles: ["^docs/"] }, accepted: false, reason: "MissingParameter" },
    ],
    classes: [
      { class: "dead-links", source: "central", mode: "shadow", evidence: ev(),
        proposal: { path: "repository-ai-native-ref.yaml", line: 15, file: MANIFEST.replace("mode: shadow\n    name: dead-links", "mode: live\n    name: dead-links"), editURL: "" } },
      { class: "docs-links", source: "central", mode: "shadow", evidence: ev({ eligible: false, clean: 3, blockers: ["below_minimum"], shadow: { ...ev().shadow, byGeneration: [] } }) },
    ],
  },
  promotions: [], exceptions: [], you: { login: "Smana", maintainer: true, operator: false },
});

