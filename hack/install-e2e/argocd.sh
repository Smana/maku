#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The Argo CD install job (v4 step 7, PR G; D23, D26, R2, R6, R7): kind-calico.sh's production install
# on Kubernetes 1.30 through Argo CD 2.12, the floor, from the wizard's gitops/argocd.yaml (gitops.sh
# says what the job changes in it, and what it checks).
#   - The Applications are applied wave by wave, each waited for, as an app of apps would sync them.
#   - Argo CD runs no Helm test hook, so the preflight is `maku preflight` through the release's
#     suspended CronJob (R2): lib.sh's preflight step, the one every leg runs.
#   - The registry's repository skips TLS verification: the registry is the run's own, in the cluster.
set -euo pipefail
KIND_MINOR="${KIND_MINOR:-1.30}"
TAG="${TAG:-install-argocd-$$}"
# shellcheck source=hack/install-e2e/kind-calico.sh
. "$(dirname "${BASH_SOURCE[0]}")/kind-calico.sh"
GITOPS=argocd
# shellcheck source=hack/install-e2e/gitops.sh
. hack/install-e2e/gitops.sh

APP=applications.argoproj.io
WAVE='.metadata.annotations."argocd.argoproj.io/sync-wave"'

argo() { k -n argocd "$@"; }

gitops_up() {
  step "Argo CD $ARGOCD_VERSION"
  k create namespace argocd >/dev/null
  curl -fsSL "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGOCD_VERSION/manifests/install.yaml" |
    argo apply --server-side --force-conflicts -f - >/dev/null
  for d in argocd-repo-server argocd-redis argocd-server; do
    argo rollout status "deploy/$d" --timeout=300s >/dev/null || die "Argo CD's $d is not Ready"
  done
  argo rollout status sts/argocd-application-controller --timeout=300s >/dev/null || die "Argo CD's application controller is not Ready"
  # The charts' OCI repository: the run's registry, in the cluster.
  argo apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Secret
metadata:
  name: repo-maku-charts
  labels: {argocd.argoproj.io/secret-type: repository}
stringData: {type: helm, name: maku-charts, url: $REGISTRY/charts, enableOCI: "true", insecure: "true"}
YAML
}

# app_wait APP: until Argo CD reports APP Synced and Healthy.
app_wait() {
  local s=""
  for _ in $(seq 120); do
    s=$(argo get "$APP/$1" -o jsonpath='{.status.sync.status}/{.status.health.status}/{.status.operationState.phase}' 2>/dev/null) || s=""
    [[ $s != Synced/Healthy/* || $s == */Running ]] || return 0
    sleep 5
  done
  argo get "$APP/$1" -o yaml | yq '.status | {"conditions": .conditions, "operationState": .operationState.message, "resources": [.resources[] | select(.status != "Synced" or (.health.status // "Healthy") != "Healthy")]}' >&2 || true
  die "the Application $1 is $s, not Synced/Healthy"
}

gitops_apply() {
  local f="$work/argocd.yaml" w a
  yq -e 'select(.spec.source.chart != null) | .spec.source.repoURL == "ghcr.io/smana/charts"' "$1" >/dev/null ||
    die "the wizard's chart Applications no longer name ghcr.io/smana/charts: update argocd.sh"
  REG="$REGISTRY/charts" SHA="$GIT_SHA" VALUES="$work/umbrella-values.yaml" REL="$RELEASE" yq '
    (select(.spec.source.chart != null) | .spec.source.repoURL) = strenv(REG) |
    (select(.spec.source.path == "config/agentgateway") | .spec.source.targetRevision) = strenv(SHA) |
    (select(.metadata.name == strenv(REL)) | .spec.source.helm.valuesObject) = load(strenv(VALUES))
  ' "$1" >"$f"
  for w in $(yq -N "$WAVE" "$f" | sort -nu); do
    step "Argo CD: sync wave $w"
    W="$w" yq "select($WAVE == strenv(W))" "$f" | k apply -f - >/dev/null
    for a in $(W="$w" yq -N "select($WAVE == strenv(W)) | .metadata.name" "$f"); do app_wait "$a"; done
  done
}

gitops_order() {
  local crds umbrella
  crds=$(argo get "$APP/$RELEASE-crds" -o jsonpath='{.status.history[0].deployedAt}')
  umbrella=$(argo get "$APP/$RELEASE" -o jsonpath='{.status.history[0].deployStartedAt}')
  [[ -n $crds && -n $umbrella && ! $crds > $umbrella ]] ||
    die "the CRD chart was first synced at '$crds', after the umbrella started at '$umbrella'"
}

gitops_owner() { k get crd "$1" -o jsonpath='{.metadata.labels.app\.kubernetes\.io/instance}'; }

gitops_state() {
  argo get "$APP" -o json | jq -r '.items[] | "\(.metadata.name) \(.status.sync.status) revision=\(.status.sync.revision) syncs=\(.status.history | length)"'
}

# argo_settle: until no Application has a refresh pending or a sync running.
argo_settle() {
  local busy=""
  for _ in $(seq 90); do
    busy=$(argo get "$APP" -o json | jq -r '.items[] | select(.metadata.annotations["argocd.argoproj.io/refresh"] != null or .status.operationState.phase == "Running") | .metadata.name')
    [[ -n $busy ]] || return 0
    sleep 2
  done
  die "Argo CD is still busy with: $busy"
}

gitops_reconcile() {
  local a
  for a in $(argo get "$APP" -o name); do argo annotate "$a" --overwrite argocd.argoproj.io/refresh=hard >/dev/null; done
  argo_settle
  # A self-heal or an automated sync starts within the controller's next pass.
  sleep 30
  argo_settle
}

gitops_prune() {
  local started
  started=$(argo get "$APP/$RELEASE" -o jsonpath='{.status.operationState.startedAt}')
  argo patch "$APP/$RELEASE" --type merge -p '{"operation":{"initiatedBy":{"username":"install-e2e"},"sync":{"prune":true}}}' >/dev/null
  for _ in $(seq 90); do
    [[ $(argo get "$APP/$RELEASE" -o jsonpath='{.status.operationState.startedAt}') == "$started" ]] || break
    sleep 2
  done
  argo_settle
  [[ $(argo get "$APP/$RELEASE" -o jsonpath='{.status.operationState.phase}') == Succeeded ]] || die "the pruning sync of $RELEASE did not succeed"
  app_wait "$RELEASE"
  echo "ok: a pruning sync of $RELEASE left agentgateway's CRDs"
  argo delete "$APP/agentgateway-crds" --wait --timeout=120s >/dev/null
  echo "ok: the Application agentgateway-crds is gone (no finalizer: its CRDs are not cascaded)"
}

leg_diagnostics() {
  argo get "$APP" -o json | jq -r '.items[] | "\(.metadata.name): \(.status.sync.status)/\(.status.health.status) \(.status.operationState.phase // "") \(.status.operationState.message // "") \(.status.conditions // [] | map(.message) | join("; "))"' || true
  for d in argocd-repo-server argocd-application-controller; do
    echo "--- argocd/$d"
    argo logs "$(argo get pod -l "app.kubernetes.io/name=$d" -o name | head -1)" --tail=40 || true
  done
}

install_e2e_main
