#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Proves governed intake revisions against a real broker on a throwaway k3d cluster. Every external
# dependency is the in-cluster e2e-stub: no real GitHub, OIDC or model endpoint is contacted.
#
# The planner and the classifier call the model gateway's clients listener (plan v4 step 4, Task 12):
# agentgateway's controller and maku-gateway run in the release, the stub is their `custom` provider
# over plain http, and the broker holds a projected token per client, never a provider key.
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
# The gateway probe's floor (charts/maku-gateway/tests/probe), as install.sh's gateway leg.
GATEWAY_API=v1.2.1
# The provider key the gateway sends the stub, from its Secret (D14): the stub records its digest.
PROVIDER_KEY=e2e-gateway-key
step() { printf '\n==> %s\n' "$*"; }
die() { echo "FAIL: $*" >&2; exit 1; }
assert_eq() { [[ $1 == "$2" ]] || die "$3: got '$1', want '$2'"; }
field() { sed -n "s/^$1=//p" "$2" | head -1; }

issue_view() {
  gh issue view "$1" --repo=owner/repo --json body,labels,updatedAt,comments
}

has_question_labels() {
  issue_view "$1" | jq -e '[.labels[].name] |
    ((index("factory/needs-author") != null) and (index("factory/ready") == null))' >/dev/null
}

wait_question() {
  local issue=$1
  for _ in $(seq 90); do
    if has_question_labels "$issue"; then return 0; fi
    sleep 2
  done
  issue_view "$issue" >&2 || true
  die "issue #$issue did not reach factory/needs-author without factory/ready"
}

wait_status_generation() {
  local issue=$1 generation=$2 out=$3
  for _ in $(seq 90); do
    issue_view "$issue" >"$out"
    if jq -e --arg owner "generation=$generation -->" '
      [.comments[] | select(.author.login == "maku-demo[bot]" and
        (.body | contains("<!-- agent-factory issue-status -->")))] as $status |
      ($status | length == 1) and ($status[0].body | contains($owner))' "$out" >/dev/null; then
      return 0
    fi
    sleep 2
  done
  cat "$out" >&2
  die "issue #$issue did not adopt one generation-$generation status comment"
}

status_id() {
  jq -r '[.comments[] | select(.author.login == "maku-demo[bot]" and
    (.body | contains("<!-- agent-factory issue-status -->"))) | .id] |
    if length == 1 then .[0] else empty end' "$1"
}

status_body() {
  jq -r '.comments[] | select(.author.login == "maku-demo[bot]" and
    (.body | contains("<!-- agent-factory issue-status -->"))) | .body' "$1"
}

