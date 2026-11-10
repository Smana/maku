#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Proves governed intake revisions against a real broker on a throwaway k3d cluster. Every external
# dependency is the in-cluster e2e-stub: no real GitHub, OIDC or model endpoint is contacted.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."
# shellcheck source=hack/quickstart/versions.env
. hack/quickstart/versions.env

NAME=maku-intake-k3d
NS=maku
TAG=e2e
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.35.0-k3s1}"
VALUES=charts/agent-factory/tests/intake-values.yaml
STUB=charts/agent-factory/tests/e2e-stub.yaml
step() { printf '\n==> %s\n' "$*"; }
die() { echo "FAIL: $*" >&2; exit 1; }
assert_eq() { [[ $1 == "$2" ]] || die "$3: got '$1', want '$2'"; }
field() { sed -n "s/^$1=//p" "$2" | head -1; }

umask 077
work="$(mktemp -d "${TMPDIR:-/tmp}/maku-intake-k3d.XXXXXX")"
export KUBECONFIG="$work/kubeconfig"
created=false
stub_port=
stub_url=
broker_port=
pf_pids=()
dump() {
  $created && [[ -s $KUBECONFIG ]] || return 0
  echo "--- spec-intake failed: diagnostics ---" >&2
  kubectl get pods,pvc,svc,networkpolicy -A -o wide >&2 || true
  kubectl -n "$NS" describe pods >&2 || true
  kubectl get events -A --sort-by=.lastTimestamp >&2 || true
  for d in room-broker run-controller agent-factory; do
    kubectl -n "$NS" logs "deploy/$d" --all-containers --tail=120 >&2 || true
  done
  kubectl -n "$NS" logs sts/rooms-db --tail=60 >&2 || true
  kubectl -n e2e logs deploy/e2e-stub --tail=120 >&2 || true
  for log in "$work"/port-forward-*.log; do
    if [[ -f $log ]]; then echo "--- $log ---" >&2; cat "$log" >&2; fi
  done
  return 0
}
onexit() {
  local rc=$?
  trap - EXIT
  for pid in "${pf_pids[@]}"; do kill "$pid" >/dev/null 2>&1 || true; done
  for pid in "${pf_pids[@]}"; do wait "$pid" >/dev/null 2>&1 || true; done
  [[ $rc -eq 0 ]] || dump
  if $created; then
    step "delete the cluster $NAME"
    k3d cluster delete "$NAME" || true
  fi
  rm -rf "$work"
  exit "$rc"
}
trap onexit EXIT

step "build the scenario client and images from this checkout"
mkdir -p "$work/bin"
go build -o "$work/bin/e2e-client" ./hack/e2e-stub/client
for image in maku-broker maku-run-controller maku-factory; do
  docker build -q -f "images/$image/Dockerfile" -t "ghcr.io/smana/$image:$TAG" . >/dev/null
done
docker build -q -f hack/e2e-stub/Dockerfile -t "ghcr.io/smana/maku-e2e-stub:$TAG" . >/dev/null

step "create the k3d cluster $NAME"
! k3d cluster get "$NAME" >/dev/null 2>&1 || die "cluster $NAME already exists; refusing to adopt or delete it"
created=true
k3d cluster create "$NAME" --image "$K3S_IMAGE" --no-lb --wait \
  --k3s-arg '--disable=traefik@server:0' --k3s-arg '--disable=servicelb@server:0' \
  --kubeconfig-update-default=false --kubeconfig-switch-context=false
k3d kubeconfig get "$NAME" >"$KUBECONFIG"
for image in maku-broker maku-run-controller maku-factory maku-e2e-stub; do
  k3d image import -c "$NAME" "ghcr.io/smana/$image:$TAG"
done
kubectl get nodes

[[ -f $VALUES && -f $STUB ]] || die "missing scenario assets: $VALUES and $STUB are required"
step "create throwaway TLS and test credentials, then start the external-service stub"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=maku-intake-e2e-ca' \
  -keyout "$work/ca.key" -out "$work/ca.crt" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj '/CN=e2e-stub.e2e.svc.cluster.local' \
  -keyout "$work/stub.key" -out "$work/stub.csr" >/dev/null 2>&1
cat >"$work/stub.ext" <<'EOF'
subjectAltName=DNS:e2e-stub,DNS:e2e-stub.e2e.svc,DNS:e2e-stub.e2e.svc.cluster.local
extendedKeyUsage=serverAuth
EOF
openssl x509 -req -days 1 -in "$work/stub.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" \
  -CAcreateserial -extfile "$work/stub.ext" -out "$work/stub.crt" >/dev/null 2>&1
kubectl create namespace e2e
kubectl -n e2e create secret generic e2e-stub-tls \
  --from-file=tls.crt="$work/stub.crt" --from-file=tls.key="$work/stub.key"
