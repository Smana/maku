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
# Then the GitHub account link (v4 D8, step 5 PR C), with the stub as GitHub's OAuth web flow and the
# factory App's API, and no ZITADEL reader at all:
#   - a member with no link sees no room (X-Rooms-Access: unlinked);
#   - they start a link, follow GitHub's redirect, confirm, and /link/github shows @dev;
#   - the user token was revoked, and no ZITADEL endpoint was ever called;
#   - the room is now theirs to read, through the link;
#   - an admin's unlink makes them unlinked again.
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
LINK_VALUES=charts/agent-factory/tests/identity-link-values.yaml
STUB=charts/agent-factory/tests/e2e-stub.yaml
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
  for image in maku-broker maku-e2e-stub; do docker rmi "ghcr.io/smana/$image:$TAG" >/dev/null 2>&1 || true; done
  rm -rf "$work"
  exit "$rc"
}
trap onexit EXIT

step "build the broker and the stub from this checkout, tag $TAG"
docker build -q -f images/maku-broker/Dockerfile -t "ghcr.io/smana/maku-broker:$TAG" . >/dev/null
docker build -q -f hack/e2e-stub/Dockerfile -t "ghcr.io/smana/maku-e2e-stub:$TAG" . >/dev/null

step "create the k3d cluster $NAME"
! k3d cluster get "$NAME" >/dev/null 2>&1 || die "cluster $NAME already exists; refusing to adopt or delete it"
created=true
k3d cluster create "$NAME" --image "$K3S_IMAGE" --no-lb --wait \
  --k3s-arg '--disable=traefik@server:0' --k3s-arg '--disable=servicelb@server:0' \
  --kubeconfig-update-default=false --kubeconfig-switch-context=false >/dev/null
k3d kubeconfig get "$NAME" >"$KUBECONFIG"
# k3d reports success even when the node fails to ingest the tarball: the image counts once containerd lists it.
for image in maku-broker maku-e2e-stub; do
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
printf 'subjectAltName=DNS:e2e-stub.e2e.svc.cluster.local,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n' >"$work/stub.ext"
openssl x509 -req -days 1 -in "$work/stub.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" \
  -CAcreateserial -extfile "$work/stub.ext" -out "$work/stub.crt" >/dev/null 2>&1
k create namespace e2e >/dev/null
k -n e2e create secret generic e2e-stub-tls --from-file=tls.crt="$work/stub.crt" --from-file=tls.key="$work/stub.key" >/dev/null
k create namespace "$NS" >/dev/null
k -n "$NS" create secret generic e2e-stub-ca --from-file=ca.crt="$work/ca.crt" >/dev/null
k -n "$NS" create secret generic e2e-broker-oidc --from-literal=web-client-id=rooms-web --from-literal=cli-client-id=maku-cli >/dev/null
k -n "$NS" create secret generic e2e-github-link --from-literal=client_id=Iv1.e2e-link --from-literal=client_secret=e2e-throwaway-secret >/dev/null
# The factory App the broker resolves a GitHub id through (D7): a throwaway key; the stub never checks it.
openssl genrsa -out "$work/app.pem" 2048 2>/dev/null
sed "s|ghcr.io/smana/maku-e2e-stub:e2e|ghcr.io/smana/maku-e2e-stub:$TAG|" "$STUB" | k apply -f - >/dev/null
k -n e2e rollout status deploy/e2e-stub --timeout=180s

step "install the umbrella: the broker in preset generic, its ids by Secret"
helm dependency build charts/agent-factory >/dev/null
helm --kubeconfig "$KUBECONFIG" --kube-context "k3d-$NAME" install identity-crds charts/agent-factory-crds -n "$NS" --timeout 2m >/dev/null
helm --kubeconfig "$KUBECONFIG" --kube-context "k3d-$NAME" install identity charts/agent-factory -n "$NS" \
  -f charts/agent-factory/values-demo.yaml -f "$VALUES" -f "$LINK_VALUES" \
  --set maku.e2e.unpinnedImages=true --set "maku.broker.image.tag=$TAG" \
  --set maku.runController.enabled=false --set maku-factory.enabled=false \
  --set demo.model.baseURL=http://model.e2e.test/v1 --set demo.model.name=m --set demo.model.apiKey=e2e-throwaway \
  --set demo.githubApp.id=1 --set-file demo.githubApp.privateKey="$work/app.pem" --timeout 5m >/dev/null
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

