#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Renders the umbrella chart and asserts what it adds to maku and maku-factory: the broker's TLS
# (three Secrets, one CA that signed the serving certificate, the SANs the broker's clients dial),
# the single-replica Postgres, and the two DSN Secrets. Stability across upgrades is not testable
# here (`lookup` returns nothing under `helm template`); the live install checks it.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
helm dependency build . >/dev/null
digest='v0.0.0@sha256:0000'
# the subcharts' image digests; values-demo.yaml carries everything else
img=(--set maku.broker.image.tag=$digest --set maku.runController.image.tag=$digest
     --set maku.runController.images.harness.tag=$digest --set maku.runController.images.bridge.tag=$digest
     --set maku-factory.image.tag=$digest)
secrets=(--set demo.model.baseURL=http://model.example.test/v1 --set demo.model.name=m1 --set demo.model.apiKey=k1
         --set demo.githubApp.id=123 --set demo.githubApp.privateKey=pem1)
args=("${img[@]}" -f values-demo.yaml "${secrets[@]}")
fail=0
out=''
check() { grep -qF -- "$1" <<<"$out" || { echo "FAIL: $2" >&2; fail=1; }; }
ykind() {
  yq ea -e "[select(.kind == \"$1\") | $2] | (length > 0 and all)" - <<<"$out" >/dev/null 2>&1 || { echo "FAIL: $3" >&2; fail=1; }
}
ynone() {
  local got
  got=$(yq "$1" - <<<"$out") || { echo "FAIL: yq errored: $2" >&2; fail=1; return; }
  [ -z "$got" ] || { echo "FAIL: $2" >&2; fail=1; }
}
# secret_field NAME KEY: the decoded value of one Secret data key.
secret_field() { yq ea "select(.kind == \"Secret\" and .metadata.name == \"$1\") | .data.\"$2\"" - <<<"$out" | base64 -d; }

out="$(helm template af . -n maku "${args[@]}")"

# The golden: -f values-demo.yaml renders exactly what `--set profile=demo` rendered before the demo's
# defaults moved there (v0.4x), generated secrets masked. A deliberate change to the demo's render
# updates it: UPDATE_GOLDEN=1 bash charts/agent-factory/tests/render.sh, then review the diff.
if [[ ${UPDATE_GOLDEN:-} == 1 ]]; then
  bash tests/normalize.sh <<<"$out" >tests/golden/demo.yaml
