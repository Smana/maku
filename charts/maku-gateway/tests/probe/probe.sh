#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# SC2015: `test && pass || fail` is the point (pass never fails). SC2329: onexit runs from the trap.
# shellcheck disable=SC2015,SC2329
#
# The agentgateway probe (plan v4 step 4, Task 1): what maku-gateway relies on, asserted against
# agentgateway v1.6.0 on a throwaway k3d. Each assertion prints PASS or FAIL <what>; the exit code
# is the number of failures (0 = every assumption holds).
#   P1  JWKS: a run-like ServiceAccount's projected token is verified with the issuer's keys fetched
#       from the in-cluster apiserver (source: apiserver), without and then with the anonymous
#       discovery binding; a token for another audience from the same ServiceAccount is refused.
#   P2  CEL over jwt.aud, a list: `exists(... startsWith ...)` on the listener, `in` on the route; the
#       listener's subject Require holds beside the route's: a non-run ServiceAccount with the right
#       audience is refused.
#   P3  path and model: /m/<alias>/v1/chat/completions, matched exactly, reaches the provider at its own
#       path and nothing else does; the model is the alias's mapped model whatever the body said, and
#       the token never reaches the provider.
#   P4  presets: reasoning_effort "none" on a mistral backend, temperature 1 and no top_p on moonshot.
#   P5  metrics: the token-usage histogram carries ar_agent (the sub), the mapped model and its token
#       types; a 429 shows on a request counter carrying ar_agent.
#   P6  the Gateway API floor: the controller programs the Gateway and its routes on GATEWAY_API.
# Usage: probe.sh   (docker, k3d, helm, kubectl, jq). KEEP=1 keeps the cluster for a look around.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
NAME=maku-gw-probe
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.35.0-k3s1}"
GATEWAY_API="${GATEWAY_API:-v1.2.1}"
AGW=v1.6.0
CONTROLLER_IMAGE_TAG="$AGW@sha256:d84259a7e2d87f8f39bae385d4b032d823c0a4450aecb32e0e31e2258449645b"
work="$(mktemp -d)"
export KUBECONFIG="$work/kubeconfig"
fails=0
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; fails=$((fails + 1)); }
onexit() {
  local rc=$?
  if [[ ${KEEP:-} == 1 ]]; then echo "kept: KUBECONFIG=$KUBECONFIG; k3d cluster delete $NAME"; return; fi
  k3d cluster delete "$NAME" >/dev/null 2>&1 || true
  rm -rf "$work"
  exit "$rc"
}

! k3d cluster get "$NAME" >/dev/null 2>&1 || { echo "FAIL: cluster $NAME exists; delete it first" >&2; exit 2; }
trap onexit EXIT
# k3s turns anonymous requests off; the anonymous discovery binding (P1) needs them on, as kubeadm
# and kind have them. K3S_EXTRA_ARGS: more k3s flags, as charts/agent-factory/tests/install.sh.
k3s_args=(--k3s-arg '--disable=traefik@server:0' --k3s-arg '--disable=servicelb@server:0'
  --k3s-arg '--kube-apiserver-arg=anonymous-auth=true@server:0')
for a in ${K3S_EXTRA_ARGS:-}; do k3s_args+=(--k3s-arg "$a@server:0"); done
k3d cluster create "$NAME" --image "$K3S_IMAGE" --no-lb --wait "${k3s_args[@]}" \
  --kubeconfig-update-default=false --kubeconfig-switch-context=false >/dev/null
k3d kubeconfig get "$NAME" >"$KUBECONFIG"

echo "==> Gateway API $GATEWAY_API standard, agentgateway $AGW (CRDs, controller)"
curl -fsSL "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GATEWAY_API/standard-install.yaml" \
  | kubectl apply --server-side -f - >/dev/null
helm install agentgateway-crds oci://cr.agentgateway.dev/charts/agentgateway-crds --version "$AGW" \
  -n agentgateway-system --create-namespace >/dev/null
helm install agentgateway oci://cr.agentgateway.dev/charts/agentgateway --version "$AGW" -n agentgateway-system \
  --set controller.image.tag="$CONTROLLER_IMAGE_TAG" --set istio.enabled=false >/dev/null
kubectl -n agentgateway-system rollout status deploy/agentgateway --timeout=180s >/dev/null

echo "==> the probe's objects"
for ns in maku agents; do
  kubectl create namespace "$ns" >/dev/null
  kubectl -n "$ns" create configmap probe-scripts --from-file=echo-llm.py --from-file=client.py >/dev/null
done
issuer="$(kubectl get --raw /.well-known/openid-configuration | jq -r .issuer)"
sed "s#__ISSUER__#$issuer#" manifests.yaml | kubectl apply -f - >/dev/null
kubectl -n agents wait --for=condition=Ready pod/caller pod/bystander --timeout=180s >/dev/null
kubectl -n maku rollout status deploy/echo-llm --timeout=180s >/dev/null
kubectl -n maku wait --for=condition=Programmed gateway/model-gateway --timeout=180s >/dev/null
kubectl -n maku rollout status deploy/model-gateway --timeout=180s >/dev/null