step "the GitHub account link: a member links GitHub once, through the stub's OAuth flow"
origin=https://rooms.e2e.test
# link METHOD PATH BEARER [COOKIE] [FORM]: the broker's /link/github routes, as the web UI's session
# (oauth2-proxy's two headers, the broker's own Origin); headers to $work/headers, body to $work/body.
link() {
  local args=(-sS -o "$work/body" -D "$work/headers" -w '%{http_code}' -X "$1" -H "Origin: $origin")
  [[ -n ${3:-} ]] && args+=(-H "Authorization: Bearer $3" -H 'X-Forwarded-Access-Token: opaque-access-token')
  [[ -n ${4:-} ]] && args+=(-H "Cookie: __Host-maku-ghlink=$4")
  [[ -n ${5:-} ]] && args+=(-H 'Content-Type: application/x-www-form-urlencoded' --data "$5")
  curl "${args[@]}" "http://127.0.0.1:$broker$2"
}
page_has() { [[ $code == 200 ]] && grep -qF -- "$1" "$work/body"; } # the last answer is 200 and holds $1
header() { sed -n "s/^$1: //Ip" "$work/headers" | tr -d '\r' | head -1; }
access() { # access BEARER: the X-Rooms-Access of the member's room list, and its row count
  curl -sS -o "$work/body" -D "$work/headers" -H "Authorization: Bearer $1" -H 'X-Forwarded-Access-Token: opaque-access-token' \
    "http://127.0.0.1:$broker/api/rooms" >/dev/null
  echo "$(header X-Rooms-Access) $(jq length "$work/body")"
}
[[ "$(access "$member_web")" == "unlinked 0" ]] || die "a member with no link: $(access "$member_web"), want 'unlinked 0'"
echo "ok   a member with no GitHub link sees no room (X-Rooms-Access: unlinked)"
code="$(link GET /link/github "$member_web")"
page_has 'None yet.' || die "the link page: $code $(cat "$work/body")"
code="$(link POST /link/github/start "$member_web" '' 'x=1')"
location="$(header Location)"
nonce="$(header Set-Cookie | sed -n 's/^__Host-maku-ghlink=\([^;]*\);.*/\1/p')"
[[ $code == 303 && $location == https://e2e-stub.e2e.svc.cluster.local/login/oauth/authorize\?* && -n $nonce ]] ||
  die "the start: $code '$location' '$(header Set-Cookie)'"
# The browser at GitHub: the stub authorizes at once and redirects back with a code.
back="$(curl -sS -o /dev/null -w '%{redirect_url}' --cacert "$work/ca.crt" \
  --resolve "e2e-stub.e2e.svc.cluster.local:$stub:127.0.0.1" "${location/e2e-stub.e2e.svc.cluster.local/e2e-stub.e2e.svc.cluster.local:$stub}")"
[[ $back == "$origin/link/github/callback?"* ]] || die "the stub's authorize redirects to '$back'"
callback="${back#"$origin"}"
state="$(sed -n 's/.*[?&]state=\([^&]*\).*/\1/p' <<<"$callback")"
code="$(link GET "$callback" '' "$nonce")" # no session: the front door lets the callback through
page_has 'GitHub says you are <strong>@dev</strong> (id 123)' ||
  die "the callback: $code $(cat "$work/body")"
code="$(link POST /link/github/confirm "$member_web" "$nonce" "state=$state")"
page_has 'Linked: rooms and the factory now know you as @dev.' || die "the confirm: $code $(cat "$work/body")"
code="$(link GET /link/github "$member_web")"
page_has 'Linked to @dev (id 123)' || die "the link page after: $code $(cat "$work/body")"
echo "ok   start, GitHub's redirect and the confirm link the member to @dev; /link/github shows it"
counts="$(curl -fsS --cacert "$work/ca.crt" --resolve "e2e-stub.e2e.svc.cluster.local:$stub:127.0.0.1" \
  "https://e2e-stub.e2e.svc.cluster.local:$stub/__oauth")"
[[ $(jq -r '.revoked, .liveTokens, .zitadel' <<<"$counts" | paste -sd' ') == "1 0 0" ]] || die "the stub's OAuth counts: $counts"
echo "ok   the user token was revoked at once, and no ZITADEL endpoint was called"
[[ "$(access "$member_web")" == "ok 1" ]] || die "the linked member: $(access "$member_web"), want 'ok 1'"
echo "ok   the member reads the room through the link (X-Rooms-Access: ok)"

step "an admin unlinks the member"
code="$(link DELETE /api/links/github/alice "$admin_web")"
[[ $code == 200 && $(jq -r .unlinked "$work/body") == true ]] || die "the admin's unlink: $code $(cat "$work/body")"
[[ "$(access "$member_web")" == "unlinked 0" ]] || die "after the unlink: $(access "$member_web"), want 'unlinked 0'"
echo "ok   after an admin's unlink the member is unlinked again and sees no room"

echo 'identity k3d OK'
