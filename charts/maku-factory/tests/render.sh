#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Renders the chart and asserts what the platform relies on: the ServiceAccount name SP2's
# broker CNP and allowlist expect, restricted security, the probes, the rooms-system token, the
# broker CA (ruling SC), the config rendered where FACTORY_CONFIG points, an image by digest.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
digest='v0.0.0@sha256:0000'
out="$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" \
  --set config.repository=Smana/cloud-native-ref)"
fail=0
check() { grep -qF -- "$1" <<<"$out" || { echo "FAIL: $2" >&2; fail=1; }; }
# yq assertion on one rendered kind: ykind KIND 'expr' 'message'; expr must yield at least one
# value, and every value it yields, across every document of that kind, must be true.
ykind() {
  yq ea -e "[select(.kind == \"$1\") | $2] | (length > 0 and all)" - <<<"$out" >/dev/null 2>&1 || { echo "FAIL: $3" >&2; fail=1; }
}
# yq query that must succeed and print nothing: ynone 'expr' 'message'.
ynone() {
  local got
  got=$(yq "$1" - <<<"$out") || { echo "FAIL: yq errored: $2" >&2; fail=1; return; }
  [ -z "$got" ] || { echo "FAIL: $2" >&2; fail=1; }
}
pod='.spec.template.spec'
ctr="$pod.containers[0]"
# Parenthesised so a path appended after the helper applies to the selected item only.
vol() { echo "($pod.volumes[] | select(.name == \"$1\"))"; }
mnt() { echo "($ctr.volumeMounts[] | select(.name == \"$1\"))"; }
ykind Deployment "$pod.serviceAccountName == \"agent-factory\"" "the factory's ServiceAccount is agent-factory"
ykind Deployment '.spec.template.metadata.labels."app.kubernetes.io/name" == "agent-factory"' "the pod label SP2's broker CNP admits"
ykind Deployment "$(vol rooms-token).projected.sources[0].serviceAccountToken.audience == \"rooms-system\"" "the broker's system audience (SP2 P3)"
ykind Deployment "$(mnt rooms-token).mountPath == \"/var/run/secrets/agents/rooms\"" "the rooms token where config.broker.tokenFile reads it"
ykind Deployment "$(vol broker-ca).secret.secretName == \"openbao-ca\"" "the broker's CA (ruling SC)"
ykind Deployment "$(mnt broker-ca).mountPath == \"/etc/agent-factory/openbao-ca\"" "the CA where config.broker.caFile reads it"
ykind Deployment "$(vol oidc).secret.secretName == \"agent-factory-oidc\"" "the ZITADEL client ids the API reads tokens from"
ykind Deployment "$(mnt oidc).mountPath == \"/etc/agent-factory-oidc\"" "the client ids where config.api.clientIDFiles reads them"
ykind Deployment "$ctr.env[] | select(.name == \"FACTORY_CONFIG\") | .value == \"/etc/agent-factory/config/config.yaml\"" "FACTORY_CONFIG names the rendered config"
ykind Deployment "$(mnt config).mountPath == \"/etc/agent-factory/config\"" "the config mounted where FACTORY_CONFIG points"
check "name: api" "the run-request API port"
api_port="$(yq 'select(.kind == "Service") | .spec.ports[] | select(.name == "api") | .port' <<<"$out")"
intake_port="$(yq 'select(.kind == "Service") | .spec.ports[] | select(.name == "intake") | .port' <<<"$out")"
[ "$api_port" = 8082 ] || { echo "FAIL: the run-request API Service port is 8082, got '$api_port'" >&2; fail=1; }
[ "$api_port" != "$intake_port" ] || { echo "FAIL: the api and intake ports must differ" >&2; fail=1; }
check "containerPort: 8082" "the run-request API container port"
check "name: intake" "the RunLore intake port (FA-8)"
ykind Deployment "$(vol intake).secret.secretName == \"agent-factory-intake\"" "the intake bearer token Secret (FA-8)"
ykind Deployment "$(mnt intake).mountPath == \"/etc/agent-factory-intake\"" "the intake token where config.runlore.tokenFile reads it"
ykind Deployment "$(vol zitadel-reader).secret.secretName == \"agent-factory-zitadel-reader\"" "the ZITADEL reader D4 resolves callers with"
ykind Deployment "$(vol zitadel-reader).secret.optional == true" "an absent reader refuses human runs, never blocks the pod"
ykind Deployment "$(mnt zitadel-reader).mountPath == \"/etc/agent-factory-zitadel-reader\"" "the reader where config.api.access.readerFile reads it"
ykind Deployment "$(vol sponsored).secret.secretName == \"agent-factory-sponsored\"" "the sponsored principals' client ids and project id (F4)"
ykind Deployment "$(vol sponsored).secret.optional == true" "an absent Secret refuses sponsored tokens, never blocks the pod"
ykind Deployment "$(vol sponsored).secret.defaultMode == \"0440\"" "0440, readable through fsGroup"
ykind Deployment "$(mnt sponsored).mountPath == \"/etc/agent-factory-sponsored\"" "the files config.api.sponsored[].clientIDFile and api.projectIDFile read"
ykind ConfigMap '.data."config.yaml" | from_yaml | .repository == "Smana/cloud-native-ref"' "the config is rendered verbatim"
base="$out"
# The rollout lives under config passthrough, never a parallel top-level chart value.
for mode in shadow enforce; do
  out="$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref \
    --set config.intake.overlap.mode="$mode" --set config.intake.overlap.scoperEnforcement=true)"
  got="$(yq ea 'select(.kind == "ConfigMap") | .data."config.yaml" | from_yaml | .intake.overlap.mode' - <<<"$out")"
  [ "$got" = "$mode" ] || { echo "FAIL: config.intake.overlap.mode=$mode rendered as '$got'" >&2; fail=1; }
  ykind ConfigMap '.data."config.yaml" | from_yaml | .intake.overlap.scoperEnforcement == true' "config.intake.overlap.scoperEnforcement passthrough"
