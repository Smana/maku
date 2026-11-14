# shellcheck shell=bash
# SPDX-License-Identifier: Apache-2.0
#
# The install jobs' shared harness (v4 step 7, PR F; D23, IN10), sourced by kind-calico.sh and k3s.sh.
# Each leg installs a production release as a user would, from `maku install --print`'s own files:
#   1. the wizard writes values.yaml, the Secrets' templates and the factory objects from the leg's
#      answers (hack/install-e2e/<leg>.answers.yaml), built from this checkout;
#   2. the CRD chart, then agentgateway's CRDs server-side, then the Secrets, then the umbrella with the
#      wizard's values and overlay.yaml (the e2e stub as every external service), then factory/;
#   3. `maku preflight --strict` (the install's preflight, D24): every check passes, none warns.
# The e2e stub (hack/e2e-stub) is the humans' issuer, GitHub's API and the model provider; Postgres 16 is
# a plain Deployment outside the chart, bootstrapped with docs/database.md. The cluster, its kubeconfig
# and the images' tag are this run's own: the current kubectl context is never read or changed.
#
# The caller sets LEG (kind-calico or k3s) and defines cluster_create, cluster_load IMAGE and
# cluster_delete, then calls install_e2e_main; it may define leg_diagnostics. KUBECTL, HELM and MAKU name the binaries (lib_test.sh
# records them); PREBUILT=1 takes the images CI built once (tag e2e) instead of building them.

cd "$(dirname "${BASH_SOURCE[0]}")/../.." || exit
# shellcheck source=hack/install-e2e/versions.env
. hack/install-e2e/versions.env

: "${LEG:?LEG is kind-calico or k3s}"
NS=maku
RELEASE=agent-factory
PREBUILT="${PREBUILT:-}"
TAG="${TAG:-install-$LEG-$$}"
[[ -z $PREBUILT ]] || TAG=e2e
KUBECTL="${KUBECTL:-kubectl}"
HELM="${HELM:-helm}"
# The images a leg runs: no run starts, so the harness and the bridge are never pulled.
IMAGES=(maku-broker maku-run-controller maku-factory maku-e2e-stub)
STUB_HOST=e2e-stub.e2e.svc.cluster.local

step() { printf '\n==> %s\n' "$*"; }
die() { echo "FAIL: $*" >&2; exit 1; }
k() { "$KUBECTL" "$@"; }
h() { "$HELM" "$@"; }

umask 077
work="$(mktemp -d "${TMPDIR:-/tmp}/maku-install-$LEG.XXXXXX")"
export KUBECONFIG="$work/kubeconfig"
MAKU="${MAKU:-$work/maku}"
created=false
OUT="$work/out"     # the wizard's tree
PG_NS=external-db   # the database's namespace

# diagnostics prints what a failed leg needs: every pod, the events, each component's log.
diagnostics() {
  k get pods -A -o wide >&2 || true
  k get events -A --sort-by=.lastTimestamp 2>/dev/null | tail -60 >&2 || true
  for d in room-broker run-controller agent-factory rooms-web-proxy model-gateway; do
    echo "--- $NS/$d" >&2
    k -n "$NS" logs "deploy/$d" --all-containers --tail=80 >&2 2>/dev/null || true
  done
  k -n e2e logs deploy/e2e-stub --tail=60 >&2 || true
  # A leg that installs through another tool (flux.sh, argocd.sh) adds that tool's state.
  ! declare -F leg_diagnostics >/dev/null || leg_diagnostics >&2 || true
}

onexit() {
  local rc=$?
  trap - EXIT
  # A port-forward this run started (port_forward).
  jobs -p | xargs -r kill 2>/dev/null || true
  if [[ $rc -ne 0 ]] && $created; then diagnostics; fi
  if $created && [[ -z ${KEEP:-} ]]; then
    step "delete the cluster"
    cluster_delete || true
  fi
  if [[ -z $PREBUILT ]]; then
    for i in "${IMAGES[@]}"; do docker rmi "ghcr.io/smana/$i:$TAG" >/dev/null 2>&1 || true; done
  fi
  rm -rf "$work"
  exit "$rc"
}

build_images() {
  if [[ -n $PREBUILT ]]; then
    for i in "${IMAGES[@]}"; do docker image inspect "ghcr.io/smana/$i:$TAG" >/dev/null || die "PREBUILT=1: no image ghcr.io/smana/$i:$TAG"; done
    return
  fi
  step "build the images from this checkout, tag $TAG"
  for i in maku-broker maku-run-controller maku-factory; do
    docker build -q -f "images/$i/Dockerfile" -t "ghcr.io/smana/$i:$TAG" . >/dev/null
  done
  docker build -q -f hack/e2e-stub/Dockerfile -t "ghcr.io/smana/maku-e2e-stub:$TAG" . >/dev/null
}

