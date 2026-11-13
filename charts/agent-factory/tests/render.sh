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
# its binding, the runs namespace's Pod Security restricted label, and its two unsafe settings on the
# install banner; no RuntimeClass ClusterRole (no class).
ykind Namespace 'select(.metadata.name == "agents") | .metadata.labels."pod-security.kubernetes.io/enforce" == "restricted"' 'the demo runs namespace enforces Pod Security restricted'
got=$(yq ea '[select(.kind == "ResourceQuota" or .kind == "ValidatingAdmissionPolicy" or .kind == "ValidatingAdmissionPolicyBinding" or (.kind == "ClusterRole" and (.metadata.name | test("runtimeclass$")))) | .kind + "/" + .metadata.name] | sort | join(" ")' - <<<"$out")
# Step 3 PR B (D10): the demo runs on per-run tokens. The factory's config says perRun, its runs
# Role writes Secrets without reading them, the run-controller mounts each run's own Secret
ykind ConfigMap 'select(.metadata.name == "agent-factory-config") | (.data."config.yaml" | from_yaml | .runTokens | ((.perRun == true) and (has("enabled") | not)))' 'demo: the factory mints per run, from its own App'
ykind ConfigMap 'select(.metadata.name == "run-controller-config") | (.data."config.yaml" | from_yaml | (.gitTokens == "perRun" and (has("gitSecret") | not)))' 'demo: the run-controller mounts each run its own Secret'
ykind Role 'select(.metadata.name == "agent-factory" and .metadata.namespace == "agents") | ((.rules | map(select(.resources | contains(["secrets"])))) | ((length == 1) and ((.[0].verbs | join(",")) == "create,patch,delete")))' 'demo: the factory writes run token Secrets and reads none'
# Step 3 PR B (D10) adds the AgentRun creator and patch-limit policies and the run token policy.
want="ResourceQuota/runs ValidatingAdmissionPolicy/maku-maku-agentrun-creators ValidatingAdmissionPolicy/maku-maku-agentrun-factory-patch-limit ValidatingAdmissionPolicy/maku-maku-run-pods ValidatingAdmissionPolicy/maku-maku-run-token-secrets ValidatingAdmissionPolicy/room-broker-maku-agentruns ValidatingAdmissionPolicyBinding/maku-maku-agentrun-creators ValidatingAdmissionPolicyBinding/maku-maku-agentrun-factory-patch-limit ValidatingAdmissionPolicyBinding/maku-maku-run-pods ValidatingAdmissionPolicyBinding/maku-maku-run-token-secrets ValidatingAdmissionPolicyBinding/room-broker-maku-agentruns"
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
dom="$(helm template af . -n demo "${args[@]}" --set 'maku.agentRunPolicies.creators={system:serviceaccount:demo:agent-factory}' --set clusterDomain=example.test --set maku.clusterDomain=example.test --set maku-factory.clusterDomain=example.test | yq ea 'select(.kind == "Secret" and .metadata.name == "room-broker-tls") | .data."tls.crt"' - | base64 -d | openssl x509 -noout -ext subjectAltName)"
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
fq '.github.mergerAppIDFile == "/etc/agent-factory-github/app_id" and .github.mergerKeyFile == "/etc/agent-factory-github/private_key"' 'apps.merger.mode factory: the factory App files, as before (D12)'
fq '.repositories[0].merge.mergerLogin == .factoryLogin' 'folded, the merger login is the factory login (D12)'
ynone 'select(.kind == "Deployment" and .metadata.name == "agent-factory") | .spec.template.spec.volumes[] | select(.name == "merger")' 'apps.merger.mode factory: no merger volume (D12)'
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
# v4 D10: per-run tokens are two halves, and only the factory may create the runs it mints for
for half in "--set maku-factory.runTokens.perRun=true --set maku.runController.gitTokens=shared --set maku.runController.gitReadSecret=agent-git-read" \
            "--set maku-factory.runTokens.perRun=false --set maku.runController.gitTokens=perRun --set maku.runController.gitReadSecret="; do
  # shellcheck disable=SC2086 # two --set pairs, split on purpose
  msg="$(helm template af . -n maku "${args[@]}" $half 2>&1 >/dev/null || true)"
  grep -qF 'go together' <<<"$msg" || { echo "FAIL: $half renders: perRun and gitTokens perRun go together" >&2; fail=1; }