done
out="$base"
ykind Deployment "$ctr.image == \"ghcr.io/smana/maku-factory:$digest\"" "the image by digest"
ykind Deployment "$ctr.securityContext.readOnlyRootFilesystem == true" "restricted securityContext"
ykind Deployment "$pod.securityContext.seccompProfile.type == \"RuntimeDefault\"" "seccomp RuntimeDefault"
ykind Deployment "($ctr.securityContext.capabilities.drop | join(\",\")) == \"ALL\"" "every capability dropped"
ykind Deployment "$ctr.startupProbe.httpGet.path == \"/startupz\"" "startup probe"
ykind Deployment "$ctr.livenessProbe.httpGet.path == \"/healthz\"" "liveness probe"
ykind Deployment "$ctr.readinessProbe.httpGet.path == \"/readyz\"" "readiness probe"
ykind Deployment "($ctr.resources.limits.cpu != null and $ctr.resources.limits.memory != null)" "resource limits"
ykind Deployment "($ctr.resources.requests.cpu != null and $ctr.resources.requests.memory != null)" "resource requests"
ykind PodDisruptionBudget '.spec.minAvailable == 1' "PDB"
ykind PodDisruptionBudget '.spec.unhealthyPodEvictionPolicy == "AlwaysAllow"' "a drain evicts unready pods (#58)"
ykind Deployment "$(vol merger).secret | (.secretName == \"agent-factory-merger\" and .defaultMode == \"0440\" and .optional == true)" "the merger key is optional (#57)"
ykind Deployment "$(vol intake).secret | (.secretName == \"agent-factory-intake\" and .defaultMode == \"0440\" and .optional == true)" "the intake token is optional (#57)"
ykind Role '.rules[] | select(.resources[] == "agentruns") | has("verbs")' "AgentRun RBAC"
# H-5: the AgentRun Role and its binding sit where the binary looks (config.runs, runwatch.Kind);
# unset, runwatch.DefaultKind's: the run-controller's group in agents (v4 step 8, D5).
ykind Role 'select(.metadata.namespace == "agents") | .rules[] | select(.resources[] == "agentruns") | .apiGroups[0] == "maku.ogenki.io"' "default AgentRun Role: maku.ogenki.io in agents"
ykind RoleBinding 'select(.metadata.namespace == "agents") | (.roleRef.name == "agent-factory" and .subjects[0].namespace == "agent-system")' "default AgentRun RoleBinding in agents"
# Review C1: a 0400 root-owned key is unreadable to uid 65532; the group bit plus fsGroup is not.
ykind Deployment "$pod.securityContext.fsGroup == 65532" "the pod's fsGroup owns the mounted Secrets"
ykind Deployment "$(vol github).secret | (.secretName == \"agent-factory-github\" and .defaultMode == \"0440\")" "the App key readable by the group only"
ykind Deployment "$(mnt merger).mountPath == \"/etc/agent-factory-merger\"" "the merger key (R16) where config.github.merger*File reads it"
# Review M4: exactly the verbs the code uses.
ykind Role '.rules[] | select(.resources[] == "tasks") | ((.verbs | sort | join(",")) == "create,get,list,patch,update,watch")' "tasks without delete"
ykind Role '.rules[] | select(.resources[] == "leases") | ((.verbs | sort | join(",")) == "create,get,update")' "leases: what leader election calls"
# ConfigMaps: one rule, bounded by the Role's namespace (the release's own, so no metadata.namespace
# in the render). A resourceNames rule would be subsumed by the ledger rule's wider one.
role=$(yq eval-all 'select(.kind == "Role" and .metadata.namespace == null)' - <<<"$out")
n=$(yq '[.rules[] | select(.resources[] == "configmaps")] | length' - <<<"$role")
[ "$n" = 1 ] || { echo "FAIL: want one configmaps rule, got $n" >&2; fail=1; }
yq -e '.rules[] | select(.resources[] == "configmaps") | ((has("resourceNames") | not) and ((.verbs | sort | join(",")) == "create,delete,get,list,update,watch"))' - <<<"$role" >/dev/null \
  || { echo "FAIL: the configmaps rule: no resourceNames, verbs = stop object's watch + R50 ledgers" >&2; fail=1; }