fi
bash tests/normalize.sh <<<"$out" | diff -u tests/golden/demo.yaml - >&2 || { echo "FAIL: the demo render differs from tests/golden/demo.yaml" >&2; fail=1; }
# Rollout step 2, PR A (D16, D17, D18): the demo gains exactly the runs quota, the run-pod policy and
# its binding, and its two unsafe settings on the install banner; no RuntimeClass ClusterRole (no class).
got=$(yq ea '[select(.kind == "ResourceQuota" or .kind == "ValidatingAdmissionPolicy" or .kind == "ValidatingAdmissionPolicyBinding" or (.kind == "ClusterRole" and (.metadata.name | test("runtimeclass$")))) | .kind + "/" + .metadata.name] | sort | join(" ")' - <<<"$out")
want="ResourceQuota/runs ValidatingAdmissionPolicy/maku-maku-run-pods ValidatingAdmissionPolicy/room-broker-maku-agentruns ValidatingAdmissionPolicyBinding/maku-maku-run-pods ValidatingAdmissionPolicyBinding/room-broker-maku-agentruns"
[[ $got == "$want" ]] || { echo "FAIL: the demo's step-2 objects are '$got', want '$want'" >&2; fail=1; }
ykind ConfigMap 'select(.metadata.name == "room-broker-config") | (.data."config.yaml" | from_yaml | .install.unsafe | join(",")) == "unsafe_allow_runc,network_policy_none"' 'the demo shows its two unsafe settings: runc and no network policy'
# PR6: the skeleton disables both subcharts, so nothing but the umbrella's own templates renders
# and the gate's message is the error whatever order Helm renders in; values-demo.yaml enables them.
yq -e '.maku.enabled == false and .maku-factory.enabled == false' values.yaml >/dev/null 2>&1 || { echo "FAIL: values.yaml does not disable maku and maku-factory" >&2; fail=1; }
yq -e '.maku.enabled == true and .maku-factory.enabled == true' values-demo.yaml >/dev/null 2>&1 || { echo "FAIL: values-demo.yaml does not enable maku and maku-factory" >&2; fail=1; }
deep="$(find templates -mindepth 2 -type f)"
[[ -z $deep ]] || { echo "FAIL: a template below templates/ ($deep): the gate must not rely on Helm's render order" >&2; fail=1; }
# the packaged chart ships values-demo.yaml and the example, never tests/ (.helmignore)
pkg="$(mktemp -d)"; helm package . -d "$pkg" >/dev/null
shipped="$(tar -tzf "$pkg"/agent-factory-*.tgz)"; rm -rf "$pkg"
grep -qx 'agent-factory/values-demo.yaml' <<<"$shipped" || { echo "FAIL: the package lacks values-demo.yaml" >&2; fail=1; }
grep -qx 'agent-factory/values-mine.example.yaml' <<<"$shipped" || { echo "FAIL: the package lacks values-mine.example.yaml" >&2; fail=1; }
! grep -q '^agent-factory/tests/' <<<"$shipped" || { echo "FAIL: the package ships tests/" >&2; fail=1; }
# --set profile=demo without values-demo.yaml fails closed, naming the file (PR5)
msg="$(helm template af . -n maku "${img[@]}" --set profile=demo "${secrets[@]}" 2>&1 >/dev/null || true)"
grep -qF 'profile demo needs -f values-demo.yaml' <<<"$msg" || { echo "FAIL: --set profile=demo alone does not fail naming values-demo.yaml (got: $msg)" >&2; fail=1; }

# TLS: three Secrets, the same CA, kept on upgrade
ykind Secret 'select(.metadata.name == "room-broker-tls") | (.type == "kubernetes.io/tls" and (.data | has("tls.crt")) and (.data | has("tls.key")) and .metadata.annotations."helm.sh/resource-policy" == "keep")' 'room-broker-tls is a kept kubernetes.io/tls Secret with tls.crt and tls.key'
ykind Secret 'select(.metadata.name == "room-broker-ca") | ((.data | keys | join(",")) == "ca.crt" and .metadata.annotations."helm.sh/resource-policy" == "keep")' 'room-broker-ca holds only ca.crt, kept'
ykind Secret 'select(.metadata.name == "openbao-ca") | ((.data | keys | join(",")) == "ca.crt" and .metadata.annotations."helm.sh/resource-policy" == "keep")' "the factory's broker CA Secret (maku-factory brokerCASecret) holds ca.crt, kept"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
secret_field room-broker-ca ca.crt >"$tmp/ca.crt"
secret_field openbao-ca ca.crt >"$tmp/factory-ca.crt"
secret_field room-broker-tls tls.crt >"$tmp/tls.crt"
cmp -s "$tmp/ca.crt" "$tmp/factory-ca.crt" || { echo "FAIL: the factory's CA Secret differs from room-broker-ca" >&2; fail=1; }
openssl verify -CAfile "$tmp/ca.crt" "$tmp/tls.crt" >/dev/null 2>&1 || { echo "FAIL: the CA in room-broker-ca did not sign room-broker-tls" >&2; fail=1; }
sans="$(openssl x509 -in "$tmp/tls.crt" -noout -ext subjectAltName 2>/dev/null || true)"
for san in 'DNS:room-broker.maku.svc' 'DNS:room-broker.maku.svc.cluster.local'; do
  grep -qF "$san" <<<"$sans" || { echo "FAIL: the broker certificate lacks the SAN $san" >&2; fail=1; }
done
[[ "$(secret_field room-broker-tls tls.key)" == *"PRIVATE KEY"* ]] || { echo "FAIL: tls.key is not a private key" >&2; fail=1; }
[[ "$(secret_field room-broker-ca ca.crt)" != *"PRIVATE KEY"* ]] || { echo "FAIL: room-broker-ca carries a key" >&2; fail=1; }

