#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The Slack App's chart (plan v4 step 6, Task 14; D20, EX7, EX10): one replica, its Secrets by name as
# whole mounts, restricted, the config the App's strict loader accepts, a Role on the thread map alone,
# and the thread map rendered without data and kept.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
digest='v0.0.0@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'
base=(--set "image.tag=$digest" --set issuerURL=https://idp.example.test --set roomsURL=https://rooms.example.test
      --set config.teamID=T0123ABCD --set-json 'config.channels={"C0123ABCD":{"repositories":["owner/repo"],"maxCostUSD":2}}')
fail=0
out=''
jkind() {
  yq ea -o=json -I0 "select(.kind == \"$1\")" - <<<"$out" | jq -s -e "map($2) | (length > 0 and all)" >/dev/null 2>&1 || { echo "FAIL: $3" >&2; fail=1; }
}
bad() {
  local want="$1" msg; shift
  msg="$(helm template s . -n agent-system "$@" 2>&1 >/dev/null || true)"
  grep -qF -- "$want" <<<"$msg" || { echo "FAIL: the render does not fail with \"$want\" (got: $msg)" >&2; fail=1; }
}
out="$(helm template s . -n agent-system "${base[@]}")"
d='select(.metadata.name == "factory-slack-app")'
jkind Deployment "$d | .spec.replicas == 1 and .spec.strategy.type == \"Recreate\"" 'one replica (R26), recreated'
jkind Deployment "$d | ([.spec.template.spec.volumes[] | select(.secret) | {(.name): .secret}] | add) == {\"slack\": {\"secretName\": \"factory-slack-app\", \"defaultMode\": 440}, \"principal\": {\"secretName\": \"factory-slack-principal\", \"defaultMode\": 440}, \"webhook\": {\"secretName\": \"room-broker-webhook-slack-app\", \"defaultMode\": 440}}" 'whole-Secret mounts of the three Secrets, mode 0440, no items'
jkind Deployment "$d | .spec.template.spec | (.securityContext.runAsUser == 65532 and .securityContext.fsGroup == 65532 and .securityContext.runAsNonRoot and .containers[0].securityContext.readOnlyRootFilesystem and .containers[0].securityContext.allowPrivilegeEscalation == false and .containers[0].securityContext.capabilities.drop == [\"ALL\"])" 'restricted: 65532, read-only root, no capabilities'
jkind Deployment "$d | .spec.template.spec.containers[0] | (.livenessProbe.httpGet.port == \"metrics\" and .readinessProbe.httpGet.port == \"metrics\" and .resources.limits.memory != null and .resources.requests.cpu != null)" 'probes on metrics; requests and limits'
jkind Deployment "$d | .spec.template.spec.containers[0].image == \"ghcr.io/smana/maku-slack:$digest\"" 'the image by digest'
jkind Deployment "$d | .spec.template.metadata.annotations[\"checksum/config\"] | test(\"^[0-9a-f]{64}$\")" 'a config change rolls the pod'
jkind Service "$d | ([.spec.ports[] | .name + \":\" + (.port | tostring)] | join(\",\")) == \"events:8080,webhooks:8081,metrics:9090\"" 'the Service: events 8080, webhooks 8081, metrics 9090'
jkind Role "$d | .rules == [{\"apiGroups\": [\"\"], \"resources\": [\"configmaps\"], \"resourceNames\": [\"factory-slack-threads\"], \"verbs\": [\"get\", \"update\"]}]" 'the Role: get and update on factory-slack-threads alone'
jkind RoleBinding "$d | .subjects == [{\"kind\": \"ServiceAccount\", \"name\": \"factory-slack-app\", \"namespace\": \"agent-system\"}]" 'bound to the App'
jkind ConfigMap 'select(.metadata.name == "factory-slack-threads") | (has("data") | not) and .metadata.annotations["helm.sh/resource-policy"] == "keep"' 'the thread map: no data key (Helm never resets it), kept (EX10)'
cfg="$(yq 'select(.metadata.name == "factory-slack-app-config") | .data."config.yaml"' - <<<"$out")"
yq -o=json -I0 '.' <<<"$cfg" | jq -e '. == {"signingSecretFile": "/etc/factory-slack-app/slack/signing-secret", "botTokenFile": "/etc/factory-slack-app/slack/bot-token",
  "botUserIDFile": "/etc/factory-slack-app/slack/bot-user-id", "webhookKeyFile": "/etc/factory-slack-app/webhook/signing-key",
  "clientIDFile": "/etc/factory-slack-app/principal/client-id", "clientSecretFile": "/etc/factory-slack-app/principal/client-secret",
  "projectIDFile": "/etc/factory-slack-app/principal/project-id", "issuerURL": "https://idp.example.test",
  "brokerURL": "http://room-broker.agent-system.svc.cluster.local:8080", "roomsURL": "https://rooms.example.test", "namespace": "agent-system",
  "teamID": "T0123ABCD", "channels": {"C0123ABCD": {"repositories": ["owner/repo"], "maxCostUSD": 2}}}' >/dev/null ||
  { echo "FAIL: the config holds every file where its mount puts it, and the three addresses: $cfg" >&2; fail=1; }
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
printf '%s\n' "$cfg" >"$tmp/zitadel.yaml"
# generic preset: no project id, its own token request keys
out="$(helm template s . -n agent-system "${base[@]}" --set preset=generic --set audience=maku-api --set-json 'scopes=["agents"]' \
  --set brokerURL=http://room-broker.maku.svc.example.test:8080 --set clusterDomain=example.test --set config.proposals.enabled=true)"
