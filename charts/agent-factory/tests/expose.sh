#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The web front door's render (plan v4 step 6, Task 12; D19, EX4, EX6): oauth2-proxy's arguments
# derived from maku.broker.human per preset, its Secrets by name, its policies, and the broker's
# :8080 opened to it alone. task chart:expose:k3d proves the same live.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
helm dependency build . >/dev/null
digest='v0.0.0@sha256:0000'
img=(--set maku.broker.image.tag=$digest --set maku.runController.image.tag=$digest
     --set maku.runController.images.harness.tag=$digest --set maku.runController.images.bridge.tag=$digest
     --set maku-factory.image.tag=$digest)
secrets=(--set demo.model.baseURL=http://model.example.test/v1 --set demo.model.name=m1 --set demo.model.apiKey=k1
         --set demo.githubApp.id=123 --set demo.githubApp.privateKey=pem1)
# The identity values (the broker in preset generic, its ids by Secret) with the front door on.
wp=("${img[@]}" -f values-demo.yaml -f tests/values-demo-secret.yaml "${secrets[@]}" -f tests/identity-values.yaml
    --set webProxy.enabled=true --set webProxy.existingSecret=rooms-proxy --set maku.broker.webProxy=true
    --set maku.networkPolicies.mode=kubernetes)
fail=0
out=''
ynone() {
  local got
  got=$(yq "$1" - <<<"$out") || { echo "FAIL: yq errored: $2" >&2; fail=1; return; }
  [ -z "$got" ] || { echo "FAIL: $2" >&2; fail=1; }
}
bad() {
  local want="$1" msg; shift
  msg="$(helm template af . -n maku "$@" 2>&1 >/dev/null || true)"
  grep -qF -- "$want" <<<"$msg" || { echo "FAIL: the render does not fail with \"$want\" (got: $msg)" >&2; fail=1; }
}
# jkind KIND 'jq expr' 'message': as ykind, in jq (yq compares only scalars, never maps or lists).
jkind() {
  yq ea -o=json -I0 "select(.kind == \"$1\")" - <<<"$out" | jq -s -e "map($2) | (length > 0 and all)" >/dev/null 2>&1 || { echo "FAIL: $3" >&2; fail=1; }
}
render() { out="$(helm template af . -n maku "$@")" || { echo "FAIL: the render failed: $*" >&2; fail=1; out=''; }; }
args() { yq ea -r 'select(.kind == "Deployment" and .metadata.name == "rooms-web-proxy") | .spec.template.spec.containers[0].args[]' - <<<"$out"; }
# grep reads the whole list (a here-string): with pipefail, grep -q closing a pipe early fails it.
has_arg() { grep -qxF -- "$1" <<<"$(args)" || { echo "FAIL: oauth2-proxy lacks $1 ($2)" >&2; fail=1; }; }
no_arg() { ! grep -qE -- "$1" <<<"$(args)" || { echo "FAIL: oauth2-proxy has $1 ($2)" >&2; fail=1; }; }
env_of() { yq ea -r "select(.kind == \"Deployment\" and .metadata.name == \"rooms-web-proxy\") | .spec.template.spec.containers[0].env[] | select(.name == \"$1\") | $2" - <<<"$out"; }

# off by default: nothing of the front door renders, and the broker opens :8080 to nobody
render "${img[@]}" -f values-demo.yaml -f tests/values-demo-secret.yaml "${secrets[@]}"
ynone 'select(.metadata.name == "rooms-web-proxy") | .kind' 'webProxy off renders a rooms-web-proxy object'

# generic (the identity values): the scopes, the groups, the derived arguments
render "${wp[@]}" --set-json 'maku.broker.human.scopes=["groups","email"]'
has_arg '--provider=oidc' 'the provider'
has_arg '--oidc-issuer-url=https://e2e-stub.e2e.svc.cluster.local' 'the issuer is human.issuer'
has_arg '--redirect-url=https://rooms.e2e.test/oauth2/callback' 'the callback is publicURL/oauth2/callback'
has_arg '--upstream=http://room-broker.maku.svc.cluster.local:8080' "the upstream is the broker's :8080"
for a in --pass-authorization-header=true --pass-access-token=true --skip-auth-strip-headers=true --pass-user-headers=true \
  --pass-basic-auth=false --set-xauthrequest=false --cookie-secure=true --cookie-httponly=true --cookie-samesite=strict \
  --cookie-refresh=1h --reverse-proxy=false --skip-provider-button=true --skip-jwt-bearer-tokens=true --email-domain=* \
  --http-address=0.0.0.0:4180 --metrics-address=0.0.0.0:44180 '--skip-auth-route=GET=^/\.well-known/roomctl$'; do
  has_arg "$a" 'EX4'
