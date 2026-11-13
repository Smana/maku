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
# GATEWAY=1 (k3d only; task chart:gateway:k3d) turns the model gateway on (plan v4 step 4, Task 5):
# Gateway API's standard CRDs, agentgateway's CRDs (config/agentgateway, server-side) and controller (the umbrella),
# maku-gateway with source apiserver, and the probe's echo provider as a `custom` provider over http.
# A run-like pod's token for maku-gateway.public.tier-light gets 200 on its alias, 403 on
# tier-frontier's, and no token gets 401; the proxy's NetworkPolicy is enforced by k3s.
# The leg layers values-demo-gateway.yaml: the run-controller runs in model.mode gateway, agents holds no
# provider key, and a run the leg creates reaches its model through the gateway as its own ServiceAccount.
# METERING=1 (with GATEWAY=1; task chart:metering:k3d) adds the metering proof (plan v4 step 4, Task 17):
# tests/metering-values.yaml gives the factory a meter on a test-only VictoriaMetrics (tests/metrics-stub.yaml),
# a PromQL throttle, runs.gatewayIdentity and a split on review, and the config loads. The run's tokens
# reach VictoriaMetrics under agentgateway's own names, labelled with its subject, and the meter's
# parser prices that reply above zero (internal/factory/meter TestRecordedAgentgatewayReply).
# EXTERNAL_DB=1 (k3d only; task chart:install:k3d:external-db) brings its own Postgres (v4 D9): a plain
# postgres:16 in namespace external-db, outside the chart, bootstrapped with docs/database.md's SQL block
# as written, its three DSN Secrets created by hand, and postgres.enabled=false. The broker migrates it
# from empty and serves, and every leg's manual run of the retention CronJob purges as rooms_retention.
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
GATEWAY="${GATEWAY:-}"
[[ -z $GATEWAY || $CLUSTER_KIND == k3d ]] || { echo "FAIL: GATEWAY=1 runs on k3d only" >&2; exit 2; }
METERING="${METERING:-}"
[[ -z $METERING || -n $GATEWAY ]] || { echo "FAIL: METERING=1 needs GATEWAY=1" >&2; exit 2; }
EXTERNAL_DB="${EXTERNAL_DB:-}"
[[ -z $EXTERNAL_DB || ($CLUSTER_KIND == k3d && $NETPOL == none && -z $GATEWAY && -z ${ADOPT:-}) ]] ||
  { echo "FAIL: EXTERNAL_DB=1 runs on k3d alone, without NETPOL, GATEWAY or ADOPT" >&2; exit 2; }
# The external database's image: postgres:16, the support floor (D26), by digest.
EXTERNAL_PG_IMAGE=docker.io/library/postgres:16-alpine@sha256:f377d590e5474681d4d7838635031a2b50647923b7abfa803ce9b5ff35d5c663
# The probe's pins (charts/maku-gateway/tests/probe): Gateway API's floor, the echo provider's image.
GATEWAY_API=v1.2.1
ECHO_IMAGE=docker.io/library/python:3.15-alpine@sha256:ff4d3c9b1a48f646eedd93182aaea385c7a5452f4c8a8cd7cef686babacf4928
NAME="maku-chart-$CLUSTER_KIND"
[[ $NETPOL == none ]] || NAME="$NAME-$NETPOL"
[[ -z $GATEWAY ]] || NAME="$NAME-gateway"
[[ -z $METERING ]] || NAME="$NAME-metering"
[[ -z $EXTERNAL_DB ]] || NAME="$NAME-external-db"
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.35.0-k3s1}"
# TAG: the local images' tag; parallel runs on one Docker host set their own, or one build replaces the other's.
TAG="${TAG:-e2e}"
NS=maku
step() { printf '\n==> %s\n' "$*"; }
die() { echo "FAIL: $*" >&2; exit 1; }
# wait_established CRD...: just after a server-side apply, `kubectl wait` can fail before the CRDs
# are served (once: an accessor error), so it is retried a few times before the last, failing try.
wait_established() {
  for _ in 1 2 3 4; do
    kubectl wait --for=condition=Established "$@" --timeout=120s >/dev/null && return
    sleep 3
  done
  kubectl wait --for=condition=Established "$@" --timeout=120s >/dev/null
}

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
  for d in room-broker run-controller agent-factory egress-proxy model-gateway af-agentgateway; do kubectl -n "$NS" logs "deploy/$d" --all-containers --tail=80 >&2 || true; done
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
    # the gateway leg: agentgateway's controller fetches the apiserver's JWKS anonymously (probe P1),
    # and k3s turns anonymous requests off, where kubeadm and kind leave them on
    [[ -z $GATEWAY ]] || k3s_args+=(--k3s-arg '--kube-apiserver-arg=anonymous-auth=true@server:0')
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
  --set demo.githubApp.id=123456 --set-file demo.githubApp.privateKey="$work/app.pem"
  --set maku.e2e.unpinnedImages=true --set maku-factory.e2e.unpinnedImages=true
  --set maku.broker.image.tag=$TAG --set maku.runController.image.tag=$TAG --set maku-factory.image.tag=$TAG
  --set maku.runController.images.harness.tag="$harness" --set maku.runController.images.bridge.tag="$bridge"
  --set maku-factory.replicas=1
  # the NetworkPolicy probe below runs PROBE_IMAGE in agents: the run-pod policy must allow it
  --set "maku.runPodPolicy.extraImages={${PROBE_IMAGE%%[:@]*}}")