done
msg="$(helm template af . -n maku "${args[@]}" --set maku-factory.runTokens.perRun=true --set maku.runController.gitTokens=perRun \
  --set maku.agentRunPolicies.creators=null 2>&1 >/dev/null || true)"
grep -qF 'maku-factory.runTokens.perRun needs maku.agentRunPolicies.creators' <<<"$msg" || { echo "FAIL: perRun without agentRunPolicies.creators renders" >&2; fail=1; }
msg="$(helm template af . -n other "${args[@]}" 2>&1 >/dev/null || true)"
grep -qF 'does not name this release' <<<"$msg" || { echo "FAIL: the demo in another namespace renders with creators naming maku's factory" >&2; fail=1; }
# secrets: rendered from values, or not at all when an existing Secret is named
ykind Secret 'select(.metadata.name == "agent-model") | (.metadata.namespace == "agents" and .stringData.base_url == "http://model.example.test/v1" and .stringData.model == "m1" and .stringData.api_key == "k1")' 'agent-model in the runs namespace from demo.model'
ykind Secret 'select(.metadata.name == "agent-factory-github") | (.metadata.namespace == "maku" and .stringData.app_id == "123" and .stringData.private_key == "pem1")' 'agent-factory-github from demo.githubApp'
ynone 'select(.kind == "Secret" and .metadata.name == "agent-git") | .metadata.name' 'agent-git is rendered: the factory writes it'
ynone 'select(.kind == "Secret" and .metadata.name == "agent-git-read") | .metadata.name' 'agent-git-read is rendered: the factory writes it'
ynone 'select(.kind == "Secret" and .metadata.name == "agent-model") | select(.stringData | has("judge_model")) | .metadata.name' 'no judge_model without demo.model.judgeName: the judge runs on the run model'
judge="$(helm template af . -n maku "${args[@]}" --set demo.model.judgeName=small)"
[[ "$(yq ea 'select(.kind == "Secret" and .metadata.name == "agent-model") | .stringData.judge_model' - <<<"$judge")" == small ]] || { echo "FAIL: demo.model.judgeName does not write judge_model" >&2; fail=1; }
exist="$(helm template af . -n maku "${img[@]}" -f values-demo.yaml --set demo.model.existingSecret=agent-model --set demo.githubApp.existingSecret=agent-factory-github)"
for n in agent-model agent-factory-github; do
  [[ -z "$(yq ea "select(.kind == \"Secret\" and .metadata.name == \"$n\") | .metadata.name" - <<<"$exist")" ]] || { echo "FAIL: $n is rendered although its existingSecret is set" >&2; fail=1; }
done
msg="$(helm template af . -n maku "${img[@]}" -f values-demo.yaml 2>&1 >/dev/null || true)"
grep -qF 'demo.model.baseURL' <<<"$msg" || { echo "FAIL: no model values and no existingSecret does not fail" >&2; fail=1; }
msg="$(helm template af . -n maku "${img[@]}" -f values-demo.yaml --set demo.model.existingSecret=other --set demo.githubApp.existingSecret=agent-factory-github 2>&1 >/dev/null || true)"
grep -qF 'must have that name' <<<"$msg" || { echo "FAIL: an existingSecret the controller does not read does not fail" >&2; fail=1; }

# The model gateway (plan v4 step 4, Task 5; D25): off by default, so the demo golden above is the
# byte-identical proof. On, with cnref-like values, its objects render in the release namespace.
yq -e '.maku-gateway.enabled == false and .agentgateway.enabled == false' values.yaml >/dev/null 2>&1 || { echo "FAIL: values.yaml does not disable maku-gateway and agentgateway" >&2; fail=1; }
# cnref-like values serve the planner, so the broker is pointed at the gateway (Task 12, GW8)
yq '{"maku-gateway": ., "maku": {"broker": {"modelGateway": {"service": "model-gateway"}}}}' ../maku-gateway/tests/values-cnref-like.yaml >"$tmp/gateway.yaml"
gw="$(helm template af . -n maku "${args[@]}" -f "$tmp/gateway.yaml")" || { echo "FAIL: the demo with the gateway does not render" >&2; fail=1; gw=''; }
# objects < RENDER: every object of a render as kind/namespace/name, the release namespace when unset.
objects() { yq e 'select(.kind != null) | .kind + "/" + (.metadata.namespace // "maku") + "/" + .metadata.name' - | grep -vx -- '---' | sort; }
gwobjs='^(Gateway|HTTPRoute|AgentgatewayParameters|AgentgatewayBackend|AgentgatewayPolicy)/|^(NetworkPolicy|ConfigMap)/maku/model-gateway'
got="$(objects <<<"$gw" | grep -E "$gwobjs" | cut -d/ -f1,2 | sort -u | paste -sd, -)"
[[ $got == "AgentgatewayBackend/maku,AgentgatewayParameters/maku,AgentgatewayPolicy/maku,ConfigMap/maku,Gateway/maku,HTTPRoute/maku,NetworkPolicy/maku" ]] ||
  { echo "FAIL: the gateway's objects are not all in the release namespace (got '$got')" >&2; fail=1; }
demo="$(helm template af . -n maku "${args[@]}")"
diff <(objects <<<"$demo") <(objects <<<"$gw" | grep -vE "$gwobjs") >&2 || { echo "FAIL: turning the gateway on adds or drops another object" >&2; fail=1; }
ctl="$(helm template af . -n maku "${args[@]}" --set agentgateway.enabled=true | yq e 'select(.kind == "Deployment" and .spec.template.metadata.labels."app.kubernetes.io/name" == "agentgateway") | .spec.template.spec.containers[0].image' -)"
[[ $ctl == *"controller:v1.6.0@sha256:d84259a7e2d87f8f39bae385d4b032d823c0a4450aecb32e0e31e2258449645b" ]] || { echo "FAIL: agentgateway.enabled does not render the pinned controller (got '$ctl')" >&2; fail=1; }
msg="$(helm template af . -n maku "${args[@]}" -f "$tmp/gateway.yaml" --set maku-gateway.runs.namespace=other 2>&1 >/dev/null || true)"
grep -qF 'maku-gateway.runs (namespace "other", namePrefix "run-") must equal maku.runs (namespace "agents", namePrefix "run-")' <<<"$msg" || { echo "FAIL: a runs mismatch does not fail naming both keys (got: $msg)" >&2; fail=1; }
msg="$(helm template af . -n maku "${args[@]}" -f "$tmp/gateway.yaml" --set maku-gateway.broker.serviceAccount=other 2>&1 >/dev/null || true)"
grep -qF 'maku-gateway.broker.serviceAccount "other" must be the broker' <<<"$msg" || { echo "FAIL: a broker ServiceAccount mismatch does not fail (got: $msg)" >&2; fail=1; }
# The broker's clients (Task 12, GW8): the gateway serving the planner and the broker pointed at it agree.
[[ "$(yq ea 'select(.kind == "Deployment" and .metadata.name == "room-broker") | .spec.template.spec.volumes[] | select(.name == "model-gateway") | .projected.sources | map(.serviceAccountToken.audience) | join(",")' - <<<"$gw")" == "maku-gateway.client.planner,maku-gateway.client.classifier" ]] ||
  { echo "FAIL: the umbrella with the gateway does not mount the broker's client tokens" >&2; fail=1; }
gwbad() { # gwbad 'expected message' --set ...: the gateway render must fail with it
  local want="$1" msg; shift
  msg="$(helm template af . -n maku "${args[@]}" -f "$tmp/gateway.yaml" "$@" 2>&1 >/dev/null || true)"
  grep -qF -- "$want" <<<"$msg" || { echo "FAIL: $* does not fail with \"$want\" (got: $msg)" >&2; fail=1; }
}
gwbad 'maku-gateway.clients.planner.enabled needs maku.broker.modelGateway.service "model-gateway" (got "")' --set maku.broker.modelGateway.service=
gwbad 'maku.broker.modelGateway.port 9000 must equal maku-gateway.listeners.clients 8082' --set maku.broker.modelGateway.port=9000
gwbad 'maku.broker.modelGateway.audiencePrefix "other" must equal maku-gateway.audiencePrefix "maku-gateway"' --set maku.broker.modelGateway.audiencePrefix=other
gwbad 'maku.broker.modelGateway.namespace "elsewhere" must be the release namespace "maku"' --set maku.broker.modelGateway.namespace=elsewhere
gwbad 'maku.broker.human.intake.plan routes through maku-gateway (no url set), but maku-gateway.clients.planner.enabled is false' \
  --set maku-gateway.clients.planner.enabled=false --set maku.broker.human.intake.plan.enabled=true
gwbad 'maku.broker.human.slackIntents routes through maku-gateway (no url set), but maku-gateway.clients.classifier.enabled is false' \
  --set maku.broker.human.slackIntents.principal=slack-app
# a block with its own url and apiKeyFile keeps working beside the gateway (D25)
helm template af . -n maku "${args[@]}" -f "$tmp/gateway.yaml" --set maku-gateway.clients.planner.enabled=false \
  --set maku.broker.human.intake.plan.enabled=true --set maku.broker.human.intake.plan.url=http://gw.ai.svc:8080/v1 \
  --set maku.broker.human.intake.plan.apiKeyFile=/etc/planner/key >/dev/null 2>&1 ||
  { echo "FAIL: a planner with its own url and apiKeyFile does not render beside the gateway" >&2; fail=1; }

# The gateway demo (plan v4 step 4, Task 10): runs call the gateway with their own token, and no provider
# key reaches the runs namespace. values-demo.yaml alone stays the golden above, byte for byte.
gwd=("${img[@]}" -f values-demo.yaml -f values-demo-gateway.yaml --set demo.model.apiKey=k1
     --set demo.githubApp.id=123 --set demo.githubApp.privateKey=pem1
     --set maku-gateway.identity.issuer=https://kubernetes.default.svc.cluster.local
     --set maku-gateway.providers.demo.host=api.example.test
     --set maku-gateway.aliases.public.agent-default.model=m1 --set maku-gateway.aliases.public.tier-light.model=m2)
out="$(helm template af . -n maku "${gwd[@]}")" || { echo "FAIL: the gateway demo does not render" >&2; fail=1; out=''; }
ynone 'select(.kind == "Secret" and .metadata.name == "agent-model")' 'the gateway demo renders no agent-model'
ynone 'select(.kind == "Secret" and .metadata.namespace == "agents")' 'the gateway demo renders no Secret in the runs namespace'
ykind Secret 'select(.metadata.name == "demo-model") | (.metadata.namespace == "maku" and .stringData.apiKey == "k1")' 'the demo key is its provider'"'"'s Secret, in the release namespace'
ykind AgentgatewayBackend '(.metadata.name == "demo-agent-default" or .metadata.name == "demo-tier-light" or .metadata.name == "model-gateway-jwks")' 'the demo provider serves agent-default'
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (.model.mode == "gateway" and .model.gateway.url == "http://model-gateway.maku.svc.cluster.local" and (.model.gateway.aliases.public | join(",")) == "agent-default,tier-light" and (has("modelSecret") | not))' 'the gateway demo: the run-controller calls this release'"'"'s model-gateway'
ykind ConfigMap 'select(.metadata.name=="agent-factory-config") | .data."config.yaml" | from_yaml | .runs.gatewayIdentity == true' 'the gateway demo: the factory states its runs carry a gateway identity'
yq ea 'select(.kind == "ConfigMap" and .metadata.name == "agent-factory-config") | .data."config.yaml"' - <<<"$out" >"$tmp/gw-factory-config.yaml"
(cd ../.. && CHART_DEMO_FACTORY_CONFIG="$tmp/gw-factory-config.yaml" go test -count=1 -run TestRenderedDemoChartConfigParses ./internal/factory/config >"$tmp/parse-gw.log" 2>&1) ||
  { echo "FAIL: the gateway demo's factory config does not parse:" >&2; cat "$tmp/parse-gw.log" >&2; fail=1; }
# Task 17: the metering leg's values (install.sh METERING=1): a meter with a PromQL throttle and a split
# on the portable kind, which the factory accepts with runs.gatewayIdentity.
mout="$(helm template af . -n maku "${gwd[@]}" -f tests/metering-values.yaml)" || { echo "FAIL: the metering values do not render" >&2; fail=1; mout=''; }
yq ea 'select(.kind == "ConfigMap" and .metadata.name == "agent-factory-config") | .data."config.yaml"' - <<<"$mout" >"$tmp/metering-config.yaml"
yq -e '.loops.review.split.maxChildren == 3 and .meter.throttle.promQuery != null and .runs.gatewayIdentity' "$tmp/metering-config.yaml" >/dev/null 2>&1 ||
  { echo "FAIL: the metering config lacks its split, throttle or identity" >&2; fail=1; }
(cd ../.. && CHART_DEMO_FACTORY_CONFIG="$tmp/metering-config.yaml" go test -count=1 -run TestRenderedDemoChartConfigParses ./internal/factory/config >"$tmp/parse-m.log" 2>&1) ||
  { echo "FAIL: the metering leg's factory config does not parse:" >&2; cat "$tmp/parse-m.log" >&2; fail=1; }
grep -qE 'name: metrics' <<<"$(yq 'select(.kind == "NetworkPolicy" and .metadata.name == "model-gateway") | .spec.ingress[] | select(.ports[0].port == 15020)' - <<<"$mout")" ||
  { echo "FAIL: the metering values do not admit the metrics namespace to the gateway's 15020" >&2; fail=1; }
# GW12: the gateway's prices default to the factory's meter.prices (one source); the gateway's own win.
catalogs() { yq ea '[select(.kind == "ConfigMap" and .metadata.name == "model-gateway-model-catalog")] | length' - <<<"$out"; }
[[ "$(catalogs)" == 1 ]] || { echo "FAIL: the gateway demo renders $(catalogs) model catalogs, want 1" >&2; fail=1; }
ykind ConfigMap 'select(.metadata.name=="model-gateway-model-catalog") | .data."catalog.json" | from_yaml | (.providers | length) == 0' 'the gateway demo without prices: an empty catalog'
out="$(helm template af . -n maku "${gwd[@]}" --set-json 'maku-factory.config.meter.prices={"m1":{"input":1.4,"cacheRead":0.26,"output":4.4},"agent-default":{"input":1,"cacheRead":0.1,"output":2}}')" ||
  { echo "FAIL: the gateway demo with factory prices does not render" >&2; fail=1; out=''; }
[[ "$(catalogs)" == 1 ]] || { echo "FAIL: factory prices render $(catalogs) model catalogs, want 1" >&2; fail=1; }
ykind ConfigMap 'select(.metadata.name=="model-gateway-model-catalog") | .data."catalog.json" | from_yaml | .providers.demo.models | ((keys | join(",")) == "m1" and .m1.rates.input == "1.40" and .m1.rates.cacheRead == "0.26" and .m1.rates.output == "4.40" and (.m1.rates | length) == 3)' \
  'the catalog defaults to maku-factory.config.meter.prices, the served models only'
out="$(helm template af . -n maku "${gwd[@]}" --set-json 'maku-factory.config.meter.prices={"m1":{"input":1.4,"cacheRead":0.26,"output":4.4}}' \
  --set-json 'maku-gateway.modelCatalog.prices={"m2":{"input":3,"cacheRead":0.3,"output":9}}')" ||
  { echo "FAIL: the gateway demo with its own prices does not render" >&2; fail=1; out=''; }
[[ "$(catalogs)" == 1 ]] || { echo "FAIL: the gateway's own prices render $(catalogs) model catalogs, want 1" >&2; fail=1; }
ykind ConfigMap 'select(.metadata.name=="model-gateway-model-catalog") | .data."catalog.json" | from_yaml | (.providers.demo.models | keys | join(",")) == "m2"' \
  'maku-gateway.modelCatalog.prices wins over the factory'"'"'s'
out="$(helm template af . -n maku "${gwd[@]}")"
yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml"' - <<<"$out" >"$tmp/rc-gateway.yaml"
(cd ../.. && go run ./charts/maku/tests/loadconfig run-controller-gateway "$tmp/rc-gateway.yaml") || fail=1
# bad WANT ARGS...: the render fails naming WANT.
bad() {
  local want="$1" msg; shift
  msg="$(helm template af . -n maku "$@" 2>&1 >/dev/null || true)"
  grep -qF -- "$want" <<<"$msg" || { echo "FAIL: $* does not fail with \"$want\" (got: $msg)" >&2; fail=1; }
}
bad 'maku.runController.model.mode gateway needs maku-gateway.enabled, or maku.runController.model.gateway.service' "${args[@]}" \
  --set maku.runController.model.mode=gateway --set-json 'maku.runController.model.gateway.aliases={"public":["agent-default"]}'
bad "maku.runController.model.gateway.aliases.public [agent-default] must be maku-gateway.aliases.public's keys [agent-default, tier-light]" "${gwd[@]}" \
  --set-json 'maku.runController.model.gateway.aliases={"public":["agent-default"]}'
bad 'maku.runController.model.gateway.audiencePrefix "other" must equal maku-gateway.audiencePrefix "maku-gateway"' "${gwd[@]}" --set maku.runController.model.gateway.audiencePrefix=other
bad 'maku.runController.model.gateway.ports (8090, 8081) must equal maku-gateway.listeners' "${gwd[@]}" --set maku.runController.model.gateway.ports.public=8090
bad 'maku.runController.model.gateway.service "elsewhere" must be empty or maku-gateway.name "model-gateway"' "${gwd[@]}" --set maku.runController.model.gateway.service=elsewhere
bad 'demo.model.baseURL: with maku.runController.model.mode gateway' "${gwd[@]}" --set demo.model.baseURL=http://model.example.test/v1
bad 'maku-factory.config.runs.gatewayIdentity is true but maku.runController.model.mode is "secret"' "${args[@]}" --set maku-factory.config.runs.gatewayIdentity=true
bad 'maku.runController.model.mode gateway needs maku-factory.config.runs.gatewayIdentity: true' "${gwd[@]}" --set maku-factory.config.runs.gatewayIdentity=false
# ANR's form: an external gateway, maku-gateway off.
out="$(helm template af . -n maku "${img[@]}" -f values-demo.yaml --set demo.githubApp.id=123 --set demo.githubApp.privateKey=pem1 --set maku.runController.model.mode=gateway \
  --set maku-factory.config.runs.gatewayIdentity=true \
  --set maku.runController.model.gateway.service=agentgateway --set maku.runController.model.gateway.namespace=ai \
  --set-json 'maku.runController.model.gateway.aliases={"public":["agent-default"]}')" || { echo "FAIL: an external gateway does not render" >&2; fail=1; out=''; }
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | .model.gateway.url == "http://agentgateway.ai.svc.cluster.local"' 'an external gateway by service and namespace'
ynone 'select(.kind == "Gateway")' 'an external gateway renders no Gateway'

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
