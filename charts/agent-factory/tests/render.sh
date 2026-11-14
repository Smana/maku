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
# The demo in model.mode secret (tests/values-demo-secret.yaml): the assertions below that predate step 7's
# R4 hold for it. The shipped demo, in model.mode gateway, is gwd (the golden).
args=("${img[@]}" -f values-demo.yaml -f tests/values-demo-secret.yaml "${secrets[@]}")
# The shipped demo (step 7 R4): runs call the model gateway with their own token, and no provider key
# reaches the runs namespace.
gwd=("${img[@]}" -f values-demo.yaml --set demo.model.apiKey=k1
     --set demo.githubApp.id=123 --set demo.githubApp.privateKey=pem1
     --set maku-gateway.identity.issuer=https://kubernetes.default.svc.cluster.local
     --set maku-gateway.providers.demo.host=api.example.test
     --set maku-gateway.aliases.public.agent-default.model=m1 --set maku-gateway.aliases.public.tier-light.model=m2)
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
# jkind KIND 'jq expr' 'message': as ykind, in jq (yq compares only scalars, never maps or lists).
jkind() {
  yq ea -o=json -I0 "select(.kind == \"$1\")" - <<<"$out" | jq -s -e "map($2) | (length > 0 and all)" >/dev/null 2>&1 || { echo "FAIL: $3" >&2; fail=1; }
}
# secret_field NAME KEY: the decoded value of one Secret data key.
secret_field() { yq ea "select(.kind == \"Secret\" and .metadata.name == \"$1\") | .data.\"$2\"" - <<<"$out" | base64 -d; }

out="$(helm template af . -n maku "${args[@]}")"

# The golden: the shipped demo (-f values-demo.yaml, model.mode gateway since step 7's R4), generated
# secrets masked. A deliberate change to the demo's render updates it: UPDATE_GOLDEN=1 bash
# charts/agent-factory/tests/render.sh, then review the diff.
demo_shipped="$(helm template af . -n maku "${gwd[@]}")"
if [[ ${UPDATE_GOLDEN:-} == 1 ]]; then
  bash tests/normalize.sh <<<"$demo_shipped" >tests/golden/demo.yaml
fi
bash tests/normalize.sh <<<"$demo_shipped" | diff -u tests/golden/demo.yaml - >&2 || { echo "FAIL: the demo render differs from tests/golden/demo.yaml" >&2; fail=1; }
yq -e 'select(.metadata.name == "run-controller-config") | .data."config.yaml" | from_yaml | .model.gateway.url' - <<<"$demo_shipped" >/dev/null 2>&1 ||
  { echo "FAIL: the shipped demo's runs do not call the model gateway (step 7 R4)" >&2; fail=1; }
! yq -e 'select(.kind == "Secret" and .metadata.name == "agent-model")' - <<<"$demo_shipped" >/dev/null 2>&1 ||
  { echo "FAIL: the shipped demo writes agent-model, a provider key in the runs namespace" >&2; fail=1; }
# Rollout step 2, PR A (D16, D17, D18): the demo gains exactly the runs quota, the run-pod policy and
# its binding, the runs namespace's Pod Security restricted label, and its two unsafe settings on the
# install banner; no RuntimeClass ClusterRole (no class).
ykind Namespace 'select(.metadata.name == "agents") | .metadata.labels."pod-security.kubernetes.io/enforce" == "restricted"' 'the demo runs namespace enforces Pod Security restricted'
got=$(yq ea '[select(.kind == "ResourceQuota" or .kind == "ValidatingAdmissionPolicy" or .kind == "ValidatingAdmissionPolicyBinding" or (.kind == "ClusterRole" and (.metadata.name | test("runtimeclass$")))) | .kind + "/" + .metadata.name] | sort | join(" ")' - <<<"$out")
# Step 3 PR B (D10): the demo runs on per-run tokens. The factory's config says perRun, its runs
# Role writes Secrets without reading them, the run-controller mounts each run's own Secret
ykind ConfigMap 'select(.metadata.name == "agent-factory-config") | (.data."config.yaml" | from_yaml | .runTokens | ((.perRun == true) and (has("enabled") | not)))' 'demo: the factory mints per run, from its own App'
ykind ConfigMap 'select(.metadata.name == "run-controller-config") | (.data."config.yaml" | from_yaml | ((has("gitTokens") | not) and (has("gitSecret") | not)))' 'demo: the run-controller mounts each run its own Secret'
ykind Role 'select(.metadata.name == "agent-factory" and .metadata.namespace == "agents") | ((.rules | map(select(.resources | contains(["secrets"])))) | ((length == 1) and ((.[0].verbs | join(",")) == "create,patch,delete")))' 'demo: the factory writes run token Secrets and reads none'
# Step 3 PR B (D10) adds the AgentRun creator and patch-limit policies and the run token policy.
want="ResourceQuota/runs ValidatingAdmissionPolicy/maku-maku-agentrun-creators ValidatingAdmissionPolicy/maku-maku-agentrun-factory-patch-limit ValidatingAdmissionPolicy/maku-maku-run-pods ValidatingAdmissionPolicy/maku-maku-run-token-secrets ValidatingAdmissionPolicy/room-broker-maku-agentruns ValidatingAdmissionPolicyBinding/maku-maku-agentrun-creators ValidatingAdmissionPolicyBinding/maku-maku-agentrun-factory-patch-limit ValidatingAdmissionPolicyBinding/maku-maku-run-pods ValidatingAdmissionPolicyBinding/maku-maku-run-token-secrets ValidatingAdmissionPolicyBinding/room-broker-maku-agentruns"
[[ $got == "$want" ]] || { echo "FAIL: the demo's step-2 objects are '$got', want '$want'" >&2; fail=1; }
ykind ConfigMap 'select(.metadata.name == "room-broker-config") | (.data."config.yaml" | from_yaml | .install.unsafe | join(",")) == "unsafe_allow_runc,network_policy_none,model_key_in_sandbox"' 'the demo shows its three unsafe settings: runc, no network policy and the model key in the sandbox (v4 step 8, R4)'
ykind ConfigMap 'select(.metadata.name == "run-controller-config") | (.data."config.yaml" | from_yaml | .modelSecret == "agent-model")' 'the demo keeps model.mode secret, which maku.demoValues admits (v4 step 8, R4)'
# PR6: the skeleton disables both subcharts, so nothing but the umbrella's own templates renders
# and the gate's message is the error whatever order Helm renders in; values-demo.yaml enables them.
yq -e '.maku.enabled == false and .maku-factory.enabled == false' values.yaml >/dev/null 2>&1 || { echo "FAIL: values.yaml does not disable maku and maku-factory" >&2; fail=1; }
yq -e '.maku.enabled == true and .maku-factory.enabled == true' values-demo.yaml >/dev/null 2>&1 || { echo "FAIL: values-demo.yaml does not enable maku and maku-factory" >&2; fail=1; }
# One template relies on Helm's render order, deepest first: the gate's own, deeper than any subchart's
# template, so profile production's one message comes first (proved below, Task 19). No other may.
deep="$(find templates -mindepth 2 -type f)"
[[ $deep == templates/gate/renders/before/subcharts.yaml ]] || { echo "FAIL: templates below templates/ ($deep): only the gate's may rely on Helm's render order" >&2; fail=1; }
# the packaged chart ships values-demo.yaml and the example, never tests/ (.helmignore)
pkg="$(mktemp -d)"; helm package . -d "$pkg" >/dev/null
shipped="$(tar -tzf "$pkg"/agent-factory-*.tgz)"; rm -rf "$pkg"
grep -qx 'agent-factory/values-demo.yaml' <<<"$shipped" || { echo "FAIL: the package lacks values-demo.yaml" >&2; fail=1; }
grep -qx 'agent-factory/values-mine.example.yaml' <<<"$shipped" || { echo "FAIL: the package lacks values-mine.example.yaml" >&2; fail=1; }
grep -qx 'agent-factory/values-production.example.yaml' <<<"$shipped" || { echo "FAIL: the package lacks values-production.example.yaml" >&2; fail=1; }
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
grep -qF 'profile is required: demo (-f values-demo.yaml), or production (docs/upgrading.md#from-the-demo-to-production)' <<<"$msg" || { echo "FAIL: an unset profile does not fail with the profile message (got: $msg)" >&2; fail=1; }

# v4 step 6, Task 19 (EX16, R6): profile production renders, a gate per enabled component. It refuses
# the demo's values, and names every missing seam of every enabled component in one message, before
# any subchart refuses the first one it meets.
demo_out="$out"
prod=("${img[@]}" -f tests/production-values.yaml)
# pbad WANT ARGS...: the production render fails, its message holding WANT.
pbad() {
  local want="$1" msg; shift
  msg="$(helm template af . -n maku "$@" 2>&1 >/dev/null || true)"
  grep -qF -- "$want" <<<"$msg" || { echo "FAIL: production: $* does not fail with \"$want\" (got: $msg)" >&2; fail=1; }
}
pbad 'demoValues: values-demo.yaml is the demo'"'"'s' "${prod[@]}" --set demoValues=true
pbad 'demoValues: values-demo.yaml is the demo'"'"'s' "${img[@]}" -f values-demo.yaml -f tests/values-demo-secret.yaml "${secrets[@]}" --set profile=production
pbad 'postgres.enabled: the bundled Postgres is demo-only (D9)' "${prod[@]}" --set postgres.enabled=true
pbad 'networkPolicies.plain: the demo'"'"'s ingress-only policy' "${prod[@]}" --set networkPolicies.plain=true
pbad 'maku.demoValues: the maku chart'"'"'s demo marker' "${prod[@]}" --set maku.demoValues=true
pbad 'maku-factory.demo, maku-factory.config.demo' "${prod[@]}" --set maku-factory.demo=true
# model.mode secret puts the provider key in the sandbox (D7): demo-only, so production refuses it
pbad '  - maku.runController.model.mode: secret is demo-only (D7)' "${prod[@]}" --set maku.runController.model.mode=secret --set maku-factory.config.runs.gatewayIdentity=false
pbad 'maku.sandbox.unsafeAllowRunc: runs on the node'"'"'s own runtime are for the demo and kind only (D18)' "${prod[@]}" --set maku.sandbox.unsafeAllowRunc=true --set maku.sandbox.runtimeClassName=
pbad "removed in v4 step 8: set sandbox.unsafeAllowRunc" "${prod[@]}" --set maku.runController.allowUnsandboxed=true
# an empty production file with maku on: one message, every maku seam on its own line, the gate first
msg="$(helm template af . -n maku --set profile=production --set maku.enabled=true 2>&1 >/dev/null || true)"
for line in 'maku.broker.image.tag: <version>@sha256:<digest>' 'maku.runController.image.tag: <version>@sha256:<digest>' \
  'maku.runController.images.harness.tag:' 'maku.runController.images.bridge.tag:' 'maku.broker.human.issuer: the OIDC issuer' \
  'maku.broker.runIssuer.issuer:' 'maku.broker.database.brokerSecret: the broker'"'"'s DSN Secret' 'maku.broker.publicURL: the https URL'; do
  grep -qF -- "  - $line" <<<"$msg" || { echo "FAIL: an empty production file does not list '$line' (got: $msg)" >&2; fail=1; }
done
# maku.sandbox.runtimeClassName defaults to gvisor (v4 step 8): no seam unless a values file empties it
grep -qF 'profile production: 8 missing or refused seams (docs/upgrading.md#from-the-demo-to-production):' <<<"$msg" ||
  { echo "FAIL: an empty production file's one message does not count its 8 seams (got: $msg)" >&2; fail=1; }
pbad 'maku.sandbox.runtimeClassName: the RuntimeClass' "${prod[@]}" --set maku.sandbox.runtimeClassName=
grep -qF 'templates/gate/renders/before/subcharts.yaml' <<<"$msg" || { echo "FAIL: a subchart refused before the gate (got: $msg)" >&2; fail=1; }
pbad 'maku.broker.human.mode: oidc (got local' --set profile=production --set maku.enabled=true --set maku.broker.human.mode=local
# every enabled component's seams in the one message: the factory's beside maku's
msg="$(helm template af . -n maku --set profile=production --set maku.enabled=true --set maku-factory.enabled=true 2>&1 >/dev/null || true)"
for line in 'maku-factory.image.tag: <version>@sha256:<digest>' 'maku-factory.config.api.humanIssuer:' 'maku.broker.publicURL:'; do
  grep -qF -- "  - $line" <<<"$msg" || { echo "FAIL: maku and the factory: the message does not list '$line' (got: $msg)" >&2; fail=1; }