[[ $NETPOL == none ]] || values+=(--set maku.networkPolicies.mode=kubernetes)
[[ -z $EXTERNAL_DB ]] || values+=(--set postgres.enabled=false)
if [[ -z $GATEWAY ]]; then
  values+=(--set demo.model.baseURL=https://model.invalid/v1 --set demo.model.name=stub --set demo.model.apiKey=dummy)
else
  issuer="$(kubectl get --raw /.well-known/openid-configuration | jq -r .issuer)"
  # The gateway demo (values-demo-gateway.yaml) with the probe's echo as its provider: the umbrella
  # renders the provider's key Secret demo-model from demo.model.apiKey ("Bearer not-a-key", 16
  # characters on the wire), and the run-controller runs in model.mode gateway.
  cat >"$work/gateway.yaml" <<EOF
maku:
  runController:
    model:
      gateway:
        aliases: {public: [agent-default, tier-frontier, tier-light], internal: []}
maku-gateway:
  identity:
    issuer: $issuer
  providers:
    demo:
      host: echo-llm.models.svc.cluster.local
      port: 8080
      path: /provider/v1/chat/completions
      tls: false
      egress:
        peers: [{namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: models}}}]
  aliases:
    public:
      agent-default: {provider: demo, model: echo-default}
      tier-light: {provider: demo, model: echo-light}
      tier-frontier: {provider: demo, model: echo-frontier}
  modelCatalog:
    prices:
      echo-light: {input: 1.00, cacheRead: 0.10, output: 2.00}
EOF
  # the gateway's caller below runs the echo image in agents: the run-pod policy must allow it
  values+=(-f charts/agent-factory/values-demo-gateway.yaml -f "$work/gateway.yaml" --set demo.model.apiKey=not-a-key
    --set "maku.runPodPolicy.extraImages={${PROBE_IMAGE%%[:@]*},${ECHO_IMAGE%%[:@]*}}")
  [[ -z $METERING ]] || values+=(-f charts/agent-factory/tests/metering-values.yaml)
fi
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
  if [[ -n $GATEWAY ]]; then
    step "the gateway's prerequisites: Gateway API $GATEWAY_API, agentgateway's CRDs and the echo provider"
    curl -fsSL "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GATEWAY_API/standard-install.yaml" |
      kubectl apply --server-side -f - >/dev/null
    kubectl wait --for=condition=Established crd/gateways.gateway.networking.k8s.io crd/httproutes.gateway.networking.k8s.io --timeout=120s >/dev/null
    # agentgateway's CRDs, vendored and applied apart from any Helm release (too big for one, docs/gateway.md)
    kubectl apply --server-side -f config/agentgateway >/dev/null
    wait_established crd/agentgatewaybackends.agentgateway.dev crd/agentgatewaypolicies.agentgateway.dev \
      crd/agentgatewayparameters.agentgateway.dev crd/agentgatewaymodels.agentgateway.dev
    kubectl create namespace models >/dev/null
    kubectl -n models create configmap echo-llm --from-file=charts/maku-gateway/tests/probe/echo-llm.py >/dev/null
    kubectl -n models apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata: {name: echo-llm}
