#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The k3s install job (v4 step 7, PR F; D23, IN10, R11: k3d is k3s): a production release from the
# wizard's files on k3s's own network and Ingress:
#   - k3s's flannel with kube-router's NetworkPolicy controller (networkPolicies.mode kubernetes);
#   - its bundled Traefik serves the web host (expose.mode ingress) to rooms-web-proxy, and nothing
#     reaches the broker around it;
#   - Postgres 16 outside the chart (lib.sh), the e2e stub as every external service.
# sandbox.runtimeClassName gvisor names a test-only RuntimeClass whose handler is runc: this leg proves
# the network, Ingress and database seams, not the sandbox. kind-calico.sh proves the sandbox.
set -euo pipefail
LEG=k3s
# shellcheck source=hack/install-e2e/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

NAME="maku-$TAG"
CTX="k3d-$NAME"
KUBECTL_ARGS=(--context "$CTX")
k() { "$KUBECTL" "${KUBECTL_ARGS[@]}" "$@"; }
h() { "$HELM" --kube-context "$CTX" "$@"; }

cluster_create() {
  ! k3d cluster get "$NAME" >/dev/null 2>&1 || die "cluster $NAME already exists; refusing to adopt or delete it"
  # anonymous-auth=true: the discovery binding (overlay.yaml) admits an unauthenticated JWKS read only
  # once the apiserver lets the request reach authorization (docs/gateway.md, as install.sh's k3d legs).
  k3d cluster create "$NAME" --image "$K3S_IMAGE" --no-lb --wait --k3s-arg '--disable=servicelb@server:0' \
    --k3s-arg '--kube-apiserver-arg=anonymous-auth=true@server:0' \
    --kubeconfig-update-default=false --kubeconfig-switch-context=false >/dev/null
  k3d kubeconfig get "$NAME" >"$KUBECONFIG"
}

# cluster_load IMAGE: k3d can report success when the node did not ingest the tarball; the image
# counts once containerd lists it.
cluster_load() {
  for _ in 1 2 3; do
    k3d image import -c "$NAME" "$1" >/dev/null
    docker exec "k3d-$NAME-server-0" ctr -n k8s.io images ls -q | grep -qxF "$1" && return
    sleep 2
  done
  die "k3d image import: $1 never reached the node"
}

cluster_delete() { k3d cluster delete "$NAME" >/dev/null; }

leg_before_umbrella() {
  # Test-only: the RuntimeClass production names, on runc (no run starts; the runtimeclass check
  # reads the class, not its handler).
  k apply -f - >/dev/null <<'YAML'
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata: {name: gvisor}
handler: runc
YAML
  k -n kube-system rollout status deploy/traefik --timeout=180s >/dev/null
}

leg_checks() {
  step "Traefik serves the web host to oauth2-proxy, and nothing reaches the broker around it"
  k -n "$NS" get ingress rooms-web -o jsonpath='{.spec.ingressClassName} {.spec.rules[0].host}' | grep -qx 'traefik rooms.e2e.test' ||
    die "the rooms-web Ingress is not traefik's for rooms.e2e.test"
  local out
  out="$(curl_in "http://traefik.kube-system.svc.cluster.local/ping" rooms.e2e.test)"
  [[ $out == "200 OK" ]] || die "Traefik's rooms.e2e.test/ping is not oauth2-proxy's: $out"
  out="$(curl_in "http://traefik.kube-system.svc.cluster.local/.well-known/roomctl" rooms.e2e.test)"
  [[ $out == "200 "*'"issuer":"https://e2e-stub.e2e.svc.cluster.local"'* ]] || die "/.well-known/roomctl does not reach the broker through Traefik: $out"
  out="$(curl_in "http://traefik.kube-system.svc.cluster.local/api/rooms" rooms.e2e.test)"
  [[ $out == 302* || $out == 401* ]] || die "/api/rooms without a session answered $out: oauth2-proxy must refuse it"
  out="$(curl_in "https://room-broker.$NS.svc.cluster.local:8080/" "")"
  [[ $out == 000* ]] || die "the broker's :8080 answered a pod that is not the front door: $out"
  echo "ok: Traefik -> rooms-web-proxy -> broker; the broker's :8080 refuses any other pod"
}

# curl_in URL HOST: "<status> <body>" of a GET from a pod in namespace e2e (000 when nothing answers).
curl_in() {
  k -n e2e run "curl-$RANDOM" --rm -i --restart=Never --quiet --image "$CURL_IMAGE" \
    --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"c","image":"'"$CURL_IMAGE"'","args":'"$(jq -cn --arg u "$1" --arg h "$2" '["-sk","-m","10","-o","/dev/stdout","-w"," %{http_code}"] + (if $h == "" then [] else ["-H", "Host: " + $h] end) + [$u]')"',"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}' \
    2>/dev/null | awk '{out = out (NR > 1 ? "\n" : "") $0; last = $0} END {n = split(last, f, " "); sub(/ ?[0-9]+$/, "", out); print f[n] " " out}'
}

install_e2e_main