ynone 'select(.kind == "Role" or .kind == "ClusterRole") | .rules[] | select(.resources[] == "secrets")' "the factory never reads Secrets through the API"
# The Flux reader (MC7, R34) renders only when enabled: no ClusterRole by default, then exactly
# get on gitrepositories and list on kustomizations and helmreleases, no watch.
ynone 'select(.kind == "ClusterRole" or .kind == "ClusterRoleBinding") | select(.metadata.name == "agent-factory-flux-reader")' "no Flux cluster-scoped RBAC unless monitor.flux.enabled"
flux=$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref --set monitor.flux.enabled=true)
cr=$(yq 'select(.kind == "ClusterRole" and .metadata.name == "agent-factory-flux-reader")' - <<<"$flux")
[ "$(yq '.rules | length' - <<<"$cr")" = 3 ] || { echo "FAIL: the Flux ClusterRole has exactly three rules" >&2; fail=1; }
for rule in "source.toolkit.fluxcd.io gitrepositories get" "kustomize.toolkit.fluxcd.io kustomizations list" "helm.toolkit.fluxcd.io helmreleases list"; do
  set -- $rule
  yq -e ".rules[] | select(.apiGroups[0] == \"$1\" and (.resources | join(\",\")) == \"$2\" and (.verbs | sort | join(\",\")) == \"$3\")" - <<<"$cr" >/dev/null \
    || { echo "FAIL: Flux ClusterRole rule $2 wants exactly verbs $3" >&2; fail=1; }
done
yq -e 'select(.kind == "ClusterRoleBinding") | (.roleRef.name == "agent-factory-flux-reader" and .subjects[0].name == "agent-factory" and .subjects[0].namespace == "agent-system")' - <<<"$flux" >/dev/null \
  || { echo "FAIL: the Flux ClusterRoleBinding binds the factory's ServiceAccount" >&2; fail=1; }