done
has_arg '--scope=openid profile email offline_access groups' 'generic: the base scopes plus human.scopes, once each'
has_arg '--oidc-groups-claim=groups' 'a top-level roles claim is the groups claim'
has_arg '--allowed-group=agents-admin' 'the admin group'
has_arg '--allowed-group=agents-member' 'the member group'
no_arg '^--skip-auth-route=GET=\^/link/github' 'without the GitHub account link, its callback is no skip-auth route'
no_arg '^--(client-id|client-secret|cookie-secret)=' 'a credential as an argument'
[[ $(env_of OAUTH2_PROXY_EXTRA_JWT_ISSUERS .value) == 'https://e2e-stub.e2e.svc.cluster.local=$(CLI_CLIENT_ID)' ]] ||
  { echo 'FAIL: EXTRA_JWT_ISSUERS is not <issuer>=$(CLI_CLIENT_ID)' >&2; fail=1; }
[[ $(env_of CLI_CLIENT_ID '.valueFrom.secretKeyRef | .name + "/" + .key') == e2e-broker-oidc/cli-client-id ]] ||
  { echo "FAIL: CLI_CLIENT_ID is not broker.oidc.existingSecret's cli key" >&2; fail=1; }
for e in OAUTH2_PROXY_CLIENT_ID:client-id OAUTH2_PROXY_CLIENT_SECRET:client-secret OAUTH2_PROXY_COOKIE_SECRET:cookie-secret; do
  [[ $(env_of "${e%%:*}" '.valueFrom.secretKeyRef | .name + "/" + .key') == "rooms-proxy/${e#*:}" ]] ||
    { echo "FAIL: ${e%%:*} is not rooms-proxy/${e#*:} by reference" >&2; fail=1; }
done
ynone 'select(.kind == "Deployment" and .metadata.name == "rooms-web-proxy") | .spec.template.spec.containers[0].env[] | select(.value != null and (.name | test("SECRET|CLIENT_ID$"))) | .name' 'a credential rendered as a value'
ynone 'select(.kind == "Secret" and (.metadata.name | test("proxy"))) | .metadata.name' 'the chart renders a web proxy Secret'
ynone 'select(.kind == "Deployment" and .metadata.name == "rooms-web-proxy") | .spec.template.spec.containers[0].env[] | select(.name == "PROJECT_ID" or .name == "OAUTH2_PROXY_SCOPE") | .name' 'generic: a ZITADEL project scope'
jkind Deployment 'select(.metadata.name == "rooms-web-proxy") | (.spec.replicas == 2 and .spec.template.spec.automountServiceAccountToken == false and .spec.template.spec.securityContext.runAsNonRoot == true and (.spec.template.spec.containers[0] | (.securityContext.readOnlyRootFilesystem and .securityContext.allowPrivilegeEscalation == false and .securityContext.capabilities.drop == ["ALL"] and .securityContext.seccompProfile.type == "RuntimeDefault" and .livenessProbe.httpGet.path == "/ping" and .readinessProbe.httpGet.path == "/ready" and .resources.limits.memory == "128Mi" and .resources.requests.cpu == "50m")))' 'the proxy: 2 replicas, no token, restricted, both probes, requests and limits'
jkind Deployment 'select(.metadata.name == "rooms-web-proxy") | .spec.template.spec.containers[0].image == "quay.io/oauth2-proxy/oauth2-proxy:v7.15.5@sha256:8498b0d0ef0a7b29686414000a08aee467f02d0299c9ed1e006a8f33fc017916"' 'the proxy runs the image the probe asserted, by digest'
pin="$(sed -n 's/^PROXY_IMAGE="\${PROXY_IMAGE:-\(.*\)}"$/\1/p' tests/expose-probe/probe.sh)"
[[ $pin == "$(yq .webProxy.image values.yaml)" ]] || { echo "FAIL: webProxy.image and the probe's PROXY_IMAGE differ ($pin)" >&2; fail=1; }
jkind ServiceAccount 'select(.metadata.name == "rooms-web-proxy") | .automountServiceAccountToken == false' 'the ServiceAccount mounts no token'
jkind Service 'select(.metadata.name == "rooms-web-proxy") | ([.spec.ports[] | .name + ":" + (.port | tostring)] | join(",")) == "http:80,metrics:44180"' 'the Service: http 80, metrics 44180'
jkind PodDisruptionBudget 'select(.metadata.name == "rooms-web-proxy") | .spec.maxUnavailable == 1' 'a PDB'
render "${wp[@]}" --set webProxy.replicas=1
ynone 'select(.kind == "PodDisruptionBudget" and .metadata.name == "rooms-web-proxy") | .metadata.name' 'one replica renders a PDB'

