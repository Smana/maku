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
#       listener's subject Require is enforced with the route's: a non-run ServiceAccount with the right
#       audience is refused.
#   P3  path and model: /m/<alias>/v1/chat/completions, matched exactly, reaches the provider at its own
#       path and nothing else does; the model is the alias's mapped model whatever the body said, and
#       the token never reaches the provider.
#   P4  presets: reasoning_effort "none" on a mistral backend, temperature 1 and no top_p on moonshot.
#   P5  metrics: the token-usage histogram carries ar_agent (the sub), the mapped model and its token
#       types; a 429 shows on a request counter carrying ar_agent.
#   P6  the Gateway API floor: the controller programs the Gateway and its routes on GATEWAY_API.
#   P7  gemini and vertex (Gemini design G1, G5, G8): the chart's own backends against fake-gemini.py,
#       which enforces thought signatures. vertex: which Vertex path the native provider calls, the
#       metadata server's token and no key, reasoning_effort as a thinking level, a two-tool loop
#       from a caller that drops unknown fields, the catalog key and the cost. gemini: the
#       OpenAI-compatible path and key, and what a caller must echo for the signature.
# Usage: probe.sh   (docker, k3d, helm, kubectl, jq, yq, openssl). KEEP=1 keeps the cluster for a look around.
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
  kubectl -n "$ns" create configmap probe-scripts --from-file=echo-llm.py --from-file=client.py \
    --from-file=fake-gemini.py --from-file=tool-loop.py >/dev/null
done
# P7: a CA of the probe's own signs fake-gemini's certificate for Google's hostnames.
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=probe-ca -keyout "$work/ca.key" -out "$work/ca.crt" 2>/dev/null
openssl req -newkey rsa:2048 -nodes -subj /CN=fake-gemini -keyout "$work/tls.key" -out "$work/tls.csr" 2>/dev/null
printf 'subjectAltName=DNS:aiplatform.eu.rep.googleapis.com,DNS:generativelanguage.googleapis.com\n' >"$work/san.ext"
openssl x509 -req -in "$work/tls.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" -CAcreateserial -days 1 \
  -extfile "$work/san.ext" -out "$work/tls.crt" 2>/dev/null
kubectl -n maku create secret tls fake-gemini-tls --cert="$work/tls.crt" --key="$work/tls.key" >/dev/null
kubectl -n maku create configmap probe-ca --from-file=ca.crt="$work/ca.crt" >/dev/null
kubectl -n maku create secret generic probe-gemini-key --from-literal=apiKey=probe-gemini-key-value >/dev/null # pragma: allowlist secret
kubectl apply -f gemini.yaml >/dev/null
# The chart's own gemini and vertex objects; each backend trusts the probe CA for its host.
helm template model-gateway ../.. -n maku -f gemini-values.yaml \
  | yq e 'select((.kind == "AgentgatewayBackend" and (.metadata.name | test("^(vertex|gemini)-"))) or (.kind == "HTTPRoute" and (.metadata.name | test("gemini"))) or (.kind == "AgentgatewayPolicy" and (.spec.targetRefs[0].name // "" | test("gemini"))) or .kind == "ConfigMap")
      | (select(.metadata.name == "vertex-gemini-standard") | .spec.policies.tls) = {"sni": "aiplatform.eu.rep.googleapis.com", "caCertificateRefs": [{"name": "probe-ca"}]}
      | (select(.metadata.name == "gemini-gemini-light") | .spec.policies.tls.caCertificateRefs) = [{"name": "probe-ca"}]' - \
  | kubectl -n maku apply -f - >/dev/null
kubectl -n maku rollout status deploy/fake-gemini --timeout=180s >/dev/null
ip="$(kubectl -n maku get svc fake-gemini -o jsonpath='{.spec.clusterIP}')"
kubectl -n kube-system create configmap coredns-custom --from-literal=gemini.server="aiplatform.eu.rep.googleapis.com:53 generativelanguage.googleapis.com:53 {
    hosts {
        $ip aiplatform.eu.rep.googleapis.com generativelanguage.googleapis.com
    }
}" >/dev/null
kubectl -n kube-system rollout restart deploy/coredns >/dev/null
kubectl -n kube-system rollout status deploy/coredns --timeout=180s >/dev/null
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
[[ ${out%% *} == 403 ]] && pass "P2 a non-run ServiceAccount holding the right audience is 403: the listener's subject Require is enforced together with the route's audience Require" \
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

echo "==> P7: gemini and vertex"
# loop ALIAS MODE: the two-tool loop's JSON line, retried while the route is not yet programmed.
loop() {
  local out=''
  for _ in $(seq 20); do
    out="$(kubectl -n agents exec caller -- python3 /app/tool-loop.py "$PUB/m/$1/v1/chat/completions" "/tokens/public-$1" "$1" "$2")"
    jq -e '.statuses[0] != 404 and .statuses[0] != 503' <<<"$out" >/dev/null 2>&1 && break
    sleep 3
  done
  printf '%s' "$out"
}
v="$(loop gemini-standard drop)"
echo "    vertex: $v"
r="$(jq -c '.report // {}' <<<"$v")"
[[ $(jq -c .statuses <<<"$v") == '[200,200,200]' ]] && pass "P7 vertex: a two-tool loop from a caller that drops unknown fields succeeds (the signature rides in the tool call id)" \
  || fail "P7 vertex: the tool loop: $v"