spec:
  replicas: 1
  selector: {matchLabels: {app.kubernetes.io/name: echo-llm}}
  template:
    metadata: {labels: {app.kubernetes.io/name: echo-llm}}
    spec:
      automountServiceAccountToken: false
      securityContext: {runAsNonRoot: true, runAsUser: 65534, runAsGroup: 65534, seccompProfile: {type: RuntimeDefault}}
      containers:
        - name: echo
          image: $ECHO_IMAGE
          command: [python3, -u, /app/echo-llm.py, "8080"]
          ports: [{name: http, containerPort: 8080}]
          readinessProbe: {httpGet: {path: /healthz, port: http}}
          resources: {requests: {cpu: 10m, memory: 32Mi}, limits: {cpu: 200m, memory: 64Mi}}
          securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}
          volumeMounts: [{name: app, mountPath: /app, readOnly: true}]
      volumes: [{name: app, configMap: {name: echo-llm}}]
---
apiVersion: v1
kind: Service
metadata: {name: echo-llm}
spec:
  selector: {app.kubernetes.io/name: echo-llm}
  ports: [{name: http, port: 8080, targetPort: http}]
EOF
    if [[ -n $METERING ]]; then
      step "the metering stub: a single-binary VictoriaMetrics scraping the gateway's pods"
      kubectl apply -f charts/agent-factory/tests/metrics-stub.yaml >/dev/null
    fi
  fi
  if [[ -n $EXTERNAL_DB ]]; then
    step "EXTERNAL_DB: a plain postgres:16 outside the chart, bootstrapped with docs/database.md"
    kubectl create namespace external-db >/dev/null
    kubectl -n external-db apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata: {name: pg}
spec:
  replicas: 1
  selector: {matchLabels: {app.kubernetes.io/name: pg}}
  template:
    metadata: {labels: {app.kubernetes.io/name: pg}}
    spec:
      automountServiceAccountToken: false
      securityContext: {runAsNonRoot: true, runAsUser: 70, runAsGroup: 70, fsGroup: 70, seccompProfile: {type: RuntimeDefault}}
      containers:
        - name: postgres
          image: $EXTERNAL_PG_IMAGE
          env:
            - {name: POSTGRES_PASSWORD, value: e2e-superuser-only}
            - {name: PGDATA, value: /var/lib/postgresql/data/pgdata}
          ports: [{name: postgres, containerPort: 5432}]
          readinessProbe: {exec: {command: [pg_isready, -h, 127.0.0.1, -U, postgres]}, periodSeconds: 3}
          resources: {requests: {cpu: 100m, memory: 128Mi}, limits: {cpu: "1", memory: 512Mi}}
          securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}}
          volumeMounts: [{name: data, mountPath: /var/lib/postgresql/data}, {name: run, mountPath: /var/run/postgresql}]
      volumes: [{name: data, emptyDir: {}}, {name: run, emptyDir: {}}]
---
apiVersion: v1
kind: Service
metadata: {name: pg}
spec:
  selector: {app.kubernetes.io/name: pg}
  ports: [{name: postgres, port: 5432, targetPort: postgres}]
EOF
    kubectl -n external-db rollout status deploy/pg --timeout=180s
    # The page's first sql block, as written: the five statements and the \connect between them.
    awk '/^```sql$/ {on=1; next} on && /^```$/ {exit} on' docs/database.md >"$work/bootstrap.sql"
    [[ $(grep -c '^CREATE ROLE' "$work/bootstrap.sql") == 3 ]] || die "docs/database.md's first sql block is not the bootstrap"
    kubectl -n external-db exec -i deploy/pg -- psql -v ON_ERROR_STOP=1 -U postgres -d postgres <"$work/bootstrap.sql"
    for r in owner broker retention; do
      pw=$(sed -nE "s/^CREATE ROLE rooms_$r LOGIN PASSWORD '([^']+)';$/\1/p" "$work/bootstrap.sql")
      [[ -n $pw ]] || die "no password for rooms_$r in the bootstrap"
      kubectl -n "$NS" create secret generic "rooms-$r-dsn" \
        --from-literal=uri="postgresql://rooms_$r:$pw@pg.external-db.svc:5432/rooms?sslmode=disable" >/dev/null
    done
    echo "ok: postgres:16 bootstrapped from docs/database.md, three DSN Secrets created"
  fi
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
if [[ -z $EXTERNAL_DB ]]; then
  kubectl -n "$NS" rollout status sts/rooms-db --timeout=180s
else
  ! kubectl -n "$NS" get sts rooms-db >/dev/null 2>&1 || die "postgres.enabled=false still installed the bundled rooms-db"