# the GitHub account link adds exactly its callback, GET only (by the broker's Secret, or set directly)
for gl in '--set maku.broker.githubLink.existingSecret=gh-link' '--set maku.broker.human.githubLink.clientIDFile=/a'; do
  # shellcheck disable=SC2086
  render "${wp[@]}" $gl
  has_arg '--skip-auth-route=GET=^/link/github/callback$' "$gl: GET /link/github/callback is a skip-auth route"
  [[ $(args | grep -c -- '^--skip-auth-route=') == 2 ]] || { echo "FAIL: $gl: skip-auth routes $(args | grep -- '^--skip-auth-route=' | tr '\n' ' ')" >&2; fail=1; }
done

# roles claims oauth2-proxy cannot read (a pointer, a dotted path; Task 9 X5): no groups check there
for rc in /realm_access/roles realm_access.roles 'https://example.test/roles'; do
  render "${wp[@]}" --set "maku.broker.human.rolesClaim=$rc"
  no_arg '^--(allowed-group|oidc-groups-claim)=' "rolesClaim $rc"
done

# zitadel: the project scope through $(PROJECT_ID), from the OIDC Secret
render "${wp[@]}" --set maku.broker.human.preset=zitadel --set maku.broker.human.tokenUse=access --set maku.broker.human.rolesClaim=
[[ $(env_of PROJECT_ID '.valueFrom.secretKeyRef | .name + "/" + .key') == e2e-broker-oidc/project-id ]] || { echo 'FAIL: zitadel: PROJECT_ID is not the OIDC Secret project-id' >&2; fail=1; }
[[ $(env_of OAUTH2_PROXY_SCOPE .value) == 'openid profile email offline_access urn:zitadel:iam:org:project:id:$(PROJECT_ID):aud' ]] || { echo 'FAIL: zitadel: the scope lacks the project' >&2; fail=1; }
no_arg '^--scope=' 'zitadel: a --scope beside OAUTH2_PROXY_SCOPE'
no_arg '^--allowed-group=' 'zitadel without a roles claim: allowed-group'

# extraArgs: appended; a derived key is refused, naming it
render "${wp[@]}" --set webProxy.extraArgs.silence-ping-logging=true
has_arg '--silence-ping-logging=true' 'extraArgs are appended'
# Security review of #545: reverse-proxy=true trusts every client's X-Forwarded-Uri, which then matches
# a skip-auth route; trusted-ip, skip-auth-regex, the config files and the rest widen what passes.
for k in upstream skip-auth-strip-headers cookie-secure client-secret extra-jwt-issuers trusted-ip skip-auth-regex \
  skip-auth-preflight whitelist-domain config alpha-config reverse-proxy pass-user-headers; do
  bad "webProxy.extraArgs.$k: the chart derives it" "${wp[@]}" --set "webProxy.extraArgs.$k=x"
done

# extraEnv may not set oauth2-proxy's own options: its OAUTH2_PROXY_* variables are its flags
bad 'webProxy.extraEnv OAUTH2_PROXY_REVERSE_PROXY: oauth2-proxy reads it as a flag' "${wp[@]}" --set-json 'webProxy.extraEnv=[{"name":"OAUTH2_PROXY_REVERSE_PROXY","value":"true"}]'
render "${wp[@]}" --set-json 'webProxy.extraEnv=[{"name":"SSL_CERT_FILE","value":"/ca.crt"}]'
[[ $(env_of SSL_CERT_FILE .value) == /ca.crt ]] || { echo 'FAIL: extraEnv SSL_CERT_FILE is not passed' >&2; fail=1; }

# fail closed
bad 'webProxy.existingSecret is empty: name the Secret holding the web client and the cookie secret (keys client-id, client-secret, cookie-secret; D14)' "${wp[@]}" --set webProxy.existingSecret=
bad 'maku.broker.webProxy (false) must equal webProxy.enabled (true)' "${wp[@]}" --set maku.broker.webProxy=false
bad 'maku.broker.webProxy (true) must equal webProxy.enabled (false)' "${wp[@]}" --set webProxy.enabled=false
bad 'webProxy.enabled needs maku.broker.publicURL, https' "${wp[@]}" --set maku.broker.publicURL=http://rooms.e2e.test
bad 'webProxy.enabled needs maku.broker.human.mode oidc' "${wp[@]}" --set maku.broker.human.mode=local --set maku.broker.publicURL=http://localhost:8080 --set maku.broker.oidc.existingSecret=
bad 'webProxy.enabled needs maku.broker.oidc.existingSecret' "${wp[@]}" --set maku.broker.oidc.existingSecret=
bad 'webProxy.enabled needs maku.networkPolicies.mode cilium or kubernetes' "${wp[@]}" --set maku.networkPolicies.mode=none