yq 'select(.metadata.name == "factory-slack-app-config") | .data."config.yaml"' - <<<"$out" >"$tmp/generic.yaml"
yq -o=json -I0 '.' "$tmp/generic.yaml" | jq -e '(has("projectIDFile") | not) and .preset == "generic" and .audience == "maku-api" and .scopes == ["agents"] and .brokerURL == "http://room-broker.maku.svc.example.test:8080" and .proposals.enabled' >/dev/null ||
  { echo "FAIL: generic: no projectIDFile, the preset's keys, the set brokerURL, config verbatim" >&2; fail=1; }
(cd ../.. && go run ./charts/maku/tests/loadconfig slack-app "$tmp/zitadel.yaml" "$tmp/generic.yaml") || fail=1
# fail closed
bad 'image.tag is <version>@sha256:<digest>' --set image.tag=v1 "${base[@]:2}"
bad 'roomsURL is required' "${base[@]}" --set roomsURL=
bad 'issuerURL is required' "${base[@]}" --set issuerURL=
bad 'config.brokerURL: the chart derives it' "${base[@]}" --set config.brokerURL=http://x
bad 'config.signingSecretFile: the chart derives it' "${base[@]}" --set config.signingSecretFile=/x
helm template s . -n agent-system --set e2e.unpinnedImages=true --set image.tag=e2e "${base[@]:2}" >/dev/null || { echo 'FAIL: e2e.unpinnedImages admits a plain tag' >&2; fail=1; }
# the support floor (D26)
msg="$(helm template s . "${base[@]}" --kube-version 1.29.0 2>&1 >/dev/null || true)"
grep -qF kubeVersion <<<"$msg" || { echo "FAIL: Kubernetes 1.29 renders" >&2; fail=1; }

bad 'networkPolicy.from is empty: default-deny would drop Slack'"'"'s events on :8080' "${base[@]}" --set networkPolicy.mode=kubernetes
out="$(helm template s . -n agent-system "${base[@]}" --set-json 'extraEnv=[{"name":"SSL_CERT_FILE","value":"/ca/ca.crt"}]' \
  --set-json 'extraVolumes=[{"name":"ca","secret":{"secretName":"idp-ca"}}]' --set-json 'extraVolumeMounts=[{"name":"ca","mountPath":"/ca"}]')"