fi
kubectl -n "$NS" rollout status deploy/room-broker --timeout=180s
kubectl -n "$NS" rollout status deploy/run-controller --timeout=180s
init=$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=room-broker -o jsonpath='{.items[0].status.initContainerStatuses[*].state.terminated.reason}')
[[ "$init" == *Completed* ]] || die "the broker's migrate init container did not complete: '$init'"
echo "ok: Postgres, broker (migrate init: $init) and run-controller are Ready"
if [[ -n $EXTERNAL_DB ]]; then
  mig=$(kubectl -n "$NS" logs deploy/room-broker -c migrate)
  [[ $(grep -c 'migration applied' <<<"$mig") -ge 16 ]] || { echo "$mig" >&2; die "migrate did not apply the schema to the external Postgres"; }
  grep -q 'schema up to date' <<<"$mig" || die "migrate did not finish on the external Postgres"
  echo "ok: the broker migrated postgres:16 from empty and serves"
fi

step "the retention CronJob (v4 D9): a manual run connects as rooms_retention and purges"
kubectl -n "$NS" create job --from=cronjob/room-broker-retention retention-now >/dev/null || die "no CronJob room-broker-retention"
kubectl -n "$NS" wait --for=condition=Complete job/retention-now --timeout=180s ||
  { kubectl -n "$NS" logs job/retention-now >&2 || true; die "the retention job did not complete"; }
kubectl -n "$NS" logs job/retention-now | grep -F 'purged expired rooms' || die "the retention job did not purge"
kubectl -n "$NS" delete job retention-now --wait=false >/dev/null

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

step "per-run tokens (v4 D10): the factory writes run token Secrets and reads none; a run waits for its own"
# The dummy App never mints, and the factory never passes start-up: the Roles, the admission policy
# and the run-controller's wait are what is proved. The nightly proves a real mint.
sa=system:serviceaccount:$NS
for v in create patch delete; do
  kubectl auth can-i "$v" secrets -n agents --as="$sa:agent-factory" >/dev/null || die "the factory cannot $v Secrets in agents"
done
for v in get list watch; do
  ! kubectl auth can-i "$v" secrets -n agents --as="$sa:agent-factory" >/dev/null || die "the factory may $v Secrets in agents"
done
kubectl auth can-i get secret/run-abcdefgh-git -n agents --as="$sa:run-controller" >/dev/null || die "the run-controller cannot read a run's token Secret"
# The policy bounds the factory's writes: an apply over agent-model is refused, and answers no object.
# Server-side apply, as the minter writes: one PATCH, no read first (kubectl patch reads, which RBAC refuses).
for _ in $(seq 30); do
  refused=$(printf '%s\n' 'apiVersion: v1' 'kind: Secret' 'metadata: {name: agent-model, namespace: agents, labels: {x: "y"}}' |
    kubectl apply --server-side --force-conflicts --field-manager=probe --dry-run=server --as="$sa:agent-factory" -f - 2>&1) && refused=""
  grep -qF 'ValidatingAdmissionPolicy' <<<"$refused" && break
  sleep 2
done
grep -qF 'run token Secrets' <<<"$refused" || die "the factory's patch of agent-model was not refused by the run token policy: $refused"
echo "ok: the factory's patch of agent-model is refused (run token policy)"
# A run the factory creates waits Pending for its own Secret. In the unenforced leg the network
# preflight refuses every run first (by design): the run must end Failed, PreflightFailed, with no pod.
kubectl apply --as="$sa:agent-factory" -f - <<EOF
apiVersion: maku.ogenki.io/v1alpha1
kind: AgentRun
metadata: {name: run-abcdefgh, namespace: agents}
spec:
  role: implementer
  repository: Smana/demo
  baseRef: main
  principal: system:factory
  dataClass: public
  task: {text: "a run that never starts"}
  budget: {maxMinutes: 5}
EOF
want='Pending/SecretMissing/*run-abcdefgh-git*' what="waits Pending (SecretMissing) for its own token Secret"
[[ $NETPOL != unenforced ]] || { want='Failed/PreflightFailed/network:*' what="is refused by the network preflight (PreflightFailed)"; }
reason=""
for _ in $(seq 60); do
  reason=$(kubectl -n agents get agentruns.maku.ogenki.io run-abcdefgh -o jsonpath='{.status.phase}/{.status.conditions[?(@.type=="Ready")].reason}/{.status.conditions[?(@.type=="Ready")].message}') || reason=""
  # shellcheck disable=SC2053 # $want is a glob on purpose
  [[ $reason == $want ]] && break
  sleep 3
