#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The dashboards' queries under promtool (v4 step 6, Task 8). Renders every dashboard (maku-gateway on,
# so agent-gateway is in), then dashboards.py:
#   1. substitutes Grafana's variables in every panel expr ($__rate_interval, $__interval and $__range
#      -> 5m; each templating variable -> .*), refuses ${datasource} in an expr and any $ left after
#      it (a label_replace's $1 aside), and writes each expr as a recording rule, so
#      `promtool check rules` parses every one of them;
#   2. evaluates the queries cloud-native-ref's test-agent-dashboard-queries.sh,
#      test-agent-identity-queries.sh and test-agent-identity-dashboard.py covered, on their synthetic
#      series under Maku's names, with `promtool test rules`;
#   3. holds the identity page's rules: one principal at a time, never ranked.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
command -v promtool >/dev/null || { echo "FAIL: promtool is not installed (mise install)" >&2; exit 1; }
python3 -I -c 'import yaml' 2>/dev/null || { echo "FAIL: dashboards.py needs PyYAML (python3 -m pip install pyyaml)" >&2; exit 1; }
helm dependency build . >/dev/null
digest='v0.0.0@sha256:0000'
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
helm template af . -n maku \
  --set maku.broker.image.tag=$digest --set maku.runController.image.tag=$digest \
  --set maku.runController.images.harness.tag=$digest --set maku.runController.images.bridge.tag=$digest \
  --set maku-factory.image.tag=$digest -f values-demo.yaml -f values-demo-gateway.yaml \
  --set demo.model.apiKey=k1 --set demo.githubApp.id=123 --set demo.githubApp.privateKey=pem1 \
  --set maku-gateway.identity.issuer=https://kubernetes.default.svc.cluster.local --set maku-gateway.providers.demo.host=model.example.test \
  --set maku-gateway.aliases.public.agent-default.model=m1 --set maku-gateway.aliases.public.tier-light.model=m2 \
  --set monitoring.dashboards.enabled=true >"$tmp/render.yaml"
mkdir "$tmp/dashboards"
for uid in $(yq ea -r '[select(.kind == "ConfigMap" and (.metadata.name | test("^maku-dashboard-"))) | .metadata.name | sub("^maku-dashboard-"; "")] | .[]' "$tmp/render.yaml"); do
  yq ea -r "select(.kind == \"ConfigMap\" and .metadata.name == \"maku-dashboard-$uid\") | .data.\"$uid.json\"" "$tmp/render.yaml" >"$tmp/dashboards/$uid.json"
done
python3 -I tests/monitoring/dashboards.py "$tmp/dashboards" "$tmp"
promtool check rules "$tmp/exprs.yaml" >/dev/null || { promtool check rules "$tmp/exprs.yaml" >&2; echo "FAIL: promtool does not parse every dashboard query" >&2; exit 1; }
(cd "$tmp" && promtool test rules queries.test.yaml) || { echo "FAIL: promtool test rules on the dashboards' queries" >&2; exit 1; }
echo "agent-factory dashboard query checks passed"