# defaultMode renders as 0400 or 0440 (yq keeps the octal spelling); only the group-readable one is allowed.
ynone 'select(.kind == "Deployment") | .spec.template.spec.volumes[].secret.defaultMode | select(. == "0400" or . == "400")' "a 0400 Secret is root-only: the factory runs as 65532"
# S-1, D6: TLS on the run-request API is off by default: no Certificate, no pair, no config.api.tls
ynone 'select(.kind == "Certificate")' "no Certificate unless api.tls.issuerRef.name"
ynone 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.name == "api-tls")' "no API pair unless api.tls.secretName"
ynone 'select(.kind == "ConfigMap") | .data."config.yaml" | from_yaml | .api.tls | select(. != null)' "plaintext by default: no config.api.tls"
# api.tls on: the pair mounted, config.api.tls pointed at it, the Certificate, the port named https
# Factory resources D7: the factory reads the three kinds and writes their status, nothing more.
ykind Role '.rules[] | select((.resources | join(",")) == "factories,repositories,mergepolicies") | (.verbs | join(",")) == "get,list,watch"' "the factory reads its resources"
ykind Role '.rules[] | select((.resources | join(",")) == "factories/status,repositories/status,mergepolicies/status") | (.verbs | join(",")) == "get,update,patch"' "the factory writes their status"
ykind ClusterRole 'select(.metadata.name == "maku-viewer") | .metadata.labels."rbac.authorization.k8s.io/aggregate-to-view" == "true"' "maku-viewer aggregates into view"
ykind ClusterRole 'select(.metadata.name == "maku-mergepolicy-editor") | .metadata.labels."rbac.authorization.k8s.io/aggregate-to-edit" == "true"' "maku-mergepolicy-editor aggregates into edit"
ykind ClusterRole 'select(.metadata.name == "maku-factory-admin") | .metadata.labels."rbac.authorization.k8s.io/aggregate-to-admin" == "true"' "maku-factory-admin aggregates into admin"
# Each role's exact rules: viewer reads, the editor writes only merge policies, admin writes all three.
ykind ClusterRole 'select(.metadata.name == "maku-viewer") | ((.rules | length) == 1 and (.rules[0].apiGroups | join(",")) == "maku.ogenki.io" and (.rules[0].resources | join(",")) == "factories,repositories,mergepolicies" and (.rules[0].verbs | join(",")) == "get,list,watch")' "maku-viewer is read-only on the three kinds"
ykind ClusterRole 'select(.metadata.name == "maku-mergepolicy-editor") | ((.rules | length) == 1 and (.rules[0].apiGroups | join(",")) == "maku.ogenki.io" and (.rules[0].resources | join(",")) == "mergepolicies" and (.rules[0].verbs | join(",")) == "get,list,watch,create,update,patch,delete")' "maku-mergepolicy-editor covers only mergepolicies"
ykind ClusterRole 'select(.metadata.name == "maku-factory-admin") | ((.rules | length) == 1 and (.rules[0].apiGroups | join(",")) == "maku.ogenki.io" and (.rules[0].resources | join(",")) == "factories,repositories,mergepolicies" and (.rules[0].verbs | join(",")) == "get,list,watch,create,update,patch,delete")' "maku-factory-admin covers the three kinds"
# v4 step 8: the chart ships no CRD, in templates or crds/: agent-factory-crds alone carries them.
crds="$(helm template agent-factory . --set image.tag='v0.0.0@sha256:0000' --include-crds | yq ea 'select(.kind == "CustomResourceDefinition") | .metadata.name' -)"
[[ -z $crds && ! -e crds ]] || { echo "FAIL: the chart ships CRDs ($crds): agent-factory-crds alone carries them" >&2; fail=1; }
# Test-only injection is empty by default, and accepts the same env/volume shapes as the broker.
ynone 'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "ROOMS_GITHUB_API_URL")' "no GitHub API override by default"
ynone 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.name == "e2e-ca")' "no extra volume by default"
ynone 'select(.kind == "Deployment") | .spec.template.spec.containers[0].volumeMounts[] | select(.name == "e2e-ca")' "no extra volume mount by default"
base="$out"
out="$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref \
  --set 'extraEnv[0].name=ROOMS_GITHUB_API_URL' --set 'extraEnv[0].value=https://e2e-stub.e2e.svc.cluster.local/' \
  --set 'extraVolumes[0].name=e2e-ca' --set 'extraVolumes[0].secret.secretName=e2e-ca' \
  --set 'extraVolumeMounts[0].name=e2e-ca' --set 'extraVolumeMounts[0].mountPath=/etc/e2e-ca' --set 'extraVolumeMounts[0].readOnly=true')"
ykind Deployment "$ctr.env[] | select(.name == \"ROOMS_GITHUB_API_URL\") | .value == \"https://e2e-stub.e2e.svc.cluster.local/\"" "extraEnv injects the in-cluster GitHub API root"
ykind Deployment "$(vol e2e-ca).secret.secretName == \"e2e-ca\"" "extraVolumes injects the test CA"
ykind Deployment "$(mnt e2e-ca) | (.mountPath == \"/etc/e2e-ca\" and .readOnly == true)" "extraVolumeMounts mounts the test CA read-only"
out="$base"
out="$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref \
  --set api.tls.secretName=agent-factory-api-tls --set api.tls.issuerRef.name=openbao)"