# wizard runs `maku install --print` from the leg's answers into $work/out. The images are pinned to
# a placeholder digest (the wizard reads no registry); the umbrella is installed with this run's tag.
wizard() {
  step "maku install --print --answers hack/install-e2e/$LEG.answers.yaml"
  [[ -x $MAKU ]] || go build -o "$MAKU" ./cmd/maku
  local pin d
  d="$(printf '0%.0s' $(seq 64))"
  {
    cat "hack/install-e2e/$LEG.answers.yaml"
    echo "images.pins:"
    for pin in maku-broker maku-run-controller maku-harness maku-bridge maku-factory maku-slack; do echo "  - $pin=v0.0.0@sha256:$d"; done
  } >"$work/answers.yaml"
  "$MAKU" install --print --out "$OUT" --answers "$work/answers.yaml" >/dev/null
  for f in values.yaml crds/values.yaml secrets/client-ids.yaml secrets/web-proxy.yaml secrets/templates/database.yaml factory/factory.yaml; do
    [[ -s $OUT/$f ]] || die "the wizard wrote no $f"
  done
}

# stub_up: a one-day throwaway CA, the stub's certificate, the stub.
stub_up() {
  step "the e2e stub: the issuer, GitHub and the model provider, behind a throwaway CA"
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=maku-install-e2e-ca' \
    -addext 'basicConstraints=critical,CA:TRUE' -addext 'keyUsage=critical,keyCertSign,cRLSign' \
    -keyout "$work/ca.key" -out "$work/ca.crt" >/dev/null 2>&1
  cert stub "$STUB_HOST" "DNS:$STUB_HOST"
  k create namespace e2e >/dev/null
  k -n e2e create secret generic e2e-stub-tls --from-file=tls.crt="$work/stub.crt" --from-file=tls.key="$work/stub.key" >/dev/null
  # The stub's fence file names this release's factory, which then owns owner/repo (the merge gate's read).
  sed "s|ghcr.io/smana/maku-e2e-stub:e2e|ghcr.io/smana/maku-e2e-stub:$TAG|" charts/agent-factory/tests/e2e-stub.yaml |
    F="$RELEASE" yq 'select(.kind == "Deployment").spec.template.spec.containers[0].env += [{"name": "E2E_STUB_FENCE_FACTORY", "value": strenv(F)}]' |
    k apply -f - >/dev/null
  k -n e2e rollout status deploy/e2e-stub --timeout=180s >/dev/null
}

# cert NAME CN SANS: a serving certificate from the run's CA, into $work/NAME.{crt,key}.
cert() {
  openssl req -newkey rsa:2048 -nodes -subj "/CN=$2" -keyout "$work/$1.key" -out "$work/$1.csr" >/dev/null 2>&1
  printf 'subjectAltName=%s\nextendedKeyUsage=serverAuth\nauthorityKeyIdentifier=keyid,issuer\n' "$3" >"$work/$1.ext"
  openssl x509 -req -days 1 -in "$work/$1.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" -CAcreateserial \
    -extfile "$work/$1.ext" -out "$work/$1.crt" >/dev/null 2>&1
}

# port_forward NS TARGET PORT: a background port-forward to TARGET's PORT, on a free local port it
# sets in PF_PORT. onexit stops it; so does `kill "$PF_PID"`.
port_forward() {
  local pflog="$work/port-forward-$1-${2//\//-}.log"
  k -n "$1" port-forward "$2" ":$3" >"$pflog" 2>&1 &
  # shellcheck disable=SC2034 # the callers' own
  PF_PID=$!
  PF_PORT=""
  for _ in $(seq 60); do
    PF_PORT=$(sed -nE 's/^Forwarding from 127\.0\.0\.1:([0-9]+) .*/\1/p' "$pflog" | head -1)
    [[ -z $PF_PORT ]] || return 0
    sleep 1
  done
  die "port-forward $1/$2:$3 did not start: $(cat "$pflog")"
}

# wait_crds CRD...: just after a server-side apply, `kubectl wait` can fail before the CRDs are served.
wait_crds() {
  for _ in 1 2 3 4 5; do
    k wait --for=condition=Established "$@" --timeout=120s >/dev/null 2>&1 && return
    sleep 3
  done
  k wait --for=condition=Established "$@" --timeout=120s >/dev/null
}