new_issue() {
  local title=$1 body=$2 url number
  url=$(printf '%s' "$body" | gh issue create --repo=owner/repo "--title=$title" --body-file=-)
  number=${url##*/}
  [[ $number =~ ^[0-9]+$ ]] || die "fakegh returned an invalid issue URL: $url"
  gh issue edit "$number" --repo=owner/repo --add-label=class:docs
  gh issue edit "$number" --repo=owner/repo --add-label=factory/ready
  printf '%s\n' "$number"
}

room_for_issue() {
  local issue=$1 index
  local out="$work/rooms-$issue.out"
  "$client" status >"$out"
  index=$(awk -F= -v issue="$issue" '$1 ~ /^rooms\.[0-9]+\.issue$/ && $2 == issue {
    sub(/^rooms\./, "", $1); sub(/\.issue$/, "", $1); print $1; exit
  }' "$out")
  [[ -n $index ]] || { cat "$out" >&2; die "no visible room for issue #$issue"; }
  field "rooms.$index.id" "$out"
}

umask 077
work="$(mktemp -d "${TMPDIR:-/tmp}/maku-intake-k3d.XXXXXX")"
export KUBECONFIG="$work/kubeconfig"
created=false
stub_port=
stub_url=
broker_port=
gw_port=
pf_pids=()
dump() {
  $created && [[ -s $KUBECONFIG ]] || return 0
  echo "--- spec-intake failed: diagnostics ---" >&2
  kubectl get pods,pvc,svc,networkpolicy -A -o wide >&2 || true
  kubectl -n "$NS" get tasks -o jsonpath='{range .items[*]}{.metadata.name}{" phase="}{.status.phase}{" statusComment="}{.status.statusComment}{"\n"}{end}' >&2 || true
  kubectl -n "$NS" describe pods >&2 || true
  kubectl get events -A --sort-by=.lastTimestamp >&2 || true
  for d in room-broker run-controller agent-factory model-gateway intake-agentgateway; do
    kubectl -n "$NS" logs "deploy/$d" --all-containers --tail=120 >&2 || true
  done
  for p in $(kubectl -n agents get pods -o name 2>/dev/null); do kubectl -n agents logs "$p" --all-containers --prefix --tail=80 >&2 || true; done
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
go build -o "$work/bin/maku" ./cmd/maku
go build -o "$work/bin/gh" ./hack/e2e-stub/fakegh
cat >"$work/tls-forward.go" <<'EOF'
package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
)

func forward(client net.Conn, target string) {
	defer func() { _ = client.Close() }()
	upstream, err := net.Dial("tcp", target)
	if err != nil {
		return
	}
	defer func() { _ = upstream.Close() }()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}

func main() {
	certificate, err := tls.LoadX509KeyPair(os.Args[1], os.Args[2])
	if err != nil {
		panic(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13,
	})
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Args[4], []byte(listener.Addr().String()), 0o600); err != nil {
		panic(err)
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			return
		}
		go forward(connection, os.Args[3])
	}
}
EOF
go build -o "$work/bin/tls-forward" "$work/tls-forward.go"
export PATH="$work/bin:$PATH" E2E_MAKU="$work/bin/maku" XDG_CONFIG_HOME="$work/config" SSL_CERT_FILE="$work/ca.crt"
for image in maku-broker maku-run-controller maku-factory; do
  docker build -q -f "images/$image/Dockerfile" -t "ghcr.io/smana/$image:$TAG" . >/dev/null
done
docker build -q -f hack/e2e-stub/Dockerfile -t "ghcr.io/smana/maku-e2e-stub:$TAG" . >/dev/null
rel="${MAKU_RELEASE:-v0.41.0}"
digest() { docker buildx imagetools inspect "ghcr.io/smana/$1:$rel" --format '{{.Manifest.Digest}}'; }
harness_release="$rel@$(digest maku-harness)"
bridge="$rel@$(digest maku-bridge)"
harness="$TAG"
echo "harness $harness_release, bridge $bridge"

step "create the k3d cluster $NAME"
! k3d cluster get "$NAME" >/dev/null 2>&1 || die "cluster $NAME already exists; refusing to adopt or delete it"
created=true
k3d cluster create "$NAME" --image "$K3S_IMAGE" --no-lb --wait \
  --k3s-arg '--disable=traefik@server:0' --k3s-arg '--disable=servicelb@server:0' \
  --k3s-arg '--kube-apiserver-arg=anonymous-auth=true@server:0' \
  --kubeconfig-update-default=false --kubeconfig-switch-context=false
# anonymous-auth: agentgateway's controller fetches the apiserver's JWKS anonymously (probe P1); k3s
# turns anonymous requests off, where kubeadm and kind leave them on.
k3d kubeconfig get "$NAME" >"$KUBECONFIG"
for image in maku-broker maku-run-controller maku-factory maku-e2e-stub; do
  k3d image import -c "$NAME" "ghcr.io/smana/$image:$TAG"
done
kubectl get nodes

[[ -f $VALUES && -f $STUB ]] || die "missing scenario assets: $VALUES and $STUB are required"
step "create throwaway TLS and test credentials, then start the external-service stub"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=maku-intake-e2e-ca' \
  -addext 'basicConstraints=critical,CA:TRUE' -addext 'keyUsage=critical,keyCertSign,cRLSign' \
  -keyout "$work/ca.key" -out "$work/ca.crt" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj '/CN=e2e-stub.e2e.svc.cluster.local' \
  -keyout "$work/stub.key" -out "$work/stub.csr" >/dev/null 2>&1