ykind Certificate '.spec.secretName == "agent-factory-api-tls"' "the Certificate writes api.tls.secretName"
ykind Certificate '.spec.dnsNames | contains(["agent-factory.agent-system.svc", "agent-factory.agent-system.svc.cluster.local"])' "the Certificate names the factory Service"
ykind Certificate '(.spec.issuerRef.name == "openbao" and .spec.issuerRef.kind == "ClusterIssuer")' "the Certificate's issuer is api.tls.issuerRef"
ykind Deployment "$(vol api-tls).secret | (.secretName == \"agent-factory-api-tls\" and .defaultMode == \"0440\")" "the API's pair, readable through fsGroup"
ykind Deployment "$(mnt api-tls) | (.mountPath == \"/etc/agent-factory-api-tls\" and .readOnly == true)" "the pair mounted read-only"
ykind ConfigMap '.data."config.yaml" | from_yaml | (.api.tls.certFile == "/etc/agent-factory-api-tls/tls.crt" and .api.tls.keyFile == "/etc/agent-factory-api-tls/tls.key")' "config.api.tls reads the mounted pair"
ykind ConfigMap '.data."config.yaml" | from_yaml | .repository == "Smana/cloud-native-ref"' "the rest of the config is still rendered verbatim"
ykind Service '.spec.ports[] | select(.port == 8082) | .name == "https"' "the API's Service port is named https"
out="$base"
# demo: no OIDC volume (no human tokens), and in the runs namespace exactly the Secret access the
# run token writer needs: create (unscopable by name) and get, update on agent-git and agent-git-read only.
ykind Role 'select(.metadata.namespace == "agents") | [.rules[] | select(.resources[] == "secrets")] | length == 0' "default: no Secret rule in the runs namespace"
# H2 (HR11): the publisher's token is minted from the agents App: no projected token is mounted.
ynone 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.name == "octo-sts-token")' "no octo-sts token (v4 step 8, R3)"
# H3 (HR24): no git mirror cache by default; with hardening.gitCache.sizeLimit, an emptyDir of that
# limit at /var/cache/maku/git, named to the factory by MAKU_GIT_CACHE.
ynone 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.name == "git-cache")' "no git cache unless hardening.gitCache.sizeLimit"
ynone 'select(.kind == "Deployment") | .spec.template.spec.containers[0].env[] | select(.name == "MAKU_GIT_CACHE")' "no MAKU_GIT_CACHE unless hardening.gitCache.sizeLimit"
out="$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref \
  --set hardening.gitCache.sizeLimit=2Gi)"
ykind Deployment "$(vol git-cache).emptyDir.sizeLimit == \"2Gi\"" "the git mirror cache is an emptyDir of hardening.gitCache.sizeLimit"
ykind Deployment "$(mnt git-cache) | (.mountPath == \"/var/cache/maku/git\" and (.readOnly // false) == false)" "the git cache at /var/cache/maku/git, writable"
ykind Deployment "$ctr.env[] | select(.name == \"MAKU_GIT_CACHE\") | .value == \"/var/cache/maku/git\"" "MAKU_GIT_CACHE names the git cache"
out="$base"
demo="$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref --set demo=true)"
out="$demo"
ynone 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.name == "oidc")' "demo: no oidc volume"
ynone 'select(.kind == "Deployment") | .spec.template.spec.containers[0].volumeMounts[] | select(.name == "oidc")' "demo: no oidc mount"
ykind Deployment "$(vol broker-ca).secret.secretName == \"openbao-ca\"" "demo: the broker CA stays required"
runsrole=$(yq eval-all 'select(.kind == "Role" and .metadata.namespace == "agents")' - <<<"$out")
yq -e '[.rules[] | select(.resources[] == "secrets" and .verbs[] == "create")] | (length == 1 and ((.[0] | has("resourceNames")) == false) and ((.[0].verbs | join(",")) == "create"))' - <<<"$runsrole" >/dev/null \
  || { echo "FAIL: demo: create on secrets, unscoped, alone" >&2; fail=1; }
yq -e '[.rules[] | select(.resources[] == "secrets" and .verbs[] != "create")] | (length == 1 and ((.[0].verbs | sort | join(",")) == "get,update") and ((.[0].resourceNames | join(",")) == "agent-git,agent-git-read"))' - <<<"$runsrole" >/dev/null \
  || { echo "FAIL: demo: get, update on agent-git and agent-git-read only" >&2; fail=1; }
ynone 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.name == "run-tokens")' "demo: the run tokens come from the factory's App: no run-tokens volume"
ynone 'select(.kind == "ClusterRole") | .rules[] | select(.resources[] == "secrets")' "demo: no cluster-wide Secret access"
ynone 'select(.kind == "Role" and .metadata.namespace == null) | .rules[] | select(.resources[] == "secrets")' "demo: no Secret access in the factory's own namespace"
out="$base"
ynone 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.name == "run-tokens")' "default: no run-tokens volume"
# runTokens (v4 step 2, PR3): the writer's App mounted, the config pointed at it, both Secrets by name
out="$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref \
  --set runTokens.enabled=true --set runTokens.appSecret=agents-app)"