PUB=http://model-gateway.maku.svc.cluster.local:8080
INT=http://model-gateway.maku.svc.cluster.local:8081
# call URL TOKEN MODEL [MESSAGE]: "<status> <body>" from the run-like caller
call() { kubectl -n agents exec caller -- python3 /app/client.py "$@"; }
# report "<status> <body>": the provider's report (the completion's content) as JSON
report() { sed -E 's/^[0-9]+ //' <<<"$1" | jq -c '.choices[0].message.content | fromjson' 2>/dev/null || echo '{}'; }
# eventually DESC STATUS ARGS...: the call answers STATUS within 90 s (the controller fetches and
# pushes the JWKS asynchronously); prints the last answer.
eventually() {
  local want=$1 out=''; shift
  for _ in $(seq 30); do
    out="$(call "$@")"
    [[ ${out%% *} == "$want" ]] && break
    sleep 3
  done
  printf '%s' "$out"
}

echo "==> P6: Gateway API $GATEWAY_API"
routes="$(kubectl -n maku get httproute -o json | jq -r '[.items[] | .status.parents[]?.conditions[]? | select(.type == "Accepted" and .status == "True")] | length')"
total="$(kubectl -n maku get httproute -o json | jq '.items | length')"
if [[ $routes == "$total" && $total -gt 0 ]]; then pass "P6 the Gateway is Programmed and its $total routes Accepted on Gateway API $GATEWAY_API standard"
else fail "P6 $routes of $total routes Accepted on Gateway API $GATEWAY_API"; fi

echo "==> P1: JWKS from the apiserver"
out="$(call "$PUB/m/tier-light/v1/chat/completions" /tokens/public-tier-light tier-light)"
for _ in $(seq 10); do [[ ${out%% *} == 200 ]] && break; sleep 3; out="$(call "$PUB/m/tier-light/v1/chat/completions" /tokens/public-tier-light tier-light)"; done
if [[ ${out%% *} == 200 ]]; then pass "P1 without the discovery binding: the token is verified"
else pass "P1 without the discovery binding: refused (${out:0:120}), as expected: the controller fetches the JWKS anonymously"; nobinding=1; fi
kubectl create clusterrolebinding maku-gateway-oidc-discovery --clusterrole=system:service-account-issuer-discovery \
  --group=system:unauthenticated >/dev/null
out="$(eventually 200 "$PUB/m/tier-light/v1/chat/completions" /tokens/public-tier-light tier-light)"
if [[ ${out%% *} == 200 ]]; then pass "P1 with the discovery binding: a run-like token for maku-gateway.public.tier-light is accepted"
else fail "P1 with the discovery binding: ${out:0:200}"; fi
out="$(call "$PUB/m/tier-light/v1/chat/completions" /tokens/room-broker tier-light)"
[[ ${out%% *} == 403 || ${out%% *} == 401 ]] && pass "P1 a room-broker token from the same ServiceAccount is refused (${out%% *})" || fail "P1 a room-broker token: ${out:0:200}"
out="$(call "$PUB/m/tier-light/v1/chat/completions" - tier-light)"
[[ ${out%% *} == 401 ]] && pass "P1 no token: 401" || fail "P1 no token: ${out:0:200}"

echo "==> P2: CEL over jwt.aud"
out="$(call "$INT/m/tier-light/v1/chat/completions" /tokens/public-tier-light tier-light)"
[[ ${out%% *} == 403 ]] && pass "P2 exists/startsWith: a public token on the internal listener is 403" || fail "P2 a public token on internal: ${out:0:200}"
out="$(call "$INT/m/tier-light/v1/chat/completions" /tokens/internal-tier-light tier-light)"
[[ ${out%% *} == 200 ]] && pass "P2 exists/startsWith: an internal token on the internal listener is 200" || fail "P2 an internal token on internal: ${out:0:200}"
out="$(call "$PUB/m/tier-frontier/v1/chat/completions" /tokens/public-tier-light tier-frontier)"
[[ ${out%% *} == 403 ]] && pass "P2 in: a tier-light token on the tier-frontier route is 403" || fail "P2 a tier-light token on tier-frontier: ${out:0:200}"
out="$(kubectl -n agents exec bystander -- python3 /app/client.py "$PUB/m/tier-light/v1/chat/completions" /tokens/public-tier-light tier-light)"
[[ ${out%% *} == 403 ]] && pass "P2 the listener's subject Require holds beside the route's: a non-run ServiceAccount with the right audience is 403" \
  || fail "P2 a non-run ServiceAccount with maku-gateway.public.tier-light: ${out:0:200}"

