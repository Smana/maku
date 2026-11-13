#!/usr/bin/env bash
# A reproducible probe of the token shapes Dex and Keycloak issue (v4 step 5, Task 1): what the
# broker's generic preset reads. Each assertion prints PASS or FAIL <what>; NOTE lines record a
# shape that is neither (azp present or not, a JWT or an opaque access token). Throwaway
# containers on loopback, removed on exit. Needs docker, curl and jq; yq converts keycloak.yaml.
#
#   bash hack/oidc-probe/probe.sh            # both IdPs
#   bash hack/oidc-probe/probe.sh dex        # one
#
# The exit status is the number of FAIL lines.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEX_IMAGE="${DEX_IMAGE:-ghcr.io/dexidp/dex:v2.41.1}"
KEYCLOAK_IMAGE="${KEYCLOAK_IMAGE:-quay.io/keycloak/keycloak:26.0.8}"
DEX_ISSUER="http://127.0.0.1:5556/dex"
KEYCLOAK_ISSUER="http://127.0.0.1:8180/realms/agents"
REDIRECT="http://127.0.0.1:9/callback"
PASSWORD="probe-password"

fails=0
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; fails=$((fails + 1)); }
note() { echo "NOTE $*"; }

# claims prints a JWT's payload as JSON, or nothing for an opaque token.
claims() {
  local p
  p="$(cut -d. -f2 <<<"$1" | tr '_-' '/+')"
  while (( ${#p} % 4 )); do p+='='; done
  base64 -d <<<"$p" 2>/dev/null | jq -c . 2>/dev/null || true
}
is_jwt() { [[ "$(tr -cd . <<<"$1" | wc -c)" -eq 2 && -n "$(claims "$1")" ]]; }
# aud_holds <claims> <client>: aud is the client, or an array holding it.
aud_holds() { jq -e --arg c "$2" '.aud | if type == "array" then index($c) != null else . == $c end' <<<"$1" >/dev/null; }

work="$(mktemp -d)"
containers=()
# shellcheck disable=SC2329 # the EXIT trap calls it
cleanup() {
  for c in "${containers[@]}"; do docker rm -f "$c" >/dev/null 2>&1 || true; done
  rm -rf "$work"
}
trap cleanup EXIT

wait_for() {
  local url="$1"
  for _ in $(seq 1 90); do
    curl -fsS -o /dev/null "$url" 2>/dev/null && return 0
    sleep 2
  done
  echo "probe: $url never answered" >&2
  return 1
}

start_dex() {
  containers+=(oidc-probe-dex)
  docker run -d --rm --name oidc-probe-dex --memory 128m -p 127.0.0.1:5556:5556 \
    -v "$here/dex.yaml:/etc/dex/config.yaml:ro" "$DEX_IMAGE" dex serve /etc/dex/config.yaml >/dev/null
  wait_for "$DEX_ISSUER/.well-known/openid-configuration"
}

start_keycloak() {
  yq -o=json '.' "$here/keycloak.yaml" >"$work/agents-realm.json"
  chmod 0644 "$work/agents-realm.json"
  chmod 0755 "$work"
  containers+=(oidc-probe-keycloak)
  docker run -d --rm --name oidc-probe-keycloak --memory 1g -p 127.0.0.1:8180:8080 \
    -e KC_BOOTSTRAP_ADMIN_USERNAME=admin -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin -e JAVA_OPTS_KC_HEAP=-Xmx512m \
    -v "$work/agents-realm.json:/opt/keycloak/data/import/agents-realm.json:ro" \
    "$KEYCLOAK_IMAGE" start-dev --import-realm --http-port 8080 --hostname http://127.0.0.1:8180 >/dev/null
  wait_for "$KEYCLOAK_ISSUER/.well-known/openid-configuration"
}

# token <endpoint> <form...>: the token response, or {} on an error status.
token() {
  local ep="$1"; shift
  curl -sS "$ep" "$@" | jq -c 'if type == "object" then . else {} end' 2>/dev/null || echo '{}'
}

# dex_mock_code: an authorization code from Dex's mockCallback connector, whose identity carries
# groups. Follows Dex's redirects by hand until one reaches REDIRECT.
dex_mock_code() {
  local url="$DEX_ISSUER/auth?client_id=maku-web&redirect_uri=$REDIRECT&response_type=code&scope=openid+profile+email+groups+offline_access&state=probe-state&connector_id=mock"
  local _
  for _ in $(seq 1 8); do
    url="$(curl -sS -o /dev/null -b "$work/jar" -c "$work/jar" -w '%{redirect_url}' "$url")"
    [[ "$url" == /* ]] && url="http://127.0.0.1:5556$url"
    if [[ "$url" == "$REDIRECT"* ]]; then
      sed -n 's/.*[?&]code=\([^&]*\).*/\1/p' <<<"$url"
      return 0
    fi
    [[ -n "$url" ]] || break
  done
  return 1
}

# probe <name> <issuer> <username> <roles claim> <roles source: id|access> <scope>
probe() {
  local name="$1" issuer="$2" user="$3" roles="$4" roles_in="$5" scope="$6"
  echo "== $name ($issuer)"
  local disc ep
  disc="$(curl -fsS "$issuer/.well-known/openid-configuration")"
  ep="$(jq -r .token_endpoint <<<"$disc")"

  # O1: discovery names the keys, the token endpoint and the device endpoint.
  for f in jwks_uri token_endpoint device_authorization_endpoint; do
    if jq -e --arg f "$f" '.[$f] | type == "string" and length > 0' <<<"$disc" >/dev/null; then
      pass "$name O1 discovery has $f"
    else
      fail "$name O1 discovery has no $f (web-only: maku login cannot run its device grant)"
    fi
  done

  # O2: the ID token from the password grant.
  local resp id at idc
  resp="$(token "$ep" -d grant_type=password -d client_id=maku-web -d client_secret=probe-web-secret \
    -d "username=$user" -d "password=$PASSWORD" -d "scope=$scope")"
  id="$(jq -r '.id_token // empty' <<<"$resp")"
  at="$(jq -r '.access_token // empty' <<<"$resp")"
  if [[ -z "$id" ]]; then
    fail "$name O2 the password grant returned no ID token: $(jq -c '{error, error_description}' <<<"$resp")"
  else
    idc="$(claims "$id")"
    if aud_holds "$idc" maku-web; then pass "$name O2 the ID token's aud holds the client"; else fail "$name O2 the ID token's aud does not hold the client: $(jq -c .aud <<<"$idc")"; fi
    if jq -e 'has("azp")' <<<"$idc" >/dev/null; then note "$name O2 the ID token has azp: $(jq -r .azp <<<"$idc")"; else note "$name O2 the ID token has no azp"; fi
    note "$name O2 the ID token's aud is $(jq -c .aud <<<"$idc")"
  fi

  # O2: the roles claim, from the ID token (or, where the IdP puts it there only, the access token).
  local rc=""
  if [[ "$name" == dex ]]; then
    # Dex's password connector carries no groups; the mockCallback connector's identity does.
    local code
    if code="$(dex_mock_code)" && [[ -n "$code" ]]; then
      local mock
      mock="$(token "$ep" -d grant_type=authorization_code -d "code=$code" -d "redirect_uri=$REDIRECT" \
        -d client_id=maku-web -d client_secret=probe-web-secret)"
      rc="$(claims "$(jq -r '.id_token // empty' <<<"$mock")")"
    fi
  elif [[ "$roles_in" == access && -n "$at" ]]; then
    rc="$(claims "$at")"
  else
    rc="${idc:-}"
  fi
  local path
  if [[ "$roles" == /* ]]; then
    path="$(jq -cn --arg p "$roles" '$p | ltrimstr("/") | split("/") | map(gsub("~1"; "/") | gsub("~0"; "~"))')"
  else
    path="$(jq -cn --arg p "$roles" '[$p]')"
  fi
  if [[ -n "$rc" ]] && jq -e --argjson p "$path" 'getpath($p) | type == "array" and length > 0' <<<"$rc" >/dev/null; then
    pass "$name O2 the roles are at $roles in the $roles_in token: $(jq -c --argjson p "$path" 'getpath($p)' <<<"$rc")"
  else
    fail "$name O2 no roles at $roles in the $roles_in token"
  fi
  if [[ -n "${idc:-}" ]]; then
    if jq -e '.realm_access.roles' <<<"$idc" >/dev/null; then note "$name O2 the ID token carries /realm_access/roles"; fi
    if jq -e '.groups' <<<"$idc" >/dev/null; then note "$name O2 the ID token carries groups"; else note "$name O2 the ID token carries no groups"; fi
  fi

  # O3: a refresh carries a new ID token.
  local rt refreshed
  rt="$(jq -r '.refresh_token // empty' <<<"$resp")"
  if [[ -z "$rt" ]]; then
    fail "$name O3 the password grant returned no refresh token"
  else
    refreshed="$(token "$ep" -d grant_type=refresh_token -d "refresh_token=$rt" -d client_id=maku-web -d client_secret=probe-web-secret)"
    if jq -e '.id_token | type == "string" and length > 0' <<<"$refreshed" >/dev/null; then
      pass "$name O3 a refresh returns id_token"
    else
      fail "$name O3 a refresh returns no id_token (maku login again at expiry)"
    fi
  fi

  # O4: client credentials give a JWT access token naming its client.
  local cc cct ccc
  cc="$(token "$ep" -u maku-slack:probe-slack-secret -d grant_type=client_credentials -d scope=openid)"
  cct="$(jq -r '.access_token // empty' <<<"$cc")"
  if [[ -z "$cct" ]]; then
    fail "$name O4 client credentials returned no access token: $(jq -c '{error, error_description}' <<<"$cc")"
  elif ! is_jwt "$cct"; then
    fail "$name O4 the client-credentials access token is opaque"
  else
    ccc="$(claims "$cct")"
    if jq -e '.azp == "maku-slack" or .client_id == "maku-slack"' <<<"$ccc" >/dev/null; then
      pass "$name O4 the client-credentials token names its client: azp=$(jq -r '.azp // "-"' <<<"$ccc") client_id=$(jq -r '.client_id // "-"' <<<"$ccc")"
    else
      fail "$name O4 the client-credentials token names no client in azp or client_id: $(jq -c '{aud, azp, client_id, sub}' <<<"$ccc")"
    fi
    note "$name O4 the client-credentials token's aud is $(jq -c .aud <<<"$ccc")"
  fi

  # O5: the human access token's shape.
  if [[ -z "$at" ]]; then
    note "$name O5 no access token"
  elif is_jwt "$at"; then
    note "$name O5 the access token is a JWT: aud=$(claims "$at" | jq -c .aud) azp=$(claims "$at" | jq -r '.azp // "-"')"
  else
    note "$name O5 the access token is opaque"
  fi
}

want="${1:-all}"
if [[ "$want" == all || "$want" == dex ]]; then
  start_dex
  probe dex "$DEX_ISSUER" alice@example.com groups id "openid profile email groups offline_access"
fi
if [[ "$want" == all || "$want" == keycloak ]]; then
  start_keycloak
  # Keycloak has no "groups" scope (the client's mapper adds the claim), and its online refresh token
  # is the one oauth2-proxy and maku hold.
  probe keycloak "$KEYCLOAK_ISSUER" alice groups id "openid profile email"
  probe keycloak "$KEYCLOAK_ISSUER" alice /realm_access/roles access "openid profile email"
fi
echo "== $fails FAIL"
exit "$fails"