[[ $(jq -r .path <<<"$r") == /v1/projects/probe-project/locations/eu/publishers/google/models/gemini-4-neon:generateContent ]] \
  && pass "P7 vertex: the native provider calls generateContent on the publisher model, not endpoints/openapi" || fail "P7 vertex path: $r"
[[ $(jq -r .host <<<"$r") == aiplatform.eu.rep.googleapis.com ]] && pass "P7 vertex: region eu dials aiplatform.eu.rep.googleapis.com" || fail "P7 vertex host: $r"
[[ $(jq -r .auth <<<"$r") == fake-wi-token ]] && pass "P7 vertex: the proxy sends the metadata server's access token, and holds no key" || fail "P7 vertex auth: $r"
jq -e '.thinkingConfig == {"thinkingLevel": "low"}' <<<"$r" >/dev/null \
  && pass "P7 vertex: reasoning_effort low arrives as thinkingLevel low, no thinkingBudget (Gemini 4 refuses a budget)" || fail "P7 vertex thinkingConfig: $r"
jq -e '.signatures == ["sig-1", "sig-2"]' <<<"$r" >/dev/null && pass "P7 vertex: both signatures came back on their function calls" || fail "P7 vertex signatures: $r"
jq -e '[.ids[] | contains("__thought__sig-")] | all' <<<"$v" >/dev/null && pass "P7 vertex: each tool call id carries its signature (<id>__thought__<sig>)" || fail "P7 vertex ids: $v"
jq -e '.usage.completion_tokens == 30 and .usage.prompt_tokens == 100' <<<"$v" >/dev/null \
  && pass "P7 vertex: thinking tokens count as output (10 candidates + 20 thoughts = 30)" || fail "P7 vertex usage: $v"
hits="$(kubectl -n maku exec deploy/fake-gemini -- python3 -c 'import urllib.request; print(urllib.request.urlopen("http://127.0.0.1:8080/report").read().decode())')"
jq -e '[.mds[] | select(.path | contains("/service-accounts/default/token"))] | length > 0' <<<"$hits" >/dev/null \
  && pass "P7 vertex: the token came from the metadata server" || fail "P7 vertex metadata server hits: $hits"
sa="$(kubectl -n maku get deploy model-gateway -o jsonpath='{.spec.template.spec.serviceAccountName}')"
[[ $sa == model-gateway ]] && pass "P7 the proxy runs as ServiceAccount model-gateway (the Gateway's name): the one Workload Identity binds" || fail "P7 proxy ServiceAccount: $sa"
pod="$(kubectl -n maku get pod -l gateway.networking.k8s.io/gateway-name=model-gateway -o jsonpath='{.items[0].metadata.name}')"
costs="$(kubectl -n maku logs "$pod" | jq -Rrc 'fromjson? | select(."gen_ai.provider.name" == "gcp.vertex_ai" and ."http.status" == 200) | ."cost.total"' | sort -u | paste -sd' ' -)"
# 100 input at 3.00 and 30 output (thinking included) at 15.00 per million: 0.00075 a call.
[[ $costs == 0.00075 ]] && pass "P7 vertex: each call's cost.total is 0.00075, priced under gcp.vertex_ai, thinking at the output rate" \
  || fail "P7 vertex cost: the gcp.vertex_ai calls' cost.total are '$costs'"
g="$(loop gemini-light echo)"
echo "    gemini, echo: $g"
r="$(jq -c '.report // {}' <<<"$g")"
[[ $(jq -c .statuses <<<"$g") == '[200,200,200]' ]] && pass "P7 gemini: a caller that echoes extra_content round-trips the signature through the gateway" || fail "P7 gemini echo: $g"
jq -e '.path == "/v1beta/openai/chat/completions" and .host == "generativelanguage.googleapis.com" and .model == "gemini-4-hydrogen" and .reasoning_effort == "low" and .auth == "bearer len=22"' <<<"$r" >/dev/null \
  && pass "P7 gemini: the OpenAI-compatible path, the mapped model, reasoning_effort as sent, the key from the Secret" || fail "P7 gemini request: $r"
g="$(loop gemini-light drop)"
echo "    gemini, drop: $g"
[[ $(jq -c .statuses <<<"$g") == '[200,400]' ]] && pass "P7 gemini: a caller that drops extra_content gets the 400 on its second turn (the caller's job, not the gateway's)" \
  || fail "P7 gemini drop: $g"

echo "==> $fails failure(s); agentgateway $AGW, Gateway API $GATEWAY_API, $(kubectl version -o json | jq -r .serverVersion.gitVersion)"
[[ -z ${nobinding:-} ]] || echo "NOTE source: apiserver needs identity.jwks.allowAnonymousDiscovery (the controller's JWKS fetch is anonymous)"
exit "$fails"
