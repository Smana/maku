#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The PrometheusRule under promtool (v4 step 6, Tasks 6 and 22). Renders it with production-shaped
# values, so every group and every rule is in (an issuer, networkPolicies.mode kubernetes,
# kube-state-metrics, slack.enabled, maku-gateway with its scraper admitted, a priced catalog and a
# meter, a knowledge endpoint), extracts .spec, runs `promtool check rules`, then `promtool test rules` on tests/monitoring/*.test.yaml. Those read
# rules.yaml, the rule with its annotations stripped: promtool compares annotations exactly, and only
# annotations.test.yaml (rules-annotated.yaml) owns them. Every alert must be named by at least one test.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
command -v promtool >/dev/null || { echo "FAIL: promtool is not installed (mise install)" >&2; exit 1; }
helm dependency build . >/dev/null
digest='v0.0.0@sha256:0000'
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
helm template af . -n maku \
  --set maku.broker.image.tag=$digest --set maku.runController.image.tag=$digest \
  --set maku.runController.images.harness.tag=$digest --set maku.runController.images.bridge.tag=$digest \
  --set maku-factory.image.tag=$digest -f values-demo.yaml -f values-demo-gateway.yaml --set demo.model.apiKey=k1 \
  --set maku-gateway.identity.issuer=https://kubernetes.default.svc.cluster.local --set maku-gateway.providers.demo.host=api.example.test \
  -f tests/metering-values.yaml \
  --set maku-gateway.aliases.public.agent-default.model=echo-default --set maku-gateway.aliases.public.tier-light.model=echo-light \
  --set-json 'maku-gateway.metrics.allowFrom=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"monitoring"}}}]' \
  --set demo.githubApp.id=123 --set demo.githubApp.privateKey=pem1 \
  --set maku.broker.human.mode=oidc --set maku.broker.human.issuer=https://idp.example.test \
  --set maku.networkPolicies.mode=kubernetes \
  --set maku.broker.roomTools.knowledge.url=http://runlore.runlore.svc.cluster.local:8080/mcp \
  --set-json 'maku.broker.roomTools.knowledge.networkPolicy.to=[{"podSelector":{"matchLabels":{"app.kubernetes.io/name":"runlore"}}}]' \
  --set-json 'maku.networkPolicies.kubernetes.metricsFrom=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"monitoring"}}}]' \
  --set monitoring.serviceMonitor.enabled=true --set monitoring.prometheusRule.enabled=true \
  --set monitoring.prometheusRule.kubeStateMetrics=true --set monitoring.prometheusRule.docsURL=https://docs.example.test \
  -f values-slack.example.yaml --set expose.mode=none --set slack.networkPolicy.mode=kubernetes --set slack.issuerURL=https://idp.example.test \
  --set-json 'slack.networkPolicy.from=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"gateway"}}}]' |
  yq ea 'select(.kind == "PrometheusRule" and .metadata.name == "maku") | .spec' - >"$tmp/rules-annotated.yaml"
fail=0
groups="$(yq '[.groups[].name] | join(" ")' "$tmp/rules-annotated.yaml")"
[[ $groups == "maku-rooms maku-factory maku-run-path maku-slack maku-gateway" ]] || { echo "FAIL: the production-shaped groups are '$groups'" >&2; fail=1; }
# every rule the files hold renders with these values
want="$(yq ea '[.rules[] | (.alert // .record)] | sort | join(" ")' files/rules/*.yaml)"
got="$(yq '[.groups[].rules[] | (.alert // .record)] | sort | join(" ")' "$tmp/rules-annotated.yaml")"
[[ $got == "$want" ]] || { echo "FAIL: the production-shaped render drops rules: got '$got', want '$want'" >&2; fail=1; }
promtool check rules "$tmp/rules-annotated.yaml" >/dev/null || { echo "FAIL: promtool check rules" >&2; fail=1; }
yq 'del(.groups[].rules[].annotations)' "$tmp/rules-annotated.yaml" >"$tmp/rules.yaml"
for a in $(yq '.groups[].rules[] | .alert // ""' "$tmp/rules.yaml"); do
  grep -qE "alertname: $a\b" tests/monitoring/*.test.yaml || { echo "FAIL: no promtool test evaluates $a" >&2; fail=1; }
done
cp tests/monitoring/*.test.yaml "$tmp/"
(cd "$tmp" && promtool test rules ./*.test.yaml) || { echo "FAIL: promtool test rules" >&2; fail=1; }
[[ $fail == 0 ]] || exit 1
echo "agent-factory monitoring checks passed"
