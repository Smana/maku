#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Installs charts/agent-factory with profile=demo on a throwaway cluster and asserts it.
#   usage: install.sh k3d|kind        (task chart:install:k3d | chart:install:kind)
# Images: broker, run-controller and factory are built from this checkout and loaded; harness and
# bridge are the latest release's, by digest (no run starts here). A dummy GitHub App means the
# factory cannot pass start-up: the assertion is that it parses its config, mounts everything and
# logs the App lookup failure. The cluster is always deleted, with its own kubeconfig.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."
CLUSTER_KIND="${1:?usage: install.sh k3d|kind}"
# shellcheck source=hack/quickstart/versions.env
. hack/quickstart/versions.env
NAME="maku-chart-$CLUSTER_KIND"
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.35.0-k3s1}"
TAG=e2e
NS=maku
step() { printf '\n==> %s\n' "$*"; }
die() { echo "FAIL: $*" >&2; exit 1; }

work="$(mktemp -d)"
export KUBECONFIG="$work/kubeconfig"
dump() {
  [[ -s $KUBECONFIG ]] || return 0
  echo "--- install failed: diagnostics ---" >&2
  kubectl get pods,pvc,svc -A -o wide >&2 || true
  kubectl -n "$NS" describe pods >&2 || true
  kubectl get events -A --sort-by=.lastTimestamp >&2 || true
  for d in room-broker run-controller agent-factory; do kubectl -n "$NS" logs "deploy/$d" --all-containers --tail=80 >&2 || true; done
  kubectl -n "$NS" logs sts/rooms-db --tail=40 >&2 || true
}
delete_cluster() {
  case "$CLUSTER_KIND" in
    k3d) k3d cluster delete "$NAME" ;;
    kind) kind delete cluster --name "$NAME" ;;
  esac
}
onexit() {
  local rc=$?
  [[ $rc -eq 0 ]] || dump
  step "delete the cluster $NAME"
  delete_cluster || true
  rm -rf "$work"
  exit "$rc"
}
trap onexit EXIT

step "build the images from this checkout"
for i in maku-broker maku-run-controller maku-factory; do
  docker build -q -f "images/$i/Dockerfile" -t "ghcr.io/smana/$i:$TAG" . >/dev/null
done
rel="${MAKU_RELEASE:-$(gh api repos/Smana/maku/releases/latest -q .tag_name)}"
digest() { docker buildx imagetools inspect "ghcr.io/smana/$1:$rel" --format '{{.Manifest.Digest}}'; }
harness="$rel@$(digest maku-harness)"
bridge="$rel@$(digest maku-bridge)"
echo "harness $harness, bridge $bridge"

step "create the $CLUSTER_KIND cluster $NAME"
# A cluster that already bears the name is somebody's: refuse rather than delete it in the trap.
case "$CLUSTER_KIND" in
  k3d)
    ! k3d cluster get "$NAME" >/dev/null 2>&1 || { trap - EXIT; die "cluster $NAME exists"; }
    k3d cluster create "$NAME" --image "$K3S_IMAGE" --no-lb --wait \
      --k3s-arg '--disable=traefik@server:0' --k3s-arg '--disable=servicelb@server:0' \
      --kubeconfig-update-default=false --kubeconfig-switch-context=false
    k3d kubeconfig get "$NAME" >"$KUBECONFIG"
    for i in maku-broker maku-run-controller maku-factory; do k3d image import -c "$NAME" "ghcr.io/smana/$i:$TAG"; done ;;
  kind)
    ! kind get clusters 2>/dev/null | grep -qx "$NAME" || { trap - EXIT; die "cluster $NAME exists"; }
    kind create cluster --name "$NAME" --image "$KIND_NODE_IMAGE" --wait 120s --kubeconfig "$KUBECONFIG"
    for i in maku-broker maku-run-controller maku-factory; do kind load docker-image --name "$NAME" "ghcr.io/smana/$i:$TAG"; done ;;
esac
kubectl get nodes