# prereqs: what README.md's "Before" asks of the cluster.
prereqs() {
  step "before: Gateway API's and prometheus-operator's CRDs, the namespace, Postgres"
  # k3s's Traefik may own them already (its CRD chart): the floor's fields are taken over, as a platform would.
  curl -fsSL "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GATEWAY_API/standard-install.yaml" |
    k apply --server-side --force-conflicts -f - >/dev/null
  curl -fsSL "https://github.com/prometheus-operator/prometheus-operator/releases/download/$PROMETHEUS_OPERATOR/stripped-down-crds.yaml" |
    k apply --server-side -f - >/dev/null
  wait_crds crd/gateways.gateway.networking.k8s.io crd/httproutes.gateway.networking.k8s.io crd/servicemonitors.monitoring.coreos.com \
    crd/podmonitors.monitoring.coreos.com crd/prometheusrules.monitoring.coreos.com
  k create namespace monitoring >/dev/null
  k create namespace "$NS" >/dev/null
  postgres
}

# postgres: a plain postgres:16 in namespace PG_NS, outside the chart, bootstrapped with
# docs/database.md's SQL block as written; the wizard's three DSN Secrets are filled from it.
postgres() {
  k create namespace "$PG_NS" >/dev/null
  k -n "$PG_NS" apply -f - >/dev/null <<YAML
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
          image: $PG_IMAGE
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
YAML
  k -n "$PG_NS" rollout status deploy/pg --timeout=180s >/dev/null
  awk '/^```sql$/ {on=1; next} on && /^```$/ {exit} on' docs/database.md >"$work/bootstrap.sql"
  [[ $(grep -c '^CREATE ROLE' "$work/bootstrap.sql") == 3 ]] || die "docs/database.md's first sql block is not the bootstrap"
  k -n "$PG_NS" exec -i deploy/pg -- psql -q -v ON_ERROR_STOP=1 -U postgres -d postgres <"$work/bootstrap.sql" >/dev/null
}

# dsn ROLE: the role's DSN, its password from the bootstrap SQL.
dsn() {
  local pw
  pw=$(sed -nE "s/^CREATE ROLE rooms_$1 LOGIN PASSWORD '([^']+)';$/\1/p" "$work/bootstrap.sql")
  [[ -n $pw ]] || die "no password for rooms_$1 in docs/database.md"
  printf 'postgresql://rooms_%s:%s@pg.%s.svc.cluster.local:5432/rooms?sslmode=disable' "$1" "$pw" "$PG_NS"
}

# secrets: the wizard's Secrets, their empty values filled with this run's throwaway ones, and the
# Secrets README.md's "Before" names: the Apps', the broker's TLS, the factory's CA, the stub's CA.
secrets() {
  step "the Secrets: the wizard's, filled, and the ones it names"
  local s="$OUT/secrets"
  # SSL_CERT_FILE replaces the system roots: the bundle is the stub's CA and the cluster's own, which
  # signs the API server's certificate (the runs' and the factory's JWKS).
  k -n "$NS" get configmap kube-root-ca.crt -o jsonpath='{.data.ca\.crt}' >"$work/kube-ca.crt"
  [[ -s $work/kube-ca.crt ]] || die "no kube-root-ca.crt in $NS"
  cat "$work/ca.crt" "$work/kube-ca.crt" >"$work/bundle.crt"
  k -n "$NS" create secret generic e2e-stub-ca --from-file=ca.crt="$work/bundle.crt" >/dev/null
  cert broker room-broker "DNS:room-broker.$NS.svc,DNS:room-broker.$NS.svc.cluster.local"
  k -n "$NS" create secret tls room-broker-tls --cert="$work/broker.crt" --key="$work/broker.key" >/dev/null
  for c in room-broker-ca openbao-ca; do k -n "$NS" create secret generic "$c" --from-file=ca.crt="$work/ca.crt" >/dev/null; done
  # The Apps: a throwaway key; the stub never checks a signature. Two ids, as the answers name two
  # slugs: the factory refuses an agents App that is its own (D12). The stub serves App 1's routes
  # to either, so the factory App stays 1, the merge gate's integration_id.
  openssl genrsa -out "$work/app.pem" 2048 2>/dev/null
  k -n "$NS" create secret generic agent-factory-github --from-literal=app_id=1 --from-file=private_key="$work/app.pem" >/dev/null
  k -n "$NS" create secret generic agent-factory-agents --from-literal=app_id=2 --from-file=private_key="$work/app.pem" >/dev/null
  k apply -n "$NS" -f "$s/client-ids.yaml" >/dev/null
  yq '.stringData."client-secret" = "e2e-throwaway"' "$s/web-proxy.yaml" | k apply -n "$NS" -f - >/dev/null
  yq '.stringData.client_id = "Iv1.e2e-link" | .stringData.client_secret = "e2e-throwaway"' "$s/templates/github-link.yaml" | k apply -n "$NS" -f - >/dev/null
  yq '.stringData.apiKey = "e2e-provider-key"' "$s/templates/model-provider.yaml" | k apply -n "$NS" -f - >/dev/null
  local broker owner retention
  broker="$(dsn broker)" owner="$(dsn owner)" retention="$(dsn retention)"
  B="$broker" O="$owner" R="$retention" yq '(select(.metadata.name == "rooms-broker-dsn") | .stringData.uri) = strenv(B) |
    (select(.metadata.name == "rooms-owner-dsn") | .stringData.uri) = strenv(O) |
    (select(.metadata.name == "rooms-retention-dsn") | .stringData.uri) = strenv(R)' "$s/templates/database.yaml" | k apply -n "$NS" -f - >/dev/null
  for x in rooms-broker-dsn rooms-owner-dsn rooms-retention-dsn; do
    [[ -n $(k -n "$NS" get secret "$x" -o jsonpath='{.data.uri}') ]] || die "the wizard's $x is still empty"
  done
}