ykind Deployment "$(vol run-tokens).secret | (.secretName == \"agents-app\" and .defaultMode == \"0440\")" "runTokens: the App Secret, 0440"
ykind Deployment "$(mnt run-tokens) | (.mountPath == \"/etc/agent-factory-run-tokens\" and .readOnly == true)" "runTokens: mounted at /etc/agent-factory-run-tokens"
ykind ConfigMap '.data."config.yaml" | from_yaml | .runTokens | (.enabled == true and .appIDFile == "/etc/agent-factory-run-tokens/app_id" and .privateKeyFile == "/etc/agent-factory-run-tokens/private_key")' "runTokens: the config reads the mounted App"
ykind Role 'select(.metadata.namespace == "agents") | .rules[] | select(.resources[] == "secrets" and .verbs[] == "update") | (.resourceNames | join(",")) == "agent-git,agent-git-read"' "runTokens: get, update on both run Secrets"
out="$base"
ynone 'select(.kind == "ValidatingAdmissionPolicy" or .kind == "ValidatingAdmissionPolicyBinding")' "default: no run token policy"
# runTokens.perRun (v4 D10, plan PR3): create, patch, delete on Secrets with no get and no
# resourceNames, and the policy that bounds them, scoped to the runs namespace, always beside it
out="$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref \
  --set config.runs.group=maku.ogenki.io --set config.runs.namePrefix=maku-run- \
  --set runTokens.enabled=true --set runTokens.appSecret=agents-app --set runTokens.perRun=true)"
ykind Role 'select(.metadata.namespace == "agents") | (.rules | map(select(.resources | contains(["secrets"])))) | ((length == 1) and ((.[0].verbs | join(",")) == "create,patch,delete") and (.[0] | has("resourceNames") | not))' "perRun: Secrets create, patch, delete, unscoped, and nothing else"
ynone 'select(.kind == "Role") | .rules[] | select(.resources[] == "secrets") | .verbs[] | select(. == "get" or . == "list" or . == "watch" or . == "update")' "perRun: the factory never reads a Secret"
ykind ConfigMap '.data."config.yaml" | from_yaml | .runTokens | (.enabled == true and .perRun == true)' "perRun: the config says perRun"
ykind ValidatingAdmissionPolicy '((.spec.matchConditions[0].expression | contains("system:serviceaccount:agent-system:agent-factory")) and (.spec.validations[0].expression | contains("^maku-run-[a-z2-7]{8}-git$")))' "perRun: the policy holds the factory's ServiceAccount to run token Secrets"
ykind ValidatingAdmissionPolicyBinding '(((.spec.validationActions | join(",")) == "Deny") and ((.spec.matchResources.namespaceSelector.matchExpressions[0].values | join(",")) == "agents"))' "perRun: the binding denies, in the runs namespace"
out="$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref \
  --set config.runs.group=maku.ogenki.io --set demo=true --set runTokens.perRun=true)"
ykind ConfigMap '.data."config.yaml" | from_yaml | .runTokens | (.perRun == true and (has("enabled") | not))' "demo perRun: the factory's App, perRun alone in the config"
ykind ValidatingAdmissionPolicy '.metadata.name == "maku-agent-system-run-token-secrets"' "demo perRun: the policy renders"
out="$base"
# refuse 'message' --set ...: the render must fail, naming message
refuse() {
  local want="$1" err; shift
  if err="$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref "$@" 2>&1 >/dev/null)"; then
    echo "FAIL: $* must not render" >&2; fail=1
  elif ! grep -qF -- "$want" <<<"$err"; then
    echo "FAIL: $* failed, but not with \"$want\": $err" >&2; fail=1
  fi
}
# apps (v4 D12, plan PR9): the new keys map onto the existing Secrets. The factory App by its new
# key renders exactly as githubSecret does.
render() { helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref "$@"; }
[ "$(render --set apps.factory.existingSecret=agent-factory-github)" = "$base" ] \
  || { echo "FAIL: apps.factory.existingSecret naming githubSecret's default changes the render" >&2; fail=1; }
