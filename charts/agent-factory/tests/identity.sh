#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Proves the broker in preset generic against a generic OIDC issuer (v4 D8) on a throwaway k3d
# cluster. The issuer is the in-cluster e2e-stub: discovery, its JWKS, and ID tokens minted for
# any sub, client and groups. Nothing external is contacted but the images' registries.
#
# The broker is called as oauth2-proxy would call it, with the ID token in Authorization and a
# distinct access token in X-Forwarded-Access-Token:
#   - a member's web session lists the rooms;
#   - a token in no agents group is refused 403;
#   - the CLI client's ID token as a bearer reads, and is never the web UI, so it cannot decide (P18).
#
# EXPOSE=httproute|ingress (task chart:expose:k3d; plan v4 step 6, Task 12) installs the web exposed
# through the web front door (tests/expose-values.yaml) in networkPolicies.mode kubernetes, behind
# agentgateway (Gateway API) or k3s's Traefik (Ingress), and plays the browser from a client pod:
#   - GET / redirects to the stub issuer, and its code flow signs in; that session lists the rooms
#     as the web UI;
#   - GET /.well-known/roomctl answers without a session, and so does GET /link/github/callback when
#     the GitHub account link is on, while POST to it and GET /link/github still need a session;
#   - a CLI bearer reads /api/rooms, and the same bearer with a forged X-Forwarded-Access-Token is
#     still not the web UI (P18);
#   - the broker's :8080 refuses a pod that is not the front door;
#   - /v1/ws stays open EXPOSE_WS_SECONDS (360).
#
# The cluster, its kubeconfig and the images' tag are this run's own: the current kubectl context
# is never read or changed.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."

TAG="${TAG:-identity-$(date +%s)-$$}"
NAME="maku-${TAG}"
NS=maku
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.35.0-k3s1}"
VALUES=charts/agent-factory/tests/identity-values.yaml
STUB=charts/agent-factory/tests/e2e-stub.yaml
# The images this run builds and loads. The expose leg adds the run-controller: networkPolicies.mode
# kubernetes is the run-controller's (its egress proxy), so the chart refuses it without one. No run
# starts, so the harness and the bridge are never pulled.
images=(maku-broker maku-e2e-stub)
[[ -z ${EXPOSE:-} ]] || images+=(maku-run-controller)
EXPOSE="${EXPOSE:-}"
case "$EXPOSE" in
  "" | httproute | ingress) ;;
  *) echo "FAIL: EXPOSE=$EXPOSE is not httproute or ingress" >&2; exit 2 ;;
esac
EXPOSE_WS_SECONDS="${EXPOSE_WS_SECONDS:-360}"
GATEWAY_API=v1.2.1
PY_IMAGE=docker.io/library/python:3.15-alpine@sha256:ff4d3c9b1a48f646eedd93182aaea385c7a5452f4c8a8cd7cef686babacf4928
step() { printf '\n==> %s\n' "$*"; }
die() { echo "FAIL: $*" >&2; exit 1; }

umask 077
work="$(mktemp -d "${TMPDIR:-/tmp}/maku-identity-k3d.XXXXXX")"
export KUBECONFIG="$work/kubeconfig"
k() { kubectl --kubeconfig "$KUBECONFIG" --context "k3d-$NAME" "$@"; }
created=false
pf_pids=()
onexit() {
  local rc=$?
  trap - EXIT
  for pid in "${pf_pids[@]}"; do kill "$pid" >/dev/null 2>&1 || true; done
  if [[ $rc -ne 0 ]] && $created; then
    k get pods -A -o wide >&2 || true
    k -n "$NS" logs deploy/room-broker --all-containers --tail=120 >&2 || true
    k -n e2e logs deploy/e2e-stub --tail=60 >&2 || true
  fi
  if $created; then
    step "delete the cluster $NAME"
    k3d cluster delete "$NAME" >/dev/null || true
  fi
  for image in "${images[@]}"; do docker rmi "ghcr.io/smana/$image:$TAG" >/dev/null 2>&1 || true; done
  rm -rf "$work"
  exit "$rc"
}
trap onexit EXIT

