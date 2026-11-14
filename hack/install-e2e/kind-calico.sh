#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The kind install job (v4 step 7, PR F; D23, IN10): a production release from the wizard's files on
# kind with Calico, which enforces the NetworkPolicies, and gVisor's runsc behind the RuntimeClass
# gvisor:
#   - networkPolicies.mode kubernetes, the web exposed through agentgateway (expose.mode httproute),
#     model.mode gateway; Postgres 16 outside the chart, the e2e stub as every external service;
#   - the run-controller's preflight passes, so its network probe ran under runsc, and a pod under
#     the RuntimeClass reports gVisor's kernel;
#   - the web host, through the Gateway, is oauth2-proxy's.
# TWO_INSTALLS=1 (D2, R10) then installs a second release, team-b, in its own namespace with its own
# runs namespace and database, beside the first: no cluster-scoped name collides, both pass
# `maku preflight --strict`, and a run-labelled pod of team-b reaches no part of the first release.
set -euo pipefail
LEG=kind-calico
# shellcheck source=hack/install-e2e/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck source=hack/quickstart/versions.env
. hack/quickstart/versions.env

# KIND_MINOR (1.37, 1.36, 1.35) picks versions.env's node image; KIND_NODE names one outright.
if [[ -z ${KIND_NODE:-} ]]; then
  v="KIND_NODE_${KIND_MINOR:-1.37}"
  v="${v//./_}"
  KIND_NODE="${!v:-}"
  [[ -n $KIND_NODE ]] || die "KIND_MINOR=$KIND_MINOR: versions.env pins no kind node image for it"
fi
TWO_INSTALLS="${TWO_INSTALLS:-}"
NAME="mi-${TAG//[^a-z0-9-]/-}"
NAME="${NAME:0:30}"
CTX="kind-$NAME"
k() { "$KUBECTL" --context "$CTX" "$@"; }
h() { "$HELM" --kube-context "$CTX" "$@"; }

cluster_create() {
  ! kind get clusters 2>/dev/null | grep -qxF "$NAME" || die "cluster $NAME already exists; refusing to adopt or delete it"
  bash hack/quickstart/fetch-runsc.sh >/dev/null
  cat >"$work/kind.yaml" <<YAML
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true # Calico
  podSubnet: 192.168.0.0/16
nodes:
  - role: control-plane
    extraMounts:
      - {hostPath: $PWD/.quickstart/bin/runsc, containerPath: /usr/local/bin/runsc}
      - {hostPath: $PWD/.quickstart/bin/containerd-shim-runsc-v1, containerPath: /usr/local/bin/containerd-shim-runsc-v1}
      - {hostPath: $PWD/.quickstart/bin/gvisor-bin, containerPath: /usr/local/bin/gvisor-bin}
containerdConfigPatches:
  - |-
    [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runsc]
      runtime_type = "io.containerd.runsc.v1"
YAML
  kind create cluster --name "$NAME" --image "$KIND_NODE" --config "$work/kind.yaml" --kubeconfig "$KUBECONFIG" --wait 0s >/dev/null
  step "Calico $CALICO_VERSION, then the RuntimeClass gvisor (handler runsc)"
  curl -fsSL "https://raw.githubusercontent.com/projectcalico/calico/$CALICO_VERSION/manifests/calico.yaml" | k apply -f - >/dev/null
  k -n kube-system rollout status ds/calico-node --timeout=300s >/dev/null
  k wait --for=condition=Ready node --all --timeout=180s >/dev/null
  k apply -f - >/dev/null <<'YAML'
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata: {name: gvisor}
handler: runsc
YAML
}

cluster_load() { kind load docker-image --name "$NAME" "$1" >/dev/null; }
cluster_delete() { kind delete cluster --name "$NAME" >/dev/null; }

leg_before_umbrella() {
  k create namespace edge >/dev/null
  k apply -f - >/dev/null <<'YAML'
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: edge, namespace: edge}
spec:
  gatewayClassName: agentgateway
  listeners:
    - {name: http, protocol: HTTP, port: 8080, allowedRoutes: {namespaces: {from: All}}}
YAML
}

# run_pod NS NAME SCRIPT: a busybox pod's log, after it ends (PROBE_IMAGE; the restricted profile).
run_pod() {
  local ns=$1 name=$2 rc
  shift 2
  k -n "$ns" apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata: {name: $name}
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  ${RUNTIME_CLASS:+runtimeClassName: $RUNTIME_CLASS}
  securityContext: {runAsNonRoot: true, runAsUser: 65534, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: c
      image: $PROBE_IMAGE
      command: [sh, -c, '$*']
      resources: {requests: {cpu: 10m, memory: 16Mi}, limits: {cpu: 200m, memory: 64Mi}}
      securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}
YAML
  for _ in $(seq 90); do
    rc=$(k -n "$ns" get pod "$name" -o jsonpath='{.status.phase}') || rc=""
    [[ $rc == Succeeded || $rc == Failed ]] && break
    sleep 2
  done
  k -n "$ns" logs "$name" 2>&1 || true
  k -n "$ns" delete pod "$name" --now --wait=false >/dev/null 2>&1 || true
}