done
pbad 'maku-factory.apps.factory.existingSecret: the factory App'"'"'s Secret' "${prod[@]}" --set maku-factory.apps.factory.existingSecret=
# the agreements are listed too: a disagreeing identity, a web front door without its Secret
pbad '  - maku.broker.human.issuer "https://idp.example.test" and maku-factory.config.api.humanIssuer "https://other.example.test" must be equal' "${prod[@]}" --set maku-factory.config.api.humanIssuer=https://other.example.test
pbad '  - webProxy.existingSecret is empty' "${prod[@]}" --set webProxy.enabled=true --set maku.broker.webProxy=true
pbad '  - expose.web.hostnames is empty' "${prod[@]}" --set expose.mode=httproute --set webProxy.enabled=true --set maku.broker.webProxy=true --set webProxy.existingSecret=rooms-proxy
# the Slack App's three Secrets by name: its own schema refuses an empty one
pbad "at '/slackSecret': minLength: got 0, want 1" "${prod[@]}" --set slack.enabled=true --set slack.slackSecret=
# with every seam named it renders: the broker, the run-controller and the factory, none of the demo's own
out="$(helm template af . -n maku "${prod[@]}")" || { echo "FAIL: tests/production-values.yaml does not render" >&2; fail=1; out=''; }
for d in room-broker run-controller agent-factory; do
  ykind Deployment "select(.metadata.name == \"$d\") | true" "production renders the Deployment $d"
done
ynone 'select(.kind == "StatefulSet" or (.kind == "Secret" and (.metadata.name | test("^(room-broker-tls|room-broker-ca|openbao-ca|agent-model|agent-factory-github|rooms-.*-dsn)$"))) or .kind == "NetworkPolicy" and (.metadata.name == "rooms-db" or .metadata.name == "agent-factory-api"))' 'production renders a demo object: the bundled Postgres, a generated Secret or the plain policy'
ykind ConfigMap 'select(.metadata.name == "room-broker-config") | (.data."config.yaml" | from_yaml | has("install") | not)' 'production with every seam named shows no unsafe setting'
# Review of #565: production renders no broker TLS of its own (tls.yaml is the demo's), so ExternalSecrets
# may deliver room-broker-tls, room-broker-ca and openbao-ca there; the demo still refuses them.
eso_tls=(--set externalSecrets.enabled=true --set externalSecrets.storeRef.name=store
  --set-json 'externalSecrets.secrets={"brokerTLS":{"dataFrom":[{"extract":{"key":"maku/room-broker-tls"}}]},"brokerCA":{"dataFrom":[{"extract":{"key":"maku/ca"}}]},"factoryBrokerCA":{"dataFrom":[{"extract":{"key":"maku/ca"}}]}}')
out="$(helm template af . -n maku "${prod[@]}" "${eso_tls[@]}")" || { echo "FAIL: production refuses ExternalSecrets for the broker's TLS" >&2; fail=1; out=''; }
got="$(yq ea '[select(.kind == "ExternalSecret") | .spec.target.name] | sort | join(",")' - <<<"$out")"
[[ $got == "openbao-ca,room-broker-ca,room-broker-tls" ]] || { echo "FAIL: production's TLS ExternalSecrets are '$got'" >&2; fail=1; }
pbad 'the umbrella (its broker TLS)' "${args[@]}" "${eso_tls[@]}"
# staged adoption (D25): the factory alone, with its seams, renders the factory and nothing of maku's
out="$(helm template af . -n maku --set profile=production --set maku-factory.enabled=true --set maku-factory.image.tag=$digest)" ||
  { echo "FAIL: maku.enabled=false with maku-factory alone does not render" >&2; fail=1; out=''; }
ykind Deployment 'select(.metadata.name == "agent-factory") | true' 'the factory alone renders its Deployment'
ynone 'select(.metadata.name | test("^(room-broker|run-controller|egress-proxy)")) | .metadata.name' 'the factory alone renders a maku object'
# networkPolicies.mode none stays allowed, on the install banner (PR9)
out="$(helm template af . -n maku "${prod[@]}" --set maku.networkPolicies.mode=none)" || { echo "FAIL: production in networkPolicies mode none does not render" >&2; fail=1; out=''; }
ykind ConfigMap 'select(.metadata.name == "room-broker-config") | (.data."config.yaml" | from_yaml | .install.unsafe | join(",")) == "network_policy_none"' 'production in mode none lists network_policy_none on the banner (PR9)'
# v4 step 6, Task 20: values-production.example.yaml renders as shipped, and its configs load; the
# cloud-native-ref-shaped values (ANR at 1529b3b) render exactly tests/golden/production-cnref-like.yaml,
# in cnref's namespace, and the broker's, the run-controller's and the Slack App's configs load strictly.
# A deliberate change updates the golden: UPDATE_GOLDEN=1 bash charts/agent-factory/tests/render.sh.
out="$(helm template af . -n maku -f values-production.example.yaml)" || { echo "FAIL: values-production.example.yaml does not render" >&2; fail=1; out=''; }
for k in HTTPRoute PrometheusRule ServiceMonitor; do ykind "$k" 'true' "values-production.example.yaml renders a $k"; done
ykind ConfigMap 'select(.metadata.name == "room-broker-config") | (.data."config.yaml" | from_yaml | has("install") | not)' 'the production example shows no unsafe setting'
yq 'select(.metadata.name == "room-broker-config") | .data."config.yaml"' - <<<"$out" >"$tmp/ex-broker.yaml"
yq 'select(.metadata.name == "run-controller-config") | .data."config.yaml"' - <<<"$out" >"$tmp/ex-rc.yaml"
(cd ../.. && go run ./charts/maku/tests/loadconfig broker "$tmp/ex-broker.yaml" && go run ./charts/maku/tests/loadconfig run-controller-gateway "$tmp/ex-rc.yaml") ||
  { echo "FAIL: the production example's broker or run-controller config does not load" >&2; fail=1; }
cnref=("${img[@]}" --set slack.image.tag=v0.0.0@sha256:0000000000000000000000000000000000000000000000000000000000000000 -f tests/values-cnref-like.yaml)
out="$(helm template agent-factory . -n agent-system "${cnref[@]}")" || { echo "FAIL: tests/values-cnref-like.yaml does not render" >&2; fail=1; out=''; }
# The dashboards' JSON is masked (tests/monitoring.sh and Task 8's promtool checks hold it), so the
# golden is the install's shape.
cnref_golden() { bash tests/normalize.sh <<<"$out" | yq '(select(.kind == "ConfigMap" and (.metadata.name | test("^maku-dashboard-"))) | .data[]) |= "<dashboard JSON>"' -; }
if [[ ${UPDATE_GOLDEN:-} == 1 ]]; then cnref_golden >tests/golden/production-cnref-like.yaml; fi
cnref_golden | diff -u tests/golden/production-cnref-like.yaml - >&2 || { echo "FAIL: the cnref-like render differs from tests/golden/production-cnref-like.yaml" >&2; fail=1; }
for c in room-broker-config:broker run-controller-config:run-controller-gateway factory-slack-app-config:slack-app; do
  yq "select(.metadata.name == \"${c%%:*}\") | .data.\"config.yaml\"" - <<<"$out" >"$tmp/cnref-${c#*:}.yaml"
  (cd ../.. && go run ./charts/maku/tests/loadconfig "${c#*:}" "$tmp/cnref-${c#*:}.yaml") || { echo "FAIL: the cnref-like ${c%%:*} does not load" >&2; fail=1; }
done
# Task 21: the k3d production leg's values (identity.sh PROFILE=production) render offline, and the
# broker's and the factory's configs load, so a live failure is the cluster's, not the values'.
k3d=(-f tests/identity-values.yaml -f tests/identity-link-values.yaml -f tests/expose-values.yaml -f tests/production-k3d-values.yaml
  --set expose.mode=httproute --set-json 'expose.httproute.parentRefs=[{"name":"edge","namespace":"edge","sectionName":"http"}]'
  --set-json 'expose.networkPolicy.from=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"edge"}}}]'
  --set maku.e2e.unpinnedImages=true --set maku-factory.e2e.unpinnedImages=true --set maku.broker.image.tag=e2e --set maku.runController.image.tag=e2e
  --set maku.runController.images.harness.tag=e2e --set maku.runController.images.bridge.tag=e2e --set maku-factory.image.tag=e2e)
out="$(helm template identity . -n maku "${k3d[@]}")" || { echo "FAIL: the k3d production values do not render" >&2; fail=1; out=''; }
yq 'select(.metadata.name == "room-broker-config") | .data."config.yaml"' - <<<"$out" >"$tmp/k3d-broker.yaml"
yq 'select(.metadata.name == "agent-factory-config") | .data."config.yaml"' - <<<"$out" >"$tmp/k3d-factory.yaml"
(cd ../.. && go run ./charts/maku/tests/loadconfig broker "$tmp/k3d-broker.yaml" &&
  CHART_FACTORY_CONFIG="$tmp/k3d-factory.yaml" go test -count=1 -run TestRenderedChartConfigParses ./internal/factory/config >"$tmp/parse.log" 2>&1) ||
  { echo "FAIL: the k3d production leg's broker or factory config does not load" >&2; cat "$tmp/parse.log" >&2; fail=1; }
out="$demo_out"

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
[[ "$(yq '.runs | del(.gatewayIdentity)' - <<<"$fcfg" | yq -o=json -I0 'sort_keys(.)' -)" == "$(yq '.runs' - <<<"$bcfg" | yq -o=json -I0 'sort_keys(.)' -)" ]] || { echo "FAIL: the factory and the broker disagree on runs" >&2; fail=1; }
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
# v4 step 8 (Q14): the factory mints each run's token, or a demo names a laptop's PAT; never both, never neither.
msg="$(helm template af . -n maku "${args[@]}" --set maku.runController.gitSecret=agent-git 2>&1 >/dev/null || true)"
grep -qF 'maku-factory.runTokens.perRun and maku.runController.gitSecret ("agent-git"): choose one' <<<"$msg" || { echo "FAIL: perRun beside a laptop PAT renders" >&2; fail=1; }
msg="$(helm template af . -n maku "${args[@]}" --set maku-factory.runTokens.perRun=false 2>&1 >/dev/null || true)"
grep -qF 'maku-factory.runTokens.perRun: the factory mints each run' <<<"$msg" || { echo "FAIL: a factory without perRun and without a PAT renders" >&2; fail=1; }
msg="$(helm template af . -n maku "${args[@]}" --set maku-factory.runTokens.perRun=true \
  --set maku.agentRunPolicies.creators=null 2>&1 >/dev/null || true)"