# the cluster domain flows into the SANs
dom="$(helm template af . -n demo "${args[@]}" --set clusterDomain=example.test --set maku.clusterDomain=example.test --set maku-factory.clusterDomain=example.test | yq ea 'select(.kind == "Secret" and .metadata.name == "room-broker-tls") | .data."tls.crt"' - | base64 -d | openssl x509 -noout -ext subjectAltName)"
grep -qF 'DNS:room-broker.demo.svc.example.test' <<<"$dom" || { echo "FAIL: clusterDomain does not reach the SANs" >&2; fail=1; }

# the umbrella's helpers must not replace the subcharts': the factory keeps its name label (a selector/template mismatch broke the first live install)
ykind Deployment 'select(.metadata.name == "agent-factory") | (.spec.template.metadata.labels."app.kubernetes.io/name" == "agent-factory" and .spec.selector.matchLabels."app.kubernetes.io/name" == "agent-factory")' 'the factory Deployment keeps its app.kubernetes.io/name label'

# Postgres
ykind StatefulSet 'select(.metadata.name == "rooms-db") | (.spec.replicas == 1 and (.spec.template.spec.containers[0].image | test("^docker[.]io/library/postgres:17@sha256:[0-9a-f]{64}$")))' 'rooms-db is one replica, postgres:17 by digest'
ykind StatefulSet 'select(.metadata.name == "rooms-db") | .spec.volumeClaimTemplates[0].spec.resources.requests.storage == "1Gi"' 'a 1Gi PVC'
ynone 'select(.kind == "StatefulSet" and .metadata.name == "rooms-db") | .spec.volumeClaimTemplates[0].spec.storageClassName // ""' 'the PVC leaves the StorageClass to the cluster default'
ykind StatefulSet 'select(.metadata.name == "rooms-db") | .spec.template.spec.containers[0] | (.livenessProbe != null and .readinessProbe != null)' 'liveness and readiness probes'
ykind StatefulSet 'select(.metadata.name == "rooms-db") | .spec.template.spec.containers[0].resources | (.requests.cpu != null and .requests.memory != null and .limits.cpu != null and .limits.memory != null)' 'requests and limits'
ykind StatefulSet 'select(.metadata.name == "rooms-db") | ((.spec.template.spec.securityContext | (.runAsNonRoot == true and .seccompProfile.type == "RuntimeDefault")) and (.spec.template.spec.containers[0].securityContext | (.allowPrivilegeEscalation == false and .capabilities.drop[0] == "ALL")))' 'restricted security context'
ykind Service 'select(.metadata.name == "rooms-db") | .spec.ports[0].port == 5432' 'Service rooms-db on 5432'
init="$(yq ea 'select(.kind == "ConfigMap" and .metadata.name == "rooms-db-init") | .data."init.sh"' - <<<"$out")"
for want in 'CREATE ROLE rooms_owner LOGIN' 'CREATE ROLE rooms_broker LOGIN' 'CREATE ROLE rooms_retention LOGIN' 'CREATE DATABASE rooms OWNER rooms_owner' 'GRANT CREATE, USAGE ON SCHEMA public TO rooms_owner'; do
  grep -qF "$want" <<<"$init" || { echo "FAIL: the init script lacks: $want" >&2; fail=1; }
done
grep -qiE 'CREATEROLE|SUPERUSER' <<<"$init" && { echo "FAIL: the init script grants CREATEROLE or SUPERUSER" >&2; fail=1; }

# DSNs
ykind Secret 'select(.metadata.name == "rooms-broker-dsn") | .stringData.uri | test("^postgresql://rooms_broker:[A-Za-z0-9]{32}@rooms-db[.]maku[.]svc:5432/rooms[?]sslmode=disable$")' 'rooms-broker-dsn: rooms_broker, in-cluster host, sslmode=disable'
ykind Secret 'select(.metadata.name == "rooms-owner-dsn") | .stringData.uri | test("^postgresql://rooms_owner:[A-Za-z0-9]{32}@rooms-db[.]maku[.]svc:5432/rooms[?]sslmode=disable$")' 'rooms-owner-dsn: rooms_owner, in-cluster host, sslmode=disable'
ykind Secret 'select(.metadata.name == "rooms-db-passwords") | .metadata.annotations."helm.sh/resource-policy" == "keep"' 'the passwords are kept'
ykind Secret 'select(.metadata.name == "rooms-broker-dsn" or .metadata.name == "rooms-owner-dsn") | .metadata.annotations."helm.sh/resource-policy" == "keep"' 'the DSN Secrets are kept'