step "install"
openssl genrsa -out "$work/app.pem" 2048 2>/dev/null
helm dependency build charts/agent-factory >/dev/null
values=(--set profile=demo
  --set demo.model.baseURL=http://model.invalid/v1 --set demo.model.name=stub --set demo.model.apiKey=dummy
  --set demo.githubApp.id=123456 --set-file demo.githubApp.privateKey="$work/app.pem"
  --set maku.e2e.unpinnedImages=true --set maku-factory.e2e.unpinnedImages=true
  --set maku.broker.image.tag=$TAG --set maku.runController.image.tag=$TAG --set maku-factory.image.tag=$TAG
  --set maku.runController.images.harness.tag="$harness" --set maku.runController.images.bridge.tag="$bridge"
  --set maku-factory.replicas=1)
# No --wait: the factory cannot become Ready with a dummy App; each part is awaited below.
helm install af charts/agent-factory -n "$NS" --create-namespace "${values[@]}" --timeout 5m

step "readiness"
kubectl -n "$NS" rollout status sts/rooms-db --timeout=180s
kubectl -n "$NS" rollout status deploy/room-broker --timeout=180s
kubectl -n "$NS" rollout status deploy/run-controller --timeout=180s
init=$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=room-broker -o jsonpath='{.items[0].status.initContainerStatuses[*].state.terminated.reason}')
[[ "$init" == *Completed* ]] || die "the broker's migrate init container did not complete: '$init'"
echo "ok: Postgres, broker (migrate init: $init) and run-controller are Ready"

step "the factory parses its config, mounts everything and reports the dummy App"
found=0
for _ in $(seq 60); do
  kubectl -n "$NS" logs deploy/agent-factory --all-containers >"$work/all.log" 2>&1 || true
  # the App lookup is the first thing the factory does once its config has parsed
  if grep -F "find the App's installation" "$work/all.log" >"$work/factory.log"; then found=1; break; fi
  sleep 3
done
((found)) || { cat "$work/all.log" >&2; die "the factory did not log the App lookup failure (find the App's installation)"; }
head -3 "$work/factory.log"
! grep -E '(^|[^a-z])config: ' "$work/all.log" || die "the factory logged a config error"
kubectl -n "$NS" get pods -l app.kubernetes.io/name=agent-factory
reasons=$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=agent-factory -o jsonpath='{.items[*].status.containerStatuses[*].state.waiting.reason}{.items[*].status.containerStatuses[*].lastState.terminated.reason}') || die "kubectl could not read the factory pods"
! grep -qE 'CreateContainerConfigError|ContainerCannotRun|InvalidImageName|ErrImage' <<<"$reasons" ||
  die "the factory failed before it started (a mount or the image)"

step "no Cilium, cert-manager or CNPG objects"
# By API group: a core name such as `certificates` is not cert-manager's.
res=$(kubectl api-resources -o name) || die "kubectl api-resources failed"
! grep -E '(^|\.)(cilium\.io|cert-manager\.io|cnpg\.io)$' <<<"$res" || die "the cluster has Cilium, cert-manager or CNPG APIs; the test cluster must lack them"
for r in ciliumnetworkpolicies.cilium.io certificates.cert-manager.io issuers.cert-manager.io; do
  ! kubectl -n "$NS" get "$r" >/dev/null 2>&1 || die "$r is served"
done
objs=$(kubectl get all,cm,secret -A -o name) || die "kubectl could not list the objects"
! grep -E 'ciliumnetworkpolic|cert-manager' <<<"$objs" || die "a Cilium or cert-manager object exists"
echo "ok: the cluster has none of those APIs, and the install needed none"

step "a second helm upgrade with the same values keeps the CA, the certificate and both DSNs"
snap() {
  for s in room-broker-ca room-broker-tls rooms-broker-dsn rooms-owner-dsn; do
    local d
    d=$(kubectl -n "$NS" get secret "$s" -o jsonpath='{.data}') || die "kubectl get secret $s failed"
    printf '%s %s\n' "$s" "$(sha256sum <<<"$d" | cut -d' ' -f1)"
  done
}
before="$(snap)" || exit 1
helm upgrade af charts/agent-factory -n "$NS" "${values[@]}" --timeout 5m >/dev/null
after="$(snap)" || exit 1
[[ "$before" == "$after" ]] || { echo "before:" >&2; echo "$before" >&2; echo "after:" >&2; echo "$after" >&2; die "a Secret changed across helm upgrade"; }
echo "$after"
echo "install OK on $CLUSTER_KIND"