cat >"$work/stub.ext" <<'EOF'
subjectAltName=DNS:e2e-stub,DNS:e2e-stub.e2e.svc,DNS:e2e-stub.e2e.svc.cluster.local,DNS:github.com,IP:127.0.0.1
extendedKeyUsage=serverAuth
authorityKeyIdentifier=keyid,issuer
EOF
openssl x509 -req -days 1 -in "$work/stub.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" \
  -CAcreateserial -extfile "$work/stub.ext" -out "$work/stub.crt" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj '/CN=localhost' \
  -keyout "$work/maku.key" -out "$work/maku.csr" >/dev/null 2>&1
printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\nauthorityKeyIdentifier=keyid,issuer\n' >"$work/maku.ext"
openssl x509 -req -days 1 -in "$work/maku.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" \
  -CAcreateserial -extfile "$work/maku.ext" -out "$work/maku.crt" >/dev/null 2>&1
kubectl create namespace e2e
kubectl -n e2e create secret generic e2e-stub-tls \
  --from-file=tls.crt="$work/stub.crt" --from-file=tls.key="$work/stub.key"
kubectl create namespace "$NS"
kubectl -n "$NS" create secret generic e2e-stub-ca --from-file=ca.crt="$work/ca.crt"
printf 'rooms-proxy' >"$work/client-id"
printf 'roomctl' >"$work/roomctl-client-id"
printf 'project' >"$work/project-id"
printf 'slack-app' >"$work/sponsored-client-id"
printf '{"pat":"e2e-throwaway","githubIdpId":"github"}' >"$work/reader.json"
# No planner or classifier key: the broker calls the gateway with its projected tokens.
kubectl -n "$NS" create secret generic e2e-broker-config \
  --from-file=client-id="$work/client-id" --from-file=roomctl-client-id="$work/roomctl-client-id" \
  --from-file=project-id="$work/project-id" --from-file=sponsored-client-id="$work/sponsored-client-id" \
  --from-file=reader.json="$work/reader.json"
# The stub provider's key, which the gateway reads by name (D14).
kubectl -n "$NS" create secret generic e2e-provider-key --from-literal=apiKey="$PROVIDER_KEY"
kubectl apply -f "$STUB"
cat >"$work/coredns-custom.yaml" <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns-custom
  namespace: kube-system
data:
  maku-intake.override: |
    rewrite name exact github.com e2e-stub.e2e.svc.cluster.local.
EOF
kubectl apply -f "$work/coredns-custom.yaml"
kubectl -n kube-system rollout restart deploy/coredns
kubectl -n kube-system rollout status deploy/coredns --timeout=180s
kubectl -n e2e rollout status deploy/e2e-stub --timeout=180s

step "derive the test harness with only the throwaway CA added"
cat >"$work/harness.Dockerfile" <<EOF
FROM ghcr.io/smana/maku-harness:$harness_release
USER root
COPY ca.crt /usr/local/share/ca-certificates/maku-intake-e2e.crt
RUN update-ca-certificates
# LiteLLM otherwise uses certifi and misses the merged system bundle.
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
USER 10001
EOF
docker build -q -f "$work/harness.Dockerfile" -t "ghcr.io/smana/maku-harness:$TAG" "$work" >/dev/null
k3d image import -c "$NAME" "ghcr.io/smana/maku-harness:$TAG"

step "install the chart with images loaded into this cluster"
openssl genrsa -out "$work/app.pem" 2048 2>/dev/null
helm dependency build charts/agent-factory >/dev/null
values=(-f charts/agent-factory/values-demo.yaml
  --set maku.e2e.unpinnedImages=true --set maku-factory.e2e.unpinnedImages=true
  --set "maku.broker.image.tag=$TAG" --set "maku.runController.image.tag=$TAG" --set "maku-factory.image.tag=$TAG"
  --set "maku.runController.images.harness.tag=$harness"
  --set "maku.runController.images.bridge.tag=$bridge"
  --set maku-factory.replicas=1
  --set demo.githubApp.id=1 --set-file demo.githubApp.privateKey="$work/app.pem")
