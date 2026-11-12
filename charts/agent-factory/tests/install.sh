#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Installs charts/agent-factory with values-demo.yaml on a throwaway cluster and asserts it.
#   usage: install.sh k3d|kind        (task chart:install:k3d | chart:install:kind)
# The CRD chart (release af-crds) installs first. ADOPT=1 instead installs the umbrella alone, creates
# a Room, adopts the CRDs into af-crds with docs/upgrading.md's commands, and asserts the Room survives.
# Images: broker, run-controller and factory are built from this checkout and loaded; harness and
# bridge are the latest release's, by digest (no run starts here). A dummy GitHub App means the
# factory cannot pass start-up: the assertion is that it parses its config, mounts everything and
# logs the App lookup failure. The cluster is always deleted, with its own kubeconfig.
# NETPOL (k3d only) picks the network leg (v4 D6):
#   none        the demo as shipped: networkPolicies.mode none (default);
#   kubernetes  mode kubernetes on k3s's kube-router, which enforces NetworkPolicy: the run-controller's
#               preflight passes within 3 minutes, its probe went through the egress proxy;
#   unenforced  mode kubernetes on a k3s started with --disable-network-policy: the preflight refuses
#               runs (`preflight failed: network:`, gauge network 0).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."
CLUSTER_KIND="${1:?usage: install.sh k3d|kind}"
# shellcheck source=hack/quickstart/versions.env
. hack/quickstart/versions.env
NETPOL="${NETPOL:-none}"
case "$NETPOL" in
  none) ;;
  kubernetes | unenforced) [[ $CLUSTER_KIND == k3d ]] || { echo "FAIL: NETPOL=$NETPOL runs on k3d only" >&2; exit 2; } ;;
  *) echo "FAIL: NETPOL=$NETPOL is not none, kubernetes or unenforced" >&2; exit 2 ;;
esac
NAME="maku-chart-$CLUSTER_KIND"
[[ $NETPOL == none ]] || NAME="$NAME-$NETPOL"
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
  kubectl -n "$NS" logs deploy/room-broker -c migrate --tail=40 >&2 || true
  # the cluster's own DNS and forwarding, apart from the chart: CoreDNS, its endpoints, the node's filter table
  kubectl -n kube-system logs deploy/coredns --tail=40 >&2 || true
  kubectl -n kube-system get endpointslice -l kubernetes.io/service-name=kube-dns -o wide >&2 || true
  [[ $CLUSTER_KIND != k3d ]] || docker exec "k3d-$NAME-server-0" sh -c 'iptables-save -t filter; iptables-save -t nat | grep -c KUBE-SVC' >&2 || true
  kubectl get events -A --sort-by=.lastTimestamp >&2 || true
  for d in room-broker run-controller agent-factory egress-proxy; do kubectl -n "$NS" logs "deploy/$d" --all-containers --tail=80 >&2 || true; done
  kubectl get networkpolicy -A >&2 || true
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
    k3s_args=(--k3s-arg '--disable=traefik@server:0' --k3s-arg '--disable=servicelb@server:0')
    # the unenforced leg: k3s without its NetworkPolicy controller, a CNI that does not enforce
    [[ $NETPOL != unenforced ]] || k3s_args+=(--k3s-arg '--disable-network-policy@server:0')
    # K3S_EXTRA_ARGS: more k3s flags, space-separated (e.g. a laptop's relaxed kubelet eviction
    # thresholds: --kubelet-arg=eviction-hard=nodefs.available<1%,imagefs.available<1%)
    for a in ${K3S_EXTRA_ARGS:-}; do k3s_args+=(--k3s-arg "$a@server:0"); done
    k3d cluster create "$NAME" --image "$K3S_IMAGE" --no-lb --wait "${k3s_args[@]}" \
      --kubeconfig-update-default=false --kubeconfig-switch-context=false
    k3d kubeconfig get "$NAME" >"$KUBECONFIG"
    for i in maku-broker maku-run-controller maku-factory; do k3d image import -c "$NAME" "ghcr.io/smana/$i:$TAG"; done ;;
  kind)
    ! kind get clusters 2>/dev/null | grep -qx "$NAME" || { trap - EXIT; die "cluster $NAME exists"; }
    kind create cluster --name "$NAME" --image "$KIND_NODE_IMAGE" --wait 120s --kubeconfig "$KUBECONFIG"
    for i in maku-broker maku-run-controller maku-factory; do kind load docker-image --name "$NAME" "ghcr.io/smana/$i:$TAG"; done ;;