grep -qF 'maku-factory.runTokens.perRun needs maku.agentRunPolicies.creators' <<<"$msg" || { echo "FAIL: perRun without agentRunPolicies.creators renders" >&2; fail=1; }
msg="$(helm template af . -n other "${args[@]}" 2>&1 >/dev/null || true)"
grep -qF 'does not name this release' <<<"$msg" || { echo "FAIL: the demo in another namespace renders with creators naming maku's factory" >&2; fail=1; }
# secrets: rendered from values, or not at all when an existing Secret is named
ykind Secret 'select(.metadata.name == "agent-model") | (.metadata.namespace == "agents" and .stringData.base_url == "http://model.example.test/v1" and .stringData.model == "m1" and .stringData.api_key == "k1")' 'agent-model in the runs namespace from demo.model'
ykind Secret 'select(.metadata.name == "agent-factory-github") | (.metadata.namespace == "maku" and .stringData.app_id == "123" and .stringData.private_key == "pem1")' 'agent-factory-github from demo.githubApp'
ynone 'select(.kind == "Secret" and (.metadata.name == "agent-git" or .metadata.name == "agent-git-read")) | .metadata.name' 'no shared git Secret is rendered: each run gets its own'
ynone 'select(.kind == "Secret" and .metadata.name == "agent-model") | select(.stringData | has("judge_model")) | .metadata.name' 'no judge_model without demo.model.judgeName: the judge runs on the run model'
judge="$(helm template af . -n maku "${args[@]}" --set demo.model.judgeName=small)"
[[ "$(yq ea 'select(.kind == "Secret" and .metadata.name == "agent-model") | .stringData.judge_model' - <<<"$judge")" == small ]] || { echo "FAIL: demo.model.judgeName does not write judge_model" >&2; fail=1; }
exist="$(helm template af . -n maku "${img[@]}" -f values-demo.yaml -f tests/values-demo-secret.yaml --set demo.model.existingSecret=agent-model --set demo.githubApp.existingSecret=agent-factory-github)"
for n in agent-model agent-factory-github; do
  [[ -z "$(yq ea "select(.kind == \"Secret\" and .metadata.name == \"$n\") | .metadata.name" - <<<"$exist")" ]] || { echo "FAIL: $n is rendered although its existingSecret is set" >&2; fail=1; }
done
msg="$(helm template af . -n maku "${img[@]}" -f values-demo.yaml -f tests/values-demo-secret.yaml 2>&1 >/dev/null || true)"
grep -qF 'demo.model.baseURL' <<<"$msg" || { echo "FAIL: no model values and no existingSecret does not fail" >&2; fail=1; }
msg="$(helm template af . -n maku "${img[@]}" -f values-demo.yaml -f tests/values-demo-secret.yaml --set demo.model.existingSecret=other --set demo.githubApp.existingSecret=agent-factory-github 2>&1 >/dev/null || true)"
grep -qF 'must have that name' <<<"$msg" || { echo "FAIL: an existingSecret the controller does not read does not fail" >&2; fail=1; }

# The model gateway (plan v4 step 4, Task 5; D25): off by default, so the demo golden above is the
# byte-identical proof. On, with cnref-like values, its objects render in the release namespace.
yq -e '.maku-gateway.enabled == false and .agentgateway.enabled == false' values.yaml >/dev/null 2>&1 || { echo "FAIL: values.yaml does not disable maku-gateway and agentgateway" >&2; fail=1; }
# cnref-like values serve the planner, so the broker is pointed at the gateway (Task 12, GW8)
# values-demo.yaml's own provider stays beside cnref's (Helm cannot drop a map key): it is given a host.
yq '{"maku-gateway": (. * {"providers": {"demo": {"host": "unused.example.test"}}}), "maku": {"broker": {"modelGateway": {"service": "model-gateway"}}}}' ../maku-gateway/tests/values-cnref-like.yaml >"$tmp/gateway.yaml"
gw="$(helm template af . -n maku "${args[@]}" -f "$tmp/gateway.yaml")" || { echo "FAIL: the demo with the gateway does not render" >&2; fail=1; gw=''; }
# objects < RENDER: every object of a render as kind/namespace/name, the release namespace when unset.
objects() { yq e 'select(.kind != null) | .kind + "/" + (.metadata.namespace // "maku") + "/" + .metadata.name' - | grep -vx -- '---' | sort; }
gwobjs='^(Gateway|HTTPRoute|AgentgatewayParameters|AgentgatewayBackend|AgentgatewayPolicy)/|^(NetworkPolicy|ConfigMap|ClusterRoleBinding)/maku/model-gateway'
got="$(objects <<<"$gw" | grep -E "$gwobjs" | cut -d/ -f1,2 | sort -u | paste -sd, -)"
[[ $got == "AgentgatewayBackend/maku,AgentgatewayParameters/maku,AgentgatewayPolicy/maku,ClusterRoleBinding/maku,ConfigMap/maku,Gateway/maku,HTTPRoute/maku,NetworkPolicy/maku" ]] ||
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

# The gateway demo (plan v4 step 4, Task 10; the shipped demo since step 7 R4): runs call the gateway with
# their own token, and no provider key reaches the runs namespace.
out="$(helm template af . -n maku "${gwd[@]}")" || { echo "FAIL: the gateway demo does not render" >&2; fail=1; out=''; }
ynone 'select(.kind == "Secret" and .metadata.name == "agent-model")' 'the gateway demo renders no agent-model'
ykind ConfigMap 'select(.metadata.name == "room-broker-config") | (.data."config.yaml" | from_yaml | .install.unsafe | join(",")) == "unsafe_allow_runc,network_policy_none"' 'the gateway demo: no model key in the sandbox on the banner'
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
mout="$(helm template af . -n maku "${gwd[@]}" -f tests/metering-values.yaml \
  --set maku-gateway.aliases.public.agent-default.model=echo-default --set maku-gateway.aliases.public.tier-light.model=echo-light)" || { echo "FAIL: the metering values do not render" >&2; fail=1; mout=''; }
yq ea 'select(.kind == "ConfigMap" and .metadata.name == "agent-factory-config") | .data."config.yaml"' - <<<"$mout" >"$tmp/metering-config.yaml"
yq -e '.loops.review.split.maxChildren == 3 and .meter.throttle.promQuery != null and .runs.gatewayIdentity' "$tmp/metering-config.yaml" >/dev/null 2>&1 ||
  { echo "FAIL: the metering config lacks its split, throttle or identity" >&2; fail=1; }
(cd ../.. && CHART_DEMO_FACTORY_CONFIG="$tmp/metering-config.yaml" go test -count=1 -run TestRenderedDemoChartConfigParses ./internal/factory/config >"$tmp/parse-m.log" 2>&1) ||
  { echo "FAIL: the metering leg's factory config does not parse:" >&2; cat "$tmp/parse-m.log" >&2; fail=1; }
grep -qE 'name: metrics' <<<"$(yq 'select(.kind == "NetworkPolicy" and .metadata.name == "model-gateway") | .spec.ingress[] | select(.ports[0].port == 15020)' - <<<"$mout")" ||
  { echo "FAIL: the metering values do not admit the metrics namespace to the gateway's 15020" >&2; fail=1; }
# The factory-level USD proof's values (intake.sh USD=1): gateway-mode runs on the stub provider, a
# meter, and a catalog that prices every model the gateway serves, the runs' and the broker clients'.
uout="$(helm template af . -n maku -f values-demo.yaml "${img[@]}" --set demo.githubApp.id=1 --set demo.githubApp.privateKey=pem1 \
  -f tests/intake-values.yaml --set maku-gateway.identity.issuer=https://kubernetes.default.svc.cluster.local -f tests/usd-values.yaml)" ||
  { echo "FAIL: the USD proof's values do not render" >&2; fail=1; uout=''; }
yq ea 'select(.kind == "ConfigMap" and .metadata.name == "agent-factory-config") | .data."config.yaml"' - <<<"$uout" >"$tmp/usd-config.yaml"
yq -e '.runs.gatewayIdentity and .meter.url == "http://vmsingle.metrics.svc.cluster.local:8428" and .meter.prices."stub-default".input == 1000' "$tmp/usd-config.yaml" >/dev/null 2>&1 ||
  { echo "FAIL: the USD proof's factory config lacks its identity, meter or prices" >&2; fail=1; }
(cd ../.. && CHART_DEMO_FACTORY_CONFIG="$tmp/usd-config.yaml" go test -count=1 -run TestRenderedDemoChartConfigParses ./internal/factory/config >"$tmp/parse-usd.log" 2>&1) ||
  { echo "FAIL: the USD proof's factory config does not parse:" >&2; cat "$tmp/parse-usd.log" >&2; fail=1; }
[[ "$(yq ea 'select(.kind == "ConfigMap" and .metadata.name == "model-gateway-model-catalog") | .data."catalog.json" | from_yaml | .providers.stub.models | keys | sort | join(",")' - <<<"$uout")" == "stub-default,stub-frontier,stub-light" ]] ||
  { echo "FAIL: the USD proof's catalog does not price every model the stub provider serves" >&2; fail=1; }
yq ea 'select(.kind == "ConfigMap" and .metadata.name == "run-controller-config") | .data."config.yaml"' - <<<"$uout" | yq -e '.model.mode == "gateway"' >/dev/null 2>&1 ||
  { echo "FAIL: the USD proof's runs are not in model.mode gateway" >&2; fail=1; }
# GW12: the gateway's prices default to the factory's meter.prices (one source); the gateway's own win.
catalogs() { yq ea '[select(.kind == "ConfigMap" and .metadata.name == "model-gateway-model-catalog")] | length' - <<<"$out"; }
[[ "$(catalogs)" == 1 ]] || { echo "FAIL: the gateway demo renders $(catalogs) model catalogs, want 1" >&2; fail=1; }
ykind ConfigMap 'select(.metadata.name=="model-gateway-model-catalog") | .data."catalog.json" | from_yaml | (.providers | length) == 0' 'the gateway demo without prices: an empty catalog'
out="$(helm template af . -n maku "${gwd[@]}" --set-json 'maku-factory.config.meter.prices={"m1":{"input":1.4,"cacheRead":0.26,"output":4.4},"m2":{"input":3,"cacheRead":0.3,"output":9},"agent-default":{"input":1,"cacheRead":0.1,"output":2}}')" ||
  { echo "FAIL: the gateway demo with factory prices does not render" >&2; fail=1; out=''; }
[[ "$(catalogs)" == 1 ]] || { echo "FAIL: factory prices render $(catalogs) model catalogs, want 1" >&2; fail=1; }
ykind ConfigMap 'select(.metadata.name=="model-gateway-model-catalog") | .data."catalog.json" | from_yaml | .providers.demo.models | ((keys | join(",")) == "m1,m2" and .m1.rates.input == "1.40" and .m1.rates.cacheRead == "0.26" and .m1.rates.output == "4.40" and (.m1.rates | length) == 3)' \
  'the catalog defaults to maku-factory.config.meter.prices, the served models only (agent-default is no provider model)'
# the umbrella-rendered catalog carries maku-gateway's own labels, as the subchart's objects do
[[ "$(yq ea 'select(.kind == "ConfigMap" and .metadata.name == "model-gateway-model-catalog") | .metadata.labels' - <<<"$out")" == \
   "$(yq ea 'select(.kind == "NetworkPolicy" and .metadata.name == "model-gateway") | .metadata.labels' - <<<"$out")" ]] ||
  { echo "FAIL: the umbrella's catalog does not carry maku-gateway's labels" >&2; fail=1; }
# once the factory prices anything, a served model without a row fails the render (review #518)
msg="$(helm template af . -n maku "${gwd[@]}" --set-json 'maku-factory.config.meter.prices={"m1":{"input":1.4,"cacheRead":0.26,"output":4.4}}' 2>&1 >/dev/null || true)"
grep -qF 'maku-gateway serves m2 (tier-light, provider demo) but maku-factory.config.meter.prices has no row for it' <<<"$msg" ||
  { echo "FAIL: a served model with no factory price does not fail the render (got: $msg)" >&2; fail=1; }
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
# v4 D8, PR2: the broker and the factory read one identity; a disagreement names both keys.
idv=("${args[@]}" -f tests/identity-values.yaml)
out="$(helm template af . -n maku "${idv[@]}")"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.human.preset == "generic" and .human.clientIDFile == "/etc/room-broker/oidc/web-client-id" and (.human | has("projectIDFile") | not))' 'the identity values render the generic broker with its ids by Secret'
# step 5 PR C: the identity leg's GitHub account link renders, its broker config filled from the Secret
out="$(helm template af . -n maku "${idv[@]}" -f tests/identity-link-values.yaml)" || { echo 'FAIL: the identity link values do not render' >&2; fail=1; out=''; }
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.human.githubLink.clientIDFile == "/etc/room-broker/github-link/client_id" and .human.githubLink.webURL == "https://e2e-stub.e2e.svc.cluster.local" and .human.access.ttl == "1m" and (.human.access | has("readerFile") | not))' 'the identity link values: the link, and room access without a reader'
yq ea 'select(.kind == "ConfigMap" and .metadata.name == "room-broker-config") | .data."config.yaml"' - <<<"$out" >"$tmp/identity-link.yaml"
(cd ../.. && go run ./charts/maku/tests/loadconfig broker "$tmp/identity-link.yaml") || fail=1
fapi=(--set maku-factory.config.api.humanIssuer=https://e2e-stub.e2e.svc.cluster.local --set maku-factory.config.api.preset=generic
      --set maku-factory.config.api.rolesClaim=groups)
helm template af . -n maku "${idv[@]}" "${fapi[@]}" >/dev/null || { echo 'FAIL: an agreeing broker and factory must render' >&2; fail=1; }
bad 'maku.broker.human.rolesClaim "groups" and maku-factory.config.api.rolesClaim "roles" must be equal' "${idv[@]}" "${fapi[@]}" --set maku-factory.config.api.rolesClaim=roles
bad 'maku.broker.human.preset "generic" and maku-factory.config.api.preset "zitadel" must be equal' "${idv[@]}" "${fapi[@]}" --set maku-factory.config.api.preset=zitadel
bad 'maku.broker.human.tokenUse "id" and maku-factory.config.api.tokenUse "access" must be equal' "${idv[@]}" "${fapi[@]}" --set maku-factory.config.api.tokenUse=access
bad 'maku.broker.human.issuer "https://e2e-stub.e2e.svc.cluster.local" and maku-factory.config.api.humanIssuer "https://other" must be equal' "${idv[@]}" "${fapi[@]}" --set maku-factory.config.api.humanIssuer=https://other
bad 'maku.broker.human.groups.member "agents-member" and maku-factory.config.api.groups.member "devs" must be equal' "${idv[@]}" "${fapi[@]}" --set maku-factory.config.api.groups.member=devs
bad 'maku.broker.human.audience "" and maku-factory.config.api.audience "https://api.rooms" must be equal' "${idv[@]}" "${fapi[@]}" --set maku-factory.config.api.audience=https://api.rooms
bad 'maku.broker.human.mcp.sponsoredClientClaim "cid" and maku-factory.config.api.sponsoredClientClaim "" must be equal' "${idv[@]}" "${fapi[@]}" --set maku.broker.human.mcp.sponsoredClientClaim=cid
# v4 D24 (step 7 PR B): the maku preflight collects the factory's checks, which need its route grant.
bad 'maku.preflight.enabled needs maku-factory.preflight.enabled' "${args[@]}" --set maku.preflight.enabled=true
helm template af . -n maku "${args[@]}" --set maku.preflight.enabled=true --set maku-factory.preflight.enabled=true >/dev/null ||
  { echo 'FAIL: both preflight switches on must render' >&2; fail=1; }
