#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Several installs in one cluster (D2, step 7 R10): two releases of the same name, in two namespaces
# with two runs namespaces, render no cluster-scoped object of the same name, and every cluster-scoped
# object they render carries its namespace. The aggregated ClusterRoles are agent-factory-crds' alone,
# installed once per cluster. The RuntimeClass (maku.runController.createRuntimeClass) is the
# cluster's, created by one install at most, and so is agentgateway's controller: both stay off here.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
helm dependency build . >/dev/null
d='v0.0.0@sha256:0000'
img=(--set maku.broker.image.tag=$d --set maku.runController.image.tag=$d --set maku.runController.images.harness.tag=$d
  --set maku.runController.images.bridge.tag=$d --set maku-factory.image.tag=$d
  --set slack.image.tag=v0.0.0@sha256:0000000000000000000000000000000000000000000000000000000000000000)
fail=0
cluster='^(Namespace|ClusterRole|ClusterRoleBinding|ValidatingAdmissionPolicy|ValidatingAdmissionPolicyBinding|MutatingAdmissionPolicy|MutatingAdmissionPolicyBinding|ValidatingWebhookConfiguration|MutatingWebhookConfiguration|RuntimeClass|CustomResourceDefinition|PriorityClass|GatewayClass|IngressClass|StorageClass|APIService)$'
# objects NS SET: the cluster-scoped kind/name of one install in namespace NS, runs namespace runs-NS.
objects() {
  local ns=$1 set=$2 per=(--set maku.runs.namespace="runs-$1" --set maku-gateway.runs.namespace="runs-$1"
    --set maku-factory.config.runs.namespace="runs-$1" --set-json "maku.agentRunPolicies.creators=[\"system:serviceaccount:$1:agent-factory\"]")
  case $set in
    cnref)
      helm template af . -n "$ns" "${img[@]}" -f tests/values-cnref-like.yaml "${per[@]}" --set maku-factory.monitor.flux.enabled=true \
        --set "maku.broker.factoryURL=http://agent-factory.$ns.svc.cluster.local:8082" \
        --set-json "maku.broker.systemPrincipals={\"system:serviceaccount:$ns:agent-factory\":\"system:factory\"}" \
        --set "maku.broker.webhooks[0].url=http://factory-slack-app.$ns.svc.cluster.local:8081/webhooks" \
        --set maku.preflight.enabled=true --set maku-factory.preflight.enabled=true --set maku.preflight.image.tag=$d ;;
    demo)
      helm template af . -n "$ns" "${img[@]}" -f values-demo.yaml "${per[@]}" --set postgres.enabled=false --set agentgateway.enabled=false \
        --set demo.model.apiKey=k --set demo.githubApp.id=1 --set demo.githubApp.privateKey=p --set maku-factory.monitor.flux.enabled=true \
        --set maku-gateway.identity.issuer=https://kubernetes.default.svc.cluster.local --set maku-gateway.providers.demo.host=m.example.test \
        --set maku-gateway.aliases.public.agent-default.model=m --set maku-gateway.aliases.public.tier-light.model=m ;;
  esac | yq e "select(.kind != null and (.kind | test(\"$cluster\"))) | .kind + \"/\" + .metadata.name" - | grep -v '^---$' | sort -u
}
for set in cnref demo; do
  a="$(objects team-a "$set")" || { echo "FAIL: $set: the team-a render failed" >&2; fail=1; continue; }
  b="$(objects team-b "$set")" || { echo "FAIL: $set: the team-b render failed" >&2; fail=1; continue; }
  [[ -n $a ]] || { echo "FAIL: $set renders no cluster-scoped object: the test reads nothing" >&2; fail=1; }
  both="$(comm -12 <(echo "$a") <(echo "$b"))"
  [[ -z $both ]] || { echo "FAIL: $set: two installs collide on: $(tr '\n' ' ' <<<"$both")" >&2; fail=1; }
  fixed="$(grep -v -- 'team-a' <<<"$a" || true)"
  [[ -z $fixed ]] || { echo "FAIL: $set: cluster-scoped objects without the install's namespace: $(tr '\n' ' ' <<<"$fixed")" >&2; fail=1; }
done
# The aggregated roles are the CRD chart's, under their names, kept like the CRDs.
roles="$(helm template af-crds ../agent-factory-crds | yq e 'select(.kind == "ClusterRole") | .metadata.name + " " + (.metadata.annotations."helm.sh/resource-policy" // "")' - | grep -v '^---$' | sort | tr '\n' ',')"
[[ $roles == "agent-factory-flux-reader keep,maku-factory-admin keep,maku-mergepolicy-editor keep,maku-viewer keep," ]] ||
  { echo "FAIL: agent-factory-crds' ClusterRoles are '$roles'" >&2; fail=1; }
[[ $fail == 0 ]] || exit 1
echo "agent-factory two-install checks passed"