values+=(-f "$VALUES" --set "maku-gateway.identity.issuer=$(kubectl get --raw /.well-known/openid-configuration | jq -r .issuer)")
# The system roots, not the throwaway CA this script trusts elsewhere (SSL_CERT_FILE).
(unset SSL_CERT_FILE; curl -fsSL "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GATEWAY_API/standard-install.yaml") |
  kubectl apply --server-side -f - >/dev/null
kubectl wait --for=condition=Established crd/gateways.gateway.networking.k8s.io crd/httproutes.gateway.networking.k8s.io --timeout=120s
# agentgateway's CRDs, vendored and applied apart from any Helm release (too big for one, docs/gateway.md)
kubectl apply --server-side -f config/agentgateway >/dev/null
kubectl wait --for=condition=Established crd/agentgatewaybackends.agentgateway.dev crd/agentgatewaypolicies.agentgateway.dev \
  crd/agentgatewayparameters.agentgateway.dev crd/agentgatewaymodels.agentgateway.dev --timeout=120s
helm install intake-crds charts/agent-factory-crds -n "$NS" --create-namespace --timeout 2m
kubectl wait --for=condition=Established crd -l app.kubernetes.io/part-of=agent-factory --timeout=120s
helm install intake charts/agent-factory -n "$NS" --create-namespace "${values[@]}" --timeout 5m

step "wait for the database, the controllers and the model gateway"
kubectl -n "$NS" rollout status sts/rooms-db --timeout=180s
kubectl -n "$NS" rollout status deploy/room-broker --timeout=180s
kubectl -n "$NS" rollout status deploy/run-controller --timeout=180s
kubectl -n "$NS" rollout status deploy/agent-factory --timeout=180s
kubectl -n "$NS" rollout status deploy/intake-agentgateway --timeout=180s
kubectl -n "$NS" wait --for=condition=Programmed gateway/model-gateway --timeout=180s
kubectl -n "$NS" rollout status deploy/model-gateway --timeout=180s

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
export E2E_STUB_URL="https://127.0.0.1:$stub_port" E2E_STUB_CA_FILE="$work/ca.crt"
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
"$work/bin/tls-forward" "$work/maku.crt" "$work/maku.key" "127.0.0.1:$broker_port" "$work/maku-forward.addr" \
  >"$work/tls-forward.log" 2>&1 &
pf_pids+=("$!")
for _ in $(seq 30); do
  [[ -s $work/maku-forward.addr ]] && break
  sleep 1
done
[[ -s $work/maku-forward.addr ]] || die 'the local MCP TLS forward did not start'
"$work/bin/maku" configure --url "https://$(cat "$work/maku-forward.addr")" \
  --issuer https://e2e-stub.e2e.svc.cluster.local --client-id roomctl --project-id project \
  --factory-login 'maku-demo[bot]'
jq -n --arg token "$(cat "$work/token")" '{access_token: $token, token_type: "Bearer"}' >"$work/config/maku/token.json"
text='Document the verification command in README.md.'

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

step "planner: the plan came through the gateway's /clients/planner/v1, the model forced, the key the gateway's"
# The stub records what each model call carried, its Authorization as a digest. Through the gateway
# the model is clients.planner's mapped model and the bearer is the provider key from its Secret: the
# broker's projected token never reaches the provider.
key_digest="sha256:$(printf 'Bearer %s' "$PROVIDER_KEY" | sha256sum | cut -d' ' -f1)"
last_received() { # last_received ROLE: the stub's last recorded call of that role
  curl_stub "$stub_url/__received" | jq -c --arg role "$1" '[.[] | select(.role == $role)] | last'
}
got=$(last_received planner)
jq -e --arg auth "$key_digest" '.model == "stub-frontier" and .path == "/v1/chat/completions" and
  .authorization == $auth and .reasoning_effort == "low" and .temperature == 0' <<<"$got" >/dev/null ||
  die "the provider did not receive the planner's call through the gateway: $got"

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