# v4 D14, PR10: ExternalSecrets for the seams the values list, against the store they name; off, nothing.
esv=("${args[@]}" -f tests/externalsecrets-values.yaml)
out="$(helm template af . -n maku "${args[@]}")"
ynone 'select(.kind=="ExternalSecret")' 'externalSecrets off renders no ExternalSecret'
out="$(helm template af . -n maku "${esv[@]}")"
es="$(yq ea 'select(.kind=="ExternalSecret")' - <<<"$out")"
if [[ ${UPDATE_GOLDEN:-} == 1 ]]; then printf '%s\n' "$es" >tests/golden/externalsecrets.yaml; fi
printf '%s\n' "$es" | diff -u tests/golden/externalsecrets.yaml - >&2 || { echo 'FAIL: the ExternalSecrets differ from tests/golden/externalsecrets.yaml' >&2; fail=1; }
ykind ExternalSecret 'select(.metadata.name=="db-broker") | (.spec.target.name == "db-broker" and .spec.target.creationPolicy == "Owner" and .spec.target.deletionPolicy == "Retain" and .spec.secretStoreRef.kind == "SecretStore" and .spec.secretStoreRef.name == "agents-secrets" and (.spec.target.template.data.uri | test("^postgresql://")))' 'databaseBroker: its resolved name, Owner and Retain, the named store, the template passed through'
ykind ExternalSecret 'select(.metadata.name=="db-owner") | .spec.dataFrom[0].extract.key == "maku/rooms-owner"' 'a dataFrom.extract is trusted and passed through'
ykind ExternalSecret 'select(.metadata.name=="db-ca") | .spec.refreshInterval == "24h"' 'a seam may set its own refreshInterval'
ykind ExternalSecret 'select(.metadata.name=="agent-factory-intake") | (.metadata.namespace == "maku" and .spec.refreshInterval == "1h" and .apiVersion == "external-secrets.io/v1")' 'a default name, the release namespace, the default refresh, ESO v1'
ynone 'select(.kind=="Secret" and (.metadata.name=="db-broker" or .metadata.name=="agent-factory-intake"))' 'never a Secret beside an ExternalSecret of the same name'
bad 'externalSecrets.enabled needs externalSecrets.storeRef.name' "${esv[@]}" --set externalSecrets.storeRef.name=
bad 'externalSecrets.secrets.databaseBroker: key uri is required (docs/install-secrets.md)' "${esv[@]}" --set 'externalSecrets.secrets.databaseBroker.template=null'
bad 'externalSecrets.secrets.databaseBroker: key dsn is required (docs/install-secrets.md)' "${esv[@]}" --set maku.broker.database.secretKey=dsn
bad 'externalSecrets.secrets.nope: not a seam id (docs/install-secrets.md)' "${esv[@]}" --set-json 'externalSecrets.secrets.nope={"dataFrom":[{"extract":{"key":"x"}}]}'
bad 'externalSecrets.secrets.factoryApp: the demo writes agent-factory-github from demo.githubApp' "${esv[@]}" --set-json 'externalSecrets.secrets.factoryApp={"dataFrom":[{"extract":{"key":"x"}}]}'
bad 'externalSecrets.secrets.databaseBroker: the bundled Postgres (postgres.enabled) writes rooms-broker-dsn' "${args[@]}" -f tests/externalsecrets-values.yaml \
  --set postgres.enabled=true --set maku.broker.database.brokerSecret=rooms-broker-dsn
# Review of #544: a runtime writer refuses its Secret only while it runs; one writer per Secret name.
xf='{"dataFrom":[{"extract":{"key":"x"}}]}'
bad 'externalSecrets.secrets.runGit: no Secret name: set maku.runController.gitSecret' "${esv[@]}" --set-json "externalSecrets.secrets.runGit=$xf"
bad 'externalSecrets.secrets.factoryAPITLS: cert-manager (maku-factory.api.tls.issuerRef) writes factory-api-tls' "${esv[@]}" \
  --set maku-factory.api.tls.secretName=factory-api-tls --set maku-factory.api.tls.issuerRef.name=ca --set-json "externalSecrets.secrets.factoryAPITLS=$xf"
out="$(helm template af . -n maku "${esv[@]}" --set maku-factory.api.tls.secretName=factory-api-tls --set-json "externalSecrets.secrets.factoryAPITLS=$xf")"
ykind ExternalSecret 'select(.metadata.name=="factory-api-tls") | .spec.dataFrom[0].extract.key == "x"' 'without issuerRef the API pair is the operator'"'"'s'
bad 'externalSecrets.secrets.gatewayProvider.demo: the umbrella writes demo-model' "${gwd[@]}" -f tests/externalsecrets-values.yaml \
  --set-json "externalSecrets.secrets.gatewayProvider\.demo=$xf"
bad 'externalSecrets.secrets.runModel: the umbrella writes agent-model' "${esv[@]}" --set-json "externalSecrets.secrets.runModel=$xf"
bad 'externalSecrets.secrets.databaseOwner and externalSecrets.secrets.databaseBroker both target db-broker' "${esv[@]}" --set maku.broker.database.ownerSecret=db-broker
bad 'externalSecrets.secrets.intake: maku-factory.enabled is false' "${esv[@]}" --set maku-factory.enabled=false
bad 'externalSecrets.secrets.databaseCA: data or dataFrom is required' "${esv[@]}" --set 'externalSecrets.secrets.databaseCA.data=null'
out="$(helm template af . -n maku "${esv[@]}" --set externalSecrets.storeRef.kind=ClusterSecretStore)"
ykind ExternalSecret '.spec.secretStoreRef.kind == "ClusterSecretStore"' 'a ClusterSecretStore is accepted'
bad 'maku.runController.model.mode gateway needs maku-gateway.enabled, or maku.runController.model.gateway.service' "${args[@]}" \
  --set maku.runController.model.mode=gateway --set-json 'maku.runController.model.gateway.aliases={"public":["agent-default"]}'
bad "maku.runController.model.gateway.aliases.public [agent-default] must be maku-gateway.aliases.public's keys [agent-default, tier-light]" "${gwd[@]}" \
  --set-json 'maku.runController.model.gateway.aliases={"public":["agent-default"]}'
bad 'maku.runController.model.gateway.audiencePrefix "other" must equal maku-gateway.audiencePrefix "maku-gateway"' "${gwd[@]}" --set maku.runController.model.gateway.audiencePrefix=other
bad 'maku.runController.model.gateway.ports (8090, 8081) must equal maku-gateway.listeners' "${gwd[@]}" --set maku.runController.model.gateway.ports.public=8090
bad 'maku.runController.model.gateway.service "elsewhere" must be empty or maku-gateway.name "model-gateway"' "${gwd[@]}" --set maku.runController.model.gateway.service=elsewhere
bad 'demo.model.baseURL: with maku.runController.model.mode gateway' "${gwd[@]}" --set demo.model.baseURL=http://model.example.test/v1
bad 'maku-factory.config.runs.gatewayIdentity is true but maku.runController.model.mode is "secret"' "${args[@]}" --set maku-factory.config.runs.gatewayIdentity=true
# v4 step 8 (D7, R4): model.mode secret is demo-only; the maku subchart reads its own marker.
bad 'runController.model.mode secret is demo-only (v4 D7)' "${args[@]}" --set maku.demoValues=false
bad 'maku.runController.model.mode gateway needs maku-factory.config.runs.gatewayIdentity: true' "${gwd[@]}" --set maku-factory.config.runs.gatewayIdentity=false
# ANR's form: an external gateway, maku-gateway off.
out="$(helm template af . -n maku "${img[@]}" -f values-demo.yaml --set demo.githubApp.id=123 --set demo.githubApp.privateKey=pem1 --set maku.runController.model.mode=gateway --set maku-gateway.enabled=false --set agentgateway.enabled=false \
  --set maku-factory.config.runs.gatewayIdentity=true \
  --set maku.runController.model.gateway.service=agentgateway --set maku.runController.model.gateway.namespace=ai \
  --set-json 'maku.runController.model.gateway.aliases={"public":["agent-default"]}')" || { echo "FAIL: an external gateway does not render" >&2; fail=1; out=''; }
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | .model.gateway.url == "http://agentgateway.ai.svc.cluster.local"' 'an external gateway by service and namespace'
ynone 'select(.kind == "Gateway")' 'an external gateway renders no Gateway'

# v4 D9: the bundled Postgres is the demo's alone. The demo gains exactly the third DSN Secret, the
# retention CronJob reading it, and the job's ingress to rooms-db. postgres.enabled=false with the demo
# values renders no database and no DSN Secret, and the broker reads the Secrets it names; any other
# profile with the bundled Postgres on fails, naming D9.
demo="$(helm template af . -n maku "${args[@]}")"
out="$demo"
ykind Secret 'select(.metadata.name == "rooms-retention-dsn") | .stringData.uri | test("^postgresql://rooms_retention:[A-Za-z0-9]{32}@rooms-db[.]maku[.]svc:5432/rooms[?]sslmode=disable$")' 'demo: the retention DSN Secret, as rooms_retention'
ykind CronJob 'select(.metadata.name == "room-broker-retention") | .spec.jobTemplate.spec.template.spec.containers[0].env[0].valueFrom.secretKeyRef.name == "rooms-retention-dsn"' 'demo: the retention CronJob reads rooms-retention-dsn'
ykind NetworkPolicy 'select(.metadata.name == "rooms-db") | (.spec.ingress[0].from | map(.podSelector.matchLabels."app.kubernetes.io/name") | join(",")) == "room-broker,room-broker-retention"' 'demo: rooms-db admits the broker and the retention job'
out="$(helm template af . -n maku "${args[@]}" --set postgres.enabled=false)"
ynone 'select(.kind == "StatefulSet" or .kind == "Service" and .metadata.name == "rooms-db" or .kind == "ConfigMap" and .metadata.name == "rooms-db-init" or .kind == "NetworkPolicy" and .metadata.name == "rooms-db" or .kind == "Secret" and (.metadata.name | test("^rooms-")))' 'postgres.enabled=false still renders the bundled database or its Secrets'
got=$(yq ea '[select(.kind == "Deployment" and .metadata.name == "room-broker") | .spec.template.spec | (.initContainers[0].env[0].valueFrom.secretKeyRef.name, .containers[0].env[] | select(.name == "ROOMS_DATABASE_URL") | .valueFrom.secretKeyRef.name)] + [select(.kind == "CronJob") | .spec.jobTemplate.spec.template.spec.containers[0].env[0].valueFrom.secretKeyRef.name] | join(",")' - <<<"$out")
[[ $got == "rooms-owner-dsn,rooms-broker-dsn,rooms-retention-dsn" ]] || { echo "FAIL: postgres.enabled=false: the broker and the job read '$got', want the three named Secrets" >&2; fail=1; }
for p in production ""; do
  msg="$(helm template af . -n maku "${img[@]}" --set profile="$p" --set postgres.enabled=true 2>&1 >/dev/null || true)"
  grep -qF 'the bundled Postgres is demo-only (D9): bring your own and name its DSN Secrets' <<<"$msg" || { echo "FAIL: profile '$p' with postgres.enabled does not fail naming D9 (got: $msg)" >&2; fail=1; }
done
yq -e '.postgres.enabled == false' values.yaml >/dev/null 2>&1 || { echo "FAIL: values.yaml does not turn the bundled Postgres off" >&2; fail=1; }

# values-mine.example.yaml renders (dummy values for the CHANGE lines) and its config parses
sed -e 's#sk-change-me#k1#' -e 's#id: "123456"#id: "123"#' -e 's#my-app\[bot\]#maku-demo[bot]#' values-mine.example.yaml >"$tmp/mine.yaml"
mine="$(helm template af . -n maku "${img[@]}" -f values-demo.yaml -f "$tmp/mine.yaml")" || { echo "FAIL: values-mine.example.yaml does not render" >&2; fail=1; mine=''; }
if [[ -n $mine ]]; then
  yq ea 'select(.kind == "ConfigMap" and .metadata.name == "agent-factory-config") | .data."config.yaml"' - <<<"$mine" >"$tmp/mine-config.yaml"
  yq -e '.repositories | length == 1' "$tmp/mine-config.yaml" >/dev/null 2>&1 || { echo "FAIL: the example's config lacks its repository" >&2; fail=1; }
  yq -e '.repositories[0].merge.approveToMerge == true' "$tmp/mine-config.yaml" >/dev/null 2>&1 || { echo "FAIL: the example's repository drops approve-to-merge" >&2; fail=1; }
  (cd ../.. && CHART_DEMO_FACTORY_CONFIG="$tmp/mine-config.yaml" go test -count=1 -run TestRenderedDemoChartConfigParses ./internal/factory/config >"$tmp/parse2.log" 2>&1) || { echo "FAIL: the example's config does not parse:" >&2; cat "$tmp/parse2.log" >&2; fail=1; }