# the subcharts consume the Secrets this chart creates
ykind Deployment 'select(.metadata.name == "room-broker") | (.spec.template.spec.containers[0].env[] | select(.name == "ROOMS_DATABASE_URL") | .valueFrom.secretKeyRef.name) == "rooms-broker-dsn"' 'the broker reads rooms-broker-dsn'
ykind Deployment 'select(.metadata.name == "room-broker") | (.spec.template.spec.volumes[] | select(.secret.secretName == "room-broker-tls") | .name) != ""' 'the broker mounts room-broker-tls'

# nothing needing cert-manager or CNPG
for k in Certificate Issuer ClusterIssuer Cluster; do
  ynone "select(.kind == \"$k\") | .kind" "the default render has a $k (cert-manager or CNPG)"
done

# the readiness probe waits for the real server (TCP), not the entrypoint's socket-only init server
ykind StatefulSet 'select(.metadata.name == "rooms-db") | (.spec.template.spec.containers[0].readinessProbe.exec.command | join(" ")) == "pg_isready -h 127.0.0.1 -U postgres -d postgres"' 'readiness probes 127.0.0.1, not the socket'

# plain NetworkPolicies (the demo has no Cilium): the database admits only the broker, the factory's API only the broker
ykind NetworkPolicy 'select(.metadata.name == "rooms-db") | (.spec.podSelector.matchLabels."app.kubernetes.io/name" == "rooms-db" and (.spec.policyTypes | join(",")) == "Ingress" and (.spec.ingress | length) == 1 and .spec.ingress[0].from[0].podSelector.matchLabels."app.kubernetes.io/name" == "room-broker" and .spec.ingress[0].ports[0].port == 5432)' 'rooms-db admits 5432 only from the room-broker pods'
ykind NetworkPolicy 'select(.metadata.name == "agent-factory-api") | (.spec.podSelector.matchLabels."app.kubernetes.io/name" == "agent-factory" and (.spec.policyTypes | join(",")) == "Ingress" and ([.spec.ingress[] | select(.from != null)] | length) == 1 and .spec.ingress[0].from[0].podSelector.matchLabels."app.kubernetes.io/name" == "room-broker" and .spec.ingress[0].ports[0].port == 8082)' "the factory's API (8082) admits only the room-broker pods"
ykind NetworkPolicy 'select(.metadata.name == "agent-factory-api") | ((.spec.ingress | length) == 2 and .spec.ingress[1].from == null and (.spec.ingress[1].ports | length) == 1 and .spec.ingress[1].ports[0].port == "ops")' "the factory's ops port (probes, 9090) is admitted from anywhere, by name"
ykind NetworkPolicy 'select(.metadata.name == "agent-factory-api") | (.spec.ingress[0].from != null and (.spec.ingress[0].ports | length) == 1 and .spec.ingress[0].ports[0].port == 8082)' 'the factory API (8082) is admitted only through the broker rule'
ykind NetworkPolicy 'select(.metadata.name == "agent-factory-api") | ((.spec.ingress | length) == 2 and .spec.ingress[0].ports[0].port != 8080 and .spec.ingress[0].ports[0].port != "intake" and .spec.ingress[1].ports[0].port != 8080 and .spec.ingress[1].ports[0].port != "intake")' 'no rule admits the intake port (8080)'
ynone 'select(.kind == "NetworkPolicy") | .spec.egress // ""' 'a plain NetworkPolicy restricts egress (runs need GitHub and the model)'
np="$(helm template af . -n maku "${args[@]}" --set networkPolicies.plain=false | yq ea 'select(.kind == "NetworkPolicy") | .metadata.name' -)"
[[ -z $np ]] || { echo "FAIL: networkPolicies.plain=false still renders NetworkPolicies ($np)" >&2; fail=1; }