leg_checks() {
  step "the sandbox: runsc behind the RuntimeClass gvisor"
  [[ $(k get runtimeclass gvisor -o jsonpath='{.handler}') == runsc ]] || die "the RuntimeClass gvisor's handler is not runsc"
  k create namespace sandbox-probe >/dev/null
  local out
  out="$(RUNTIME_CLASS=gvisor run_pod sandbox-probe gvisor-probe 'dmesg | head -3')"
  grep -qi gvisor <<<"$out" || die "a pod under the RuntimeClass gvisor does not run on gVisor: $out"
  echo "ok: a pod under gvisor runs on gVisor's kernel ($(head -1 <<<"$out"))"

  step "the web host, through agentgateway's Gateway, is oauth2-proxy's"
  k -n edge wait --for=condition=Programmed gateway/edge --timeout=180s >/dev/null || die "the Gateway edge is not Programmed"
  out="$(run_pod e2e web-probe "wget -qO- --header 'Host: rooms.e2e.test' http://edge.edge.svc.cluster.local:8080/ping")"
  [[ $out == OK* ]] || die "rooms.e2e.test/ping through the Gateway is not oauth2-proxy's: $out"
  echo "ok: rooms.e2e.test/ping through the Gateway answers oauth2-proxy's OK"

  [[ -z $TWO_INSTALLS ]] || team_b
}

# team_b: a second release in namespace team-b (D2, R10): its own wizard answers, runs namespace and
# database; the CRD chart, the RuntimeClass and agentgateway's controller stay the first release's.
team_b() {
  step "a second install, team-b, beside the first (D2)"
  local b="$work/team-b" args out phase="" broker_ip pg_ip
  mkdir -p "$b"
  yq '.namespace = "team-b" | ."web.url" = "https://team-b.e2e.test"' "hack/install-e2e/$LEG.answers.yaml" >"$b/answers.yaml"
  sed -n '/^images.pins:/,$p' "$work/answers.yaml" >>"$b/answers.yaml"
  OUT="$b/out" NS=team-b PG_NS=external-db-b
  "$MAKU" install --print --out "$OUT" --answers "$b/answers.yaml" >/dev/null
  k create namespace team-b >/dev/null
  postgres
  secrets
  mapfile -t args < <(umbrella_args)
  h install "$RELEASE" charts/agent-factory -n team-b "${args[@]}" \
    --set maku.runs.namespace=runs-team-b --set maku-gateway.runs.namespace=runs-team-b \
    --set maku-factory.config.runs.namespace=runs-team-b --set agentgateway.enabled=false \
    --set "maku.runPodPolicy.extraImages={${PROBE_IMAGE%%[:@]*}}" --timeout 5m >/dev/null
  k apply -n team-b -f "$OUT/factory/" >/dev/null
  ready
  preflight
  # The negative control: a run-labelled pod of team-b, made by its run-controller, reaches neither
  # the first release's broker nor its database (by address: the runs namespace resolves nothing).
  broker_ip=$(k -n maku get svc room-broker -o jsonpath='{.spec.clusterIP}')
  pg_ip=$(k -n external-db get svc pg -o jsonpath='{.spec.clusterIP}')
  k -n runs-team-b create --as system:serviceaccount:team-b:run-controller -f - >/dev/null <<YAML || die "team-b's run-controller could not create the probe pod"
apiVersion: v1
kind: Pod
metadata: {name: cross-ns, labels: {agents.ogenki.io/run-id: crossns}}
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  runtimeClassName: gvisor
  securityContext: {runAsNonRoot: true, runAsUser: 65534, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: c
      image: $PROBE_IMAGE
      command: [sh, -c, 'echo started; nc -z -w 3 $broker_ip 8443 && echo reached broker; nc -z -w 3 $pg_ip 5432 && echo reached database; echo done']
      resources: {requests: {cpu: 10m, memory: 16Mi}, limits: {cpu: 200m, memory: 64Mi}}
      securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}
YAML
  for _ in $(seq 90); do
    phase=$(k -n runs-team-b get pod cross-ns -o jsonpath='{.status.phase}' 2>/dev/null) || phase=""
    [[ $phase == Succeeded || $phase == Failed ]] && break
    sleep 2
  done
  out="$(k -n runs-team-b logs cross-ns 2>&1)" || out=""
  grep -q '^done' <<<"$out" || die "team-b's cross-namespace probe did not finish (phase '$phase'): $out"
  ! grep -q '^reached' <<<"$out" || die "a run of team-b reached the first release: $out"
  OUT="$work/out" NS=maku PG_NS=external-db
  echo "ok: team-b beside the first: maku preflight --strict passes, and its runs reach neither the first broker nor its database"
}

install_e2e_main