kubectl create namespace "$NS"
kubectl -n "$NS" create secret generic e2e-stub-ca --from-file=ca.crt="$work/ca.crt"
printf 'rooms-proxy' >"$work/client-id"
printf 'roomctl' >"$work/roomctl-client-id"
printf 'project' >"$work/project-id"
printf 'e2e-planner-key' >"$work/planner-key"
printf '{"pat":"e2e-throwaway","githubIdpId":"github"}' >"$work/reader.json"
kubectl -n "$NS" create secret generic e2e-broker-config \
  --from-file=client-id="$work/client-id" --from-file=roomctl-client-id="$work/roomctl-client-id" \
  --from-file=project-id="$work/project-id" --from-file=planner-key="$work/planner-key" \
  --from-file=reader.json="$work/reader.json"
kubectl apply -f "$STUB"
kubectl -n e2e rollout status deploy/e2e-stub --timeout=180s

step "install the chart with images loaded into this cluster"
openssl genrsa -out "$work/app.pem" 2048 2>/dev/null
helm dependency build charts/agent-factory >/dev/null
values=(--set profile=demo
  --set maku.e2e.unpinnedImages=true --set maku-factory.e2e.unpinnedImages=true
  --set "maku.broker.image.tag=$TAG" --set "maku.runController.image.tag=$TAG" --set "maku-factory.image.tag=$TAG"
  --set maku.runController.images.harness.tag='v0.0.0@sha256:0000'
  --set maku.runController.images.bridge.tag='v0.0.0@sha256:0000'
  --set maku-factory.replicas=1
  --set demo.githubApp.id=1 --set-file demo.githubApp.privateKey="$work/app.pem")
values+=(-f "$VALUES")
helm install intake charts/agent-factory -n "$NS" --create-namespace "${values[@]}" --timeout 5m

step "wait for the database and controllers"
kubectl -n "$NS" rollout status sts/rooms-db --timeout=180s
kubectl -n "$NS" rollout status deploy/room-broker --timeout=180s
kubectl -n "$NS" rollout status deploy/run-controller --timeout=180s
kubectl -n "$NS" rollout status deploy/agent-factory --timeout=180s

start_port_forward() {
  local ns=$1 resource=$2 remote=$3 log=$4 variable=$5 pid line port
  kubectl -n "$ns" port-forward --address=127.0.0.1 "$resource" ":$remote" >"$log" 2>&1 &
  pid=$!
  pf_pids+=("$pid")
  for _ in $(seq 60); do
    if ! kill -0 "$pid" >/dev/null 2>&1; then cat "$log" >&2; die "port-forward $resource stopped"; fi
    line=$(grep -m1 'Forwarding from 127.0.0.1:' "$log" || true)
    if [[ $line =~ 127\.0\.0\.1:([0-9]+) ]]; then
      port=${BASH_REMATCH[1]}
      printf -v "$variable" '%s' "$port"
      return 0
    fi
    sleep 1
  done
  cat "$log" >&2
  die "port-forward $resource did not become ready"
}

curl_stub() {
  curl -fsS --cacert "$work/ca.crt" \
    --resolve "e2e-stub.e2e.svc.cluster.local:$stub_port:127.0.0.1" "$@"
}

step "mint a throwaway OIDC token from the stub"
start_port_forward e2e svc/e2e-stub 443 "$work/port-forward-stub.log" stub_port
stub_url="https://e2e-stub.e2e.svc.cluster.local:$stub_port"
for _ in $(seq 60); do
  if curl_stub "$stub_url/healthz" >/dev/null 2>&1; then break; fi
  sleep 1
done
curl_stub -X POST "$stub_url/token" |
  jq -er '.access_token | select(length > 0)' >"$work/token"
printf 'header = "Authorization: Bearer %s"\n' "$(cat "$work/token")" >"$work/curl-token.conf"

step "connect to the broker's human MCP endpoint"
start_port_forward "$NS" deploy/room-broker 8080 "$work/port-forward-broker.log" broker_port
export E2E_BROKER_URL="http://127.0.0.1:$broker_port"
export E2E_TOKEN_FILE="$work/token"
client="$work/bin/e2e-client"
text='Document the verification command.'

step "planner: one ranked g1 marker and its broker-owned hash"
"$client" plan --text "$text" --repo owner/repo --out "$work/plan.json" >"$work/plan.out"
old_hash=$(field hash "$work/plan.out")
[[ $old_hash =~ ^sha256:[0-9a-f]{64}$ ]] || die "initial hash is invalid: $old_hash"
assert_eq "$(field gaps "$work/plan.out")" 1 'initial gap count'
assert_eq "$(field recommended "$work/plan.out")" 'task check' 'g1 recommendation'
jq -e '.round == 1 and .gaps[0].id == "g1" and (.issues | length == 1)' "$work/plan.json" >/dev/null ||
  die "initial plan is not round 1 with one g1 issue"
body=$(jq -r '.issues[0].body' "$work/plan.json")
assert_eq "$(grep -oF '[NEEDS CLARIFICATION:' <<<"$body" | wc -l | tr -d ' ')" 1 'initial clarification marker count'
grep -qF '[NEEDS CLARIFICATION: g1 ' <<<"$body" || die 'the initial marker is not g1'

step "planner: a candidate revision gets a new hash and no remaining gaps"
jq '.issues[0].body |= (gsub("\\[NEEDS CLARIFICATION: g1 [^]]+\\]"; "task check") + "\n\n## Decisions\nD1 [g1]: run task check, because it verifies the change")' \
  "$work/plan.json" >"$work/candidate.json"
