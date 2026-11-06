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
# the subcharts' own required values; the demo profile will carry them
args=(--set maku.broker.image.tag=$digest --set maku.runController.image.tag=$digest
      --set maku.runController.images.harness.tag=$digest --set maku.runController.images.bridge.tag=$digest
      --set maku-factory.image.tag=$digest
      --set maku.broker.human.mode=local --set maku.broker.roomTools.enabled=false
      --set maku.broker.runIssuer.mode=tokenreview)
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
dom="$(helm template af . -n demo "${args[@]}" --set clusterDomain=example.test --set maku.clusterDomain=example.test | yq ea 'select(.kind == "Secret" and .metadata.name == "room-broker-tls") | .data."tls.crt"' - | base64 -d | openssl x509 -noout -ext subjectAltName)"
grep -qF 'DNS:room-broker.demo.svc.example.test' <<<"$dom" || { echo "FAIL: clusterDomain does not reach the SANs" >&2; fail=1; }

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

[[ $fail == 0 ]] || exit 1
echo "agent-factory chart render checks passed"