esac
kubectl get nodes

step "the cluster's own DNS resolves, before the chart"
# A fault here is the cluster's or the runner's, never the chart's: say so rather than time out later.
if [[ $NETPOL == unenforced ]]; then
  # k3s's NetworkPolicy controller is what loads br_netfilter; with it disabled, a host that has not
  # loaded the module bridges a same-node Service backend's reply past the reverse NAT: DNS times out.
  bnf=$(docker exec "k3d-$NAME-server-0" cat /proc/sys/net/bridge/bridge-nf-call-iptables 2>/dev/null) || bnf=absent
  [[ $bnf == 1 ]] || die "net.bridge.bridge-nf-call-iptables is '$bnf' in the k3d node: load br_netfilter on the host (sudo modprobe br_netfilter); without k3s's NetworkPolicy controller nothing loads it, and pod DNS times out"
  echo "ok: br_netfilter is active in the node (bridge-nf-call-iptables=1)"
fi
kubectl -n kube-system rollout status deploy/coredns --timeout=120s
dnsok=0
for _ in $(seq 20); do
  kubectl -n default delete pod dns-smoke --ignore-not-found --now >/dev/null 2>&1 || true
  kubectl -n default run dns-smoke --image="$PROBE_IMAGE" --restart=Never --command -- \
    nslookup kubernetes.default.svc.cluster.local >/dev/null 2>&1 || true
  kubectl -n default wait --for=jsonpath='{.status.phase}'=Succeeded pod/dns-smoke --timeout=20s >/dev/null 2>&1 && { dnsok=1; break; }
  sleep 3
done
kubectl -n default logs dns-smoke 2>&1 | tail -4 || true
kubectl -n default delete pod dns-smoke --ignore-not-found --now >/dev/null 2>&1 || true
((dnsok)) || die "the $CLUSTER_KIND cluster's DNS does not resolve kubernetes.default from a pod, before any chart object exists: an environment fault"
echo "ok: the cluster's DNS resolves"

step "install"
openssl genrsa -out "$work/app.pem" 2048 2>/dev/null
helm dependency build charts/agent-factory >/dev/null
values=(-f charts/agent-factory/values-demo.yaml
  --set demo.model.baseURL=http://model.invalid/v1 --set demo.model.name=stub --set demo.model.apiKey=dummy
  --set demo.githubApp.id=123456 --set-file demo.githubApp.privateKey="$work/app.pem"
  --set maku.e2e.unpinnedImages=true --set maku-factory.e2e.unpinnedImages=true
  --set maku.broker.image.tag=$TAG --set maku.runController.image.tag=$TAG --set maku-factory.image.tag=$TAG
  --set maku.runController.images.harness.tag="$harness" --set maku.runController.images.bridge.tag="$bridge"
  --set maku-factory.replicas=1
  # the NetworkPolicy probe below runs PROBE_IMAGE in agents: the run-pod policy must allow it
  --set "maku.runPodPolicy.extraImages={${PROBE_IMAGE%%[:@]*}}")
[[ $NETPOL == none ]] || values+=(--set maku.networkPolicies.mode=kubernetes)
# Not --wait: Helm's status poller once held a fresh CI cluster's rooms CRD "InProgress" for 2m; Established is what matters.
install_crds() {
  helm install af-crds charts/agent-factory-crds -n "$NS" --create-namespace --timeout 2m &&
    kubectl wait --for=condition=Established crd -l app.kubernetes.io/part-of=agent-factory --timeout=120s
}
# No --wait: the factory cannot become Ready with a dummy App; each part is awaited below.
install_umbrella() { helm install af charts/agent-factory -n "$NS" --create-namespace "${values[@]}" --timeout 5m; }
crds=(agentruns factories mergepolicies repositories rooms tasks)
if [[ ${ADOPT:-} == 1 ]]; then
  # PR2: an install from before the CRD chart. The subcharts' crds/ create the CRDs, a Room is
  # created, the CRDs are handed to the af-crds release (docs/upgrading.md), and the CRD chart installs.
  step "ADOPT: the umbrella alone, its subcharts' crds/ create the CRDs"
  install_umbrella
  for c in "${crds[@]}"; do kubectl wait --for=condition=Established "crd/$c.maku.ogenki.io" --timeout=60s; done
  # a Room is named with a C2 id: 8 characters of [a-z2-7]
  kubectl apply -f - <<EOF
