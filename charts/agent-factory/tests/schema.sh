#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The umbrella's values contract: values.schema.json rejects unknown and malformed values, and the
# profile gate fails closed. Each negative case must fail with its message; the demo must render.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
helm dependency build . >/dev/null
digest='v0.0.0@sha256:0000'
img=(--set maku.broker.image.tag=$digest --set maku.runController.image.tag=$digest
     --set maku.runController.images.harness.tag=$digest --set maku.runController.images.bridge.tag=$digest
     --set maku-factory.image.tag=$digest)
secrets=(--set demo.model.baseURL=http://model.example.test/v1 --set demo.model.name=m1 --set demo.model.apiKey=k1
         --set demo.githubApp.id=123 --set demo.githubApp.privateKey=pem1)
demo=("${img[@]}" -f values-demo.yaml "${secrets[@]}")
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
rejects 'an unknown profile' "value must be one of" "${demo[@]}" --set profile=prod
rejects 'tls.days 0' "minimum: got 0, want 1" "${demo[@]}" --set tls.days=0
rejects 'a storage that is not a quantity' "does not match pattern" "${demo[@]}" --set postgres.storage=lots
rejects 'a postgres image without a digest' "does not match pattern" "${demo[@]}" --set postgres.image=postgres:17
rejects 'networkPolicies.plain as a string' "got string, want boolean" "${demo[@]}" --set-string networkPolicies.plain=yes
rejects 'profile production' 'profile production: its seams arrive in rollout steps 2 to 6 (docs/superpowers/specs/2026-11-12-v4-production-path-design.md); use -f values-demo.yaml for the demo' "${img[@]}" --set profile=production
rejects 'an unset profile' 'profile is required: demo (-f values-demo.yaml), or production (rollout steps 2 to 6)' "${img[@]}"
rejects '--set profile=demo alone' 'profile demo needs -f values-demo.yaml' "${img[@]}" --set profile=demo "${secrets[@]}"

# the demo renders, and so does a typed override of each key the schema constrains
helm template af . -n maku "${demo[@]}" >/dev/null || { echo "FAIL: -f values-demo.yaml does not render" >&2; fail=1; }
helm template af . -n maku "${demo[@]}" --set tls.days=30 --set postgres.storage=2Gi --set networkPolicies.plain=false \
  --set postgres.storageClassName=standard >/dev/null || { echo "FAIL: valid overrides do not render" >&2; fail=1; }

# the support floor (D26): kubeVersion >=1.30 in the umbrella and both subcharts
rejects 'Kubernetes 1.29' 'kubeVersion' "${demo[@]}" --kube-version 1.29.0
helm template af . -n maku "${demo[@]}" --kube-version 1.30.0 >/dev/null || { echo "FAIL: Kubernetes 1.30 does not render" >&2; fail=1; }
for c in maku maku-factory agent-factory-crds; do
  msg="$(helm template x "../$c" --kube-version 1.29.0 2>&1 >/dev/null || true)"
  grep -qF 'kubeVersion' <<<"$msg" || { echo "FAIL: the $c chart does not refuse Kubernetes 1.29 (got: $msg)" >&2; fail=1; }
done

[[ $fail == 0 ]] || exit 1
echo "agent-factory schema checks passed"