"$client" revise --text "$text" --repo owner/repo --plan "$work/plan.json" --candidate "$work/candidate.json" \
  --hash "$old_hash" --out "$work/revised.json" >"$work/revised.out"
revised_hash=$(field hash "$work/revised.out")
[[ $revised_hash =~ ^sha256:[0-9a-f]{64}$ && $revised_hash != "$old_hash" ]] ||
  die "revision did not replace $old_hash with a valid new hash: $revised_hash"
assert_eq "$(field gaps "$work/revised.out")" 0 'revised gap count'
assert_eq "$(field canStart "$work/revised.out")" true 'revised canStart'
! jq -e '.issues[].body | contains("[NEEDS CLARIFICATION:")' "$work/revised.json" >/dev/null ||
  die 'the revised plan retains a clarification marker'

step "planner: accept-the-rest is deterministic and spends no model call"
"$client" plan --text "$text" --repo owner/repo --out "$work/fresh.json" >"$work/fresh.out"
fresh_hash=$(field hash "$work/fresh.out")
curl_stub "$stub_url/__counts" >"$work/counts-before.json"
assert_eq "$(jq -r '.planner' "$work/counts-before.json")" 2 'planner calls before accept-rest'
"$client" accept-rest --plan "$work/fresh.json" --hash "$fresh_hash" --out "$work/final.json" >"$work/final.out"
final_hash=$(field hash "$work/final.out")
[[ $final_hash =~ ^sha256:[0-9a-f]{64}$ && $final_hash != "$fresh_hash" ]] ||
  die "accept-rest did not replace $fresh_hash with a valid new hash: $final_hash"
assert_eq "$(field gaps "$work/final.out")" 0 'accept-rest gap count'
assert_eq "$(field canStart "$work/final.out")" true 'accept-rest canStart'
curl_stub "$stub_url/__counts" >"$work/counts-after.json"
assert_eq "$(jq -r '.planner' "$work/counts-after.json")" 2 'planner calls after accept-rest'
assert_eq "$(jq -r '.total' "$work/counts-after.json")" "$(jq -r '.total' "$work/counts-before.json")" 'total model calls across accept-rest'
assert_eq "$(jq -r '.scoper' "$work/counts-after.json")" 0 'unexpected scoper calls in the planner phase'
grep -qF 'A1 [g1]: assumed task check' "$work/final.json" || die 'accept-rest did not record its A1 assumption'

step "planner: the final plan revalidates, while issue_draft refuses a marker"
"$client" accept-rest --plan "$work/final.json" --hash "$final_hash" --out "$work/checked.json" >"$work/checked.out"
assert_eq "$(field hash "$work/checked.out")" "$final_hash" 'unchanged final plan hash'
assert_eq "$(field gaps "$work/checked.out")" 0 'final plan-check gap count'

jq -n '{jsonrpc:"2.0",id:1,method:"initialize",params:{protocolVersion:"2025-06-18",capabilities:{},clientInfo:{name:"intake-e2e-shell",version:"1"}}}' \
  >"$work/mcp-init.json"
curl -fsS --config "$work/curl-token.conf" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' --data-binary @"$work/mcp-init.json" \
  "$E2E_BROKER_URL/mcp" >"$work/mcp-init.out"
jq -e '.result.protocolVersion == "2025-06-18"' "$work/mcp-init.out" >/dev/null || {
  cat "$work/mcp-init.out" >&2; die 'MCP initialization for issue_draft failed';
}
printf '%s' '{"jsonrpc":"2.0","method":"notifications/initialized"}' >"$work/mcp-notify.json"
status=$(curl -sS -o /dev/null -w '%{http_code}' --config "$work/curl-token.conf" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  --data-binary @"$work/mcp-notify.json" "$E2E_BROKER_URL/mcp")
assert_eq "$status" 202 'MCP initialized notification status'
jq -n --arg repo owner/repo --arg title "$(jq -r '.issues[0].title' "$work/fresh.json")" \
  --arg body "$(jq -r '.issues[0].body' "$work/fresh.json")" \
  '{jsonrpc:"2.0",id:2,method:"tools/call",params:{name:"issue_draft",arguments:{repo:$repo,title:$title,body:$body}}}' \
  >"$work/mcp-draft.json"
curl -fsS --config "$work/curl-token.conf" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
  --data-binary @"$work/mcp-draft.json" "$E2E_BROKER_URL/mcp" >"$work/mcp-draft.out"
jq -e '.result.isError == true and (.result.content[0].text | contains("unresolved_marker"))' \
  "$work/mcp-draft.out" >/dev/null || { cat "$work/mcp-draft.out" >&2; die 'issue_draft did not return unresolved_marker'; }
curl_stub "$stub_url/__counts" >"$work/counts-final.json"
assert_eq "$(jq -r '.planner' "$work/counts-final.json")" 2 'planner calls after final checks'

step "scoper, need_answer and status generations: PENDING (Task 6)"
echo 'spec-intake k3d OK'
