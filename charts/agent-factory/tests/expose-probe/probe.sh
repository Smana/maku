#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# SC2015: `test && pass || fail` is the point (pass never fails). SC2329: onexit runs from the trap.
# shellcheck disable=SC2015,SC2329
#
# The exposure probe (plan v4 step 6, Task 9): what expose.mode and the web front door rely on,
# asserted on a throwaway k3d with Traefik (Ingress), agentgateway (Gateway API, the vendored
# controller) and oauth2-proxy (webProxy.image) pointed at the e2e stub issuer. Each assertion
# prints PASS or FAIL <what>; RECORD lines are findings the chart reads, not pass/fail. The exit
# code is the number of failures.
#   X1  an HTTPRoute Exact /slack/events + POST answers 404 to GET and to /slack/eventsx; Traefik's
#       Exact Ingress path answers 404 to /slack/eventsx
#   X2  RequestHeaderModifier removes each of the ten identity headers a client brings
#   X3  a WebSocket, pinged every 30 s as /v1/ws is, stays open X3_SECONDS (360) through agentgateway
#       with and without timeouts, through Traefik, and through agentgateway then oauth2-proxy
#   X4  oauth2-proxy never forwards a client's X-Forwarded-Access-Token: not with a CLI bearer
#       (skip-jwt-bearer-tokens), not with a session, not on a skip-auth route; which other identity
#       headers it passes is recorded
#   X5  oidc-groups-claim with a nested claim (realm_access.roles): read or not, recorded
#   X6  HTTPRoute.spec.rules[].timeouts is kept by the standard CRDs at GATEWAY_API (v1.2.1)
# Usage: probe.sh   (docker, k3d, helm, kubectl, jq, openssl; network for the images and Gateway API's
# CRDs). TAG names the cluster and the stub's image; KEEP=1 keeps the cluster.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../../../.." && pwd)"
TAG="${TAG:-xp-$(date +%s)}"
NAME="maku-${TAG}"
[[ ${#NAME} -le 32 ]] || { echo "FAIL: cluster name $NAME is over k3d's 32 characters" >&2; exit 2; }
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.35.0-k3s1}"
GATEWAY_API="${GATEWAY_API:-v1.2.1}"
X3_SECONDS="${X3_SECONDS:-360}"
# webProxy.image's pin (charts/agent-factory/values.yaml; render.sh checks they agree).
PROXY_IMAGE="${PROXY_IMAGE:-quay.io/oauth2-proxy/oauth2-proxy:v7.15.5@sha256:8498b0d0ef0a7b29686414000a08aee467f02d0299c9ed1e006a8f33fc017916}"
# The agentgateway controller the umbrella pins.
AGW_TAG="$(yq '.agentgateway.controller.image.tag' "$root/charts/agent-factory/values.yaml")"
PY_IMAGE=docker.io/library/python:3.15-alpine@sha256:ff4d3c9b1a48f646eedd93182aaea385c7a5452f4c8a8cd7cef686babacf4928
ISSUER=https://e2e-stub.e2e.svc.cluster.local
umask 077
work="$(mktemp -d "${TMPDIR:-/tmp}/maku-expose-probe.XXXXXX")"
export KUBECONFIG="$work/kubeconfig"
k() { kubectl --kubeconfig "$KUBECONFIG" --context "k3d-$NAME" "$@"; }
fails=0
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; fails=$((fails + 1)); }
record() { echo "RECORD $*"; }
created=false
onexit() {
  local rc=$?
  trap - EXIT
  if [[ ${KEEP:-} == 1 ]] && $created; then echo "kept: KUBECONFIG=$KUBECONFIG; k3d cluster delete $NAME"; exit "$rc"; fi
  $created && { k3d cluster delete "$NAME" >/dev/null 2>&1 || true; }
  docker rmi "ghcr.io/smana/maku-e2e-stub:$TAG" >/dev/null 2>&1 || true
  rm -rf "$work"
  exit "$rc"
}
trap onexit EXIT

echo "==> the stub issuer's image, tag $TAG"
docker build -q -f "$root/hack/e2e-stub/Dockerfile" -t "ghcr.io/smana/maku-e2e-stub:$TAG" "$root" >/dev/null

echo "==> k3d $NAME, with Traefik"
! k3d cluster get "$NAME" >/dev/null 2>&1 || { echo "FAIL: cluster $NAME exists; delete it first" >&2; exit 2; }
created=true
k3d cluster create "$NAME" --image "$K3S_IMAGE" --no-lb --wait --k3s-arg '--disable=servicelb@server:0' \
  --kubeconfig-update-default=false --kubeconfig-switch-context=false >/dev/null
k3d kubeconfig get "$NAME" >"$KUBECONFIG"
for _ in 1 2 3; do
  k3d image import -c "$NAME" "ghcr.io/smana/maku-e2e-stub:$TAG" >/dev/null
  docker exec "k3d-$NAME-server-0" ctr -n k8s.io images ls -q | grep -qxF "ghcr.io/smana/maku-e2e-stub:$TAG" && break
  sleep 2
done

# k3s installs Traefik through a HelmChart job, whose CRD chart brings its own Gateway API CRDs: let it
# finish first, then replace them with GATEWAY_API's (--force-conflicts), the floor the chart supports.
for _ in $(seq 100); do k -n kube-system get deploy/traefik >/dev/null 2>&1 && break; sleep 3; done
k -n kube-system rollout status deploy/traefik --timeout=300s >/dev/null

echo "==> Gateway API $GATEWAY_API standard, agentgateway (vendored CRDs and controller $AGW_TAG)"
curl -fsSL "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GATEWAY_API/standard-install.yaml" |
  k apply --server-side --force-conflicts -f - >/dev/null
k apply --server-side -f "$root/config/agentgateway" >/dev/null
k wait --for=condition=Established crd --all --timeout=120s >/dev/null 2>&1
helm --kubeconfig "$KUBECONFIG" --kube-context "k3d-$NAME" install agentgateway "$root/charts/agent-factory/vendor/agentgateway" \
  -n agentgateway-system --create-namespace --set controller.image.tag="$AGW_TAG" --set istio.enabled=false >/dev/null
k -n agentgateway-system rollout status deploy/agentgateway --timeout=180s >/dev/null

echo "==> the stub issuer (TLS from a one-day CA), oauth2-proxy, the backend and the routes"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=maku-expose-probe-ca' \
  -addext 'basicConstraints=critical,CA:TRUE' -addext 'keyUsage=critical,keyCertSign,cRLSign' \
  -keyout "$work/ca.key" -out "$work/ca.crt" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj '/CN=e2e-stub.e2e.svc.cluster.local' -keyout "$work/stub.key" -out "$work/stub.csr" >/dev/null 2>&1
printf 'subjectAltName=DNS:e2e-stub.e2e.svc.cluster.local\nextendedKeyUsage=serverAuth\nauthorityKeyIdentifier=keyid,issuer\n' >"$work/stub.ext"
openssl x509 -req -days 1 -in "$work/stub.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" -CAcreateserial \
  -extfile "$work/stub.ext" -out "$work/stub.crt" >/dev/null 2>&1
k create namespace e2e >/dev/null
k create namespace probe >/dev/null
k -n e2e create secret generic e2e-stub-tls --from-file=tls.crt="$work/stub.crt" --from-file=tls.key="$work/stub.key" >/dev/null
k -n probe create secret generic e2e-stub-ca --from-file=ca.crt="$work/ca.crt" >/dev/null
k -n probe create secret generic probe-proxy --from-literal=client-id=rooms-web --from-literal=client-secret=probe-throwaway \
  --from-literal=cookie-secret="$(openssl rand -base64 32 | tr -- '+/' '-_')" >/dev/null
k -n probe create configmap probe-scripts --from-file="$here/echo.py" --from-file="$here/client.py" >/dev/null
# The stub's manifest without its NetworkPolicy (the probe enforces none) and its image retagged.
yq 'select(.kind != "NetworkPolicy")' "$root/charts/agent-factory/tests/e2e-stub.yaml" |
  sed "s|ghcr.io/smana/maku-e2e-stub:e2e|ghcr.io/smana/maku-e2e-stub:$TAG|" | k apply -f - >/dev/null
k -n e2e rollout status deploy/e2e-stub --timeout=180s >/dev/null
sed -e "s|__PROXY_IMAGE__|$PROXY_IMAGE|g" -e "s|__PY_IMAGE__|$PY_IMAGE|g" -e "s|__ISSUER__|$ISSUER|g" "$here/manifests.yaml" |
  k apply -f - >/dev/null
k -n probe wait --for=condition=Ready pod/client --timeout=180s >/dev/null
for d in echo proxy proxy-nested; do k -n probe rollout status "deploy/$d" --timeout=180s >/dev/null; done
k -n probe wait --for=condition=Programmed gateway/probe-gw --timeout=180s >/dev/null
k -n probe rollout status deploy/probe-gw --timeout=180s >/dev/null

GW=http://probe-gw.probe.svc.cluster.local:8080
TRAEFIK=http://traefik.kube-system.svc.cluster.local
PROXY=http://proxy.probe.svc.cluster.local
NESTED=http://proxy-nested.probe.svc.cluster.local
# c ARGS...: the client's JSON answer (client.py)
c() { k -n probe exec client -- python3 /app/client.py --cacert /etc/e2e/ca/ca.crt "$@"; }
# token SUB AUD GROUPS [EXTRA]: the stub's ID token, minted in-cluster over its TLS
token() {
  k -n probe exec client -- python3 -c "import json,ssl,urllib.request as u;print(json.load(u.urlopen('$ISSUER/test/id-token?sub=$1&aud=$2&groups=$3${4:-}',context=ssl.create_default_context(cafile='/etc/e2e/ca/ca.crt')))['id_token'])"
}
status() { jq -r .status <<<"$1"; }
# eventually STATUS ARGS...: the client's answer once it has STATUS (routes program asynchronously)
eventually() {
  local want=$1 out=''; shift
  for _ in $(seq 40); do out="$(c "$@")"; [[ $(status "$out") == "$want" ]] && break; sleep 3; done
  printf '%s' "$out"
}

echo "==> X6: timeouts on Gateway API $GATEWAY_API standard"
v="$(k get crd httproutes.gateway.networking.k8s.io -o jsonpath='{.metadata.annotations.gateway\.networking\.k8s\.io/bundle-version}')"
[[ $v == "$GATEWAY_API" ]] || fail "X6 the HTTPRoute CRD is bundle $v, not $GATEWAY_API"
kept="$(k -n probe get httproute web-timeouts -o jsonpath='{.spec.rules[0].timeouts.request}')"
[[ $kept == 0s ]] && pass "X6 HTTPRoute.spec.rules[].timeouts.request is kept by the $GATEWAY_API standard CRDs" \
  || fail "X6 timeouts.request after apply: '$kept' (pruned: the CRDs lack the field)"
for _ in $(seq 40); do
  accepted="$(k -n probe get httproute -o json | jq '[.items[] | select([.status.parents[]?.conditions[]? | select(.type == "Accepted" and .status == "True")] | length > 0)] | length')"
  [[ $accepted == 4 ]] && break; sleep 3
done
[[ $accepted == 4 ]] && pass "X6 agentgateway accepts the four routes, timeouts included" || fail "X6 $accepted of 4 routes Accepted"

echo "==> X1: the Slack surface matches POST /slack/events exactly"
out="$(eventually 200 http POST http://slack.probe.test/slack/events --via "$GW")"
[[ $(status "$out") == 200 && $(jq -r .body.path <<<"$out") == /slack/events ]] && pass "X1 POST /slack/events reaches the backend" || fail "X1 POST /slack/events: $out"
for probe in "GET /slack/events" "POST /slack/eventsx" "POST /slack/events/x" "POST /slack" "GET /"; do
  out="$(c http "${probe%% *}" "http://slack.probe.test${probe#* }" --via "$GW")"
  [[ $(status "$out") == 404 ]] && pass "X1 agentgateway: $probe is 404" || fail "X1 agentgateway: $probe answered $(status "$out")"
done
out="$(eventually 200 http POST http://slack.probe.test/slack/events --via "$TRAEFIK")"
[[ $(status "$out") == 200 ]] && pass "X1 Traefik: POST /slack/events reaches the backend" || fail "X1 Traefik POST /slack/events: $out"
for p in /slack/eventsx /slack/events/x /; do
  out="$(c http POST "http://slack.probe.test$p" --via "$TRAEFIK")"
  [[ $(status "$out") == 404 ]] && pass "X1 Traefik: POST $p is 404 (pathType Exact)" || fail "X1 Traefik: POST $p answered $(status "$out")"
done
out="$(c http GET http://slack.probe.test/slack/events --via "$TRAEFIK")"
record "X1 Traefik: GET /slack/events answers $(status "$out"): Ingress has no method match, the Slack App answers 405"

echo "==> X2: the web route removes each identity header"
names=(X-Forwarded-Access-Token X-Forwarded-User X-Forwarded-Email X-Forwarded-Groups X-Forwarded-Preferred-Username
  X-Auth-Request-User X-Auth-Request-Email X-Auth-Request-Preferred-Username X-Auth-Request-Groups X-Auth-Request-Access-Token)
forged=()
for n in "${names[@]}"; do forged+=(-H "$n: forged"); done
out="$(eventually 200 http GET http://web.probe.test/api/rooms --via "$GW" "${forged[@]}" -H 'X-Probe: kept')"
[[ $(jq -r '.body.headers["x-probe"]' <<<"$out") == kept ]] || fail "X2 the route did not reach the backend: $out"
for n in "${names[@]}"; do
  got="$(jq -r --arg h "${n,,}" '.body.headers[$h] // "<absent>"' <<<"$out")"
  [[ $got == '<absent>' ]] && pass "X2 $n is removed" || fail "X2 $n reached the backend as '$got'"
done

echo "==> X4: oauth2-proxy never forwards a client's X-Forwarded-Access-Token"
cli="$(token alice maku-cli agents-member '&email=alice@e2e.test')"
out="$(eventually 200 http GET "$PROXY/api/rooms" -H "Authorization: Bearer $cli" "${forged[@]}")"
[[ $(status "$out") == 200 ]] || fail "X4 a CLI bearer through oauth2-proxy: $out"
xfat="$(jq -r '.body.headers["x-forwarded-access-token"] // "<absent>"' <<<"$out")"
if [[ $xfat == forged ]]; then fail "X4 a CLI bearer: the forged X-Forwarded-Access-Token reached the upstream"
elif [[ $xfat == "$cli" ]]; then pass "X4 a CLI bearer: X-Forwarded-Access-Token is overwritten with the bearer itself (both headers equal: never a web session)"
else pass "X4 a CLI bearer: X-Forwarded-Access-Token is '${xfat:0:24}', not the forged value"; fi
[[ $(jq -r '.body.headers.authorization' <<<"$out") == "Bearer $cli" ]] && pass "X4 a CLI bearer: Authorization is the bearer" || fail "X4 Authorization: $(jq -c .body.headers <<<"$out")"
for n in "${names[@]:1}"; do
  got="$(jq -r --arg h "${n,,}" '.body.headers[$h] // "<absent>"' <<<"$out")"
  record "X4 oauth2-proxy with a CLI bearer passes $n: $([[ $got == forged ]] && echo "the client's (passed through)" || echo "no ($got)")"
done
out="$(c http GET "$PROXY/.well-known/roomctl" "${forged[@]}" -H "Authorization: Bearer forged")"
[[ $(status "$out") == 200 ]] && pass "X4 GET /.well-known/roomctl is a skip-auth route (no session)" || fail "X4 GET /.well-known/roomctl: $out"
xfat="$(jq -r '.body.headers["x-forwarded-access-token"] // "<absent>"' <<<"$out")"
[[ $xfat == '<absent>' ]] && pass "X4 a skip-auth route strips a client's X-Forwarded-Access-Token (skip-auth-strip-headers)" || fail "X4 a skip-auth route forwarded X-Forwarded-Access-Token '$xfat'"
out="$(c http GET "$PROXY/link/github/callback?code=x&state=y")"
[[ $(status "$out") == 200 && $(jq -r .body.path <<<"$out") == '/link/github/callback?code=x&state=y' ]] && pass "X4 GET /link/github/callback?code&state passes without a session" || fail "X4 GET /link/github/callback: $out"
for probe in "POST /link/github/callback" "GET /link/github" "GET /link/github/callbackx" "GET /api/rooms"; do
  out="$(c http "${probe%% *}" "$PROXY${probe#* }")"
  s="$(status "$out")"
  [[ $s == 302 || $s == 401 || $s == 403 ]] && pass "X4 $probe without a session is $s, not the upstream" || fail "X4 $probe without a session: $out"
done
out="$(c http GET "$PROXY/" -H 'Host: web.probe.test')"
[[ $(status "$out") == 302 && $(jq -r .location <<<"$out") == "$ISSUER/oauth/v2/authorize?"* ]] && pass "X4 GET / without a session redirects to the issuer's authorization endpoint" || fail "X4 GET /: $out"
login="$(c login http://web.probe.test/oauth2/start?rd=/ --via "$PROXY")"
cookie="$(jq -r .cookie <<<"$login")"
[[ -n $cookie ]] && pass "X4 the code flow signs in: $(jq -r '.hops | join(" -> ")' <<<"$login")" || fail "X4 the code flow: $login"
out="$(c http GET http://web.probe.test/api/rooms --via "$PROXY" -H "Cookie: $cookie" "${forged[@]}")"
xfat="$(jq -r '.body.headers["x-forwarded-access-token"] // "<absent>"' <<<"$out")"
authz="$(jq -r '.body.headers.authorization // ""' <<<"$out")"
[[ $(status "$out") == 200 && $xfat != forged && $xfat != '<absent>' && $authz == Bearer\ * && ${authz#Bearer } != "$xfat" ]] &&
  pass "X4 a session: the ID token in Authorization, the session's own access token in X-Forwarded-Access-Token, the forged one dropped" ||
  fail "X4 a session: status $(status "$out"), X-Forwarded-Access-Token '${xfat:0:24}', Authorization '${authz:0:24}'"
attrs="$(c http GET "$PROXY/oauth2/start?rd=/" -H 'Host: web.probe.test' | jq -r .cookieAttrs)"
[[ $attrs == *Secure* && $attrs == *HttpOnly* && $attrs == *SameSite=Strict* ]] && pass "X4 the cookies are Secure, HttpOnly, SameSite=Strict ($attrs)" || fail "X4 cookie attributes: $attrs"
out="$(c http GET "$PROXY/api/rooms" -H "Authorization: Bearer $(token bob maku-cli everyone '&email=bob@e2e.test')")"
[[ $(status "$out") == 403 ]] && pass "X4 allowed-group: a bearer in no agents group is 403" || fail "X4 a bearer in no agents group: $(status "$out")"
out="$(c http GET "$PROXY/api/rooms" -H "Authorization: Bearer $(token carol maku-cli agents-member)")"
record "X4 a CLI bearer with no email claim answers $(status "$out")"

echo "==> X5: a nested groups claim"
nested="$(token alice maku-cli agents-member '&email=alice@e2e.test&groupsClaim=realm_access.roles')"
out="$(eventually 200 http GET "$NESTED/api/rooms" -H "Authorization: Bearer $nested")"
s="$(status "$out")"
if [[ $s == 200 ]]; then record "X5 oidc-groups-claim realm_access.roles is read: a member bearer passes allowed-group (200)"
else record "X5 oidc-groups-claim realm_access.roles is NOT read: a member bearer answers $s"; fi
out="$(c http GET "$NESTED/api/rooms" -H "Authorization: Bearer $(token alice maku-cli agents-member '&email=alice@e2e.test')")"
record "X5 the same proxy, the groups top-level: $(status "$out")"

echo "==> X3: a pinged WebSocket held ${X3_SECONDS}s (in parallel)"
declare -A ws=(
  [agentgateway]="ws://web.probe.test/v1/ws --via $GW"
  [agentgateway-timeouts]="ws://web-t.probe.test/v1/ws --via $GW"
  [traefik]="ws://ws.probe.test/v1/ws --via $TRAEFIK"
  [agentgateway-oauth2-proxy]="ws://proxied.probe.test/v1/ws --via $GW"
)
cli="$(token alice maku-cli agents-member '&email=alice@e2e.test')"
for leg in "${!ws[@]}"; do
  read -r url via base <<<"${ws[$leg]}"
  c ws "$url" "$X3_SECONDS" "$via" "$base" -H "Authorization: Bearer $cli" >"$work/ws-$leg.json" 2>&1 &
done
wait
for leg in "${!ws[@]}"; do
  out="$(cat "$work/ws-$leg.json")"
  [[ $(jq -r .ok <<<"$out" 2>/dev/null) == true ]] && pass "X3 $leg: open ${X3_SECONDS}s, $(jq -r .pings <<<"$out") pings, then an echo" ||
    fail "X3 $leg: $out"
done

echo "==> $fails failure(s): agentgateway $AGW_TAG, Gateway API $GATEWAY_API, $PROXY_IMAGE, Traefik $(k -n kube-system get deploy traefik -o jsonpath='{.spec.template.spec.containers[0].image}'), $(k version -o json | jq -r .serverVersion.gitVersion)"
exit "$fails"