step "build the broker and the stub from this checkout, tag $TAG"
docker build -q -f images/maku-broker/Dockerfile -t "ghcr.io/smana/maku-broker:$TAG" . >/dev/null
[[ -z $EXPOSE ]] || docker build -q -f images/maku-run-controller/Dockerfile -t "ghcr.io/smana/maku-run-controller:$TAG" . >/dev/null
docker build -q -f hack/e2e-stub/Dockerfile -t "ghcr.io/smana/maku-e2e-stub:$TAG" . >/dev/null

step "create the k3d cluster $NAME"
! k3d cluster get "$NAME" >/dev/null 2>&1 || die "cluster $NAME already exists; refusing to adopt or delete it"
created=true
k3s_args=(--k3s-arg '--disable=servicelb@server:0')
[[ $EXPOSE == ingress ]] || k3s_args+=(--k3s-arg '--disable=traefik@server:0')
k3d cluster create "$NAME" --image "$K3S_IMAGE" --no-lb --wait "${k3s_args[@]}" \
  --kubeconfig-update-default=false --kubeconfig-switch-context=false >/dev/null
k3d kubeconfig get "$NAME" >"$KUBECONFIG"
# k3d reports success even when the node fails to ingest the tarball: the image counts once containerd lists it.
for image in "${images[@]}"; do
  for _ in 1 2 3; do
    k3d image import -c "$NAME" "ghcr.io/smana/$image:$TAG" >/dev/null
    docker exec "k3d-$NAME-server-0" ctr -n k8s.io images ls -q | grep -qxF "ghcr.io/smana/$image:$TAG" && continue 2
    sleep 2
  done
  die "k3d image import: $image never reached the node"
done

step "a throwaway CA, the stub's certificate, the broker's OIDC ids"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=maku-identity-e2e-ca' \
  -addext 'basicConstraints=critical,CA:TRUE' -addext 'keyUsage=critical,keyCertSign,cRLSign' \
  -keyout "$work/ca.key" -out "$work/ca.crt" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj '/CN=e2e-stub.e2e.svc.cluster.local' \
  -keyout "$work/stub.key" -out "$work/stub.csr" >/dev/null 2>&1
printf 'subjectAltName=DNS:e2e-stub.e2e.svc.cluster.local,IP:127.0.0.1\nextendedKeyUsage=serverAuth\nauthorityKeyIdentifier=keyid,issuer\n' >"$work/stub.ext"
openssl x509 -req -days 1 -in "$work/stub.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" \
  -CAcreateserial -extfile "$work/stub.ext" -out "$work/stub.crt" >/dev/null 2>&1
k create namespace e2e >/dev/null
k -n e2e create secret generic e2e-stub-tls --from-file=tls.crt="$work/stub.crt" --from-file=tls.key="$work/stub.key" >/dev/null
k create namespace "$NS" >/dev/null
k -n "$NS" create secret generic e2e-stub-ca --from-file=ca.crt="$work/ca.crt" >/dev/null
k -n "$NS" create secret generic e2e-broker-oidc --from-literal=web-client-id=rooms-web --from-literal=cli-client-id=maku-cli >/dev/null
sed "s|ghcr.io/smana/maku-e2e-stub:e2e|ghcr.io/smana/maku-e2e-stub:$TAG|" "$STUB" | k apply -f - >/dev/null
k -n e2e rollout status deploy/e2e-stub --timeout=180s

expose=()
if [[ -n $EXPOSE ]]; then
  step "expose ($EXPOSE): the front door's Secret, the client pod, the gateway"
  k -n "$NS" create secret generic e2e-rooms-proxy --from-literal=client-id=rooms-web --from-literal=client-secret=e2e-throwaway \
    --from-literal=cookie-secret="$(openssl rand -base64 32 | tr -- '+/' '-_')" >/dev/null
  k -n "$NS" create secret generic e2e-github-link --from-literal=client_id=e2e-gh --from-literal=client_secret=e2e-throwaway >/dev/null
  k -n e2e create secret generic e2e-stub-ca --from-file=ca.crt="$work/ca.crt" >/dev/null
  k -n e2e create configmap e2e-client --from-file=charts/agent-factory/tests/expose-probe/client.py >/dev/null
  k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata: {name: e2e-client, namespace: e2e, labels: {app.kubernetes.io/name: e2e-client}}
