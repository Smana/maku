#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The shared ClusterRoles' move to agent-factory-crds (D2, step 7 R10), on a throwaway k3d cluster:
#   - an install from OLD_REF (default origin/main: the roles are the umbrella's) is upgraded to this
#     checkout without docs/upgrading.md's commands, and agent-factory-crds' upgrade is refused;
#   - with the commands (label, annotate to af-crds, keep), the CRD chart adopts the four roles, the
#     umbrella's upgrade does not delete them, and each keeps its UID.
# No image runs: the releases install without --wait and only their objects are read. The cluster
# and its kubeconfig are this run's own.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."
OLD_REF="${OLD_REF:-origin/main}"
NAME="maku-roles-${TAG:-$(date +%s)}"
NS=maku
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.35.0-k3s1}"
roles=(maku-factory-admin maku-mergepolicy-editor maku-viewer agent-factory-flux-reader)
step() { printf '\n==> %s\n' "$*"; }
die() { echo "FAIL: $*" >&2; exit 1; }
work="$(mktemp -d)"
export KUBECONFIG="$work/kubeconfig"
created=false
onexit() {
  local rc=$?
  $created && k3d cluster delete "$NAME" >/dev/null 2>&1
  rm -rf "$work"
  exit "$rc"
}
trap onexit EXIT

d='v0.0.0@sha256:0000'
values=(-f charts/agent-factory/values-demo.yaml --set demo.model.apiKey=k --set demo.githubApp.id=1 --set demo.githubApp.privateKey=p
  --set "maku.broker.image.tag=$d" --set "maku.runController.image.tag=$d" --set "maku.runController.images.harness.tag=$d"
  --set "maku.runController.images.bridge.tag=$d" --set "maku-factory.image.tag=$d" --set maku-factory.monitor.flux.enabled=true
  --set agentgateway.enabled=false --set maku-gateway.enabled=false --set maku.runController.model.mode=secret
  --set maku-factory.config.runs.gatewayIdentity=false --set demo.model.baseURL=http://m.example.test/v1 --set demo.model.name=m)

step "the charts at $OLD_REF"
mkdir -p "$work/old"
git archive "$OLD_REF" charts config | tar -x -C "$work/old"
helm dependency build "$work/old/charts/agent-factory" >/dev/null
helm dependency build charts/agent-factory >/dev/null

step "a k3d cluster, $NAME"
k3d cluster create "$NAME" --image "$K3S_IMAGE" --no-lb --wait --k3s-arg '--disable=traefik@server:0' \
  --kubeconfig-update-default=false --kubeconfig-switch-context=false >/dev/null
created=true
k3d kubeconfig get "$NAME" >"$KUBECONFIG"

step "install from $OLD_REF: the roles are the umbrella's"
helm install af-crds "$work/old/charts/agent-factory-crds" -n "$NS" --create-namespace >/dev/null
(cd "$work/old" && helm install af charts/agent-factory -n "$NS" "${values[@]}" --timeout 2m >/dev/null)
declare -A uid
for r in "${roles[@]}"; do
  uid[$r]="$(kubectl get clusterrole "$r" -o jsonpath='{.metadata.uid}')" || die "$OLD_REF did not create $r"
  [[ $(kubectl get clusterrole "$r" -o jsonpath='{.metadata.annotations.meta\.helm\.sh/release-name}') == af ]] || die "$r is not the umbrella's at $OLD_REF"
done

step "without the commands, agent-factory-crds' upgrade is refused"
msg="$(helm upgrade af-crds charts/agent-factory-crds -n "$NS" 2>&1)" && die "the CRD chart took the umbrella's roles without adoption"
grep -qF 'cannot be imported into the current release' <<<"$msg" || die "the refusal is not Helm's ownership error: $msg"
echo "ok   refused: $(grep -o 'ClusterRole "[^"]*"' <<<"$msg" | head -1) belongs to the umbrella"

step "docs/upgrading.md's commands, then both upgrades"
kubectl label clusterrole "${roles[@]}" app.kubernetes.io/managed-by=Helm --overwrite >/dev/null
kubectl annotate clusterrole "${roles[@]}" meta.helm.sh/release-name=af-crds meta.helm.sh/release-namespace="$NS" \
  helm.sh/resource-policy=keep --overwrite >/dev/null
helm upgrade af-crds charts/agent-factory-crds -n "$NS" >/dev/null
helm upgrade af charts/agent-factory -n "$NS" "${values[@]}" --timeout 2m >/dev/null
for r in "${roles[@]}"; do
  [[ $(kubectl get clusterrole "$r" -o jsonpath='{.metadata.uid}') == "${uid[$r]}" ]] || die "$r was replaced or deleted"
  [[ $(kubectl get clusterrole "$r" -o jsonpath='{.metadata.annotations.meta\.helm\.sh/release-name}') == af-crds ]] || die "$r is not af-crds'"
done
helm -n "$NS" get manifest af | grep -qE "name: (${roles[0]}|${roles[1]}|${roles[2]}|${roles[3]})$" && die "the umbrella still renders a shared role"
kubectl get clusterrolebinding "agent-factory-$NS-flux-reader" >/dev/null || die "no per-install flux-reader binding"
! kubectl get clusterrolebinding agent-factory-flux-reader >/dev/null 2>&1 || die "the old flux-reader binding is still there"
echo "ok   the four roles are af-crds', with their UIDs; the flux reader's binding is the install's"
echo "adopt-roles k3d OK"