done
# shellcheck disable=SC2053
[[ $reason == $want ]] || die "the run is '$reason', want $want: a run without its token Secret $what"
! kubectl -n agents get pod run-abcdefgh >/dev/null 2>&1 || die "a pod started without the run's token Secret"
echo "ok: a run without its token Secret $what, with no pod"
kubectl -n agents delete agentruns.maku.ogenki.io run-abcdefgh --wait=false >/dev/null

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

# The probe helpers below are defined in every leg: later steps create pods in agents with them.
# The rooms-db assertions skip EXTERNAL_DB, which has no rooms-db.
if [[ -z $EXTERNAL_DB ]]; then
step "plain NetworkPolicies: a run-labelled pod cannot reach rooms-db, the broker's pods can"
kubectl -n "$NS" get networkpolicy rooms-db agent-factory-api
fi
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
if [[ -z $EXTERNAL_DB ]]; then
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
fi

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

if [[ -n $GATEWAY ]]; then
  step "the model gateway: a run-like token reaches its alias, and only its alias"
  kubectl -n "$NS" rollout status deploy/af-agentgateway --timeout=180s
  kubectl -n "$NS" wait --for=condition=Programmed gateway/model-gateway --timeout=180s
  kubectl -n "$NS" rollout status deploy/model-gateway --timeout=180s
  kubectl -n "$NS" get networkpolicy model-gateway >/dev/null || die "no NetworkPolicy model-gateway"
  kubectl -n agents create serviceaccount run-gwprobe2 >/dev/null
  # A run-like pod (the run-controller's label, created as the run-controller: only it creates pods in
  # agents), holding a projected token for maku-gateway.public.tier-light. It calls three times: its
  # alias with its token, another alias with the same token, its alias with no token. The JWKS is
  # fetched and pushed asynchronously, so the first call is retried for up to 2 minutes.
  kubectl -n agents create --as "system:serviceaccount:$NS:run-controller" -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata: {name: gw-caller, labels: {agents.ogenki.io/run-id: gwprobe2}}
spec:
  serviceAccountName: run-gwprobe2
  restartPolicy: Never
  automountServiceAccountToken: false
  securityContext: {runAsNonRoot: true, runAsUser: 65534, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: caller
      image: $ECHO_IMAGE
      command:
        - python3
        - -c
        - |
          import json, time, urllib.error, urllib.request
          base = "http://model-gateway.$NS.svc.cluster.local:8080/m/%s/v1/chat/completions"
          def call(alias, token):
              req = urllib.request.Request(base % alias, method="POST",
                  data=json.dumps({"model": alias, "messages": [{"role": "user", "content": "hi"}]}).encode(),
                  headers={"Content-Type": "application/json"})
              if token:
                  req.add_header("Authorization", "Bearer " + open("/tokens/model").read().strip())
              try:
                  with urllib.request.urlopen(req, timeout=20) as r:
                      return r.status, r.read().decode()
              except urllib.error.HTTPError as e:
                  return e.code, e.read().decode()
              except OSError as e:
                  return 0, str(e)
          for _ in range(40):
              code, body = call("tier-light", True)
              if code == 200:
                  break
              time.sleep(3)
          print("own", code, body, flush=True)
          print("other", *call("tier-frontier", True), flush=True)
          print("none", *call("tier-light", False), flush=True)
      resources: {requests: {cpu: 10m, memory: 32Mi}, limits: {cpu: 200m, memory: 64Mi}}
      securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}
      volumeMounts: [{name: tokens, mountPath: /tokens, readOnly: true}]
  volumes:
    - name: tokens
      projected:
        sources: [{serviceAccountToken: {audience: maku-gateway.public.tier-light, expirationSeconds: 3600, path: model}}]