# mode kubernetes: the proxy admits the gateway's pods and the scraper; the broker admits the proxy alone
render "${wp[@]}" --set-json 'expose.networkPolicy.from=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"gateway"}}}]' \
  --set-json 'maku.networkPolicies.kubernetes.metricsFrom=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"monitoring"}}}]'
jkind NetworkPolicy 'select(.metadata.name == "rooms-web-proxy") | ((.spec.policyTypes | join(",")) == "Ingress,Egress" and .spec.ingress == [{"from": [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "gateway"}}}], "ports": [{"port": 4180, "protocol": "TCP"}]}, {"from": [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "monitoring"}}}], "ports": [{"port": 44180, "protocol": "TCP"}]}])' 'the proxy admits the gateway on :4180 and the scraper on :44180, nothing else'
jkind NetworkPolicy 'select(.metadata.name == "rooms-web-proxy") | (.spec.egress | length) == 3 and .spec.egress[1] == {"to": [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "room-broker"}}}], "ports": [{"port": 8080, "protocol": "TCP"}]} and .spec.egress[2].to[0].ipBlock.cidr == "0.0.0.0/0" and (.spec.egress[2].to[0].ipBlock.except | contains(["10.0.0.0/8"])) and .spec.egress[2].ports == [{"port": 443, "protocol": "TCP"}]' "the proxy reaches DNS, the broker's :8080 and public addresses on the issuer's 443"
jkind NetworkPolicy 'select(.metadata.name == "room-broker") | [.spec.ingress[] | select((.ports // []) | map(.port) | any(. == 8080))] == [{"from": [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "rooms-web-proxy"}}}], "ports": [{"port": 8080, "protocol": "TCP"}]}]' "the broker's :8080 admits the web proxy alone"
render "${wp[@]}" --set-json 'webProxy.networkPolicy.idp.to=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"e2e"}}}]' --set webProxy.networkPolicy.idp.port=8443
jkind NetworkPolicy 'select(.metadata.name == "rooms-web-proxy") | .spec.egress[2] == {"to": [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "e2e"}}}], "ports": [{"port": 8443, "protocol": "TCP"}]}' 'webProxy.networkPolicy.idp names an in-cluster IdP and its pod port'

# mode cilium: the ingress entity, the host's probes, the IdP by name
render "${wp[@]}" --set maku.networkPolicies.mode=cilium --set maku.networkPolicies.enabled=true
jkind CiliumNetworkPolicy 'select(.metadata.name == "rooms-web-proxy") | (.spec.ingress[0].fromEntities == ["ingress"] and .spec.ingress[1].fromEntities == ["host"] and (.spec.ingress | length) == 2)' 'cilium: the proxy admits the ingress entity and the probes'
jkind CiliumNetworkPolicy 'select(.metadata.name == "rooms-web-proxy") | (.spec.egress[1].toEndpoints[0].matchLabels."app.kubernetes.io/name" == "room-broker" and .spec.egress[2] == {"toFQDNs": [{"matchName": "e2e-stub.e2e.svc.cluster.local"}], "toPorts": [{"ports": [{"port": "443", "protocol": "TCP"}]}]})' "cilium: the proxy reaches the broker's :8080 and the issuer's host on 443"
jkind CiliumNetworkPolicy 'select(.metadata.name == "room-broker") | [.spec.ingress[] | select((.toPorts // []) | map(.ports[].port) | any(. == "8080"))] | length == 1' "cilium: the broker's :8080 admits one rule, the web proxy's"
ynone 'select(.kind == "NetworkPolicy" and .metadata.name == "rooms-web-proxy") | .kind' 'cilium mode renders a NetworkPolicy'

# monitoring: the proxy's ServiceMonitor, its CNP admitting the scraper
render "${wp[@]}" --set maku.networkPolicies.mode=cilium --set maku.networkPolicies.enabled=true --set monitoring.serviceMonitor.enabled=true \
  --set-json 'monitoring.serviceMonitor.cilium.fromEndpoints=[{"matchLabels":{"io.kubernetes.pod.namespace":"monitoring"}}]'
jkind ServiceMonitor 'select(.metadata.name == "rooms-web-proxy") | (.spec.selector.matchLabels."app.kubernetes.io/name" == "rooms-web-proxy" and .spec.endpoints[0].port == "metrics")' 'monitoring: a ServiceMonitor for the web proxy'
jkind CiliumNetworkPolicy 'select(.metadata.name == "rooms-web-proxy") | .spec.ingress[2] == {"fromEndpoints": [{"matchLabels": {"io.kubernetes.pod.namespace": "monitoring"}}], "toPorts": [{"ports": [{"port": "44180", "protocol": "TCP"}]}]}' 'cilium: the scraper reaches :44180'

[[ $fail == 0 ]] || exit 1
echo "agent-factory web front door checks passed"