out="$(render --set apps.factory.existingSecret=factory-app)"
ykind Deployment "$(vol github).secret.secretName == \"factory-app\"" "apps.factory: the new key wins over githubSecret"
# The agents App: mounted at /etc/agent-factory-agents and named under github.agents*; the
# runTokens alias is then left out of the config.
out="$(render --set config.runs.group=maku.ogenki.io --set runTokens.enabled=true --set apps.agents.existingSecret=x)"
ykind Deployment "$(vol agents).secret | (.secretName == \"x\" and .defaultMode == \"0440\")" "apps.agents: the agents App's Secret, 0440"
ykind Deployment "$(mnt agents) | (.mountPath == \"/etc/agent-factory-agents\" and .readOnly == true)" "apps.agents: mounted at /etc/agent-factory-agents"
ykind ConfigMap '.data."config.yaml" | from_yaml | .github | (.agentsAppIDFile == "/etc/agent-factory-agents/app_id" and .agentsKeyFile == "/etc/agent-factory-agents/private_key")' "apps.agents: the config reads the mounted App"
ykind ConfigMap '.data."config.yaml" | from_yaml | .runTokens | (.enabled == true and (has("appIDFile") | not))' "apps.agents: runTokens names no alias pair"
ynone 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.name == "run-tokens")' "apps.agents: no run-tokens volume"
out="$(render --set config.runs.group=maku.ogenki.io --set runTokens.enabled=true --set apps.agents.existingSecret=x --set runTokens.appSecret=x)"
ykind Deployment "$(vol agents).secret.secretName == \"x\"" "apps.agents and the same runTokens.appSecret: one App"
# The merger folded into the factory App: no merger volume, the config's merger files are the factory's.
out="$(render --set apps.merger.mode=factory)"
ynone 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.name == "merger")' "apps.merger.mode factory: no merger volume"
ynone 'select(.kind == "Deployment") | .spec.template.spec.containers[0].volumeMounts[] | select(.name == "merger")' "apps.merger.mode factory: no merger mount"
ykind ConfigMap '.data."config.yaml" | from_yaml | .github | (.mergerAppIDFile == "/etc/agent-factory-github/app_id" and .mergerKeyFile == "/etc/agent-factory-github/private_key")' "apps.merger.mode factory: the merger signs with the factory App's files"
out="$base"
ykind Deployment "$(vol merger).secret.secretName == \"agent-factory-merger\"" "default: the merger Secret keeps its name"
refuse 'apps.agents.existingSecret and runTokens.appSecret name different Secrets' --set runTokens.enabled=true --set apps.agents.existingSecret=x --set runTokens.appSecret=y
refuse 'apps.merger.mode factory and mergerSecret' --set apps.merger.mode=factory --set mergerSecret=m
# v4 step 8 (D5, D10, R3): the XR run kind and octo-sts are refused by name, by the schema and, with
# it skipped, by the template.
refuse "at '/hardening/publisher'" --set hardening.publisher.audience=octo-sts.dev
refuse 'hardening.publisher.audience was removed in v4 step 8' --skip-schema-validation --set hardening.publisher.audience=octo-sts.dev
refuse 'config.hardening.publisher.octoSTS was removed in v4 step 8' --set config.hardening.publisher.octoSTS.url=https://octo-sts.dev
refuse 'config.runs.group cloud.ogenki.io, cloud-native-ref'"'"'s Crossplane AgentRun, was removed in v4 step 8 (D5)' --set config.runs.group=cloud.ogenki.io
refuse 'config.github.agentsAppIDFile and apps.agents.existingSecret' --set apps.agents.existingSecret=x --set config.github.agentsAppIDFile=/a
refuse 'config.github.mergerAppIDFile and apps.merger.mode factory' --set apps.merger.mode=factory --set config.github.mergerAppIDFile=/a
refuse "value must be one of 'separate', 'factory'" --set apps.merger.mode=both
refuse "additional properties 'bogus' not allowed" --set runTokens.bogus=1
refuse 'runTokens.enabled needs apps.agents.existingSecret or runTokens.appSecret' --set runTokens.enabled=true
# Review of #510: one Secret is one App. The agents App never shares the factory's Secret (nor the
# merger's, the gate's bypass actor), and a separate merger never names the factory's.
refuse 'apps.agents.existingSecret names the factory App' --set apps.agents.existingSecret=agent-factory-github
refuse 'apps.agents.existingSecret names the factory App' --set apps.factory.existingSecret=f --set apps.agents.existingSecret=f
refuse 'runTokens.appSecret names the factory App' --set runTokens.enabled=true --set runTokens.appSecret=agent-factory-github
refuse 'apps.agents.existingSecret names the merger App' --set apps.agents.existingSecret=m --set mergerSecret=m
refuse 'mergerSecret names the factory App' --set mergerSecret=agent-factory-github
refuse 'demo: apps.agents.existingSecret needs runTokens.enabled' --set demo=true --set apps.agents.existingSecret=x
refuse 'runTokens.perRun needs runTokens.enabled' --set runTokens.perRun=true
refuse 'config.runTokens and runTokens.enabled' --set runTokens.enabled=true --set runTokens.appSecret=a --set config.runTokens.enabled=true
refuse 'api.tls.issuerRef.name needs api.tls.secretName' --set api.tls.issuerRef.name=openbao
refuse 'config.api.tls and api.tls.secretName' --set api.tls.secretName=agent-factory-api-tls --set config.api.tls.certFile=/c
# H-5: config.runs moves the Role and its binding; nothing stays behind in agents.
out=$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref \
  --set config.runs.group=maku.ogenki.io --set config.runs.namespace=runs2)
