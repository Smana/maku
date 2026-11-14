#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The Flux install job (v4 step 7, PR G; D23, D26, R6, R7): kind-calico.sh's production install on
# Kubernetes 1.30 through Flux 2.4, the floor, from the wizard's gitops/flux.yaml (gitops.sh says what
# the job changes in it, and what it checks). The OCIRepositories trust the run's CA; the GitRepository
# names this commit on its branch.
set -euo pipefail
KIND_MINOR="${KIND_MINOR:-1.30}"
TAG="${TAG:-install-flux-$$}"
# shellcheck source=hack/install-e2e/kind-calico.sh
. "$(dirname "${BASH_SOURCE[0]}")/kind-calico.sh"
GITOPS=flux
# shellcheck source=hack/install-e2e/gitops.sh
. hack/install-e2e/gitops.sh

HR=helmreleases.helm.toolkit.fluxcd.io
KS=kustomizations.kustomize.toolkit.fluxcd.io

gitops_up() {
  step "Flux $FLUX_VERSION"
  curl -fsSL "https://github.com/fluxcd/flux2/releases/download/$FLUX_VERSION/install.yaml" | k apply -f - >/dev/null
  for d in source-controller kustomize-controller helm-controller; do
    k -n flux-system rollout status "deploy/$d" --timeout=300s >/dev/null || die "Flux's $d is not Ready"
  done
  k -n "$NS" create secret generic registry-ca --from-file=ca.crt="$work/ca.crt" >/dev/null
}

gitops_apply() {
  local f="$work/flux.yaml"
  yq -e 'select(.kind == "OCIRepository") | .spec.url | test("^oci://ghcr.io/smana/charts/")' "$1" >/dev/null ||
    die "the wizard's OCIRepositories no longer name oci://ghcr.io/smana/charts/: update flux.sh"
  REG="oci://$REGISTRY/charts/" SHA="$GIT_SHA" BRANCH="$GIT_BRANCH" VALUES="$work/umbrella-values.yaml" REL="$RELEASE" yq '
    (select(.kind == "OCIRepository") | .spec.url) |= sub("^oci://ghcr.io/smana/charts/", strenv(REG)) |
    (select(.kind == "OCIRepository") | .spec.certSecretRef.name) = "registry-ca" |
    (select(.kind == "GitRepository") | .spec.ref) = {"branch": strenv(BRANCH), "commit": strenv(SHA)} |
    (select(.kind == "HelmRelease" and .metadata.name == strenv(REL)) | .spec.values) = load(strenv(VALUES))
  ' "$1" >"$f"
  k apply -f "$f" >/dev/null
  k -n "$NS" wait "$HR/$RELEASE-crds" --for=condition=Ready --timeout=300s >/dev/null || die "the HelmRelease $RELEASE-crds is not Ready"
  factory_objects
  k -n "$NS" wait "$KS/agentgateway-crds" --for=condition=Ready --timeout=300s >/dev/null || die "the Kustomization agentgateway-crds is not Ready"
  k -n "$NS" wait "$HR/$RELEASE" --for=condition=Ready --timeout=600s >/dev/null || die "the HelmRelease $RELEASE is not Ready"
}

gitops_order() {
  local crds umbrella
  k -n "$NS" get "$HR/$RELEASE" -o jsonpath='{.spec.dependsOn[*].name}' | grep -qxF "$RELEASE-crds" ||
    die "the HelmRelease $RELEASE does not depend on $RELEASE-crds"
  crds=$(k -n "$NS" get "$HR/$RELEASE-crds" -o jsonpath='{.status.history[0].firstDeployed}')
  umbrella=$(k -n "$NS" get "$HR/$RELEASE" -o jsonpath='{.status.history[0].firstDeployed}')
  [[ -n $crds && -n $umbrella && ! $crds > $umbrella ]] ||
    die "the CRD chart was first deployed at '$crds', after the umbrella at '$umbrella'"
}

gitops_owner() { k get crd "$1" -o jsonpath='{.metadata.annotations.meta\.helm\.sh/release-name}'; }

gitops_state() {
  local r
  for r in "$RELEASE-crds" "$RELEASE"; do
    k -n "$NS" get "$HR/$r" -o jsonpath='{.metadata.name} revision={.status.history[0].version} digest={.status.history[0].digest}{"\n"}'
  done
  k -n "$NS" get "$KS/agentgateway-crds" -o jsonpath='{.metadata.name} applied={.status.lastAppliedRevision}{"\n"}'
}

# flux_reconcile ANNOTATIONS... OBJECT...: request a reconcile of each object (FORCE=1 forces a Helm
# upgrade too) and wait until each has handled it.
flux_reconcile() {
  local at o got
  at="$(date +%s%N)"
  for o in "$@"; do
    k -n "$NS" annotate "$o" --overwrite "reconcile.fluxcd.io/requestedAt=$at" ${FORCE:+"reconcile.fluxcd.io/forceAt=$at"} >/dev/null
  done
  for o in "$@"; do
    for _ in $(seq 90); do
      got=$(k -n "$NS" get "$o" -o jsonpath='{.status.lastHandledReconcileAt}')
      [[ $got != "$at" ]] || break
      sleep 2
    done
    [[ $got == "$at" ]] || die "$o did not handle the reconcile requested at $at"
    k -n "$NS" wait "$o" --for=condition=Ready --timeout=300s >/dev/null || die "$o is not Ready after the reconcile"
  done
}

gitops_reconcile() {
  flux_reconcile ocirepositories.source.toolkit.fluxcd.io/agent-factory-crds ocirepositories.source.toolkit.fluxcd.io/agent-factory \
    gitrepositories.source.toolkit.fluxcd.io/maku "$KS/agentgateway-crds" "$HR/$RELEASE-crds" "$HR/$RELEASE"
}

gitops_prune() {
  local before after
  before=$(k -n "$NS" get "$HR/$RELEASE" -o jsonpath='{.status.history[0].version}')
  FORCE=1 flux_reconcile "$HR/$RELEASE"
  after=$(k -n "$NS" get "$HR/$RELEASE" -o jsonpath='{.status.history[0].version}')
  [[ $after -gt $before ]] || die "the forced reconcile did not upgrade $RELEASE (revision $before, then $after)"
  echo "ok: a forced upgrade of $RELEASE (revision $before -> $after) left agentgateway's CRDs"
  k -n "$NS" delete "$KS/agentgateway-crds" --wait --timeout=120s >/dev/null
  echo "ok: the Kustomization agentgateway-crds is gone (prune: false)"
}

leg_diagnostics() {
  k get helmreleases,kustomizations,ocirepositories,gitrepositories -A -o wide || true
  for d in source-controller kustomize-controller helm-controller; do
    echo "--- flux-system/$d"
    k -n flux-system logs "deploy/$d" --tail=40 || true
  done
}

install_e2e_main