spec:
  automountServiceAccountToken: false
  securityContext: {runAsNonRoot: true, runAsUser: 65532, runAsGroup: 65532, fsGroup: 65532, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: client
      image: $PY_IMAGE
      command: [sleep, infinity]
      securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}
      volumeMounts: [{name: app, mountPath: /app}, {name: ca, mountPath: /etc/e2e/ca}]
  volumes:
    - {name: app, configMap: {name: e2e-client}}
    - {name: ca, secret: {secretName: e2e-stub-ca, defaultMode: 0440}}
YAML
  expose=(-f charts/agent-factory/tests/expose-values.yaml --set "expose.mode=$EXPOSE"
    --set "maku.runController.image.tag=$TAG" --set "maku.runController.images.harness.tag=$TAG" --set "maku.runController.images.bridge.tag=$TAG")
  if [[ $EXPOSE == httproute ]]; then
    curl -fsSL "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GATEWAY_API/standard-install.yaml" |
      k apply --server-side -f - >/dev/null
    k apply --server-side -f config/agentgateway >/dev/null
    k wait --for=condition=Established crd --all --timeout=120s >/dev/null 2>&1
    helm --kubeconfig "$KUBECONFIG" --kube-context "k3d-$NAME" install agentgateway charts/agent-factory/vendor/agentgateway \
      -n agentgateway-system --create-namespace --set istio.enabled=false \
      --set controller.image.tag="$(yq '.agentgateway.controller.image.tag' charts/agent-factory/values.yaml)" >/dev/null
    k -n agentgateway-system rollout status deploy/agentgateway --timeout=180s >/dev/null
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
    expose+=(--set-json 'expose.httproute.parentRefs=[{"name":"edge","namespace":"edge","sectionName":"http"}]'
      --set-json 'expose.networkPolicy.from=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"edge"}}}]')
    front=http://edge.edge.svc.cluster.local:8080
  else
    expose+=(--set expose.ingress.className=traefik
      --set-json 'expose.networkPolicy.from=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"kube-system"}},"podSelector":{"matchLabels":{"app.kubernetes.io/name":"traefik"}}}]')
    front=http://traefik.kube-system.svc.cluster.local
  fi
fi

step "install the umbrella: the broker in preset generic, its ids by Secret"
helm dependency build charts/agent-factory >/dev/null
helm --kubeconfig "$KUBECONFIG" --kube-context "k3d-$NAME" install identity-crds charts/agent-factory-crds -n "$NS" --timeout 2m >/dev/null
helm --kubeconfig "$KUBECONFIG" --kube-context "k3d-$NAME" install identity charts/agent-factory -n "$NS" \
  -f charts/agent-factory/values-demo.yaml -f "$VALUES" "${expose[@]}" \
  --set maku.e2e.unpinnedImages=true --set "maku.broker.image.tag=$TAG" \
  --set maku.runController.enabled="$([[ -n $EXPOSE ]] && echo true || echo false)" --set maku-factory.enabled=false \
  --set demo.model.baseURL=http://model.e2e.test/v1 --set demo.model.name=m --set demo.model.apiKey=e2e-throwaway \
  --set demo.githubApp.id=1 --set demo.githubApp.privateKey=e2e-throwaway --timeout 5m >/dev/null
k -n "$NS" rollout status sts/rooms-db --timeout=180s
k -n "$NS" rollout status deploy/room-broker --timeout=180s

forward() { # forward NS RESOURCE REMOTE VAR: a port-forward, its local port in VAR
  local log="$work/pf-${2//\//-}.log" port=""
  k -n "$1" port-forward --address=127.0.0.1 "$2" ":$3" >"$log" 2>&1 &
  pf_pids+=("$!")
  for _ in $(seq 1 60); do
    port="$(sed -n 's/^Forwarding from 127.0.0.1:\([0-9]*\).*/\1/p' "$log" | head -1)"
    if [[ -n $port ]]; then printf -v "$4" '%s' "$port"; return; fi
    sleep 1
  done
  die "port-forward to $2 never started: $(cat "$log")"
}
stub='' broker=''
forward e2e svc/e2e-stub 443 stub
forward "$NS" deploy/room-broker 8080 broker