step "classifier: the broker's call through /clients/classifier/v1, on its own audience"
# The broker serves intent_classify to the Slack App only, behind a sponsored IdP client and a verified
# Slack link the stub does not emulate. The call itself is the broker's code (internal/intent), made by
# the scenario client with a token of the broker's own ServiceAccount, through the gateway.
start_port_forward "$NS" svc/model-gateway 8082 "$work/port-forward-gateway.log" gw_port
kubectl -n "$NS" create token room-broker --audience maku-gateway.client.classifier --duration 10m >"$work/classifier-token"
kubectl -n "$NS" create token room-broker --audience maku-gateway.client.planner --duration 10m >"$work/planner-token"
"$client" classify --url "http://127.0.0.1:$gw_port/clients/classifier/v1" --token-file "$work/classifier-token" \
  --reply 'where is it at?' >"$work/classify.out"
assert_eq "$(field intent "$work/classify.out")" status 'classifier intent'
assert_eq "$(field outcome "$work/classify.out")" ok 'classifier outcome'
got=$(last_received classifier)
jq -e --arg auth "$key_digest" '.model == "stub-light" and .authorization == $auth and .temperature == 0' <<<"$got" >/dev/null ||
  die "the provider did not receive the classifier's call through the gateway: $got"

step "a client's token answers 403 on the other client's route"
gw_status() { # gw_status TOKEN_FILE PATH: the gateway's status for one completion
  curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(cat "$1")" -H 'Content-Type: application/json' \
    --data '{"model":"tier-frontier","messages":[{"role":"user","content":"hello"}]}' "http://127.0.0.1:$gw_port$2"
}
assert_eq "$(gw_status "$work/classifier-token" /clients/planner/v1/chat/completions)" 403 'the classifier token on the planner route'
assert_eq "$(gw_status "$work/planner-token" /clients/classifier/v1/chat/completions)" 403 'the planner token on the classifier route'

# quirk PRESET MODEL CHECK WHAT: the planner on a provider of that preset, then one plan whose call the
# stub must have received as CHECK says. A preset owns its endpoint (only `custom` takes one), so the
# rendered backend is pointed at the stub after the upgrade: its transformations stay the chart's.
quirk() {
  local preset=$1 model=$2 check=$3 what=$4 got=''
  step "planner on the $preset preset: $what"
  helm upgrade intake charts/agent-factory -n "$NS" "${values[@]}" \
    --set-json "maku-gateway.providers.$preset={\"preset\":\"$preset\",\"existingSecret\":\"e2e-provider-key\"}" \
    --set "maku-gateway.clients.planner.provider=$preset" --set "maku-gateway.clients.planner.model=$model" --timeout 5m >/dev/null
  kubectl -n "$NS" patch agentgatewaybackend "$preset-planner" --type=json -p '[
    {"op":"replace","path":"/spec/ai/provider/host","value":"e2e-stub.e2e.svc.cluster.local"},
    {"op":"replace","path":"/spec/ai/provider/port","value":8080},
    {"op":"replace","path":"/spec/ai/provider/custom/formats/0/path","value":"/v1/chat/completions"},
    {"op":"remove","path":"/spec/policies/tls"}]' >/dev/null
  for _ in $(seq 30); do
    if "$client" plan --text "$text" --repo owner/repo --out "$work/quirk-$preset.json" >"$work/quirk-$preset.out" 2>&1; then
      got=$(last_received planner)
      jq -e --arg model "$model" '.model == $model' <<<"$got" >/dev/null && break
    fi
    sleep 2
  done
  jq -e --arg model "$model" --arg auth "$key_digest" ".model == \$model and .authorization == \$auth and ($check)" <<<"$got" >/dev/null ||
    { cat "$work/quirk-$preset.out" >&2; die "the $preset preset: $what (the provider received $got)"; }
}
quirk mistral mistral-small-2603 '.reasoning_effort == "none"' 'reasoning_effort "none" on the wire, though the broker sent low'
quirk moonshot k3.1-pro '.temperature == 1 and .top_p == "<absent>"' 'temperature 1 and no top_p on the wire, though the broker sent 0'
echo 'ok: the planner and the classifier through the gateway, provider quirks applied'

issue_body=$(cat <<'EOF'
# Document the verification command

## Acceptance check
The documentation names a verification command that succeeds.

## Decisions
D1: retain the original decision
EOF
)