# umbrella_args: the wizard's values, then the stub overlay, the leg's own, and this run's images.
umbrella_args() {
  printf '%s\n' -f "$OUT/values.yaml" -f hack/install-e2e/overlay.yaml
  [[ ! -f hack/install-e2e/$LEG-values.yaml ]] || printf '%s\n' -f "hack/install-e2e/$LEG-values.yaml"
  printf '%s\n' --set maku.e2e.unpinnedImages=true --set maku-factory.e2e.unpinnedImages=true \
    --set "maku.broker.image.tag=$TAG" --set "maku.runController.image.tag=$TAG" --set "maku-factory.image.tag=$TAG"
}

# install: README.md's commands, in its order.
install() {
  step "install: the CRD chart, agentgateway's CRDs, the Secrets, the umbrella, factory/"
  h install "$RELEASE-crds" charts/agent-factory-crds -n "$NS" -f "$OUT/crds/values.yaml" --timeout 2m >/dev/null
  k apply --server-side -f config/agentgateway >/dev/null
  wait_crds -l app.kubernetes.io/part-of=agent-factory crd
  secrets
  leg_before_umbrella
  local args
  mapfile -t args < <(umbrella_args)
  h dependency build charts/agent-factory >/dev/null
  h install "$RELEASE" charts/agent-factory -n "$NS" "${args[@]}" --timeout 5m >/dev/null
  k apply -n "$NS" -f "$OUT/factory/" >/dev/null
}

ready() {
  step "every component Ready ($NS)"
  for d in room-broker run-controller agent-factory rooms-web-proxy; do
    k -n "$NS" rollout status "deploy/$d" --timeout=300s >/dev/null || die "$NS/$d is not Ready"
  done
}

# preflight: `maku preflight --strict`, from this machine, through the release's CronJob.
preflight() {
  step "maku preflight --strict ($NS): every check passes, none warns"
  [[ -x $MAKU ]] || go build -o "$MAKU" ./cmd/maku
  # run-path serves the run-controller's own preflight's last result: wait until it has one (its
  # network probe starts a pod under the RuntimeClass, up to a few minutes on a fresh node).
  local rc=0 rclog="$work/rc-$NS.log" found=0
  for _ in $(seq 120); do
    k -n "$NS" logs deploy/run-controller >"$rclog" 2>&1 || true
    if grep -qE 'preflight (passed|failed)' "$rclog"; then found=1; break; fi
    sleep 3
  done
  ((found)) || { tail -40 "$rclog" >&2; die "$NS: the run-controller's preflight decided nothing within 6 minutes"; }
  # A factory check reads undecided until the factory's first fence pass over its repositories (up
  # to a minute after Ready while a repository's installation is still retried): run it again then,
  # and only then. Any other failure, or a warning, ends the step.
  local attempt pf="$work/preflight-$NS.txt" summary
  for attempt in 1 2 3 4 5 6; do
    rc=0
    "$MAKU" preflight --namespace "$NS" --release "$RELEASE" --strict --no-outside --kubeconfig "$KUBECONFIG" --context "${CTX:-}" 2>&1 | tee "$pf" || rc=${PIPESTATUS[0]}
    ((rc != 0)) || return 0
    summary="$(grep -E '^preflight: [0-9]+ fail' "$pf" | tail -1)"
    if [[ ! $summary =~ \ 0\ warn, ]] || grep -qE '=(fail|warn):' <<<"${summary//=fail:undecided/}" || ((attempt == 6)); then
      die "$NS: maku preflight --strict exited $rc"
    fi
    echo "undecided only (attempt $attempt): again in 20s"
    sleep 20
  done
}

install_e2e_main() {
  trap onexit EXIT
  build_images
  wizard
  step "create the cluster"
  created=true
  cluster_create
  for i in "${IMAGES[@]}"; do cluster_load "ghcr.io/smana/$i:$TAG"; done
  stub_up
  prereqs
  install
  ready
  preflight
  leg_checks
  echo "install OK: $LEG"
}