echo "==> P3: path and model"
out="$(call "$PUB/m/tier-light/v1/embeddings" /tokens/public-tier-light tier-light)"
[[ ${out%% *} == 404 ]] && pass "P3 exact paths: another path under the alias is 404, never sent to the provider" || fail "P3 /m/tier-light/v1/embeddings answered ${out:0:200}"
out="$(call "$PUB/m/tier-light/v1/chat/completions" /tokens/public-tier-light kimi-frontier)"
r="$(report "$out")"
[[ ${out%% *} == 200 ]] || fail "P3 the call answered ${out:0:200}"
[[ $(jq -r .path <<<"$r") == /provider/v4/chat/completions ]] && pass "P3 the provider is reached at its format path" || fail "P3 path: $r"
[[ $(jq -r .model <<<"$r") == echo-light-1 ]] && pass "P3 the model is the alias's mapped model though the body said kimi-frontier" || fail "P3 model: $r"
[[ $(jq -r .authorization <<<"$r") == '<absent>' ]] && pass "P3 the caller's token never reaches the provider" || fail "P3 authorization reached the provider: $r"
[[ $(jq -r '.["x-ar-agent"]' <<<"$r") == system:serviceaccount:agents:run-abcdefgh && $(jq -r '.["x-ar-human"]' <<<"$r") == '<absent>' ]] \
  && pass "P3 x-ar-agent is the verified sub; the forged identity headers are stripped" || fail "P3 identity headers: $r"

echo "==> P4: presets"
r="$(report "$(call "$PUB/m/mistral-light/v1/chat/completions" /tokens/public-mistral-light tier-light)")"
[[ $(jq -r .reasoning_effort <<<"$r") == none && $(jq -r .model <<<"$r") == mistral-small-2603 ]] \
  && pass "P4 mistral: reasoning_effort \"none\" though the caller sent low" || fail "P4 mistral: $r"
r="$(report "$(call "$PUB/m/kimi-frontier/v1/chat/completions" /tokens/public-kimi-frontier tier-light)")"
jq -e '.temperature == 1 and .top_p == "<absent>" and .model == "k3.1-pro"' <<<"$r" >/dev/null \
  && pass "P4 moonshot: temperature 1 and no top_p though the caller sent 0 and 0.5" || fail "P4 moonshot: $r"

echo "==> P5: metrics"
out="$(call "$PUB/m/tier-light/v1/chat/completions" /tokens/public-tier-light tier-light THROTTLE)"
[[ ${out%% *} == 429 ]] || fail "P5 the provider's 429 answered ${out:0:200}"
pod="$(kubectl -n maku get pod -l gateway.networking.k8s.io/gateway-name=model-gateway -o jsonpath='{.items[0].metadata.name}')"
kubectl -n maku port-forward "pod/$pod" 15020:15020 >/dev/null 2>&1 &
pf=$!
sleep 3
metrics="$(curl -fsS http://127.0.0.1:15020/metrics || true)"
kill "$pf" 2>/dev/null || true
printf '%s\n' "$metrics" >"$work/metrics.txt"
sub='ar_agent="system:serviceaccount:agents:run-abcdefgh"'
usage="$(grep -E '^agentgateway_gen_ai_client_token_usage_sum\{' <<<"$metrics" | grep -F "$sub" || true)"
[[ -n $usage ]] && pass "P5 agentgateway_gen_ai_client_token_usage_sum carries ar_agent = the sub" || fail "P5 no token-usage series with $sub"
grep -qF 'gen_ai_request_model="echo-light-1"' <<<"$usage" && pass "P5 gen_ai_request_model is the mapped model" || fail "P5 the usage series lacks the mapped model"
types="$(grep -oE 'gen_ai_token_type="[a-z_]+"' <<<"$usage" | sort -u | sed -E 's/.*="(.*)"/\1/' | paste -sd, -)"
bad="$(tr , '\n' <<<"$types" | grep -vxE 'input|output|input_cache_read|input_cache_write' || true)"
[[ -n $types && -z $bad ]] && pass "P5 token types: $types" || fail "P5 token types: '$types'"
throttled="$(grep -E '^agentgateway_requests_total\{' <<<"$metrics" | grep -F "$sub" | grep -F 'status="429"' || true)"
[[ -n $throttled ]] && pass "P5 a 429 shows on agentgateway_requests_total{status=\"429\"}, carrying ar_agent" \
  || fail "P5 agentgateway_requests_total has no status 429 series with $sub"

echo "==> $fails failure(s); agentgateway $AGW, Gateway API $GATEWAY_API, $(kubectl version -o json | jq -r .serverVersion.gitVersion)"
[[ -z ${nobinding:-} ]] || echo "NOTE source: apiserver needs identity.jwks.allowAnonymousDiscovery (the controller's JWKS fetch is anonymous)"
exit "$fails"