# id_token SUB AUD GROUPS: the stub's ID token, over its own TLS.
id_token() {
  curl -fsS --cacert "$work/ca.crt" --resolve "e2e-stub.e2e.svc.cluster.local:$stub:127.0.0.1" \
    "https://e2e-stub.e2e.svc.cluster.local:$stub/test/id-token?sub=$1&aud=$2&groups=$3" | jq -r .id_token
}
# rooms METHOD BEARER [ACCESS] [BODY]: the status and body of the broker's /api/rooms, as oauth2-proxy calls it.
rooms() {
  local args=(-sS -o "$work/body" -w '%{http_code}' -X "$1" -H "Authorization: Bearer $2")
  [[ -n ${3:-} ]] && args+=(-H "X-Forwarded-Access-Token: $3")
  [[ -n ${4:-} ]] && args+=(-H 'Content-Type: application/json' --data "$4")
  curl "${args[@]}" "http://127.0.0.1:$broker/api/rooms"
}

step "the broker discovers the stub's keys and admits a generic ID token"
admin_web="$(id_token admin rooms-web agents-admin)"
code="$(rooms POST "$admin_web" opaque-access-token '{"dataClass":"public","repository":"owner/repo"}')"
[[ $code == 201 ]] || die "an admin's web session creating a room: $code $(cat "$work/body")"
member_web="$(id_token alice rooms-web agents-member)"
code="$(rooms GET "$member_web" opaque-access-token)"
[[ $code == 200 ]] || die "a member's web session: $code $(cat "$work/body")"
echo "ok   a member's web session lists the rooms (200)"
code="$(rooms GET "$(id_token mallory rooms-web everyone)" opaque-access-token)"
[[ $code == 403 ]] || die "a token in no agents group: $code, want 403"
echo "ok   a token in no agents group is refused (403)"
code="$(rooms GET "$member_web")"
[[ $code == 401 ]] || die "the web client's ID token alone: $code, want 401"
echo "ok   the web client's ID token without its access token is refused (401)"

step "P18: the CLI client's ID token reads, and is never the web UI"
code="$(rooms GET "$admin_web" opaque-access-token)"
[[ $code == 200 && $(jq -r '[.[] | .you.webUI] | all' "$work/body") == true && $(jq length "$work/body") -ge 1 ]] ||
  die "the admin's web session: $code $(cat "$work/body")"
cli="$(id_token admin maku-cli agents-admin)"
code="$(rooms GET "$cli" "$cli")"
[[ $code == 200 && $(jq length "$work/body") -ge 1 ]] || die "the CLI bearer reading: $code $(cat "$work/body")"
[[ $(jq -r '[.[] | .you.webUI] | any' "$work/body") == false ]] || die "the CLI bearer passed as the web UI: $(cat "$work/body")"
echo "ok   the CLI client's ID token reads the room, and is not the web UI, which alone may decide"