step "TestStatusCommentGeneration: scoper question has one status and separate events"
issue=$(new_issue 'docs: answer the scoper question' "$issue_body")
wait_question "$issue"
question_json="$work/question-issue.json"
wait_status_generation "$issue" 1 "$question_json"
question_status_id=$(status_id "$question_json")
[[ $question_status_id =~ ^[0-9]+$ ]] || die "invalid generation-1 status ID: $question_status_id"
status_body "$question_json" >"$work/question-status.txt"
grep -qF 'Factory status · escalated' "$work/question-status.txt" || die 'the question status is not escalated'
grep -qF 'generation=1 -->' "$work/question-status.txt" || die 'the question status is not generation 1'
jq -S '[.comments[] | select(.body | contains("event=needs-author-")) | {id, body}]' \
  "$question_json" >"$work/question-events.json"
jq -e 'length >= 1' "$work/question-events.json" >/dev/null || die 'the scoper bounce has no separate event comment'
if jq -e --argjson status "$question_status_id" 'any(.[]; .id == $status)' "$work/question-events.json" >/dev/null; then
  die 'the status and needs-author event share a comment ID'
fi
room1=$(room_for_issue "$issue")
"$client" status --room "$room1" >"$work/question-brief.out"
assert_eq "$(field apiVersion "$work/question-brief.out")" 'brief/v1' 'room_brief API version'
assert_eq "$(field question.text "$work/question-brief.out")" \
  'Which command or observation proves the change is done?' 'structured scoper question'
assert_eq "$(field question.recommended "$work/question-brief.out")" \
  'Run task check to prove the change.' 'structured scoper recommendation'

step "need_answer: append only D2 and leave the maintainer to re-apply ready"
issue_view "$issue" >"$work/before-answer.json"
"$client" answer --repo owner/repo --issue "$issue" --room "$room1" --answer 'run task check' >"$work/answer.out"
assert_eq "$(field answered "$work/answer.out")" true 'need_answer answered'
assert_eq "$(field started "$work/answer.out")" false 'need_answer started'
assert_eq "$(field next "$work/answer.out")" 'a maintainer must re-apply factory/ready' 'need_answer next action'
issue_view "$issue" >"$work/after-answer.json"
jq -r .body "$work/before-answer.json" >"$work/before-answer.body"
jq -r .body "$work/after-answer.json" >"$work/after-answer.body"
{ cat "$work/before-answer.body"; printf 'D2: run task check\n'; } >"$work/want-answer.body"
cmp -s "$work/want-answer.body" "$work/after-answer.body" || {
  diff -u "$work/want-answer.body" "$work/after-answer.body" >&2 || true
  die 'need_answer did not append exactly D2 to the issue body'
}
jq -e '[.labels[].name] |
  ((index("factory/needs-author") != null) and (index("factory/ready") == null))' \
  "$work/after-answer.json" >/dev/null || die 'need_answer changed labels or applied factory/ready'

step "TestStatusCommentGeneration: generation 2 adopts the ID and generation 1 stands down"
gh issue edit "$issue" --repo=owner/repo --add-label=factory/ready
ready_json="$work/ready-issue.json"
wait_status_generation "$issue" 2 "$ready_json"
assert_eq "$(status_id "$ready_json")" "$question_status_id" 'status ID across question and generation 2'
status_body "$ready_json" >"$work/ready-status.txt"
if cmp -s "$work/question-status.txt" "$work/ready-status.txt"; then
  die 'generation 2 did not change the status content'
fi
jq -S '[.comments[] | select(.body | contains("event=needs-author-")) | {id, body}]' \
  "$ready_json" >"$work/ready-events.json"
cmp -s "$work/question-events.json" "$work/ready-events.json" || die 'status edits changed the separate event comments'
gen1_task=$(grep -oE 'task=[a-z2-7]{8} generation=1' "$work/question-status.txt" | cut -d= -f2 | cut -d' ' -f1)
[[ $gen1_task =~ ^[a-z2-7]{8}$ ]] || die "could not identify the generation-1 Task: $gen1_task"
# Force the recovery path: an active Task with no persisted ID must list comments before writing.
kubectl -n "$NS" patch task "$gen1_task" --subresource=status --type=merge -p '{"status":{"statusComment":{"id":0}}}' >/dev/null
old_stood_down=false
for _ in $(seq 60); do
  inactive=$(kubectl -n "$NS" get task "$gen1_task" -o jsonpath='{.status.statusComment.inactive}' 2>/dev/null || true)
  if [[ $inactive == true ]]; then old_stood_down=true; break; fi
  sleep 2