EOF
  phase=""
  for _ in $(seq 90); do
    phase=$(kubectl -n agents get pod gw-caller -o jsonpath='{.status.phase}') || phase=""
    [[ $phase == Succeeded || $phase == Failed ]] && break
    sleep 2
  done
  out=$(kubectl -n agents logs gw-caller 2>&1) || out=""
  echo "$out"
  own=$(grep '^own ' <<<"$out") || die "the gateway caller printed nothing (phase '$phase')"
  [[ $own == "own 200 "* ]] || { kubectl -n "$NS" logs deploy/model-gateway --tail=40 >&2 || true; kubectl -n "$NS" logs deploy/af-agentgateway --tail=40 >&2 || true; die "the run-like token was not served on its alias: $own"; }
  report=$(sed -E 's/^own 200 //' <<<"$own" | jq -r '.choices[0].message.content')
  # the provider sees the gateway's key from demo-model ("Bearer not-a-key", 16 characters), never the run's token
  jq -e '.model == "echo-light" and .path == "/provider/v1/chat/completions" and .authorization == "len=16" and .["x-ar-agent"] == "system:serviceaccount:agents:run-gwprobe2"' <<<"$report" >/dev/null ||
    die "the provider saw the wrong request: $report"
  grep -q '^other 403 ' <<<"$out" || die "the tier-light token was not refused 403 on tier-frontier: $(grep '^other' <<<"$out")"
  grep -q '^none 401 ' <<<"$out" || die "a call without a token was not refused 401: $(grep '^none' <<<"$out")"
  kubectl -n agents delete pod gw-caller --now --wait=false >/dev/null 2>&1 || true
  echo "ok: 200 on its alias (model echo-light, sub run-gwprobe2, the provider key from its Secret, never the token), 403 on another alias, 401 without a token"

  step "a run in model.mode gateway: its own token, no provider key in the runs namespace"
  ! kubectl -n agents get secret agent-model >/dev/null 2>&1 || die "agent-model exists in agents: model.mode gateway puts no provider key there"
  ! kubectl -n agents get secret -o name | grep -v -E '/(agent-git|agent-git-read|run-[a-z2-7]{8}-git)$' || die "a Secret other than git tokens exists in agents"
  # A run created as the factory creates it (the dummy App starts none, and only the factory may create
  # AgentRuns). Its token Secret is written as the minter writes it, with a dummy token: the harness
  # clones a public repository anonymously, then calls its model, agent-default, through the gateway.
  run=run-gwrunab2
  kubectl apply --as="$sa:agent-factory" -f - >/dev/null <<EOF
apiVersion: maku.ogenki.io/v1alpha1
kind: AgentRun
metadata: {name: $run, namespace: agents}
spec:
  role: implementer
  repository: ${GATEWAY_RUN_REPO:-Smana/maku-demo}
  principal: system:factory
  dataClass: public
  model: agent-default
  task: {text: "Reply with one short sentence; change nothing."}
  budget: {maxMinutes: 10}
EOF
  uid=$(kubectl -n agents get agentruns.maku.ogenki.io "$run" -o jsonpath='{.metadata.uid}')
  kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: $run-git
  namespace: agents
  labels: {app.kubernetes.io/managed-by: maku-factory, maku.ogenki.io/run: $run}
  annotations: {maku.ogenki.io/expires-at: "$(date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)"}
  ownerReferences: [{apiVersion: maku.ogenki.io/v1alpha1, kind: AgentRun, name: $run, uid: $uid}]