if [[ -n $EXPOSE ]]; then
  step "expose ($EXPOSE): the web through $front and the web front door"
  k -n "$NS" rollout status deploy/rooms-web-proxy --timeout=180s
  k -n e2e wait --for=condition=Ready pod/e2e-client --timeout=180s >/dev/null
  if [[ $EXPOSE == httproute ]]; then
    k -n edge wait --for=condition=Programmed gateway/edge --timeout=180s >/dev/null
    k -n edge rollout status deploy/edge --timeout=180s >/dev/null
  else
    for _ in $(seq 100); do k -n kube-system get deploy/traefik >/dev/null 2>&1 && break; sleep 3; done
    k -n kube-system rollout status deploy/traefik --timeout=300s >/dev/null
  fi
  web=http://rooms.e2e.test
  # c ARGS...: the client's JSON answer (tests/expose-probe/client.py), through the front
  c() { k -n e2e exec e2e-client -- python3 /app/client.py --cacert /etc/e2e/ca/ca.crt --via "$front" "$@"; }
  status() { jq -r .status <<<"$1"; }
  out=''
  for _ in $(seq 40); do out="$(c http GET "$web/")"; [[ $(status "$out") == 302 ]] && break; sleep 3; done
  [[ $(status "$out") == 302 && $(jq -r .location <<<"$out") == https://e2e-stub.e2e.svc.cluster.local/oauth/v2/authorize?* ]] ||
    die "GET / without a session does not redirect to the stub issuer: $out"
  echo "ok   GET / without a session redirects to the issuer"
  login="$(c login "$web/oauth2/start?rd=/")"
  cookie="$(jq -r .cookie <<<"$login")"
  [[ -n $cookie ]] || die "the code flow did not sign in: $login"
  echo "ok   the code flow signs in: $(jq -r '.hops | join(" -> ")' <<<"$login")"
  out="$(c http GET "$web/api/rooms" -H "Cookie: $cookie" -H 'X-Forwarded-Access-Token: forged')"
  [[ $(status "$out") == 200 && $(jq '.body | length' <<<"$out") -ge 1 && $(jq -r '[.body[] | .you.webUI] | all' <<<"$out") == true ]] ||
    die "the session through the front door: $out"
  echo "ok   the session lists the rooms as the web UI (a client's X-Forwarded-Access-Token dropped)"
  room="$(jq -r '.body[0].id' <<<"$out")"
  out="$(c http GET "$web/.well-known/roomctl")"
  [[ $(status "$out") == 200 && $(jq -r '.body | type' <<<"$out") == object ]] || die "GET /.well-known/roomctl without a session: $out"
  echo "ok   GET /.well-known/roomctl answers without a session"
  if [[ -n $(yq '.maku.broker.githubLink.existingSecret // ""' charts/agent-factory/tests/expose-values.yaml) ]]; then
    out="$(c http GET "$web/link/github/callback?code=x&state=y")"
    [[ $(status "$out") != 302 && $(status "$out") -lt 500 ]] || die "GET /link/github/callback did not reach the broker: $out"
    echo "ok   GET /link/github/callback reaches the broker without a session ($(status "$out"), the broker's own refusal)"
    for probe in "POST /link/github/callback" "GET /link/github"; do
      out="$(c http "${probe%% *}" "$web${probe#* }")"
      [[ $(status "$out") == 302 || $(status "$out") == 401 || $(status "$out") == 403 ]] || die "$probe without a session: $out"
    done
    echo "ok   POST /link/github/callback and GET /link/github still need a session"
  fi
  cli="$(id_token admin maku-cli agents-admin)"
  out="$(c http GET "$web/api/rooms" -H "Authorization: Bearer $cli")"
  [[ $(status "$out") == 200 && $(jq '.body | length' <<<"$out") -ge 1 ]] || die "a CLI bearer through the front door: $out"
  echo "ok   a CLI bearer reads /api/rooms through the front door"
  out="$(c http GET "$web/api/rooms" -H "Authorization: Bearer $cli" -H "X-Forwarded-Access-Token: forged-$RANDOM")"
  [[ $(status "$out") == 200 && $(jq -r '[.body[] | .you.webUI] | any' <<<"$out") == false ]] ||
    die "a CLI bearer with a forged X-Forwarded-Access-Token passed as the web UI: $out"
  echo "ok   the same bearer with a forged X-Forwarded-Access-Token is still not the web UI, so it cannot decide (P18)"
  out="$(k -n e2e exec e2e-client -- python3 /app/client.py http GET http://room-broker.maku.svc.cluster.local:8080/api/rooms \
    -H "Authorization: Bearer $cli" 2>&1 || true)"
  [[ $(jq -r .status <<<"$out" 2>/dev/null) != 200 ]] || die "a pod that is not the front door reached the broker's :8080: $out"
  echo "ok   the broker's :8080 refuses a pod that is not the front door"
  out="$(c ws "ws://rooms.e2e.test/v1/ws?room=$room" "$EXPOSE_WS_SECONDS" -H "Cookie: $cookie" -H 'Origin: https://rooms.e2e.test' \
    --hello "{\"type\":\"hello\",\"roomId\":\"$room\"}")"
  [[ $(jq -r .ok <<<"$out") == true ]] || die "/v1/ws through the front door: $out"
  echo "ok   /v1/ws stays open ${EXPOSE_WS_SECONDS}s through $EXPOSE and the front door ($(jq -r .pings <<<"$out") pings)"
fi

echo "identity k3d OK${EXPOSE:+ (expose $EXPOSE)}"