jkind Deployment "$d | .spec.template.spec.containers[0].env == [{\"name\": \"SSL_CERT_FILE\", \"value\": \"/ca/ca.crt\"}] and (.spec.template.spec.volumes | map(.name) | index(\"ca\")) != null and (.spec.template.spec.containers[0].volumeMounts | map(.name) | index(\"ca\")) != null" 'extraEnv, extraVolumes and extraVolumeMounts reach the App'
# Task 16 (D6): the App's default-deny, per mode, exactly the flows docs/slack.md lists. Off (none), none.
out="$(helm template s . -n agent-system "${base[@]}")"
[[ -z $(yq 'select(.kind == "NetworkPolicy" or .kind == "CiliumNetworkPolicy") | .kind' - <<<"$out") ]] || { echo 'FAIL: mode none renders a policy' >&2; fail=1; }
out="$(helm template s . -n agent-system "${base[@]}" --set networkPolicy.mode=kubernetes \
  --set-json 'networkPolicy.from=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"gateway"}}}]' \
  --set-json 'networkPolicy.metricsFrom=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"monitoring"}}}]')"
jkind NetworkPolicy "$d | .spec.ingress == [{\"from\": [{\"namespaceSelector\": {\"matchLabels\": {\"kubernetes.io/metadata.name\": \"gateway\"}}}], \"ports\": [{\"port\": 8080, \"protocol\": \"TCP\"}]}, {\"from\": [{\"podSelector\": {\"matchLabels\": {\"app.kubernetes.io/name\": \"room-broker\"}}}], \"ports\": [{\"port\": 8081, \"protocol\": \"TCP\"}]}, {\"from\": [{\"namespaceSelector\": {\"matchLabels\": {\"kubernetes.io/metadata.name\": \"monitoring\"}}}], \"ports\": [{\"port\": 9090, \"protocol\": \"TCP\"}]}]" 'kubernetes ingress: events from the gateway, webhooks from the broker, metrics from the scraper, nothing else'
jkind NetworkPolicy "$d | [.spec.egress[] | (.ports | map(.port | tostring) | join(\"+\"))] == [\"53+53\", \"443+6443\", \"8080\", \"443\"] and .spec.egress[2].to == [{\"podSelector\": {\"matchLabels\": {\"app.kubernetes.io/name\": \"room-broker\"}}}] and .spec.egress[3].to[0].ipBlock.cidr == \"0.0.0.0/0\" and (.spec.egress[3].to[0].ipBlock.except | index(\"10.0.0.0/8\")) != null" "kubernetes egress: DNS, the apiserver, the broker's :8080, public 443 (Slack and an https IdP)"
out="$(helm template s . -n agent-system "${base[@]}" --set networkPolicy.mode=cilium --set-json 'networkPolicy.cilium.metricsFromEndpoints=[{"matchLabels":{"app":"prom"}}]')"
jkind CiliumNetworkPolicy "$d | [.spec.ingress[] | (.fromEntities // .fromEndpoints | tostring) + \":\" + .toPorts[0].ports[0].port] == [\"[\\\"ingress\\\"]:8080\", \"[{\\\"matchLabels\\\":{\\\"io.kubernetes.pod.namespace\\\":\\\"agent-system\\\",\\\"app.kubernetes.io/name\\\":\\\"room-broker\\\"}}]:8081\", \"[\\\"host\\\"]:9090\", \"[{\\\"matchLabels\\\":{\\\"app\\\":\\\"prom\\\"}}]:9090\"]" 'cilium ingress: the ingress entity on 8080, the broker on 8081, the kubelet and the scraper on 9090'
jkind CiliumNetworkPolicy "$d | (.spec.egress[3] == {\"toFQDNs\": [{\"matchName\": \"slack.com\"}], \"toPorts\": [{\"ports\": [{\"port\": \"443\", \"protocol\": \"TCP\"}]}]}) and (.spec.egress[4].toFQDNs == [{\"matchName\": \"idp.example.test\"}]) and (.spec.egress | length) == 5" "cilium egress: DNS, the apiserver, the broker, slack.com and the issuer's host"
[[ $fail == 0 ]] || exit 1
echo "maku-slack render checks passed"