ykind Role 'select(.metadata.namespace == "runs2") | .rules[] | select(.resources[] == "agentruns") | .apiGroups[0] == "maku.ogenki.io"' "H-5: the AgentRun Role takes config.runs.group"
ykind RoleBinding 'select(.metadata.namespace == "runs2") | (.roleRef.name == "agent-factory" and .subjects[0].namespace == "agent-system")' "H-5: the AgentRun RoleBinding sits in config.runs.namespace"
ynone 'select(.metadata.namespace == "agents")' "H-5: nothing left in agents once config.runs.namespace moves the runs"
# GW10: config.runs.gatewayIdentity reaches the factory's config as written, and moves nothing.
gi=$(helm template agent-factory . --namespace agent-system --set image.tag="$digest" --set config.repository=Smana/cloud-native-ref \
  --set config.runs.group=maku.ogenki.io --set config.runs.namespace=runs2 --set config.runs.gatewayIdentity=true)
[[ "$(yq ea 'select(.kind == "ConfigMap" and .metadata.name == "agent-factory-config") | .data."config.yaml" | from_yaml | .runs.gatewayIdentity' - <<<"$gi")" == true ]] ||
  { echo "FAIL: config.runs.gatewayIdentity does not reach the factory's config" >&2; fail=1; }
diff <(grep -v -e gatewayIdentity -e checksum/config <<<"$gi") <(grep -v -e gatewayIdentity -e checksum/config <<<"$out") >/dev/null ||
  { echo "FAIL: config.runs.gatewayIdentity changes another object" >&2; fail=1; }
for bad in "" "v0.0.0" "latest"; do
  if helm template agent-factory . --namespace agent-system --set image.tag="$bad" >/dev/null 2>&1; then
    echo "FAIL: image.tag '$bad' must not render: only <version>@sha256:<digest>" >&2; fail=1
  fi
done
# e2e.unpinnedImages accepts a bare tag and never pulls
# v4 D8: the api block's identity keys reach the config as written; the oidc volume already carries
# the client ids they read (clientIDFiles), so nothing new is mounted.
out=$(helm template agent-factory . --namespace agent-system --set image.tag=v0.0.0@sha256:0000 --set config.api.preset=generic \
  --set config.api.tokenUse=id --set config.api.rolesClaim=/realm_access/roles --set config.api.humanJWKS=)
ykind ConfigMap '.data."config.yaml" | from_yaml | .api | (.preset == "generic" and .tokenUse == "id" and .rolesClaim == "/realm_access/roles")' "config.api's identity keys render verbatim"
ykind Deployment "$(vol oidc).secret.secretName == \"agent-factory-oidc\"" "the generic preset reads the same client ids volume"
# v4 D24: the exposure check's read of the namespace's routes, granted only with preflight.enabled,
# which is on by default since v4 step 8.
out=$(helm template agent-factory . --namespace agent-system --set image.tag=v0.0.0@sha256:0000 --set preflight.enabled=false)
ynone 'select(.kind == "Role" and .metadata.name == "agent-factory") | .rules[] | select(.resources[0] == "httproutes" or .resources[0] == "ingresses")' "no preflight, no route grant"
ynone 'select(.kind == "Deployment") | .spec.template.spec.containers[].env[] | select(.name == "MAKU_PREFLIGHT")' "no preflight, no /preflight on the factory's ops port"
out=$(helm template agent-factory . --namespace agent-system --set image.tag=v0.0.0@sha256:0000)
ykind Role 'select(.metadata.name == "agent-factory" and .metadata.namespace == null) | (.rules | map(select(.resources[0] == "httproutes" or .resources[0] == "ingresses") | (.resources | join(",")) + "=" + (.verbs | join(","))) | join(" ")) == "httproutes,grpcroutes,tlsroutes=get,list ingresses=get,list"' "preflight.enabled grants get and list on the routes and Ingresses, nothing more"
ykind Deployment '(.spec.template.spec.containers[0].env | map(select(.name == "MAKU_PREFLIGHT") | .value) | join(",")) == "true"' "preflight.enabled serves /preflight on the factory's ops port"
out=$(helm template agent-factory . --namespace agent-system --set image.tag=e2e --set e2e.unpinnedImages=true)
ykind Deployment "$ctr.image == \"ghcr.io/smana/maku-factory:e2e\"" "e2e.unpinnedImages: a bare tag renders"
ykind Deployment "$ctr.imagePullPolicy == \"Never\"" "e2e.unpinnedImages: imagePullPolicy Never"
[ "$fail" -eq 0 ] && echo PASS
exit "$fail"