# profile: demo
# an unset profile fails, naming what production must supply
msg="$(helm template af . -n maku "${img[@]}" 2>&1 >/dev/null || true)"
grep -qF 'profile is required: demo (-f values-demo.yaml), or production (rollout steps 2 to 6)' <<<"$msg" || { echo "FAIL: an unset profile does not fail with the profile message (got: $msg)" >&2; fail=1; }
msg="$(helm template af . -n maku "${img[@]}" --set profile=production 2>&1 >/dev/null || true)"
grep -qF 'profile production: its seams arrive in rollout steps 2 to 6' <<<"$msg" || { echo "FAIL: profile=production does not fail closed until rollout steps 2 to 6" >&2; fail=1; }
# maku: no Cilium, local human, the portable runs group, runc
ynone 'select(.kind == "CiliumNetworkPolicy") | .metadata.name' 'the demo renders a CiliumNetworkPolicy'
ynone 'select(.kind == "ClusterRole" or .kind == "Role") | .rules[] | select(.apiGroups[] == "cilium.io")' 'the demo grants cilium.io'
bcfg="$(yq ea 'select(.kind == "ConfigMap" and .metadata.name == "room-broker-config") | .data."config.yaml"' - <<<"$out")"
bq() { yq -e "$1" - <<<"$bcfg" >/dev/null 2>&1 || { echo "FAIL: broker config: $2" >&2; fail=1; }; }
bq '.human.mode == "local"' 'local human mode'
# task 6: the room tools on, the broker verifying each run's own token (no gateway in the demo)
bq '.roomTools.enabled == true and .roomTools.auth == "tokenreview"' 'room tools on, verified by tokenreview'
ykind Service 'select(.metadata.name == "room-broker") | .spec.ports[] | select(.name == "room-tools") | .port == 8090' 'the broker Service exposes :8090 to the runs'
ykind ClusterRoleBinding 'select(.roleRef.name == "system:auth-delegator") | .subjects[0].name == "room-broker"' "the broker's auth-delegator binding (the run issuer's, reused)"
bq '.runIssuers[0].mode == "tokenreview"' 'runs are verified by tokenreview'
bq '.systemIssuer.mode == "tokenreview"' 'the system issuer is tokenreview'
bq '.systemPrincipals."system:serviceaccount:maku:agent-factory" == "system:factory"' "the factory's ServiceAccount, in the release namespace, is system:factory"
bq '.factoryInsecure == true and (.factoryURL | test("^http://agent-factory:8082$"))' 'the broker calls the factory over http, opted in'
bq '.runs.group == "maku.ogenki.io" and .runs.namespace == "agents" and .runs.namePrefix == "run-"' 'the portable runs kind'
ynone 'select(.kind == "Certificate")' 'the demo renders a cert-manager Certificate'
rcfg="$(yq ea 'select(.kind == "ConfigMap" and (.metadata.name | test("run-controller"))) | .data[]' - <<<"$out")"
grep -qE '^networkPolicy: none$' <<<"$rcfg" || { echo "FAIL: the run controller does not run with networkPolicy: none" >&2; fail=1; }
ynone 'select(.kind == "RuntimeClass") | .metadata.name' 'the demo renders a RuntimeClass'
grep -qxF 'roomToolsURL: "http://room-broker.maku.svc.cluster.local:8090/mcp"' <<<"$rcfg" || { echo "FAIL: the run controller does not hand runs the :8090 room tools URL" >&2; fail=1; }
grep -qE '^allowUnsandboxed: true$' <<<"$rcfg" || { echo "FAIL: the run controller does not allow unsandboxed runs" >&2; fail=1; }
grep -qE '^runtimeClassName:' <<<"$rcfg" && ! grep -qE '^runtimeClassName: ""$' <<<"$rcfg" && { echo "FAIL: the run controller names a RuntimeClass" >&2; fail=1; }
# maku-factory: demo mode, one replica, the umbrella's CA, the config
ykind Deployment 'select(.metadata.name == "agent-factory") | .spec.replicas == 1' 'the factory runs one replica'
ykind Deployment 'select(.metadata.name == "agent-factory") | ([.spec.template.spec.volumes[] | select(.name == "oidc")] | length) == 0' 'the demo mounts no oidc Secret'
ykind Deployment 'select(.metadata.name == "agent-factory") | (.spec.template.spec.volumes[] | select(.name == "broker-ca") | .secret.secretName) == "openbao-ca"' "the factory mounts the umbrella's CA Secret (openbao-ca)"
fcfg="$(yq ea 'select(.kind == "ConfigMap" and .metadata.name == "agent-factory-config") | .data."config.yaml"' - <<<"$out")"
# the rendered config is a valid factory config: plan A's own Parse (a Go test, skipped without the variable)
printf '%s\n' "$fcfg" >"$tmp/factory-config.yaml"
(cd ../.. && CHART_DEMO_FACTORY_CONFIG="$tmp/factory-config.yaml" go test -count=1 -run TestRenderedDemoChartConfigParses ./internal/factory/config >"$tmp/parse.log" 2>&1) || { echo "FAIL: the rendered factory config does not parse:" >&2; cat "$tmp/parse.log" >&2; fail=1; }
fq() { yq -e "$1" - <<<"$fcfg" >/dev/null 2>&1 || { echo "FAIL: factory config: $2" >&2; fail=1; }; }
fq '.demo == true' 'demo: true'
fq '.broker.url == "https://room-broker.maku.svc.cluster.local:8443" and .broker.caFile == "/etc/agent-factory/openbao-ca/ca.crt" and .broker.tokenFile == "/var/run/secrets/agents/rooms/token"' 'the broker over https with the mounted CA and the projected token'
fq '.runs.group == "maku.ogenki.io" and .runs.namespace == "agents" and .runs.namePrefix == "run-"' 'the portable runs kind'
[[ "$(yq '.runs' - <<<"$fcfg" | yq -o=json -I0 'sort_keys(.)' -)" == "$(yq '.runs' - <<<"$bcfg" | yq -o=json -I0 'sort_keys(.)' -)" ]] || { echo "FAIL: the factory and the broker disagree on runs" >&2; fail=1; }
fq '.loops.review.scope == "scoper" and (.loops.review.judge | length > 0)' 'the review class has a scoper and a judge'
fq '.loops."docs-links".scope == "scoper" and (.loops."docs-links".judge | length > 0)' 'the docs-links class has a scoper and a judge'
fq '.templates.solo.roles | contains(["reviewer"])' 'solo carries the reviewer'
fq '.repositories | length == 1' 'one repository'
fq '.repositories[0].merge.gateStatus == true and .repositories[0].merge.policyBot == false' 'gateStatus on, policy-bot off'
fq '.repositories[0].merge.approveToMerge == true' "approve-to-merge on: a maintainer's approval of the head merges it"
fq '.repositories[0].triggerLabel == "factory/ready"' 'the factory/ready trigger'
fq '.repositories[0].classes | (has("docs-links") and has("revert"))' 'docs-links and revert'
fq '[.repositories[0].classes[] | select(.shadow != true)] | length == 0' 'every class is in shadow'
fq '[.repositories[0].classes[] | select((.allowedFiles | length) == 0)] | length == 0' 'every class carries allowedFiles'
fq '.github.mergerAppIDFile == .github.appIDFile and .github.mergerKeyFile == .github.privateKeyFile' 'the merger signs with the same App files'
fq '(has("meter") | not) and (.api | has("humanIssuer") | not)' 'no meter, no human API keys'
# The umbrella passes the overlap rollout through the factory's config, with no parallel value.
for mode in shadow enforce; do
  out="$(helm template af . -n maku "${args[@]}" --set maku-factory.config.intake.overlap.mode="$mode" \
    --set maku-factory.config.intake.overlap.scoperEnforcement=true)"
  fcfg="$(yq ea 'select(.kind == "ConfigMap" and .metadata.name == "agent-factory-config") | .data."config.yaml"' - <<<"$out")"
  got="$(yq '.intake.overlap.mode' - <<<"$fcfg")"
  [ "$got" = "$mode" ] || { echo "FAIL: factory config.intake.overlap.mode=$mode rendered as '$got'" >&2; fail=1; }
  fq '.intake.overlap.scoperEnforcement == true' 'overlap scoperEnforcement passthrough'