done
$old_stood_down || die 'generation 1 did not record status ownership stand-down'
issue_view "$issue" >"$work/after-old-generation.json"
assert_eq "$(status_id "$work/after-old-generation.json")" "$question_status_id" 'status ID after old-generation reconcile'
status_body "$work/after-old-generation.json" >"$work/after-old-status.txt"
grep -qF 'generation=2 -->' "$work/after-old-status.txt" || die 'generation 1 overwrote generation 2 ownership'
if grep -qF 'generation=1 -->' "$work/after-old-status.txt"; then
  die 'generation 1 restored its stale status content'
fi

step "TestNeedAnswerConflict: body and label mutations write no Decision"
conflict_issue=$(new_issue 'docs: reject a stale answer' "$issue_body")
wait_question "$conflict_issue"
conflict_room=$(room_for_issue "$conflict_issue")
issue_view "$conflict_issue" >"$work/conflict-initial.json"
jq -r '"External body edit.\n\n" + .body' "$work/conflict-initial.json" >"$work/conflict-external.body"
if "$client" answer --repo owner/repo --issue "$conflict_issue" --room "$conflict_room" \
  --answer 'stale answer' --conflict-body-file "$work/conflict-external.body" \
  >"$work/conflict-body.out" 2>"$work/conflict-body.err"; then
  die 'need_answer accepted a body conflict'
fi
grep -qF 'issue_changed' "$work/conflict-body.err" || {
  cat "$work/conflict-body.err" >&2; die 'body conflict did not return issue_changed';
}
issue_view "$conflict_issue" >"$work/conflict-after-body.json"
jq -r .body "$work/conflict-after-body.json" >"$work/conflict-after-body.body"
printf '\n' >>"$work/conflict-external.body"
cmp -s "$work/conflict-external.body" "$work/conflict-after-body.body" || {
  diff -u "$work/conflict-external.body" "$work/conflict-after-body.body" >&2 || true
  die 'body conflict wrote beyond the external edit'
}
if grep -qF 'D2: stale answer' "$work/conflict-after-body.body"; then die 'body conflict appended a Decision'; fi
jq -S '.labels' "$work/conflict-initial.json" >"$work/conflict-labels-before.json"
jq -S '.labels' "$work/conflict-after-body.json" >"$work/conflict-labels-after-body.json"
cmp -s "$work/conflict-labels-before.json" "$work/conflict-labels-after-body.json" || die 'body conflict changed labels'

if "$client" answer --repo owner/repo --issue "$conflict_issue" --room "$conflict_room" \
  --answer 'stale answer' --conflict-label factory/conflict \
  >"$work/conflict-label.out" 2>"$work/conflict-label.err"; then
  die 'need_answer accepted a label conflict'
fi
grep -qF 'issue_changed' "$work/conflict-label.err" || {
  cat "$work/conflict-label.err" >&2; die 'label conflict did not return issue_changed';
}
issue_view "$conflict_issue" >"$work/conflict-after-label.json"
jq -r .body "$work/conflict-after-label.json" >"$work/conflict-after-label.body"
cmp -s "$work/conflict-after-body.body" "$work/conflict-after-label.body" || die 'label conflict edited the body'
jq -e '[.labels[].name] |
  ((index("factory/conflict") != null) and (index("factory/ready") == null))' \
  "$work/conflict-after-label.json" >/dev/null || die 'label conflict did not preserve exactly the external label mutation'
if jq -e '.body | contains("D2: stale answer")' "$work/conflict-after-label.json" >/dev/null; then
  die 'label conflict appended a Decision'
fi

step 'scoper answers, conflict refusal and status generations passed'
echo 'spec-intake k3d OK'
