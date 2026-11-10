# Factory resources, plan 4 of 4: ai-native-ref serves the factory from resources

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** ai-native-ref holds the factory's catalogue, repositories and merge policies as
`Factory`, `Repository` and `MergePolicy` manifests, and the Helm values file keeps only plumbing.
`task check` runs `maku config validate` on both.

**Blocked on:** a Maku release that contains plans 1–3 (#422, #426 and plan 3's PR). Merging and
tagging that release are the owner's decisions. This plan is executed only after the owner has
merged and tagged it. Until then, at most a draft PR, pinned to that future version.

**Architecture:** the manifests live in `tooling/base/agent-factory/resources/`, applied by a new
child Kustomization `agent-factory-resources` that `dependsOn: agent-factory`. The CRDs arrive
with the chart (helm-controller, `crds: CreateReplace`), so CRs in the same Kustomization as the
HelmRelease would fail dry-run and block the upgrade that installs them. The child pattern already
exists (`tooling-factory-slack-app`, `observability-runlore-factory`).

**Spec:** `docs/superpowers/specs/2026-11-09-factory-resources-design.md`, D8 (migration over two
releases), D12 (live versus startup fields), D15 (the Factory's name is `config.factory`).

**Proven offline, 2026-11-10:** `maku config migrate` on ai-native-ref's `origin/main` config, with
the Flux substitutions replaced by placeholders, gives 7 manifests: Factory `aws-0`, plus a
Repository and a MergePolicy for each of `cloud-native-ref`, `ai-native-ref` and `runlore-kb`. It
also gives a 42-line residual file, down from 497 lines. `maku config validate` on the two:
`ok: 7 accepted`.

## Global Constraints

- ai-native-ref's `AGENTS.md` applies:
  - work in a worktree branched from `origin/main`, and rebase before every push;
  - `task check` is the evidence;
  - every child names `sourceRef: {kind: OCIRepository, name: ai-native-ref}`;
  - conventional commits with no trailers;
  - `pre-commit run --all-files` before pushing.
- The Factory's `metadata.name` and every Repository's `spec.factoryRef` are `${cluster_name}`, substituted by Flux `postBuild`. Never hardcode `aws-0`.
- `runlore.repository` must be set explicitly when `runlore.listen` is set. With resources, `Primary()` moves as repositories are added or removed (plan 2's final review).
- The new directory is a gate path, like the rest of `tooling/base/agent-factory/`: add it to `.policy.yml`'s protected paths, so no agent role can write the factory's policy.
- The child Kustomization uses `wait: false`. A Factory's `Ready=False` (GitHub down) must not wedge Flux.

## Tasks

### Task 1: The manifests and their Kustomization
- [ ] Generate with the released `maku` binary: `maku config migrate --from <extracted config> --namespace agent-system --residual residual.yaml`. Restore `${cluster_name}` where the placeholder `aws-0` was. Split the output into `factory.yaml`, `repository-<name>.yaml` and `mergepolicy-<name>.yaml` under `tooling/base/agent-factory/resources/`, each with a header comment saying where the policy for that file used to live. Carry over the values file's comments for each class: they explain the R and M decisions, and the migration must not lose them.
- [ ] Add `tooling/base/agent-factory/resources/kustomization.yaml`, `tooling/aws-0/agent-factory/resources/kustomization.yaml` (if aws-0 overlays the base), and `clusters/aws-0-agent-platform/tooling-agent-factory-resources.yaml`:
  - `path`: the aws-0 resources dir;
  - `dependsOn: [{name: agent-factory}]`, `wait: false`;
  - `postBuild.substituteFrom` as `agent-factory` has.
- [ ] Add the new directory to `.policy.yml`. Check whether the host's `check-ai-boundary.py` and this repository's `check-sources.sh` accept the new child, and run them.

### Task 2: The values file keeps only plumbing
- [ ] Replace the `config:` block with the residual. Keep `factory: ${cluster_name}` and the startup switches (`policyManagement`, `coordination`, `hardening`, `api`, `runs`, …). Set `runlore.repository` explicitly.
- [ ] Bump the HelmRelease's image to the release that contains plans 1–3. The digest comes from the release.

### Task 3: `maku config validate` in `task check`
- [ ] Add `scripts/ci/validate-factory-config.sh`. It extracts `config:` from the values ConfigMap, replaces the Flux substitutions with placeholders, and builds the resources Kustomization with `kustomize build`, applying the same placeholders. Then it runs `maku config validate --config <residual> <manifests>`. The binary is pinned to the HelmRelease's version and verified by sha256, in the same way the repository already pins tools.
- [ ] Wire it into `task check`, and add a negative test that a class with both `live: true` and `shadow: true` fails.

### Task 4: Documentation, and the live proof
- [ ] The site page on the factory's configuration:
  - where each setting lives now;
  - which fields apply live and which need a rollout (D12);
  - `kubectl get factory,repository,mergepolicy -n agent-system`.
- [ ] Live proof, in an aws-0 session the owner runs:
  1. Flux applies the child.
  2. `kubectl get` shows Accepted on all 7.
  3. A class edit applies with no pod restart.
  4. A deliberately invalid edit shows `Accepted=False` and the last good version keeps serving.

### Release N+1 (later, not this plan)
The factory refuses the file's catalogue and `repositories` blocks at startup (D8). It is a Maku change, done once ai-native-ref has migrated.