done
# clusterDomain must agree at the three places that carry it
msg="$(helm template af . -n maku "${args[@]}" --set clusterDomain=a.test --set maku.clusterDomain=a.test 2>&1 >/dev/null || true)"
grep -qF 'clusterDomain' <<<"$msg" && grep -qF 'must be equal' <<<"$msg" || { echo "FAIL: a clusterDomain that differs from maku-factory's does not fail" >&2; fail=1; }
# secrets: rendered from values, or not at all when an existing Secret is named
ykind Secret 'select(.metadata.name == "agent-model") | (.metadata.namespace == "agents" and .stringData.base_url == "http://model.example.test/v1" and .stringData.model == "m1" and .stringData.api_key == "k1")' 'agent-model in the runs namespace from demo.model'
ykind Secret 'select(.metadata.name == "agent-factory-github") | (.metadata.namespace == "maku" and .stringData.app_id == "123" and .stringData.private_key == "pem1")' 'agent-factory-github from demo.githubApp'
ynone 'select(.kind == "Secret" and .metadata.name == "agent-git") | .metadata.name' 'agent-git is rendered: the factory writes it'
exist="$(helm template af . -n maku "${img[@]}" -f values-demo.yaml --set demo.model.existingSecret=agent-model --set demo.githubApp.existingSecret=agent-factory-github)"
for n in agent-model agent-factory-github; do
  [[ -z "$(yq ea "select(.kind == \"Secret\" and .metadata.name == \"$n\") | .metadata.name" - <<<"$exist")" ]] || { echo "FAIL: $n is rendered although its existingSecret is set" >&2; fail=1; }
done
msg="$(helm template af . -n maku "${img[@]}" -f values-demo.yaml 2>&1 >/dev/null || true)"
grep -qF 'demo.model.baseURL' <<<"$msg" || { echo "FAIL: no model values and no existingSecret does not fail" >&2; fail=1; }
msg="$(helm template af . -n maku "${img[@]}" -f values-demo.yaml --set demo.model.existingSecret=other --set demo.githubApp.existingSecret=agent-factory-github 2>&1 >/dev/null || true)"
grep -qF 'must have that name' <<<"$msg" || { echo "FAIL: an existingSecret the controller does not read does not fail" >&2; fail=1; }

# values-mine.example.yaml renders (dummy values for the CHANGE lines) and its config parses
sed -e 's#sk-change-me#k1#' -e 's#id: "123456"#id: "123"#' -e 's#my-app\[bot\]#maku-demo[bot]#' values-mine.example.yaml >"$tmp/mine.yaml"
mine="$(helm template af . -n maku "${img[@]}" -f values-demo.yaml -f "$tmp/mine.yaml")" || { echo "FAIL: values-mine.example.yaml does not render" >&2; fail=1; mine=''; }
if [[ -n $mine ]]; then
  yq ea 'select(.kind == "ConfigMap" and .metadata.name == "agent-factory-config") | .data."config.yaml"' - <<<"$mine" >"$tmp/mine-config.yaml"
  yq -e '.repositories | length == 1' "$tmp/mine-config.yaml" >/dev/null 2>&1 || { echo "FAIL: the example's config lacks its repository" >&2; fail=1; }
  yq -e '.repositories[0].merge.approveToMerge == true' "$tmp/mine-config.yaml" >/dev/null 2>&1 || { echo "FAIL: the example's repository drops approve-to-merge" >&2; fail=1; }
  (cd ../.. && CHART_DEMO_FACTORY_CONFIG="$tmp/mine-config.yaml" go test -count=1 -run TestRenderedDemoChartConfigParses ./internal/factory/config >"$tmp/parse2.log" 2>&1) || { echo "FAIL: the example's config does not parse:" >&2; cat "$tmp/parse2.log" >&2; fail=1; }
fi

[[ $fail == 0 ]] || exit 1
echo "agent-factory chart render checks passed"