fi

# v4 step 6 Task 4 (D21, EX12): opt-in ServiceMonitors and PodMonitors. Off (the default), none;
# the golden above already holds the demo byte for byte.
out="$(helm template af . -n maku "${args[@]}")"
ynone 'select(.kind == "ServiceMonitor" or .kind == "PodMonitor" or .kind == "PrometheusRule") | .kind' 'monitoring off renders a monitoring object'
mon=(--set monitoring.serviceMonitor.enabled=true --set-json 'monitoring.serviceMonitor.labels={"release":"prom"}')
out="$(helm template af . -n maku "${args[@]}" "${mon[@]}")" || { echo "FAIL: monitoring.serviceMonitor.enabled does not render" >&2; fail=1; out=''; }
got=$(yq ea '[select(.kind == "ServiceMonitor" or .kind == "PodMonitor") | .kind + "/" + .metadata.name] | sort | join(" ")' - <<<"$out")
want="PodMonitor/maku-run-controller ServiceMonitor/agent-factory ServiceMonitor/room-broker"
[[ $got == "$want" ]] || { echo "FAIL: the scrape objects are '$got', want '$want'" >&2; fail=1; }
ykind ServiceMonitor '((.spec.endpoints | length) == 1 and .spec.endpoints[0].port == "metrics" and .spec.endpoints[0].path == "/metrics" and .spec.endpoints[0].interval == "30s")' 'each ServiceMonitor scrapes port metrics, /metrics, every 30s'
ykind ServiceMonitor 'select(.metadata.name == "room-broker") | .spec.selector.matchLabels."app.kubernetes.io/name" == "room-broker"' 'the broker ServiceMonitor selects the room-broker Service'
ykind ServiceMonitor 'select(.metadata.name == "agent-factory") | .spec.selector.matchLabels."app.kubernetes.io/name" == "agent-factory"' 'the factory ServiceMonitor selects the agent-factory Service'
ykind PodMonitor '((.spec.podMetricsEndpoints | length) == 1 and .spec.podMetricsEndpoints[0].port == "metrics" and .spec.podMetricsEndpoints[0].path == "/metrics")' 'each PodMonitor scrapes the pod port metrics, /metrics'
ykind PodMonitor 'select(.metadata.name == "maku-run-controller") | (.spec.selector.matchLabels."app.kubernetes.io/name" == "run-controller" and (.spec.podMetricsEndpoints[0].relabelings[] | select(.targetLabel == "job") | .replacement) == "run-controller")' 'the run-controller PodMonitor selects its pods, job run-controller'
ykind ServiceMonitor '(.metadata.labels.release == "prom" and .metadata.namespace == "maku")' 'monitoring.serviceMonitor.labels reach the ServiceMonitors'
ykind PodMonitor '(.metadata.labels.release == "prom" and .metadata.namespace == "maku")' 'monitoring.serviceMonitor.labels reach the PodMonitors'
# kubernetes mode adds the egress proxy's PodMonitor, and needs a scraper the policies admit
out="$(helm template af . -n maku "${args[@]}" "${mon[@]}" --set maku.networkPolicies.mode=kubernetes \
  --set-json 'maku.networkPolicies.kubernetes.metricsFrom=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"monitoring"}}}]')" ||
  { echo "FAIL: monitoring in kubernetes mode does not render" >&2; fail=1; out=''; }
ykind PodMonitor 'select(.metadata.name == "maku-egress-proxy") | (.spec.selector.matchLabels."app.kubernetes.io/name" == "egress-proxy" and .spec.podMetricsEndpoints[0].port == "metrics")' 'kubernetes mode: the egress proxy PodMonitor'
bad 'monitoring.serviceMonitor.enabled in maku.networkPolicies.mode kubernetes needs maku.networkPolicies.kubernetes.metricsFrom: the scraper could not reach :9090' \
  "${args[@]}" "${mon[@]}" --set maku.networkPolicies.mode=kubernetes
# cilium mode: the broker's and the run-controller's CNPs admit :9090 only from the
# scraper monitoring.serviceMonitor.cilium.fromEndpoints names, in one extra CNP rendered only with
# monitoring on; empty, the render fails
cil=(--set maku.networkPolicies.mode=cilium)
ynone 'select(.kind == "CiliumNetworkPolicy" and .metadata.name == "maku-metrics") | .kind' 'monitoring off renders the scrape CNP'
out="$(helm template af . -n maku "${args[@]}" "${cil[@]}")"
ynone 'select(.kind == "CiliumNetworkPolicy" and .metadata.name == "maku-metrics") | .kind' 'cilium mode without monitoring renders the scrape CNP'
bad 'monitoring.serviceMonitor.enabled in maku.networkPolicies.mode cilium needs monitoring.serviceMonitor.cilium.fromEndpoints: the scraper could not reach :9090' \
  "${args[@]}" "${mon[@]}" "${cil[@]}"
out="$(helm template af . -n maku "${args[@]}" "${mon[@]}" "${cil[@]}" \
  --set-json 'monitoring.serviceMonitor.cilium.fromEndpoints=[{"matchLabels":{"io.kubernetes.pod.namespace":"monitoring","app.kubernetes.io/name":"prometheus"}}]')" ||
  { echo "FAIL: monitoring in cilium mode does not render" >&2; fail=1; out=''; }
ykind CiliumNetworkPolicy 'select(.metadata.name == "maku-metrics") | ((.spec.endpointSelector.matchExpressions[0].values | join(",")) == "room-broker,run-controller" and .spec.ingress[0].fromEndpoints[0].matchLabels."app.kubernetes.io/name" == "prometheus" and .spec.ingress[0].toPorts[0].ports[0].port == "9090" and (.spec | has("egress") | not))' 'cilium mode: the scrape CNP admits the scraper on :9090 to the broker and the run-controller, and adds no egress'
# maku-gateway adds its proxies' PodMonitor (the pod's metrics port, 15020); its NetworkPolicy must
# admit the scraper (metrics.allowFrom), unless its mode is none
bad 'monitoring.serviceMonitor.enabled with maku-gateway.networkPolicy.mode "kubernetes" needs maku-gateway.metrics.allowFrom: the scraper could not reach :15020' "${gwd[@]}" "${mon[@]}"
out="$(helm template af . -n maku "${gwd[@]}" "${mon[@]}" --set maku-gateway.networkPolicy.mode=none)" || { echo "FAIL: monitoring with maku-gateway mode none does not render" >&2; fail=1; out=''; }
ykind PodMonitor 'select(.metadata.name == "maku-gateway") | .kind == "PodMonitor"' 'maku-gateway mode none: the PodMonitor needs no allowFrom'
out="$(helm template af . -n maku "${gwd[@]}" "${mon[@]}" --set-json 'maku-gateway.metrics.allowFrom=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"monitoring"}}}]')" || { echo "FAIL: monitoring with maku-gateway does not render" >&2; fail=1; out=''; }
ykind PodMonitor 'select(.metadata.name == "maku-gateway") | (.spec.selector.matchLabels."gateway.networking.k8s.io/gateway-name" == "model-gateway" and .spec.podMetricsEndpoints[0].port == "metrics" and .spec.podMetricsEndpoints[0].path == "/metrics")' 'maku-gateway: the model-gateway proxies PodMonitor'
# the gateway group's rules read the job that PodMonitor sets
out="$(helm template af . -n maku "${gwd[@]}" -f tests/metering-values.yaml --set maku-gateway.aliases.public.agent-default.model=echo-default \
  --set maku-gateway.aliases.public.tier-light.model=echo-light "${mon[@]}" --set monitoring.prometheusRule.enabled=true)"
job="$(yq ea 'select(.kind == "PodMonitor" and .metadata.name == "maku-gateway") | .spec.podMetricsEndpoints[0].relabelings[] | select(.targetLabel == "job") | .replacement' - <<<"$out")"
[[ $job == model-gateway ]] || { echo "FAIL: the maku-gateway PodMonitor sets job '$job'" >&2; fail=1; }
n="$(yq ea 'select(.kind == "PrometheusRule") | .spec.groups[] | select(.name == "maku-gateway") | .rules | length' - <<<"$out")"
k="$(yq ea -o=json -I=0 'select(.kind == "PrometheusRule") | .spec.groups[] | select(.name == "maku-gateway") | .rules[] | .expr' - <<<"$out" | grep -cF "job=\\\"$job\\\"")"
[[ $n == 7 && $k == 7 ]] || { echo "FAIL: $k of the gateway group's $n rules read the PodMonitor's job $job" >&2; fail=1; }
notes="$(helm install af . -n maku --dry-run=client "${gwd[@]}" "${mon[@]}" --set maku-gateway.networkPolicy.mode=none --set agentgateway.enabled=true 2>/dev/null | sed -n '/^NOTES:/,$p')"
grep -qF 'agentgateway.monitoring.enabled' <<<"$notes" || { echo "FAIL: NOTES does not tell agentgateway.monitoring.enabled with monitoring on" >&2; fail=1; }

# v4 step 6 Task 5 (D21, EX13): the product alerts, one PrometheusRule `maku`, a group per component
rule=(--set monitoring.prometheusRule.enabled=true --set-json 'monitoring.prometheusRule.labels={"release":"prom"}')
# groups GROUPS: the rendered rule's group names, in order
groups() { yq ea 'select(.kind == "PrometheusRule") | [.spec.groups[].name] | join(" ")' - <<<"$out"; }
# alerts: the rendered alert names, sorted
alerts() { yq ea '[select(.kind == "PrometheusRule") | .spec.groups[].rules[] | (.alert // .record)] | sort | join(" ")' - <<<"$out"; }
out="$(helm template af . -n maku "${args[@]}" "${rule[@]}")" || { echo "FAIL: monitoring.prometheusRule.enabled does not render" >&2; fail=1; out=''; }
[[ "$(groups)" == "maku-rooms maku-factory maku-run-path" ]] || { echo "FAIL: the demo's groups are '$(groups)', want 'maku-rooms maku-factory maku-run-path'" >&2; fail=1; }
ykind PrometheusRule '(.metadata.name == "maku" and .metadata.namespace == "maku" and .metadata.labels.release == "prom")' 'one PrometheusRule maku, with monitoring.prometheusRule.labels'
ynone 'select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select((.annotations.runbook_url // "") == "" or (.annotations.dashboard // "") == "") | .alert' 'every rule carries runbook_url and dashboard'
ynone 'select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.expr | test("agent-system|xplane-|\\$\\{")) | .alert' 'an expression names agent-system, xplane- or ${'
ynone 'select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(to_yaml | test("__[A-Z_]+__|agent-system|xplane-|\\$\\{")) | .alert' 'a rule keeps a placeholder, agent-system, xplane- or ${'
ykind PrometheusRule '.spec.groups[].rules[] | select(.alert == "RoomStalled") | .annotations.summary == "Room {{ $labels.room }} has a running run and no event for 30 minutes"' '{{ $labels.room }} survives the render'
ykind PrometheusRule '.spec.groups[].rules[] | select(.alert == "RoomBrokerDown") | .expr == "absent(up{job=\"room-broker\", namespace=\"maku\"} == 1)"' 'RoomBrokerDown reads the release namespace'
ykind PrometheusRule '.spec.groups[].rules[] | select(.alert == "RoomBrokerDown") | (.annotations.runbook_url | test("^https://github.com/Smana/maku/blob/v[^/]+/docs/operations.md#room-alerts$"))' 'runbook_url defaults to the release docs'
all="$(alerts)"
for a in RoomAuthnHumanIssuerNeverFetched EgressProxyDown AgentSandboxPodPending RoomKnowledgeFailing; do
  grep -qw "$a" <<<"$all" && { echo "FAIL: the demo renders $a (no issuer, mode none, no kube-state-metrics, no knowledge endpoint)" >&2; fail=1; }
done
out="$(helm template af . -n maku "${args[@]}" "${rule[@]}" --set-json 'monitoring.prometheusRule.disabled=["AgentFactoryClassPausedLong"]')"
want="$(sed 's/ AgentFactoryClassPausedLong / /' <<<" $all ")"
[[ " $(alerts) " == "$want" ]] || { echo "FAIL: disabled [AgentFactoryClassPausedLong] does not remove exactly that rule" >&2; fail=1; }
bad 'monitoring.prometheusRule.disabled names NoSuchAlert, which no group holds' "${args[@]}" "${rule[@]}" --set-json 'monitoring.prometheusRule.disabled=["NoSuchAlert"]'
# the issuer, mode kubernetes and kube-state-metrics each bring their rule; docsURL replaces the docs
out="$(helm template af . -n maku "${args[@]}" "${rule[@]}" --set maku.broker.human.mode=oidc --set maku.broker.human.issuer=https://idp.example.test \
  --set maku.networkPolicies.mode=kubernetes --set monitoring.prometheusRule.kubeStateMetrics=true --set monitoring.prometheusRule.docsURL=https://docs.example.test/maku/)" ||
  { echo "FAIL: the rule with an issuer, mode kubernetes and kube-state-metrics does not render" >&2; fail=1; out=''; }
