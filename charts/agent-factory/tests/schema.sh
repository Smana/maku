#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The umbrella's values contract: values.schema.json rejects unknown and malformed values, and the
# gate fails closed. Each negative case must fail with its message; the demo must render.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
helm dependency build . >/dev/null
digest='v0.0.0@sha256:0000'
img=(--set maku.broker.image.tag="$digest" --set maku.runController.image.tag="$digest"
     --set maku.runController.images.harness.tag="$digest" --set maku.runController.images.bridge.tag="$digest"
     --set maku-factory.image.tag="$digest")
secrets=(--set demo.model.baseURL=http://model.example.test/v1 --set demo.model.name=m1 --set demo.model.apiKey=k1
         --set demo.githubApp.id=123 --set demo.githubApp.privateKey=pem1)
demo=("${img[@]}" -f values-demo.yaml -f tests/values-demo-secret.yaml "${secrets[@]}")
fail=0

# rejects CASE WANT ARGS...: helm template fails, and its error contains WANT.
rejects() {
  local name=$1 want=$2 msg; shift 2
  if msg="$(helm template af . -n maku "$@" 2>&1 >/dev/null)"; then
    echo "FAIL: $name renders; it must fail with: $want" >&2; fail=1; return
  fi
  grep -qF -- "$want" <<<"$msg" || { echo "FAIL: $name fails without '$want' (got: $msg)" >&2; fail=1; }
}

rejects 'an unknown top-level key' "additional properties 'postgress' not allowed" "${demo[@]}" --set postgress.storage=1Gi
rejects 'profile, removed in v4 step 8' 'removed in v4 step 8: the defaults are production' "${demo[@]}" --set profile=demo
rejects 'tls.days 0' "minimum: got 0, want 1" "${demo[@]}" --set tls.days=0
rejects 'a storage that is not a quantity' "does not match pattern" "${demo[@]}" --set postgres.storage=lots
rejects 'a postgres image without a digest' "does not match pattern" "${demo[@]}" --set postgres.image=postgres:17
rejects 'networkPolicies.plain, removed in v4 step 8' "removed in v4 step 8: replaced by the default-deny policies of each component" "${demo[@]}" --set networkPolicies.plain=true
rejects 'a human issuer with a double quote (substituted into PromQL)' 'does not match pattern' "${demo[@]}" --set-string 'maku.broker.human.issuer=https://idp.example.test/"x'
rejects 'a human issuer with a backslash' 'does not match pattern' "${demo[@]}" --set-string 'maku.broker.human.issuer=https://idp.example.test/\\x'
rejects 'a runs namespace that is no DNS label' 'does not match pattern' "${demo[@]}" --set-string 'maku.runs.namespace=a"b'
rejects 'a docsURL with a double quote' 'does not match pattern' "${demo[@]}" --set-string 'monitoring.prometheusRule.docsURL=https://docs.example.test/"x'
rejects 'an unknown monitoring.serviceMonitor key' "additional properties 'enable' not allowed" "${demo[@]}" --set monitoring.serviceMonitor.enable=true
rejects 'an unknown maku-gateway key (its own strict schema)' "additional properties 'providerz' not allowed" "${demo[@]}" --set maku-gateway.enabled=true --set maku-gateway.providerz.x=1
rejects 'an unknown expose.mode' 'value must be one of' "${demo[@]}" --set expose.mode=gateway
rejects 'a hostname that is no RFC 1123 name' 'does not match pattern' "${demo[@]}" --set-json 'expose.web.hostnames=["Rooms_Example.test"]'
rejects 'a parentRef without a name' "missing property 'name'" "${demo[@]}" --set-json 'expose.httproute.parentRefs=[{"namespace":"infra"}]'
rejects 'an unknown expose.web key' "additional properties 'hostname' not allowed" "${demo[@]}" --set expose.web.hostname=x
rejects 'an unknown webProxy key' "additional properties 'enable' not allowed" "${demo[@]}" --set webProxy.enable=true
# the defaults are production (v4 step 8): the skeleton alone renders, with no component on to check
# (tests/render.sh names the seams); the demo's values without their marker are refused
helm template af . -n maku "${img[@]}" >/dev/null || { echo "FAIL: the skeleton with no component on does not render" >&2; fail=1; }
rejects 'the demo values without demoValues' 'postgres.enabled: the bundled Postgres is demo-only (D9), admitted by values-demo.yaml alone' "${demo[@]}" --set demoValues=false

# the demo renders, and so does a typed override of each key the schema constrains
helm template af . -n maku "${demo[@]}" >/dev/null || { echo "FAIL: -f values-demo.yaml does not render" >&2; fail=1; }
helm template af . -n maku "${demo[@]}" --set tls.days=30 --set postgres.storage=2Gi \
  --set postgres.storageClassName=standard >/dev/null || { echo "FAIL: valid overrides do not render" >&2; fail=1; }

# the support floor (D26): kubeVersion >=1.30 in the umbrella and both subcharts
rejects 'Kubernetes 1.29' 'kubeVersion' "${demo[@]}" --kube-version 1.29.0
helm template af . -n maku "${demo[@]}" --kube-version 1.30.0 >/dev/null || { echo "FAIL: Kubernetes 1.30 does not render" >&2; fail=1; }
for c in maku maku-factory maku-gateway agent-factory-crds; do
  msg="$(helm template x "../$c" --kube-version 1.29.0 2>&1 >/dev/null || true)"
  grep -qF 'kubeVersion' <<<"$msg" || { echo "FAIL: the $c chart does not refuse Kubernetes 1.29 (got: $msg)" >&2; fail=1; }
done

[[ $fail == 0 ]] || exit 1
echo "agent-factory schema checks passed"