stringData: {token: dummy-not-a-token}
EOF
  for _ in $(seq 90); do kubectl -n agents get pod "$run" >/dev/null 2>&1 && break; sleep 2; done
  kubectl -n agents get pod "$run" >/dev/null 2>&1 || { kubectl -n agents get agentrun "$run" -o yaml >&2; die "the run-controller created no pod for $run"; }
  pod=$(kubectl -n agents get pod "$run" -o json)
  jq -e '[.spec.volumes[] | select(.secret.secretName == "agent-model")] | length == 0' <<<"$pod" >/dev/null || die "the run pod mounts agent-model"
  jq -e '[.spec.volumes[] | select(.name == "model-token") | .projected.sources[].serviceAccountToken | .audience + " " + (.expirationSeconds | tostring)] == ["maku-gateway.public.agent-default 1200"]' <<<"$pod" >/dev/null ||
    die "the run pod's token is not maku-gateway.public.agent-default for the deadline plus 600 s: $(jq -c '.spec.volumes' <<<"$pod")"
  jq -e '[.spec.containers[0].env[] | select(.name == "LLM_BASE_URL" or .name == "LLM_API_KEY_FILE" or .name == "MODEL") | .value] == ["agent-default", "http://model-gateway.'"$NS"'.svc.cluster.local:8080/m/agent-default/v1", "/run/agent/model/token"]' <<<"$pod" >/dev/null ||
    die "the run pod's model env is wrong: $(jq -c '[.spec.containers[0].env[] | select(.name | test("MODEL|LLM_"))]' <<<"$pod")"
  echo "ok: the run pod holds its own token (maku-gateway.public.agent-default, 1200 s) and no model Secret"
  # The harness's first model call: the provider reports the verified subject the gateway forwarded.
  sub="system:serviceaccount:agents:$run"
  seen=0
  for _ in $(seq 150); do
    if kubectl -n models logs deploy/echo-llm 2>/dev/null | jq -c "select(.[\"x-ar-agent\"] == \"$sub\")" 2>/dev/null | grep -q .; then seen=1; break; fi
    sleep 4
  done
  ((seen)) || { kubectl -n agents logs "$run" -c harness --tail=60 >&2 || true; kubectl -n "$NS" logs deploy/model-gateway --tail=40 >&2 || true; die "the run's harness never reached its model through the gateway as $sub"; }
  kubectl -n models logs deploy/echo-llm | jq -c "select(.[\"x-ar-agent\"] == \"$sub\")" | head -1
  # The gateway's access log names the run by its token's subject (x_ar_agent), the field ANR's dashboards read.
  kubectl -n "$NS" logs deploy/model-gateway --tail=400 | grep -F "$sub" | grep -qF x_ar_agent || die "the gateway's access log has no x_ar_agent=$sub"
  kubectl -n "$NS" logs deploy/model-gateway --tail=400 | grep -F "$sub" | head -1 | cut -c1-400
  phase=$(kubectl -n agents get agentrun "$run" -o jsonpath='{.status.phase}')
  echo "ok: the run reached its model through the gateway as $sub (run phase now '$phase'); agents holds no provider key"
  if [[ -n $METERING ]]; then
    step "metering: the run's tokens reach the meter's query, under agentgateway's names, and are priced"
    kubectl -n metrics rollout status deploy/vmsingle --timeout=120s >/dev/null
    # the factory's own meter.query, as the release set it, run where the meter runs it
    query=$(helm -n "$NS" get values af -o json | jq -r '."maku-factory".config.meter.query')
    [[ $query == *agentgateway_gen_ai_client_token_usage_sum* ]] || die "the factory's meter.query is not the documented agentgateway query: $query"
    vmq() { kubectl get --raw "/api/v1/namespaces/metrics/services/vmsingle:8428/proxy/api/v1/query?query=$(jq -rn --arg q "$1" '$q|@uri')"; }
    got=0
    for _ in $(seq 40); do
      reply=$(vmq "$query" 2>/dev/null || true)
      if jq -e --arg sub "$sub" '[.data.result[] | select(.metric.ar_agent == $sub) | .metric.gen_ai_token_type] | (index("input") != null and index("output") != null)' <<<"$reply" >/dev/null 2>&1; then got=1; break; fi
      sleep 5
    done
    ((got)) || { echo "$reply" >&2; die "VictoriaMetrics has no input and output series for $sub under the meter's query"; }
    jq -c --arg sub "$sub" '.data.result[] | select(.metric.ar_agent == $sub) | [.metric.gen_ai_request_model, .metric.gen_ai_token_type, .value[1]]' <<<"$reply"
    printf '%s\n' "$reply" >"$work/meter-reply.json"
    METER_REPLY="$work/meter-reply.json" METER_RUN="${run#run-}" go test -count=1 -v -run TestRecordedAgentgatewayReply ./internal/factory/meter >"$work/meter-test.log" 2>&1 ||
      { cat "$work/meter-test.log" >&2; die "the meter does not price the gateway's reply for $sub"; }
    grep -F 'micro-USD at' "$work/meter-test.log" || true
    # the throttle source answers (a vector, empty unless a run was 429'd)
    tq=$(helm -n "$NS" get values af -o json | jq -r '."maku-factory".config.meter.throttle.promQuery')
    vmq "$tq" | jq -e '.status == "success" and .data.resultType == "vector"' >/dev/null || die "meter.throttle.promQuery is not a vector on VictoriaMetrics"
    echo "ok: the run's tokens are in VictoriaMetrics as $sub, the meter prices them, and the PromQL throttle answers"
  fi
  kubectl -n agents delete agentrun "$run" --wait=false >/dev/null 2>&1 || true
fi

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
echo "install OK on $CLUSTER_KIND${ADOPT:+ (ADOPT)}${GATEWAY:+ (gateway)}${METERING:+ (metering)}"