ykind PrometheusRule '.spec.groups[].rules[] | select(.alert == "RoomAuthnHumanIssuerNeverFetched") | (.expr | test("issuer=\"https://idp.example.test\""))' 'the human issuer reaches its rule'
ykind PrometheusRule '.spec.groups[].rules[] | select(.alert == "EgressProxyDown") | .expr == "absent(up{job=\"egress-proxy\", namespace=\"maku\"} == 1)"' 'mode kubernetes: EgressProxyDown'
ykind PrometheusRule '.spec.groups[].rules[] | select(.alert == "AgentSandboxPodPending") | (.expr | test("namespace=\"agents\""))' 'kubeStateMetrics: AgentSandboxPodPending on the runs namespace'
ykind PrometheusRule '.spec.groups[].rules[] | select(.alert == "RoomBrokerDown") | .annotations.runbook_url == "https://docs.example.test/maku/operations.md#room-alerts"' 'docsURL replaces the docs base'
# a knowledge endpoint brings RoomKnowledgeFailing (EX11)
out="$(helm template af . -n maku "${args[@]}" "${rule[@]}" --set maku.broker.roomTools.knowledge.url=http://runlore.runlore.svc.cluster.local:8080/mcp)"
ykind PrometheusRule '.spec.groups[] | select(.name == "maku-rooms") | .rules[] | select(.alert == "RoomKnowledgeFailing") | (.expr | test("maku_rooms_knowledge_calls_total"))' 'roomTools.knowledge.url: RoomKnowledgeFailing'
out="$(helm template af . -n maku "${args[@]}" "${rule[@]}" --set maku.broker.human.issuer=https://idp.example.test)"
ynone 'select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.alert == "RoomAuthnHumanIssuerNeverFetched") | .alert' 'human.mode local renders RoomAuthnHumanIssuerNeverFetched, though an issuer is set'
# slack.enabled adds maku-slack; maku-gateway adds no group until PR B2; maku-factory off drops its group
out="$(helm template af . -n maku "${args[@]}" "${rule[@]}" -f values-slack.example.yaml --set expose.mode=none --set slack.networkPolicy.mode=none)"
[[ "$(groups)" == "maku-rooms maku-factory maku-run-path maku-slack" ]] || { echo "FAIL: slack.enabled: groups '$(groups)'" >&2; fail=1; }
# PR B2 (Task 22): maku-gateway.enabled adds maku-gateway, on the gateway's job and the runs' subject.
# The gateway demo prices nothing and has no meter: no lookup to judge, no ledger to cross-check.
out="$(helm template af . -n maku "${gwd[@]}" "${rule[@]}")"
got="$(yq ea '[select(.kind == "PrometheusRule") | .spec.groups[] | select(.name == "maku-gateway") | .rules[] | (.alert // .record)] | join(" ")' - <<<"$out")"
[[ $got == "AgentRouterMetricContractBroken AgentGateway5xxRatioHigh AgentGatewayModelErrorRatioHigh AgentGatewayUnauthorizedBurst" ]] || { echo "FAIL: an unpriced gateway without a meter: the maku-gateway group holds '$got'" >&2; fail=1; }
# the metering values price the catalog and give the factory a meter: the whole group
gwm=("${gwd[@]}" -f tests/metering-values.yaml --set maku-gateway.aliases.public.agent-default.model=echo-default --set maku-gateway.aliases.public.tier-light.model=echo-light)
out="$(helm template af . -n maku "${gwm[@]}" "${rule[@]}")" || { echo "FAIL: the metering values with the rule do not render" >&2; fail=1; out=''; }
[[ "$(groups)" == "maku-rooms maku-factory maku-run-path maku-gateway" ]] || { echo "FAIL: maku-gateway.enabled: groups '$(groups)', want the maku-gateway group last" >&2; fail=1; }
got="$(yq ea '[select(.kind == "PrometheusRule") | .spec.groups[] | select(.name == "maku-gateway") | .rules[] | (.alert // .record)] | join(" ")' - <<<"$out")"
[[ $got == "AgentRouterMetricContractBroken AgentGateway5xxRatioHigh AgentGatewayModelErrorRatioHigh AgentGatewayUnauthorizedBurst AgentGatewayCostUnpriced agent_cost:gateway_run_usd AgentCostCrossCheckMismatch" ]] || { echo "FAIL: the maku-gateway group holds '$got'" >&2; fail=1; }
ykind PrometheusRule '.spec.groups[].rules[] | select(.record == "agent_cost:gateway_run_usd") | .expr == "sum by (run) (label_replace(max_over_time(agentgateway_gen_ai_client_cost_usd_total{job=\"model-gateway\", namespace=\"maku\", ar_agent=~\"system:serviceaccount:agents:run-.+\"}[6h]), \"run\", \"$1\", \"ar_agent\", \"system:serviceaccount:agents:run-(.+)\"))"' 'the cross-check record reads the gateway job and the runs subject, $1 intact'
ynone 'select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(to_yaml | test("__[A-Z_]+__|agent-system|agent-gateway\"|xplane-|\\$\\{|cached_input|cache_creation_input|gen_ai_client_token_usage_sum\\{(.*)namespace=\"agent")) | (.alert // .record)' 'a gateway rule keeps a placeholder or a cloud-native-ref name'
ynone 'select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.expr | test("(^|[^_])gen_ai_(client|server)_")) | (.alert // .record)' 'a gateway rule reads a relabelled gen_ai_* name, not agentgateway_*'
ynone 'select(.kind == "PrometheusRule") | .spec.groups[] | select(.name == "maku-gateway") | .rules[] | select(.expr | test("increase\\(agentgateway_gen_ai_client_cost_usd_total")) | (.alert // .record)' 'the cross-check reads a per-run cost counter with increase(), which drops its first sample'
# a priced catalog without a meter URL: the lookups are judged, the ledger is not cross-checked
out="$(helm template af . -n maku "${gwm[@]}" "${rule[@]}" --set maku-factory.config.meter.url=)" || { echo "FAIL: the metering values without a meter URL do not render" >&2; fail=1; out=''; }
got="$(yq ea '[select(.kind == "PrometheusRule") | .spec.groups[] | select(.name == "maku-gateway") | .rules[] | (.alert // .record)] | join(" ")' - <<<"$out")"
[[ $got == "AgentRouterMetricContractBroken AgentGateway5xxRatioHigh AgentGatewayModelErrorRatioHigh AgentGatewayUnauthorizedBurst AgentGatewayCostUnpriced" ]] || { echo "FAIL: no meter URL: the maku-gateway group holds '$got'" >&2; fail=1; }
# a namePrefix with a dot is escaped where it is a regex
out="$(helm template af . -n maku "${gwm[@]}" "${rule[@]}" --set maku.runs.namePrefix=r.n- --set maku-gateway.runs.namePrefix=r.n-)" || { echo "FAIL: a dotted namePrefix does not render" >&2; fail=1; out=''; }
rec="$(yq ea 'select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.record == "agent_cost:gateway_run_usd") | .expr' - <<<"$out")"
grep -qF 'ar_agent=~"system:serviceaccount:agents:r\\.n-.+"' <<<"$rec" && grep -qF '"system:serviceaccount:agents:r\\.n-(.+)"' <<<"$rec" ||
  { echo "FAIL: the run subject is not regex-escaped (PromQL doubles the backslash): $rec" >&2; fail=1; }
# without maku-factory, no ledger: the cross-check's record and alert are left out
out="$(helm template af . -n maku "${gwd[@]}" "${rule[@]}" --set maku-factory.enabled=false --set maku-factory.runTokens.perRun=false \
  --set maku-factory.config.runs.gatewayIdentity=false)" || { echo "FAIL: the gateway without the factory does not render" >&2; fail=1; out=''; }
[[ "$(groups)" == "maku-rooms maku-run-path maku-gateway" ]] || { echo "FAIL: no factory: groups '$(groups)'" >&2; fail=1; }
grep -qE 'AgentCostCrossCheckMismatch|agent_cost:gateway_run_usd' <<<"$(alerts)" && { echo "FAIL: the cross-check renders without maku-factory" >&2; fail=1; }
out="$(helm template af . -n maku "${args[@]}" "${rule[@]}" --set maku.runController.enabled=false --set maku-factory.enabled=false \
  --set maku-factory.runTokens.perRun=false)" || { echo "FAIL: the broker alone does not render its rule" >&2; fail=1; out=''; }
[[ "$(groups)" == "maku-rooms" ]] || { echo "FAIL: the broker alone: groups '$(groups)', want maku-rooms" >&2; fail=1; }