apiVersion: maku.ogenki.io/v1alpha1
kind: Room
metadata: {name: adoptab2, namespace: $NS}
spec: {owner: "human:local", driver: "human:local", dataClass: public, repository: e2e/unused}
EOF
  uid=$(kubectl -n "$NS" get room adoptab2 -o jsonpath='{.metadata.uid}')
  msg=$(install_crds 2>&1) && die "agent-factory-crds installed over unowned CRDs; Helm must refuse them"
  grep -qF 'cannot be imported into the current release' <<<"$msg" || die "the refusal is not the import error: $msg"
  echo "ok: Helm refuses the CRDs before adoption"
  step "ADOPT: hand the CRDs to the af-crds release, then install the CRD chart"
  mapfile -t owned < <(kubectl get crd -o name | grep '\.maku\.ogenki\.io$')
  [[ ${#owned[@]} == "${#crds[@]}" ]] || die "expected ${#crds[@]} maku.ogenki.io CRDs, found ${#owned[@]}"
  kubectl label "${owned[@]}" app.kubernetes.io/managed-by=Helm --overwrite
  kubectl annotate "${owned[@]}" meta.helm.sh/release-name=af-crds meta.helm.sh/release-namespace="$NS" --overwrite
  install_crds
  [[ $(kubectl -n "$NS" get room adoptab2 -o jsonpath='{.metadata.uid}') == "$uid" ]] || die "the Room created before adoption is gone or replaced"
  for c in "${crds[@]}"; do
    [[ $(kubectl get "crd/$c.maku.ogenki.io" -o jsonpath='{.metadata.annotations.helm\.sh/resource-policy}') == keep ]] || die "$c.maku.ogenki.io lacks the keep annotation after adoption"
  done
  helm -n "$NS" get manifest af-crds | grep -c '^kind: CustomResourceDefinition$' | grep -qx 6 || die "the af-crds release does not own six CRDs"
  echo "ok: the CRDs are adopted by af-crds, kept, and the Room survived (uid $uid)"
else
  step "the CRD chart first, then the umbrella"
  install_crds
  install_umbrella
fi

if [[ $NETPOL != none ]]; then
  # The preflight checks the git Secrets before the network: the dummy App never fills them, so the
  # network legs fill both with a dummy token right away, before the run-controller's first tick.
  for s in agent-git agent-git-read; do
    kubectl -n agents create secret generic "$s" --from-literal=token=dummy-not-a-token >/dev/null
  done
fi

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

step "the run token writer may fill both run Secrets, and the run-controller may read both"
# The dummy App never mints, so neither Secret is written: the Roles that let it are what is proved.
sa=system:serviceaccount:$NS
for s in agent-git agent-git-read; do
  kubectl auth can-i update "secret/$s" -n agents --as="$sa:agent-factory" >/dev/null || die "the factory cannot update $s"
  kubectl auth can-i get "secret/$s" -n agents --as="$sa:run-controller" >/dev/null || die "the run-controller cannot read $s"
done
if kubectl -n agents get secret agent-git-read >/dev/null 2>&1; then
  echo "ok: agent-git-read exists"
else
  echo "ok: agent-git-read is not written: the factory logged the dummy App's failure above"
fi

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

step "plain NetworkPolicies: a run-labelled pod cannot reach rooms-db, the broker's pods can"
kubectl -n "$NS" get networkpolicy rooms-db agent-factory-api
# probe NAMESPACE LABELS: a short-lived pod connects to rooms-db:5432. Returns 0 when nc connected and
# 1 when nc itself failed (refused or timed out); a kubectl or image-pull failure, where the pod never
# printed its marker, dies instead of passing as "cannot reach".
# The pod passes the run-pod policy (charts/maku/templates/run-pod-policy.yaml): restricted, with
# limits (the runs quota needs them too), its image allow-listed through runPodPolicy.extraImages, and
# created in agents as the run-controller (impersonated): only it creates pods there.
probe_pod() { # probe_pod NAME LABELS
  local labels
  labels=$(tr ',' '\n' <<<"$2" | sed -E 's/^([^=]+)=(.*)$/"\1": "\2"/' | paste -sd, -)
  cat <<JSON
{"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "$1", "labels": {$labels}},
 "spec": {"restartPolicy": "Never", "automountServiceAccountToken": false,
  "securityContext": {"runAsNonRoot": true, "runAsUser": 65534, "seccompProfile": {"type": "RuntimeDefault"}},
  "containers": [{"name": "probe", "image": "$PROBE_IMAGE",
   "command": ["sh", "-c", "echo np-probe-started; nc -z -w 5 rooms-db.$NS.svc 5432"],
   "resources": {"requests": {"cpu": "10m", "memory": "16Mi"}, "limits": {"cpu": "100m", "memory": "32Mi"}},
   "securityContext": {"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": {"drop": ["ALL"]}}}]}}
JSON
}
probe() {
  local pod="np-probe-$RANDOM" out phase="" rc=0 _ as=()
  [[ $1 != agents ]] || as=(--as "system:serviceaccount:$NS:run-controller")
  probe_pod "$pod" "$2" | kubectl -n "$1" create "${as[@]}" -f - >/dev/null || die "kubectl could not create the probe pod in $1"
  for _ in $(seq 60); do
    phase=$(kubectl -n "$1" get pod "$pod" -o jsonpath='{.status.phase}') || phase=""
    [[ $phase == Succeeded || $phase == Failed ]] && break
    sleep 2
  done
  out=$(kubectl -n "$1" logs "$pod" 2>&1) || out=""
  kubectl -n "$1" delete pod "$pod" --now --wait=false >/dev/null 2>&1 || true
  grep -qF np-probe-started <<<"$out" || die "the probe pod in $1 did not run (phase '$phase'; image pull or kubectl failure): $out"
  [[ $phase == Succeeded ]] || rc=1
  return "$rc"
}
kubectl get ns agents >/dev/null || die "the runs namespace agents does not exist"
# control: the broker's label from the release namespace connects (so a failure below is the policy, not DNS or the image)
# (the policy engine syncs a new pod's labels a moment after it starts: retry the control)
ctl=0
for _ in 1 2 3 4 5; do
  probe "$NS" app.kubernetes.io/name=room-broker && { ctl=1; break; }
  sleep 3
done
((ctl)) || die "the control pod labelled like the broker cannot reach rooms-db:5432"
echo "ok: a broker-labelled pod reaches rooms-db:5432"
# a run in the runs namespace, labelled as the run controller labels it
if probe agents agents.ogenki.io/run-id=np-probe,agents.ogenki.io/role=implementer,app.kubernetes.io/managed-by=run-controller; then
  case "$CLUSTER_KIND/$NETPOL" in
    # the unenforced leg's point: nothing enforces, and the preflight must say so (below)
    */unenforced) echo "ok: unenforced: a run-labelled pod reaches rooms-db:5432, as expected without enforcement" ;;
    # kindnet enforces NetworkPolicy from kind v0.24; if this node does not, say so rather than fail
    kind/*) echo "NOTE: kind did not enforce the NetworkPolicy here: enforcement is asserted on k3d only" ;;
    *) die "a run-labelled pod reached rooms-db:5432 although the policy admits only the broker" ;;
  esac
else
  echo "ok: a run-labelled pod in agents cannot reach rooms-db:5432 ($CLUSTER_KIND enforces NetworkPolicy)"
fi
kubectl get pods -A -o name | grep -F np-probe- && die "a probe pod was left behind"

if [[ $NETPOL != none ]]; then
  step "networkPolicies.mode kubernetes ($NETPOL): the egress proxy, the policies and the run-controller's network preflight"
  kubectl -n "$NS" rollout status deploy/egress-proxy --timeout=180s
  kubectl -n agents get networkpolicy default-deny >/dev/null || die "no default-deny NetworkPolicy in agents"
  for np in room-broker run-controller egress-proxy; do kubectl -n "$NS" get networkpolicy "$np" >/dev/null || die "no NetworkPolicy $np"; done
  # The first tick runs once the Secrets above exist; a probe pod takes up to a minute (pull, start, its
  # two denied attempts 3 s apart), so 3 minutes is the bound.
  want="preflight passed"
  [[ $NETPOL == kubernetes ]] || want="preflight failed: network:"
  found=0
  for _ in $(seq 90); do
    kubectl -n "$NS" logs deploy/run-controller >"$work/rc.log" 2>&1 || true
    if grep -qF "$want" "$work/rc.log"; then found=1; break; fi
    sleep 2
  done
  ((found)) || { tail -40 "$work/rc.log" >&2; die "the run-controller did not log '$want' within 3 minutes"; }
  grep -F "$want" "$work/rc.log" | head -1
  if [[ $NETPOL == kubernetes ]]; then
    # every replica: deploy/egress-proxy would read one of the two, and the probe's CONNECT may be on the other
    kubectl -n "$NS" logs -l app.kubernetes.io/name=egress-proxy --all-containers --prefix --tail=-1 >"$work/proxy.log" 2>&1 || true
    grep -qF "allow run=preflight github.com:443" "$work/proxy.log" ||
      { cat "$work/proxy.log" >&2; die "the egress proxy did not log the probe's CONNECT (allow run=preflight github.com:443)"; }
    echo "ok: the preflight passed, and its probe went through the egress proxy"
  else
    grep -F "preflight failed: network:" "$work/rc.log" | grep -qF "the CNI does not enforce NetworkPolicy" ||
      die "the network refusal does not say the CNI does not enforce NetworkPolicy"
    kubectl -n "$NS" port-forward deploy/run-controller 19090:9090 >/dev/null 2>&1 &
    pf=$!
    gauge=""
    for _ in $(seq 20); do
      gauge=$(curl -fsS http://127.0.0.1:19090/metrics 2>/dev/null | grep -F 'maku_run_controller_preflight_passed{check="network"}') && break
      sleep 1
    done
    kill "$pf" 2>/dev/null || true
    [[ $gauge == 'maku_run_controller_preflight_passed{check="network"} 0' ]] || die "the gauge is not network 0: '$gauge'"
    echo "ok: unenforced: the preflight refuses runs, and the gauge says $gauge"
  fi
  kubectl -n agents get pods -l agents.ogenki.io/run-id=preflight -o name | grep -q . && {
    sleep 10
    kubectl -n agents get pods -l agents.ogenki.io/run-id=preflight -o name | grep -q . && die "a probe pod was left behind"
  }
  true
fi

step "the runs namespace: its ResourceQuota, and the run-pod policy refuses a pod nobody but the run-controller made"
kubectl -n agents get resourcequota runs >/dev/null || die "the ResourceQuota runs is missing from agents"
kubectl get validatingadmissionpolicy "maku-$NS-run-pods" >/dev/null || die "the run-pod policy is missing"
refused=$(probe_pod np-refused app.kubernetes.io/name=np-refused | kubectl -n agents create -f - 2>&1) &&
  die "an admin's pod in agents was admitted: $refused"
grep -qF "only the run-controller" <<<"$refused" || die "an admin's pod in agents was refused, but not by the run-pod policy: $refused"
echo "ok: the quota is present and the run-pod policy refuses an admin's pod in agents"
# Not asserted here: the factory's ops port (9090, probes) stays open. The factory crash-loops on the
# dummy GitHub App, so nothing serves 9090 and a live check could not pass; render.sh asserts the rule.

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
if [[ ${ADOPT:-} == 1 ]]; then
  [[ $(kubectl -n "$NS" get room adoptab2 -o jsonpath='{.metadata.uid}') == "$uid" ]] || die "the adopted Room did not survive the umbrella's upgrade"
  echo "ok: the adopted Room survived the umbrella's upgrade"
fi
echo "install OK on $CLUSTER_KIND${ADOPT:+ (ADOPT)}"