# v4 step 6 (D21, EX14): the product dashboards, one ConfigMap each, rendered only when asked.
# The files name the runs and the gateway only through placeholders.
lit="$(grep -noE 'system:serviceaccount:[a-z0-9]|namespace=\\"(agents|agent-gateway|agent-system|maku)\\"' files/dashboards/*.json || true)"
[[ -z $lit ]] || { echo "FAIL: a dashboard names a runs, gateway or release namespace literally (use __RUN_SUBJECT__, __RUNS_NAMESPACE__, __NAMESPACE__): $lit" >&2; fail=1; }
out="$(helm template af . -n maku "${args[@]}")"
ynone 'select(.kind == "ConfigMap" and (.metadata.name | test("^maku-dashboard-"))) | .metadata.name' 'dashboards off renders a dashboard ConfigMap'
dash=(--set monitoring.dashboards.enabled=true --set-json 'monitoring.dashboards.annotations={"grafana_folder":"Agents"}')
uids='agent-factory,agent-fleet,agent-identity,agent-room,agent-run,agents-overview'
dashboards() { yq ea -r '[select(.kind == "ConfigMap" and (.metadata.name | test("^maku-dashboard-"))) | .metadata.name | sub("^maku-dashboard-"; "")] | sort | join(",")' - <<<"$out"; }
dashcheck() { # dashcheck WANT_UIDS: every dashboard ConfigMap holds <uid>.json, parses, keeps its uid, and is clean
  [[ "$(dashboards)" == "$1" ]] || { echo "FAIL: dashboards '$(dashboards)', want '$1'" >&2; fail=1; }
  ykind ConfigMap 'select(.metadata.name | test("^maku-dashboard-")) | (.metadata.labels.grafana_dashboard == "1" and .metadata.annotations.grafana_folder == "Agents" and .metadata.namespace == "maku")' 'each dashboard ConfigMap has the sidecar label and the annotations'
  local u body
  for u in ${1//,/ }; do
    body="$(yq ea -r "select(.kind == \"ConfigMap\" and .metadata.name == \"maku-dashboard-$u\") | .data.\"$u.json\"" - <<<"$out")"
    [[ "$(jq -r .uid <<<"$body" 2>/dev/null)" == "$u" ]] || { echo "FAIL: maku-dashboard-$u does not hold $u.json with uid $u" >&2; fail=1; continue; }
    ! grep -qE '\$\$\{|agent-system|xplane-|VictoriaLogs|VictoriaTraces|victoriametrics-logs|jaeger|cached_input|cache_creation_input|Envoy|__[A-Z_]+__|\$\{private_domain_name\}' <<<"$body" ||
      { echo "FAIL: maku-dashboard-$u keeps a Flux escape, a cloud-native-ref name, a logs or traces panel or a placeholder: $(grep -oE '\$\$\{|agent-system|xplane-|VictoriaLogs|VictoriaTraces|victoriametrics-logs|jaeger|cached_input|cache_creation_input|Envoy|__[A-Z_]+__' <<<"$body" | sort -u | tr '\n' ' ')" >&2; fail=1; }
    (( $(wc -c <<<"$body") < 900 * 1024 )) || { echo "FAIL: maku-dashboard-$u is 900 KiB or more" >&2; fail=1; }
  done
}
out="$(helm template af . -n maku "${args[@]}" "${dash[@]}")" || { echo "FAIL: monitoring.dashboards.enabled does not render" >&2; fail=1; out=''; }
dashcheck "$uids"
ykind ConfigMap 'select(.metadata.name | test("^maku-dashboard-")) | .metadata.labels.grafana_dashboard == "1"' 'monitoring.dashboards.labels empty: the sidecar label grafana_dashboard: "1"'
grep -qF 'job=\"agent-factory\"' <<<"$out" || { echo "FAIL: the overview reads the factory's job" >&2; fail=1; }
grep -qF 'ar_agent=\"system:serviceaccount:agents:run-${run}\"' <<<"$out" || { echo "FAIL: the run dashboard reads the run's subject (runs.namespace, runs.namePrefix)" >&2; fail=1; }
# the runs' subject is the gateway's: maku-gateway.runs first, as the rules read it
out="$(helm template af . -n maku "${args[@]}" "${dash[@]}" --set maku-gateway.runs.namespace=xr-runs --set maku-gateway.runs.namePrefix=agt-)"
dsub="$(yq ea -r '[select(.kind == "ConfigMap" and (.metadata.name | test("^maku-dashboard-"))) | .data[]] | join("")' - <<<"$out")"
grep -qF 'system:serviceaccount:xr-runs:agt-' <<<"$dsub" && ! grep -qF 'system:serviceaccount:agents:run-' <<<"$dsub" ||
  { echo "FAIL: the dashboards' run subject does not follow maku-gateway.runs" >&2; fail=1; }
out="$(helm template af . -n maku "${gwd[@]}" "${dash[@]}")" || { echo "FAIL: dashboards with maku-gateway do not render" >&2; fail=1; out=''; }
dashcheck "agent-factory,agent-fleet,agent-gateway,agent-identity,agent-room,agent-run,agents-overview"
grep -qF 'job=\"model-gateway\", namespace=\"maku\"' <<<"$out" || { echo "FAIL: the gateway panels read maku-gateway's job and namespace" >&2; fail=1; }
out="$(helm template af . -n maku "${args[@]}" --set monitoring.dashboards.enabled=true --set-json 'monitoring.dashboards.labels={"team":"agents"}')"
ykind ConfigMap 'select(.metadata.name | test("^maku-dashboard-")) | (.metadata.labels.team == "agents" and (.metadata.labels | has("grafana_dashboard") | not))' 'monitoring.dashboards.labels replace the sidecar label (grafana_dashboard: "1"), never merge with it'
out="$(helm template af . -n maku "${args[@]}" "${dash[@]}" --set maku.broker.publicURL=http://localhost:18080)" || { echo "FAIL: dashboards with a broker publicURL do not render" >&2; fail=1; out=''; }
grep -qF 'http://localhost:18080/r/${room}' <<<"$out" || { echo "FAIL: the room links follow maku.broker.publicURL" >&2; fail=1; }

# v4 step 6 Task 10 (D19, EX2, EX6): expose.mode, httproute, ingress or none (the default: the demo
# golden above holds it byte for byte). Each fail-closed rule names its keys.
ynone 'select(.kind == "HTTPRoute" or .kind == "Ingress") | .kind' 'expose.mode none renders a route'
exnp='[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"gateway"}}}]'
exw=("${idv[@]}" --set maku.networkPolicies.mode=kubernetes --set-json "expose.networkPolicy.from=$exnp"
     --set webProxy.enabled=true --set webProxy.existingSecret=rooms-proxy --set maku.broker.webProxy=true
     --set expose.mode=httproute --set-json 'expose.web.hostnames=["rooms.example.test"]'
     --set-json 'expose.httproute.parentRefs=[{"name":"gw","namespace":"infra","sectionName":"https"}]')
helm template af . -n maku "${exw[@]}" >/dev/null || { echo 'FAIL: a complete expose.mode httproute does not render' >&2; fail=1; }
bad 'expose.web.hostnames is empty: name the hosts the web is served at' "${exw[@]}" --set-json 'expose.web.hostnames=[]'
bad 'expose.web.parentRefs and expose.httproute.parentRefs are empty: name the Gateway (and its listener) the web route attaches to' "${exw[@]}" --set-json 'expose.httproute.parentRefs=[]'
helm template af . -n maku "${exw[@]}" --set-json 'expose.httproute.parentRefs=[]' --set-json 'expose.web.parentRefs=[{"name":"gw"}]' >/dev/null ||
  { echo 'FAIL: expose.web.parentRefs alone does not satisfy the parentRefs rule' >&2; fail=1; }
bad 'expose.web.className and expose.ingress.className are empty: name the IngressClass the web Ingress uses' "${exw[@]}" --set expose.mode=ingress
helm template af . -n maku "${exw[@]}" --set expose.mode=ingress --set expose.web.className=traefik >/dev/null ||
  { echo 'FAIL: expose.mode ingress with expose.web.className does not render' >&2; fail=1; }
bad 'expose.web: the broker serves humans on a port-forward in local mode (maku.broker.human.mode local)' "${exw[@]}" --set maku.broker.human.mode=local --set maku.broker.publicURL=http://localhost:8080 --set maku.broker.oidc.existingSecret=
bad "expose.web needs webProxy.enabled: in oidc mode the broker reads oauth2-proxy's identity headers, which only the web front door may set" "${exw[@]}" --set webProxy.enabled=false
bad 'expose.networkPolicy.from is empty: name the pods of your gateway or ingress controller' "${exw[@]}" --set-json 'expose.networkPolicy.from=[]'
bad "expose.web needs maku.networkPolicies.mode cilium or kubernetes: in mode none nothing keeps a client from the broker's :8080 around the web front door" "${exw[@]}" --set maku.networkPolicies.mode=none
bad 'expose.networkPolicy.cilium.fromEntities is empty: name the Cilium identity of your gateway or ingress controller' "${exw[@]}" --set maku.networkPolicies.mode=cilium --set-json 'expose.networkPolicy.cilium.fromEntities=[]'
bad 'expose.web needs maku.enabled: the web surface is the broker' "${exw[@]}" --set maku.enabled=false
helm template af . -n maku "${exw[@]}" --set expose.web.enabled=false >/dev/null || { echo 'FAIL: expose.web.enabled false does not render' >&2; fail=1; }
bad 'expose.slack.hostnames is empty: name the host Slack posts its events to' "${exw[@]}" -f values-slack.example.yaml --set slack.issuerURL=https://e2e-stub.e2e.svc.cluster.local --set slack.networkPolicy.mode=kubernetes --set-json "slack.networkPolicy.from=$exnp" --set-json 'expose.slack.hostnames=[]'
helm template af . -n maku "${exw[@]}" -f values-slack.example.yaml --set slack.issuerURL=https://e2e-stub.e2e.svc.cluster.local --set slack.networkPolicy.mode=kubernetes --set-json "slack.networkPolicy.from=$exnp" --set expose.slack.enabled=false >/dev/null ||
  { echo 'FAIL: slack.enabled with expose.slack.enabled false does not render' >&2; fail=1; }

# v4 step 6 Task 11 (D19, EX2, EX5): the routes. The goldens hold each mode's routes, byte for byte:
# UPDATE_GOLDEN=1 regenerates them.
routes() { yq ea 'select(.kind == "HTTPRoute" or .kind == "Ingress")' - <<<"$out"; }
golden() { # golden NAME: the render's routes equal tests/golden/NAME.yaml
  [[ ${UPDATE_GOLDEN:-} == 1 ]] && routes >"tests/golden/$1.yaml"
  routes | diff -u "tests/golden/$1.yaml" - >&2 || { echo "FAIL: the routes differ from tests/golden/$1.yaml" >&2; fail=1; }
}
exs=("${exw[@]}" -f values-slack.example.yaml --set slack.issuerURL=https://e2e-stub.e2e.svc.cluster.local --set slack.networkPolicy.mode=kubernetes --set-json "slack.networkPolicy.from=$exnp" --set-json 'expose.slack.hostnames=["factory-slack.example.test"]'
     --set-json 'expose.slack.parentRefs=[{"name":"public","namespace":"infra","sectionName":"factory-slack"}]')
out="$(helm template af . -n maku "${exs[@]}")" || { echo 'FAIL: expose httproute with both surfaces does not render' >&2; fail=1; out=''; }
golden expose-httproute
jkind HTTPRoute 'select(.metadata.name == "rooms-web") | (.spec.hostnames == ["rooms.example.test"] and (.spec.rules | length) == 1 and .spec.rules[0].matches == [{"path": {"type": "PathPrefix", "value": "/"}}] and .spec.rules[0].backendRefs == [{"name": "rooms-web-proxy", "port": 80}] and (.spec.rules[0] | has("timeouts") | not))' 'the web route: PathPrefix /, rooms-web-proxy:80, no timeouts unless set'
for h in X-Forwarded-Access-Token X-Forwarded-User X-Forwarded-Email X-Forwarded-Groups X-Forwarded-Preferred-Username \
  X-Auth-Request-User X-Auth-Request-Email X-Auth-Request-Preferred-Username X-Auth-Request-Groups X-Auth-Request-Access-Token \
  X-Forwarded-Uri X-Forwarded-Host X-Forwarded-Proto; do
  jkind HTTPRoute "select(.metadata.name == \"rooms-web\") | [.spec.rules[0].filters[] | select(.type == \"RequestHeaderModifier\") | .requestHeaderModifier.remove[] | select(. == \"$h\")] | length == 1" "the web route removes $h"
done
jkind HTTPRoute 'select(.metadata.name == "rooms-web") | ([.spec.rules[0].filters[].requestHeaderModifier.remove[] | select(test("[*]"))] | length == 0) and (.spec.rules[0].filters[0].requestHeaderModifier.remove | length) == 13' 'the web route removes exactly thirteen names, none a wildcard'
jkind HTTPRoute 'select(.metadata.name == "rooms-web") | .spec.parentRefs == [{"name": "gw", "namespace": "infra", "sectionName": "https"}]' 'the web route attaches to expose.httproute.parentRefs'
jkind HTTPRoute 'select(.metadata.name == "factory-slack-events") | (.spec.parentRefs == [{"name": "public", "namespace": "infra", "sectionName": "factory-slack"}] and .spec.hostnames == ["factory-slack.example.test"] and .spec.rules == [{"matches": [{"path": {"type": "Exact", "value": "/slack/events"}, "method": "POST"}], "backendRefs": [{"name": "factory-slack-app", "port": 8080}]}])' 'the Slack route: its own parentRefs, Exact POST /slack/events, factory-slack-app:8080, nothing else'
out="$(helm template af . -n maku "${exw[@]}" --set-json 'expose.httproute.timeouts={"request":"0s"}')"
jkind HTTPRoute 'select(.metadata.name == "rooms-web") | .spec.rules[0].timeouts == {"request": "0s"}' 'expose.httproute.timeouts reaches the web route'
ynone 'select(.kind == "HTTPRoute" and .metadata.name == "factory-slack-events") | .kind' 'the Slack surface renders without slack.enabled'
exi=("${exs[@]}" --set expose.mode=ingress --set expose.ingress.className=nginx --set expose.slack.className=public
     --set-json 'expose.ingress.annotations={"cert-manager.io/cluster-issuer":"letsencrypt","a":"common"}'
     --set-json 'expose.web.annotations={"a":"web"}'
     --set-json 'expose.web.tls=[{"secretName":"rooms-tls","hosts":["rooms.example.test"]}]')
out="$(helm template af . -n maku "${exi[@]}")" || { echo 'FAIL: expose ingress does not render' >&2; fail=1; out=''; }
golden expose-ingress
jkind Ingress 'select(.metadata.name == "rooms-web") | (.spec.ingressClassName == "nginx" and .spec.rules == [{"host": "rooms.example.test", "http": {"paths": [{"path": "/", "pathType": "Prefix", "backend": {"service": {"name": "rooms-web-proxy", "port": {"number": 80}}}}]}}] and .spec.tls == [{"secretName": "rooms-tls", "hosts": ["rooms.example.test"]}] and .metadata.annotations == {"a": "web", "cert-manager.io/cluster-issuer": "letsencrypt"})' 'the web Ingress: Prefix /, its class, the annotations merged (the surface wins), its tls passed through'
jkind Ingress 'select(.metadata.name == "factory-slack-events") | (.spec.ingressClassName == "public" and .spec.rules[0].http.paths == [{"path": "/slack/events", "pathType": "Exact", "backend": {"service": {"name": "factory-slack-app", "port": {"number": 8080}}}}] and (.spec | has("tls") | not))' 'the Slack Ingress: Exact /slack/events, its own class'
for k in Certificate Issuer ClusterIssuer; do
  ynone "select(.kind == \"$k\") | .kind" "an exposed render has a $k: TLS is the user's (EX5)"
done

# v4 step 6 Task 15 (D20, EX7, EX8): the Slack App as the opt-in subchart `slack`, wired by agreement.
# Off (the default), nothing of it renders: the demo golden above holds that byte for byte.
out="$(helm template af . -n maku "${args[@]}")"
ynone 'select(.metadata.name == "factory-slack-app") | .kind' 'slack off renders the App'
slk=("${idv[@]}" -f values-slack.example.yaml --set slack.issuerURL=https://e2e-stub.e2e.svc.cluster.local --set expose.mode=none --set slack.networkPolicy.mode=none)
out="$(helm template af . -n maku "${slk[@]}")" || { echo 'FAIL: values-slack.example.yaml does not render' >&2; fail=1; out=''; }
jkind Deployment 'select(.metadata.name == "factory-slack-app") | .spec.replicas == 1' 'slack.enabled renders the App'
jkind ConfigMap 'select(.metadata.name == "factory-slack-threads") | has("data") | not' 'and its thread map'
jkind Deployment 'select(.metadata.name == "room-broker") | [.spec.template.spec.volumes[] | select(.name == "webhook-slack-app") | .secret.secretName] == ["room-broker-webhook-slack-app"]' "the broker mounts the App's webhook key"
bad "slack.enabled needs the App's subscriber in maku.broker.webhooks: name slack-app, network cluster, url http://factory-slack-app.maku.svc.cluster.local:8081/webhooks, existingSecret room-broker-webhook-slack-app" "${slk[@]}" --set-json 'maku.broker.webhooks=[]'
bad 'maku.broker.webhooks[slack-app].url "http://factory-slack-app.maku.svc.cluster.local:8080/webhooks" must be http://factory-slack-app.maku.svc.cluster.local:8081/webhooks' "${slk[@]}" --set 'maku.broker.webhooks[0].url=http://factory-slack-app.maku.svc.cluster.local:8080/webhooks'
bad 'maku.broker.webhooks[slack-app].existingSecret "other" must equal slack.webhookSecret "room-broker-webhook-slack-app"' "${slk[@]}" --set 'maku.broker.webhooks[0].existingSecret=other'
bad 'slack.principal "slack-app" must be named by maku.broker.human.mcp.sponsored and maku-factory.config.api.sponsored: the broker lacks it' "${slk[@]}" --set-json 'maku.broker.human.mcp.sponsored=[]'
bad 'slack.principal "slack-app" must be named by maku.broker.human.mcp.sponsored and maku-factory.config.api.sponsored: the factory lacks it' "${slk[@]}" --set-json 'maku-factory.config.api.sponsored=[]'
bad 'slack.brokerURL "http://room-broker.other.svc.cluster.local:8080" must be the broker'"'"'s human listener, http://room-broker.maku.svc.cluster.local:8080' "${slk[@]}" --set slack.brokerURL=http://room-broker.other.svc.cluster.local:8080
bad 'maku.broker.webhooks[slack-app] targets factory-slack-app, but slack.enabled is false: every event would go to a subscriber that does not exist' "${slk[@]}" --set slack.enabled=false
# the example with monitoring and the Slack surface: its route, its ServiceMonitor, its rule group
out="$(helm template af . -n maku "${slk[@]}" --set monitoring.serviceMonitor.enabled=true --set monitoring.prometheusRule.enabled=true \
  --set-json 'monitoring.serviceMonitor.cilium.fromEndpoints=[{"matchLabels":{"app":"prom"}}]' --set maku.networkPolicies.mode=cilium --set slack.networkPolicy.mode=cilium \
  --set expose.mode=httproute --set expose.web.enabled=false --set-json 'expose.httproute.parentRefs=[{"name":"public"}]')" ||
  { echo 'FAIL: the example with monitoring and the Slack surface does not render' >&2; fail=1; out=''; }
jkind HTTPRoute 'select(.metadata.name == "factory-slack-events") | .spec.hostnames == ["factory-slack.example.com"]' 'the Slack surface routes to the App'
jkind ServiceMonitor 'select(.metadata.name == "factory-slack-app") | .spec.selector.matchLabels."app.kubernetes.io/name" == "factory-slack-app" and .spec.endpoints[0].port == "metrics"' "the App's ServiceMonitor"
jkind PrometheusRule '[.spec.groups[].name] | index("maku-slack") != null' 'the maku-slack rule group'

# the security review of #556: the App reads signing-key, so another key or secretFile breaks every delivery
bad 'maku.broker.webhooks[slack-app].key "other" must be empty or signing-key' "${slk[@]}" --set 'maku.broker.webhooks[0].key=other'
bad 'maku.broker.webhooks[slack-app].secretFile "/x" must be unset' "${slk[@]}" --set 'maku.broker.webhooks[0].secretFile=/x'
bad 'slack.issuerURL "https://other.example.test" must equal maku.broker.human.issuer "https://e2e-stub.e2e.svc.cluster.local"' "${slk[@]}" --set slack.issuerURL=https://other.example.test
helm template af . -n maku "${slk[@]}" --set 'maku.broker.webhooks[0].key=signing-key' --set slack.issuerURL=https://e2e-stub.e2e.svc.cluster.local/ >/dev/null ||
  { echo 'FAIL: key signing-key and the issuer with a trailing slash must render' >&2; fail=1; }
bad 'maku.broker.webhooks[short] targets factory-slack-app, but slack.enabled is false' "${slk[@]}" --set slack.enabled=false --set-json 'maku.broker.webhooks=[{"name":"short","network":"cluster","url":"http://factory-slack-app:8081/webhooks","events":["room.merged"],"secretFile":"/k","repositories":["owner/repo"],"networkPolicy":{"to":[{"matchLabels":{"a":"b"}}]}}]'
# Slack's events through expose.slack need the App's ingress from the gateway under default-deny
skx=("${exw[@]}" -f values-slack.example.yaml --set slack.issuerURL=https://e2e-stub.e2e.svc.cluster.local --set slack.networkPolicy.mode=kubernetes)
# the umbrella's check or the subchart's own, whichever renders first: both name the empty from
bad 'networkPolicy.from is empty' "${skx[@]}"
helm template af . -n maku "${skx[@]}" --set-json "slack.networkPolicy.from=$exnp" >/dev/null || { echo 'FAIL: the exposed App with its gateway peers does not render' >&2; fail=1; }
bad 'slack.networkPolicy.mode "none" must equal maku.networkPolicies.mode "kubernetes"' "${slk[@]}" --set maku.networkPolicies.mode=kubernetes --set maku.runController.enabled=true

# v4 step 6 Task 16 (D6): the factory's default-deny, off by default (the golden holds it), per mode.
ynone 'select((.kind == "NetworkPolicy" or .kind == "CiliumNetworkPolicy") and .metadata.name == "agent-factory") | .kind' 'networkPolicies.factory off renders the factory policy'
fnp=("${args[@]}" --set networkPolicies.plain=false --set networkPolicies.factory.enabled=true)
bad 'networkPolicies.factory.enabled needs maku.networkPolicies.mode cilium or kubernetes' "${fnp[@]}"
bad 'networkPolicies.factory.enabled and networkPolicies.plain both select the factory' "${fnp[@]}" --set maku.networkPolicies.mode=kubernetes --set networkPolicies.plain=true
bad 'networkPolicies.factory.meter.port is required with its to' "${fnp[@]}" --set maku.networkPolicies.mode=kubernetes --set-json 'networkPolicies.factory.meter.to=[{"podSelector":{}}]'
out="$(helm template af . -n maku "${fnp[@]}" --set maku.networkPolicies.mode=kubernetes \
  --set-json 'networkPolicies.factory.intakeFrom=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"runlore"}}}]' \
  --set-json 'networkPolicies.factory.otlp={"to":[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"obs"}}}],"port":4317}' \
  --set-json 'maku.networkPolicies.kubernetes.metricsFrom=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"monitoring"}}}]')" ||
  { echo 'FAIL: networkPolicies.factory in mode kubernetes does not render' >&2; fail=1; out=''; }
jkind NetworkPolicy 'select(.metadata.name == "agent-factory") | [.spec.ingress[] | (.ports[0].port | tostring) + ":" + (.from | tostring)] == ["8082:[{\"podSelector\":{\"matchLabels\":{\"app.kubernetes.io/name\":\"room-broker\"}}}]", "8080:[{\"namespaceSelector\":{\"matchLabels\":{\"kubernetes.io/metadata.name\":\"runlore\"}}}]", "9090:[{\"namespaceSelector\":{\"matchLabels\":{\"kubernetes.io/metadata.name\":\"monitoring\"}}}]"]' "kubernetes: the factory admits the broker on :8082, intakeFrom on :8080, the scraper on :9090, nothing else"
jkind NetworkPolicy 'select(.metadata.name == "agent-factory") | [.spec.egress[] | (.ports | map(.port | tostring) | join("+"))] == ["53+53", "443+6443", "8443", "443", "4317"] and .spec.egress[3].to[0].ipBlock.cidr == "0.0.0.0/0" and (.spec.egress[3].to[0].ipBlock.except | index("10.0.0.0/8")) != null' "kubernetes: the factory reaches DNS, the apiserver, the broker's :8443, public 443 (GitHub) and the collector, nothing else"
out="$(helm template af . -n maku "${fnp[@]}" --set maku.networkPolicies.mode=kubernetes)"
jkind NetworkPolicy 'select(.metadata.name == "agent-factory") | ([.spec.ingress[].ports[0].port] | index(8080)) == null' 'kubernetes: an empty intakeFrom closes :8080'
jkind NetworkPolicy 'select(.metadata.name == "agent-factory") | [.spec.ingress[] | select(.from[0].podSelector.matchLabels."app.kubernetes.io/name" == "preflight")] | length == 0' 'kubernetes: no preflight, no rule for it'
# step 7 PR F's finding: the install's preflight collects the factory's /preflight on :9090 (v4 D24)
out="$(helm template af . -n maku "${fnp[@]}" --set maku.networkPolicies.mode=kubernetes --set maku.preflight.enabled=true --set maku-factory.preflight.enabled=true)"
jkind NetworkPolicy 'select(.metadata.name == "agent-factory") | [.spec.ingress[] | select(.from[0].podSelector.matchLabels."app.kubernetes.io/name" == "preflight") | .ports[0].port] == [9090]' 'kubernetes: the factory admits the preflight on :9090'
out="$(helm template af . -n maku "${fnp[@]}" --set maku.networkPolicies.mode=cilium --set maku.preflight.enabled=true --set maku-factory.preflight.enabled=true)"
jkind CiliumNetworkPolicy 'select(.metadata.name == "agent-factory") | [.spec.ingress[] | select(.fromEndpoints[0].matchLabels."app.kubernetes.io/name" == "preflight") | .toPorts[0].ports[0].port] == ["9090"]' 'cilium: the factory admits the preflight on :9090'
out="$(helm template af . -n maku "${fnp[@]}" --set maku.networkPolicies.mode=cilium \
  --set-json 'networkPolicies.factory.github.to=[{"toFQDNs":[{"matchName":"ghes.example.test"}]}]')" ||
  { echo 'FAIL: networkPolicies.factory in mode cilium does not render' >&2; fail=1; out=''; }
# the review of #556: every URL the factory's config names gets its egress, or the render fails
out="$(helm template af . -n maku "${fnp[@]}" --set maku.networkPolicies.mode=cilium \
  --set maku-factory.config.api.humanIssuer=https://idp.example.test --set maku-factory.config.meter.url=https://vm.example.test:8428 \
  --set maku-factory.config.triage.classifierURL=http://classifier.ml.svc.cluster.local:8080/v1 \
  --set-json 'networkPolicies.factory.classifier.to=[{"matchLabels":{"io.kubernetes.pod.namespace":"ml","app":"classifier"}}]')" ||
  { echo 'FAIL: the factory policy with configured URLs does not render' >&2; fail=1; out=''; }
jkind CiliumNetworkPolicy 'select(.metadata.name == "agent-factory") | [.spec.egress[] | select(.toFQDNs != null) | (.toFQDNs[0].matchName + ":" + .toPorts[0].ports[0].port)] == ["github.com:443", "idp.example.test:443", "vm.example.test:8428"]' "cilium: the issuer's and the meter's hosts by name, on their ports"
jkind CiliumNetworkPolicy 'select(.metadata.name == "agent-factory") | [.spec.egress[] | select(.toEndpoints[0].matchLabels.app == "classifier") | .toPorts[0].ports[0].port] == ["8080"]' 'cilium: an in-cluster classifier through its named pods, on its port'
bad 'maku-factory.config.triage.classifierURL "http://classifier.ml.svc.cluster.local:8080/v1" is in-cluster: name its pods in networkPolicies.factory.classifier.to' "${fnp[@]}" --set maku.networkPolicies.mode=kubernetes --set maku-factory.config.triage.classifierURL=http://classifier.ml.svc.cluster.local:8080/v1
out="$(helm template af . -n maku "${fnp[@]}" --set maku.networkPolicies.mode=kubernetes --set maku-factory.config.meter.logsURL=https://logs.example.test:9428)"
jkind NetworkPolicy 'select(.metadata.name == "agent-factory") | [.spec.egress[] | select(.ports == [{"port": 9428, "protocol": "TCP"}]) | .to[0].ipBlock.cidr] == ["0.0.0.0/0"]' 'kubernetes: an external logs URL opens public addresses on its port'
out="$(helm template af . -n maku "${fnp[@]}" --set maku.networkPolicies.mode=cilium \
  --set-json 'networkPolicies.factory.github.to=[{"toFQDNs":[{"matchName":"ghes.example.test"}]}]')"
jkind CiliumNetworkPolicy 'select(.metadata.name == "agent-factory") | (.spec.ingress | length) == 2 and .spec.ingress[1].fromEntities == ["host"] and .spec.egress[3] == {"toFQDNs": [{"matchName": "ghes.example.test"}], "toPorts": [{"ports": [{"port": "443", "protocol": "TCP"}]}]} and (.spec.egress | length) == 4' 'cilium: the broker and the kubelet in; DNS, the apiserver, the broker and the GHES out'

[[ $fail == 0 ]] || exit 1
echo "agent-factory chart render checks passed"
