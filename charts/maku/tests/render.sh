#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Renders the umbrella chart and asserts what the platform relies on: the run kind comes from
# `runs` alone, the broker's ports, probes and RBAC, tokenreview's binding only in tokenreview
# mode, the run-controller's config, RBAC and CNP, default-deny in the runs namespace, and an image by digest unless e2e says otherwise.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
# No dependency: helm would render any chart left under charts/ (a stale dependency build) as a subchart.
[[ $(yq '.dependencies // [] | length' Chart.yaml) == 0 ]] || { echo "FAIL: Chart.yaml lists a dependency" >&2; exit 1; }
[[ ! -e charts ]] || { echo "FAIL: charts/maku/charts/ exists: a stale dependency build helm would render; move it away" >&2; exit 1; }
digest='v0.0.0@sha256:0000'
# every render needs the controller's three images; the cases below add only what they test
imgs=(--set broker.image.tag=$digest --set runController.image.tag=$digest
      --set runController.images.harness.tag=$digest --set runController.images.bridge.tag=$digest)
fail=0
out=''
check() { grep -qF -- "$1" <<<"$out" || { echo "FAIL: $2" >&2; fail=1; }; }
# yq assertion on one rendered kind: ykind KIND 'expr' 'message'; expr must yield at least one
# value, and every value it yields, across every document of that kind, must be true.
ykind() {
  yq ea -e "[select(.kind == \"$1\") | $2] | (length > 0 and all)" - <<<"$out" >/dev/null 2>&1 || { echo "FAIL: $3" >&2; fail=1; }
}
# jkind KIND 'jq expr' 'message': as ykind, in jq (yq compares only scalars, never maps or lists).
jkind() {
  yq ea -o=json -I0 "select(.kind == \"$1\")" - <<<"$out" | jq -s -e "map($2) | (length > 0 and all)" >/dev/null 2>&1 || { echo "FAIL: $3" >&2; fail=1; }
}
# jnone 'jq expr' 'message': the expression yields nothing over every object.
jnone() {
  [[ -z $(yq ea -o=json -I0 '.' - <<<"$out" | jq -c "$1" 2>&1) ]] || { echo "FAIL: $2" >&2; fail=1; }
}
# yq query that must succeed and print nothing: ynone 'expr' 'message'.
ynone() {
  local got
  got=$(yq "$1" - <<<"$out") || { echo "FAIL: yq errored: $2" >&2; fail=1; return; }
  [ -z "$got" ] || { echo "FAIL: $2" >&2; fail=1; }
}

q="$(helm template ap . -n maku -f values-quickstart.yaml \
     "${imgs[@]}")"
out="$q"
# runs block → broker config and run issuer agree
check 'namePrefix: run-' 'broker config carries runs.namePrefix'
check "subPattern: '^system:serviceaccount:agents:run-([a-z2-7]{8})\$'" 'the run issuer pattern is derived from runs'
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.human.mode == "local" and .publicURL == "http://localhost:8080")' 'local mode config'
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | .roomTools.enabled == false' 'the quickstart turns the :8090 room tools off'
ykind Deployment 'select(.metadata.name=="room-broker") | (.spec.template.spec.initContainers[0].args | join(",")) == "migrate"' 'migrate init container'
ykind Deployment 'select(.metadata.name=="room-broker") | (.spec.template.spec.initContainers[0].env | ((length == 1) and (.[0].name == "ROOMS_MIGRATION_DATABASE_URL") and (.[0].valueFrom.secretKeyRef.name == "rooms-owner-dsn") and (.[0].valueFrom.secretKeyRef.key == "uri")))' 'the migrate init container reads the owner Secret through ROOMS_MIGRATION_DATABASE_URL only'
ykind Deployment 'select(.metadata.name=="room-broker") | (.spec.template.spec.containers[0].env | map(select(.name == "ROOMS_DATABASE_URL")) | ((length == 1) and (.[0].valueFrom.secretKeyRef.name == "rooms-broker-dsn")))' 'the broker reads its own Secret through ROOMS_DATABASE_URL'
ykind Deployment 'select(.metadata.name=="room-broker") | (.spec.template.spec.containers[0].ports | map(.containerPort) | sort | join(",")) == "8080,8443,9090"' 'broker ports'
ykind Deployment 'select(.metadata.name=="room-broker") | (.spec.template.spec.containers[0] | (.livenessProbe.httpGet.path == "/healthz" and .readinessProbe.httpGet.path == "/readyz" and .startupProbe.httpGet.path == "/startupz" and .readinessProbe.httpGet.port == 9090))' 'probes on the ops port'
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.containers[0].image | test("^ghcr[.]io/smana/maku-broker:")' 'the broker image is maku-broker; the Deployment keeps its name'
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.containers[0].securityContext.readOnlyRootFilesystem == true' 'restricted securityContext'
ykind Deployment 'select(.metadata.name=="room-broker") | (.spec.template.spec.containers[0].env | map(.name) | sort | join(",")) == "POD_NAMESPACE,ROOMS_CONFIG,ROOMS_DATABASE_URL"' 'the three env vars brokerEnv requires'
ykind Service 'select(.metadata.name=="room-broker") | (.spec.ports | map(.port) | sort | join(",")) == "8443,9090"' 'local mode does not expose :8080'
ykind ClusterRoleBinding 'select(.roleRef.name=="system:auth-delegator") | .subjects[0].name == "room-broker"' 'tokenreview RBAC'
ykind Role 'select(.metadata.namespace=="agents" and .metadata.name=="room-broker") | .rules[] | select(.resources[]=="agentruns") | (.apiGroups[0] == "maku.ogenki.io" and ((.verbs|sort|join(",")) == "delete,get,list,patch,watch"))' 'AgentRun verbs in the runs namespace'
ykind Role 'select(.metadata.namespace==null) | .rules[] | select(.resources[]=="rooms") | ((.verbs|sort|join(",")) == "create,get,list,update,watch")' 'Room verbs (create: new room and fork)'
ykind Role 'select(.metadata.namespace==null and .metadata.name=="room-broker") | .rules[] | select(.resources[]=="tasks") | (.apiGroups[0] == "maku.ogenki.io" and (.resources|join(",")) == "tasks" and (.verbs|sort|join(",")) == "get,list")' 'coordination reads Tasks: get and list only (v4 MR13)'
ykind Role 'select(.metadata.namespace==null and .metadata.name=="room-broker") | .rules[] | select(.resources[]=="configmaps") | ((.resources|join(",")) == "configmaps" and (.verbs|sort|join(",")) == "get,list")' 'coordination reads the day ledgers: get and list only'
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.egress[] | select(.toEntities[0]=="kube-apiserver") | (.toPorts[0].ports | map(.port) | join(",")) == "443,6443"' 'the apiserver on 443 and kind'
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.metadata.annotations."checksum/config" | length == 64' 'a config change rolls the broker'
ykind Certificate '.spec.issuerRef.name == "quickstart-ca"' 'the Certificate issuer'
ykind Namespace '.metadata.name == "agents"' 'the runs namespace'
ykind Namespace '.metadata.annotations."helm.sh/resource-policy" == "keep"' 'uninstall keeps the runs namespace'
ykind CiliumNetworkPolicy 'select(.metadata.namespace=="agents") | (.spec.endpointSelector | length) == 0' 'default-deny in the runs namespace'
ykind CiliumNetworkPolicy 'select(.metadata.namespace=="agents") | ((.spec.ingress | length) == 1 and (.spec.ingress[0] | length) == 0 and (.spec.egress | length) == 1 and (.spec.egress[0] | length) == 0)' 'default-deny: enforcement on, no peer allowed (the empty-rule idiom)'
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.endpointSelector.matchLabels."app.kubernetes.io/name" == "room-broker"' 'broker CNP selects the broker'

# the run-controller: config from `runs`, its own identity, the verbs the reconciler calls
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (.namespace == "agents" and .namePrefix == "run-" and .runtimeClassName == "gvisor" and .broker.url == "https://room-broker.maku.svc.cluster.local:8443" and .broker.namespace == "maku" and .broker.caSecret == "room-broker-ca" and .broker.labels."app.kubernetes.io/name" == "room-broker")' 'controller config: runs, FQDN broker URL, broker labels, CA from broker.tls.caSecretName'
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | ((.broker.labels | keys | join(",")) == "app.kubernetes.io/name")' 'broker.labels is the one stable selector label (a version label would cut in-flight runs off on upgrade)'
ykind Role 'select(.metadata.namespace=="agents" and .metadata.name=="run-controller") | .rules[] | select(.resources[]=="agentruns/finalizers") | ((.verbs | join(",")) == "update")' 'controller can set blockOwnerDeletion'
ykind Deployment 'select(.metadata.name=="run-controller") | (.spec.template.spec.containers[0].ports | map(.containerPort) | sort | join(",")) == "9090,9091"' 'controller ports 9090/9091'
ykind Deployment 'select(.metadata.name=="run-controller") | (.spec.template.spec.containers[0] | (.livenessProbe.httpGet.port == 9091 and .livenessProbe.httpGet.path == "/healthz" and .readinessProbe.httpGet.port == 9091 and .readinessProbe.httpGet.path == "/readyz"))' 'controller probes on 9091'
ykind Deployment 'select(.metadata.name=="run-controller") | (.spec.template.spec.containers[0].securityContext.readOnlyRootFilesystem == true and .spec.template.metadata.annotations."checksum/config" != null and .spec.template.spec.serviceAccountName == "run-controller")' 'controller: restricted context, config checksum, own SA'
ykind Deployment 'select(.metadata.name=="run-controller") | (.spec.selector.matchLabels."app.kubernetes.io/name" == "run-controller" and .spec.template.metadata.labels."app.kubernetes.io/name" == "run-controller")' 'controller has its own name label'
ykind Role 'select(.metadata.namespace=="agents" and .metadata.name=="run-controller") | (.rules | map(.resources) | flatten | sort | join(",")) == "agentruns,agentruns/finalizers,agentruns/status,ciliumnetworkpolicies,configmaps,events,leases,pods,secrets,serviceaccounts"' 'controller RBAC in the runs namespace'
ykind Role 'select(.metadata.namespace=="agents" and .metadata.name=="run-controller") | .rules[] | select(.resources[]=="pods") | (.verbs | sort | join(",")) == "create,delete,get,list,watch"' 'controller can revoke (delete pods)'
ykind Role 'select(.metadata.namespace=="agents" and .metadata.name=="run-controller") | .rules[] | select(.resources[]=="secrets") | ((.resourceNames | join(",")) == "agent-model,agent-git" and (.verbs | join(",")) == "get")' 'get on the model and git Secrets by name; the quickstart shares one PAT, so no read Secret'
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (.unsafeSharedGitToken == true and (has("gitReadSecret") | not))' 'the quickstart shares its one PAT across roles (unsafeSharedGitToken), with no read Secret'
ykind Role 'select(.metadata.name=="run-controller-ca") | (.metadata.namespace == null and (.rules[0].resourceNames | join(",")) == "room-broker-ca" and (.rules[0].verbs | join(",")) == "get")' 'get on the CA-only Secret by name, in the release namespace'
ynone 'select(.kind=="Role") | .rules[] | select(.resources[]=="secrets") | .resourceNames[] | select(. == "room-broker-tls")' 'no Role reads the broker key pair (S-3)'
ynone 'select(.kind=="Role") | .rules[] | select(.resources[]=="secrets") | select(.resourceNames == null)' 'no Role grants Secrets without resourceNames (S-3)'
ykind CiliumNetworkPolicy 'select(.metadata.name=="run-controller") | (.spec.endpointSelector.matchLabels."app.kubernetes.io/name" == "run-controller" and (.spec.egress[] | select(.toEntities[0]=="kube-apiserver") | (.toPorts[0].ports | map(.port) | join(",")) == "443,6443"))' 'controller CNP: apiserver on apiServerPorts, own selector'
ykind RuntimeClass '(.handler == "runsc" and .metadata.name == "gvisor")' 'RuntimeClass gvisor'
ynone 'select(.metadata.labels."app.kubernetes.io/name" == "agent-factory")' 'the quickstart renders no factory object'
ynone 'select(.kind == "CustomResourceDefinition")' 'templates render no CRD: crds/ holds Room and AgentRun only'
# crds/ only shows with --include-crds: assert it holds exactly Room and AgentRun
crds="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" --include-crds | yq ea 'select(.kind == "CustomResourceDefinition") | .spec.names.kind' - | grep -v "^---$" | sort | paste -sd,)"
[[ $crds == "AgentRun,Room" ]] || { echo "FAIL: crds/ holds \"$crds\", want AgentRun,Room" >&2; fail=1; }
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.runtimeClassName == null' 'the broker runs on the default runtime: kubectl port-forward cannot reach a runsc pod'

# production-shaped render (cloud-native-ref): no migrate, no tokenreview binding, and no run
# controller: there Crossplane's composition runs the cloud.ogenki.io AgentRuns (S-5)
prod=(--set broker.runIssuer.mode=jwks --set broker.runIssuer.issuer=https://oidc --set broker.runIssuer.jwksURL=https://oidc/keys
      --set broker.database.brokerSecret=dsn --set broker.publicURL=https://rooms.example.com
      --set broker.human.issuer=https://idp.example.com --set broker.human.jwksURL=https://idp.example.com/keys
      --set broker.human.clientIDFile=/c --set broker.human.projectIDFile=/p --set broker.human.origin=https://rooms.example.com
      --set broker.human.groups.admin=a --set broker.human.groups.member=m
      --set runs.namePrefix=xplane-run- --set broker.tls.secretName=broker-cert --set broker.tls.caSecretName=broker-ca
      --set 'apiServerPorts={6443}' --set clusterDomain=corp.local
      --set-json 'runController.model.gateway.aliases={"public":["agent-default"]}')
p="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runs.group=cloud.ogenki.io --set runController.enabled=false)"
out="$p"
# D25: the XR install (runController.enabled=false) renders exactly what it rendered before rollout
# step 2: no new object, no new key. Regenerate only for a change meant to reach that install:
# UPDATE_GOLDEN=1 bash charts/maku/tests/render.sh, then review the diff.
if [[ ${UPDATE_GOLDEN:-} == 1 ]]; then printf '%s\n' "$p" >tests/golden/xr.yaml; fi
printf '%s\n' "$p" | diff -u tests/golden/xr.yaml - >&2 || { echo "FAIL: the XR render (runController.enabled=false) differs from tests/golden/xr.yaml" >&2; fail=1; }
ynone 'select(.kind=="ClusterRoleBinding")' 'jwks mode binds no auth-delegator'
ynone 'select(.kind=="RuntimeClass")' 'production renders no RuntimeClass: the cluster-scoped gvisor may already exist'
ynone 'select(.kind=="Deployment") | .spec.template.spec.initContainers | select(. != null)' 'no owner Secret, no migrate'
ynone 'select(.metadata.labels."app.kubernetes.io/name" == "run-controller")' 'runController.enabled=false renders no run controller object'
check 'xplane-run-' 'the production prefix flows through'
check 'cloud.ogenki.io' 'the production group flows through'
ykind Service 'select(.metadata.name=="room-broker") | (.spec.ports | map(.port) | sort | join(",")) == "8080,8443,9090"' 'oidc mode exposes :8080'
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | .roomTools.enabled == true' 'the default keeps the :8090 room tools on, as every existing deployment runs it'
ynone 'select(.metadata.labels."app.kubernetes.io/name" == "agent-factory")' 'the production render has no factory object'
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.runtimeClassName == null' 'production: the broker runs on the default runtime'

# the run controller under production values: it follows runs.namePrefix, broker.tls, clusterDomain
# and apiServerPorts; its group is maku.ogenki.io, the only one it reconciles
ctl="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}")"
out="$ctl"
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (.namePrefix == "xplane-run-" and .broker.caSecret == "broker-ca" and .broker.url == "https://room-broker.agent-system.svc.corp.local:8443")' 'controller follows runs, broker.tls.caSecretName, clusterDomain'
ykind Role 'select(.metadata.name=="run-controller-ca") | (.rules[0].resourceNames | join(",")) == "broker-ca"' 'the CA Role follows broker.tls.caSecretName'
ykind Role 'select(.metadata.namespace=="agents" and .metadata.name=="run-controller") | .rules[] | select(.resources[]=="secrets") | (.resourceNames | join(",")) == "agent-git,agent-git-read"' 'reviewer and triager read agent-git-read (PR2); the default gateway mode reads no model Secret'
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (.gitReadSecret == "agent-git-read" and (has("unsafeSharedGitToken") | not))' 'the controller config names the read-only Secret by default'
ynone 'select(.kind=="Role") | .rules[] | select(.resources[]=="secrets") | .resourceNames[] | select(. == "broker-cert")' 'no Role reads the key pair broker.tls.secretName names'
ykind Role 'select(.metadata.name=="run-controller" and .metadata.namespace=="agents") | .rules[0].apiGroups[0] == "maku.ogenki.io"' 'controller RBAC names the group the controller reconciles'
ykind CiliumNetworkPolicy 'select(.metadata.name=="run-controller") | (.spec.egress[] | select(.toEntities[0]=="kube-apiserver") | (.toPorts[0].ports | map(.port) | join(",")) == "6443")' 'controller CNP follows apiServerPorts'

# two DSNs, two roles, one pod: each container reads the Secret meant for it, through the variable
# its subcommand reads, and the broker never sees the owner's credential
out="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" \
     --set broker.database.ownerSecret=owner-dsn --set broker.database.brokerSecret=broker-dsn)"
ykind Deployment 'select(.metadata.name=="room-broker") | (.spec.template.spec.initContainers[0].env | ((length == 1) and (.[0].name == "ROOMS_MIGRATION_DATABASE_URL") and (.[0].valueFrom.secretKeyRef.name == "owner-dsn")))' 'the migrate init container takes broker.database.ownerSecret, under ROOMS_MIGRATION_DATABASE_URL'
ykind Deployment 'select(.metadata.name=="room-broker") | (.spec.template.spec.containers[0].env | map(select(.name == "ROOMS_DATABASE_URL")) | ((length == 1) and (.[0].valueFrom.secretKeyRef.name == "broker-dsn")))' 'the broker takes broker.database.brokerSecret, never the owner secret'
ykind Deployment 'select(.metadata.name=="room-broker") | (.spec.template.spec.containers[0].env | map(select(.name == "ROOMS_MIGRATION_DATABASE_URL")) | length == 0)' 'the broker container never sees the migration DSN'

# the runs namespace brought by the user keeps its default-deny
out="$(helm template ap . -n x -f values-quickstart.yaml --set createRunsNamespace=false "${imgs[@]}")"
ykind CiliumNetworkPolicy 'select(.metadata.namespace=="agents") | (.spec.endpointSelector | length) == 0' 'default-deny without the Namespace object'
ynone 'select(.kind=="Namespace")' 'createRunsNamespace=false renders no Namespace'

# the broker Certificate covers the host the controller hands the bridge, on a non-default cluster domain
out="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" --set clusterDomain=corp.local)"
ykind Certificate '.spec.dnsNames | contains(["room-broker.x.svc.corp.local"])' 'the Certificate names room-broker.<ns>.svc.<clusterDomain>'
ynone '[select(.kind=="Certificate") | .spec.dnsNames[] | select(test("cluster.local"))] | .[]' 'no cluster.local in the Certificate when clusterDomain=corp.local'
bhost="$(yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | .broker.url | sub("^https://", "") | sub(":[0-9]+$", "")' - <<<"$out")"
BHOST="$bhost" ykind Certificate '.spec.dnsNames | contains([strenv(BHOST)])' "a dnsName covers the controller's broker.url host ($bhost)"

# local publicURL follows the forwarded port (up.sh's LOCAL_PORT)
fwd="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" --set broker.publicURL=http://localhost:18080)"
out="$fwd"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | .publicURL == "http://localhost:18080"' 'local publicURL takes broker.publicURL'
# ALLOW_UNSANDBOXED=1's overrides never reach the broker pod
out="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" --set runController.allowUnsandboxed=true \
  --set runController.runtimeClassName= --set runController.createRuntimeClass=false)"
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.runtimeClassName == null' 'unsandboxed: the broker still has no runtimeClassName'

# the factory's two flows (S-4) open only with factoryURL: ingress to :8443 from its pods, egress to
# factoryURL's port on them, in the release namespace unless broker.factoryPodLabels names one
out="$q"
ynone 'select(.kind=="CiliumNetworkPolicy" and .metadata.name=="room-broker") | .spec.ingress[] | select(.fromEndpoints[0].matchLabels."app.kubernetes.io/name" == "agent-factory")' 'no factory ingress without factoryURL'
ynone 'select(.kind=="CiliumNetworkPolicy" and .metadata.name=="room-broker") | .spec.egress[] | select(.toEndpoints[0].matchLabels."app.kubernetes.io/name" == "agent-factory")' 'no factory egress without factoryURL'
out="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" --set broker.factoryURL=https://agent-factory.x.svc.cluster.local:8082)"
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.ingress[] | select(.fromEndpoints[0].matchLabels."app.kubernetes.io/name" == "agent-factory") | (.fromEndpoints[0].matchLabels."io.kubernetes.pod.namespace" == "x" and .toPorts[0].ports[0].port == "8443")' 'the factory reaches :8443 from its pods in the release namespace'
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.egress[] | select(.toEndpoints[0].matchLabels."app.kubernetes.io/name" == "agent-factory") | (.toEndpoints[0].matchLabels."io.kubernetes.pod.namespace" == "x" and .toPorts[0].ports[0].port == "8082")' "the broker reaches factoryURL's port on the factory pods"
out="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" --set broker.factoryURL=https://factory.example \
  --set 'broker.factoryPodLabels.io\.kubernetes\.pod\.namespace=agent-system')"
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.egress[] | select(.toEndpoints[0].matchLabels."app.kubernetes.io/name" == "agent-factory") | (.toEndpoints[0].matchLabels."io.kubernetes.pod.namespace" == "agent-system" and .toPorts[0].ports[0].port == "443")' 'an https factoryURL without a port is 443; factoryPodLabels may name the namespace'

# forge_read (hardening H4): with an audience, the broker mounts the factory token and names it.
out="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" --set broker.factoryURL=https://factory.example \
  --set broker.roomTools.enabled=true --set broker.hardening.forgeRead.audience=agent-factory)"
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.volumes[] | select(.name=="factory-token") | .projected.sources[0].serviceAccountToken.audience == "agent-factory"' 'the broker projects a token for the factory audience'
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | .roomTools.forgeReadTokenFile == "/var/run/secrets/maku/factory/token"' 'the broker config names the forge_read token'
out="$q"
ynone 'select(.kind=="Deployment" and .metadata.name=="room-broker") | .spec.template.spec.volumes[] | select(.name=="factory-token")' 'no factory token without forgeRead.audience'

# the AgentRun admission policy (S-2, D4): the broker's updates may touch the suspend annotations only.
# CEL is proven in charts/maku/tests/admission (envtest); here, its shape and binding.
out="$q"
ykind ValidatingAdmissionPolicy 'select(.metadata.name == "room-broker-maku-agentruns") | (.spec.failurePolicy == "Fail" and (.spec.matchConstraints.resourceRules[0] | (.apiGroups[0] == "maku.ogenki.io" and (.operations | join(",")) == "UPDATE" and (.resources | join(",")) == "agentruns")))' 'the policy holds every AgentRun update, failing closed'
ykind ValidatingAdmissionPolicy 'select(.metadata.name == "room-broker-maku-agentruns") | .spec.matchConditions[0].expression | test("^request.userInfo.username == .system:serviceaccount:maku:room-broker.$")' "the policy applies to the broker's ServiceAccount only"
KEYS="['agents.ogenki.io/operating-mode', 'agents.ogenki.io/remaining-seconds', 'agents.ogenki.io/resumed-at']" \
  ykind ValidatingAdmissionPolicy 'select(.metadata.name == "room-broker-maku-agentruns") | .spec.variables[] | select(.name == "suspendKeys") | .expression == strenv(KEYS)' 'exactly the three suspend keys, never the agents.ogenki.io/ prefix the meter shares'
ykind ValidatingAdmissionPolicyBinding 'select(.metadata.name == "room-broker-maku-agentruns") | (.spec.policyName == "room-broker-maku-agentruns" and (.spec.validationActions | join(",")) == "Deny" and .spec.matchResources.namespaceSelector.matchLabels."kubernetes.io/metadata.name" == "agents")' 'the binding denies, in the runs namespace'
out="$p"
ykind ValidatingAdmissionPolicy '(.metadata.name == "room-broker-agent-system-agentruns" and .spec.matchConstraints.resourceRules[0].apiGroups[0] == "cloud.ogenki.io")' "the policy follows runs.group and the release namespace"

# a misconfiguration is a helm error, for the intended reason: each case names its message
bad() { # bad 'expected message' --set ...
  local want="$1" err; shift
  if err="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" "$@" 2>&1 >/dev/null)"; then
    echo "FAIL: $* must not render" >&2; fail=1
  elif ! grep -qF -- "$want" <<<"$err"; then
    echo "FAIL: $* failed, but not with \"$want\": $err" >&2; fail=1
  fi
}
bad 'broker.human.mode "bogus" is not oidc or local' --set broker.human.mode=bogus
bad 'broker.runIssuer.mode "tokenReview" is not jwks or tokenreview' --set broker.runIssuer.mode=tokenReview
bad 'no database' --set broker.database.brokerSecret=
bad 'apiServerPorts is empty' --set apiServerPorts=null
bad 'runController.modelSecret is required' --set runController.modelSecret=
bad 'image.tag is <version>@sha256:<digest>, never a bare tag' --set runController.images.bridge.tag=v1
bad 'image.tag is <version>@sha256:<digest>' --set runController.images.harness.tag=
bad 'factory: this chart has no factory' --set factory.enabled=false
bad 'broker.runtimeClassName: the broker runs on the default runtime' --set broker.runtimeClassName=gvisor
bad 'broker.publicURL "https://rooms.example" in local mode' --set broker.publicURL=https://rooms.example
bad 'broker.publicURL "http://0.0.0.0:8080" in local mode' --set broker.publicURL=http://0.0.0.0:8080
bad 'runs.group "cloud.ogenki.io" with runController.enabled' --set runs.group=cloud.ogenki.io
bad 'broker.tls.caSecretName must not be broker.tls.secretName' --set broker.tls.caSecretName=room-broker-tls
bad 'runs.namespace "x" is the release namespace' --set runs.namespace=x
bad 'broker.tls.secretName is required' --set broker.tls.secretName=
bad 'runController.gitReadSecret is empty and runController.unsafeSharedGitToken is false' --set runController.gitReadSecret= --set runController.unsafeSharedGitToken=false
bad 'runController.gitTokens perRun and runController.unsafeSharedGitToken: choose one' --set runController.gitTokens=perRun --set runController.unsafeSharedGitToken=true
bad 'gitTokens' --set runController.gitTokens=mine
bad 'runController.gitTokens perRun and runController.gitReadSecret: choose one' --set runController.gitTokens=perRun --set runController.unsafeSharedGitToken=false

# v4 step 3 (D10): gitTokens perRun mounts each run's own Secret; the Role reads Secrets by get alone
pr="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runController.gitTokens=perRun --set runController.gitReadSecret=)"
out="$pr"
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (.gitTokens == "perRun" and (has("gitSecret") | not) and (has("gitReadSecret") | not))' 'perRun: the controller config says gitTokens perRun and names no shared Secret'
ykind Role 'select(.metadata.namespace=="agents" and .metadata.name=="run-controller") | (.rules | map(select(.resources | contains(["secrets"])))) | ((length == 1) and ((.[0].verbs | join(",")) == "get") and (.[0] | has("resourceNames") | not))' 'perRun: the run-controller Role gets Secrets with no resourceNames, and nothing else'

# S-1, D6: the factory's CA and the insecure opt-out reach the broker's config; http alone is refused
fca="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" \
  --set broker.factoryURL=https://agent-factory.x.svc:8082 --set broker.factoryCA.secretName=agent-factory-api-tls)"
out="$fca"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.factoryURL == "https://agent-factory.x.svc:8082" and .factoryCAFile == "/etc/room-broker/factory-ca/ca.crt" and (has("factoryInsecure") | not))' 'factoryCA.secretName writes factoryCAFile'
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.volumes[] | select(.name=="factory-ca") | (.secret.secretName == "agent-factory-api-tls" and .secret.items[0].key == "ca.crt")' 'the factory CA Secret, ca.crt only'
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.containers[0].volumeMounts[] | select(.name=="factory-ca") | (.mountPath == "/etc/room-broker/factory-ca" and .readOnly == true)' 'the factory CA mounted where factoryCAFile reads it'
fins="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" \
  --set broker.factoryURL=http://agent-factory.x.svc:8082 --set broker.factoryInsecure=true)"
out="$fins"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.factoryInsecure == true and (has("factoryCAFile") | not))' 'factoryInsecure reaches the config'
ynone 'select(.kind=="Deployment" and .metadata.name=="room-broker") | .spec.template.spec.volumes[] | select(.name=="factory-ca")' 'no factory CA Secret, no mount'
bad 'broker.factoryURL "http://agent-factory.x.svc:8082" is http' --set broker.factoryURL=http://agent-factory.x.svc:8082

# networkPolicies.enabled=false: a cluster without Cilium. No CiliumNetworkPolicy, no CNP verb for
# the controller, and its config says none; the default renders as before.
nonp="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set networkPolicies.enabled=false)"
out="$nonp"
ynone 'select(.kind == "CiliumNetworkPolicy")' 'networkPolicies.enabled=false renders no CiliumNetworkPolicy'
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | .networkPolicy == "none"' 'networkPolicies.enabled=false: the controller config says networkPolicy none'
ynone 'select(.kind=="Role") | .rules[] | select(.resources[]=="ciliumnetworkpolicies")' 'networkPolicies.enabled=false: no ciliumnetworkpolicies verb'
ykind Namespace '.metadata.name == "agents"' 'networkPolicies.enabled=false keeps the runs namespace'
ykind Deployment 'select(.metadata.name=="run-controller") | .spec.template.spec.serviceAccountName == "run-controller"' 'the controller still renders'
out="$q"
ynone 'select(.kind == "ConfigMap" and .metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | select(has("networkPolicy"))' 'the default config carries no networkPolicy key (cilium is the controller default)'

# roomTools.auth (task 6): key, the default, is today's render, byte for byte: no :8090 Service port,
# no ingress to it, no roomToolsURL. tokenreview opens :8090 to the runs, on the Service and the
# broker CNP, and hands the run controller the URL its harnesses call.
out="$p"
ynone 'select(.kind=="ConfigMap" and .metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | .roomTools | select(has("auth"))' 'key mode: the config carries no auth key'
for r in "$q" "$ctl"; do
  out="$r"
  ynone 'select(.kind=="Service" and .metadata.name=="room-broker") | .spec.ports[] | select(.port == 8090)' 'key mode: the Service does not expose :8090'
  ynone 'select(.kind=="CiliumNetworkPolicy" and .metadata.name=="room-broker") | .spec.ingress[] | select(.toPorts[0].ports[0].port == "8090")' 'key mode: no CNP ingress to :8090'
  ynone 'select(.kind=="ConfigMap" and .metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | select(has("roomToolsURL"))' 'key mode: no roomToolsURL'
done
rt="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set broker.roomTools.enabled=true --set broker.roomTools.auth=tokenreview)"
out="$rt"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.roomTools.enabled == true and .roomTools.auth == "tokenreview")' 'tokenreview mode reaches the broker config'
ykind Service 'select(.metadata.name=="room-broker") | .spec.ports[] | select(.name == "room-tools") | (.port == 8090 and .targetPort == 8090)' 'tokenreview mode: the Service exposes :8090'
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.ingress[] | select(.toPorts[0].ports[0].port == "8090") | (.fromEndpoints[0].matchLabels."io.kubernetes.pod.namespace" == "agents" and .fromEndpoints[0].matchExpressions[0].key == "agents.ogenki.io/run-id")' 'tokenreview mode: the runs reach :8090'
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | .roomToolsURL == "http://room-broker.maku.svc.cluster.local:8090/mcp"' 'tokenreview mode: the controller hands runs the :8090 URL, by its FQDN'
ykind ClusterRoleBinding 'select(.roleRef.name=="system:auth-delegator") | .subjects[0].name == "room-broker"' 'tokenreview mode reuses the run issuer'"'"'s auth-delegator binding'
bad 'broker.roomTools.auth "oauth" is not key or tokenreview' --set broker.roomTools.auth=oauth
bad 'broker.roomTools.auth tokenreview needs broker.runIssuer.mode tokenreview' --set broker.roomTools.auth=tokenreview \
  --set broker.runIssuer.mode=jwks --set broker.runIssuer.issuer=https://oidc --set broker.runIssuer.jwksURL=https://oidc/keys

# roomTools.knowledge (EX11): RunLore's MCP endpoint is one broker value. Unset, the config carries no
# knowledge key and no policy opens a flow to it; set, the config carries it (never its networkPolicy)
# and the broker's default-deny egress opens the URL's port to the named peers only.
for r in "$q" "$p"; do
  out="$r"
  ynone 'select(.kind=="ConfigMap" and .metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | .roomTools | select(has("knowledge"))' 'unset: the config carries no knowledge key'
done
kurl=(--set broker.roomTools.knowledge.url=http://runlore.runlore.svc.cluster.local:8080/mcp)
kc="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runController.enabled=false "${kurl[@]}" \
  --set-json 'broker.roomTools.knowledge.networkPolicy.to=[{"matchLabels":{"io.kubernetes.pod.namespace":"runlore","app.kubernetes.io/name":"runlore"}}]')"
out="$kc"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | .roomTools.knowledge | (.url == "http://runlore.runlore.svc.cluster.local:8080/mcp" and (.tools | join(",")) == "kb_search,kb_get" and (.roles | join(",")) == "triager" and .timeout == "10s" and .maxBytes == 65536 and (has("networkPolicy") | not))' 'set: the config carries the knowledge block, without its networkPolicy'
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | .roomTools.enabled == true' 'set: the room tools stay on'
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.egress[] | select(.toEndpoints[0].matchLabels."app.kubernetes.io/name" == "runlore") | (.toEndpoints[0].matchLabels."io.kubernetes.pod.namespace" == "runlore" and .toPorts[0].ports[0].port == "8080" and (.toEndpoints | length) == 1)' 'cilium: the broker reaches the knowledge peers on the URL port'
ktmp="$(mktemp -d)"
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$kc" >"$ktmp/knowledge.yaml"
go run ./tests/loadconfig broker-knowledge "$ktmp/knowledge.yaml" || fail=1
rm -rf "$ktmp"
kk="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set networkPolicies.mode=kubernetes --set broker.roomTools.enabled=true \
  --set broker.roomTools.knowledge.url=https://kb.example.com/mcp \
  --set-json 'broker.roomTools.knowledge.networkPolicy.to=[{"ipBlock":{"cidr":"10.0.0.0/8"}}]')"
out="$kk"
ykind NetworkPolicy 'select(.metadata.name=="room-broker") | .spec.egress[] | select(.to[0].ipBlock.cidr == "10.0.0.0/8") | ((.to | length) == 1 and (.ports | length) == 1 and .ports[0].port == 443)' 'kubernetes: the broker reaches the knowledge peers on 443 only'
# networkPolicy.port overrides the URL's: a policy matches the pod's port, after service translation
out="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runController.enabled=false "${kurl[@]}" --set broker.roomTools.knowledge.networkPolicy.port=9000 \
  --set-json 'broker.roomTools.knowledge.networkPolicy.to=[{"matchLabels":{"app.kubernetes.io/name":"runlore"}}]')"
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.egress[] | select(.toEndpoints[0].matchLabels."app.kubernetes.io/name" == "runlore") | .toPorts[0].ports[0].port == "9000"' 'networkPolicy.port overrides the URL port'
# cilium: an external https endpoint is a toFQDNs (or toCIDR) peer, its own rule, beside any selector
out="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runController.enabled=false --set broker.roomTools.knowledge.url=https://kb.example.com/mcp \
  --set-json 'broker.roomTools.knowledge.networkPolicy.to=[{"toFQDNs":[{"matchName":"kb.example.com"}]},{"toCIDR":["203.0.113.0/24"]}]')"
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.egress[] | select(has("toFQDNs")) | (.toFQDNs[0].matchName == "kb.example.com" and .toPorts[0].ports[0].port == "443" and (has("toEndpoints") | not))' 'cilium: a toFQDNs peer for an external endpoint, on 443'
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.egress[] | select(has("toCIDR")) | (.toCIDR[0] == "203.0.113.0/24" and .toPorts[0].ports[0].port == "443")' 'cilium: a toCIDR peer'
ynone 'select(.kind=="CiliumNetworkPolicy" and .metadata.name=="room-broker") | .spec.egress[] | select(.toEndpoints[0] | has("toFQDNs")) | .toEndpoints' 'cilium: a peer is never nested under toEndpoints'
bad 'broker.roomTools.knowledge.networkPolicy.to[0] sets toPorts' --set broker.roomTools.enabled=true --set broker.roomTools.knowledge.url=https://kb.example.com/mcp \
  --set-json 'broker.roomTools.knowledge.networkPolicy.to=[{"toFQDNs":[{"matchName":"kb.example.com"}],"toPorts":[]}]'
# the schema refuses what the broker would: a timeout over 12 s, an external http URL, credentials
bad '/broker/roomTools/knowledge/timeout' --set broker.roomTools.knowledge.url=https://kb.example.com/mcp --set broker.roomTools.knowledge.timeout=13s
bad '/broker/roomTools/knowledge/timeout' --set broker.roomTools.knowledge.url=https://kb.example.com/mcp --set broker.roomTools.knowledge.timeout=30s
bad '/broker/roomTools/knowledge/url' --set broker.roomTools.knowledge.url=http://kb.example.com/mcp
bad '/broker/roomTools/knowledge/url' --set broker.roomTools.knowledge.url=https://u:p@kb.example.com/mcp
bad 'broker.roomTools.knowledge.networkPolicy.to is empty' --set networkPolicies.mode=kubernetes --set broker.roomTools.enabled=true --set broker.roomTools.knowledge.url=https://kb.example.com/mcp
kn0="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set broker.roomTools.enabled=true --set networkPolicies.enabled=false "${kurl[@]}")"
out="$kn0"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | .roomTools.knowledge.url == "http://runlore.runlore.svc.cluster.local:8080/mcp"' 'mode none: the endpoint needs no peers'

# human.slackIntents (natural-language intents, SI3): rendered verbatim with the block, off when unset
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.human.slackIntents == null)' 'slackIntents is off unless the values set it'
si="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runController.enabled=false \
  --set broker.factoryURL=https://agent-factory.agent-system.svc:8082 \
  --set broker.human.roomctlClientIDFile=/r --set broker.human.mcp.enabled=true \
  --set 'broker.human.mcp.sponsored[0].name=slack-app' --set 'broker.human.mcp.sponsored[0].clientIDFile=/s' \
  --set broker.human.slackIntents.principal=slack-app --set broker.human.slackIntents.teamID=T01ABC \
  --set broker.human.slackIntents.url=http://gw.ai.svc:8080/v1 --set broker.human.slackIntents.apiKeyFile=/etc/classifier/key)"
out="$si"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.human.slackIntents.principal == "slack-app" and .human.slackIntents.apiKeyFile == "/etc/classifier/key")' 'slackIntents reaches the broker config'

# broker.modelGateway (plan v4 step 4, GW8): the planner and the classifier call the model gateway's
# clients listener, each with its own projected token. The chart fills url and apiKeyFile only where
# the verbatim human block leaves them unset: a block's own url or key file wins (D25).
mgtmp="$(mktemp -d)"; trap 'rm -rf "$mgtmp"' EXIT
mgplan=(--set broker.factoryURL=https://agent-factory.agent-system.svc:8082
  --set broker.human.roomctlClientIDFile=/r --set broker.human.mcp.enabled=true --set broker.human.access.readerFile=/a
  --set broker.human.intake.plan.enabled=true --set 'broker.human.intake.plan.repositories={owner/repo}'
  --set-json 'broker.human.intake.plan.classes={"docs":{"roles":["implementer"],"ceilingUSD":1,"source":"class:docs"}}')
mgsi=(--set 'broker.human.mcp.sponsored[0].name=slack-app' --set 'broker.human.mcp.sponsored[0].clientIDFile=/s'
  --set broker.human.slackIntents.principal=slack-app --set broker.human.slackIntents.teamID=T01ABC)
brokercfg() { yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$1"; }
mgp="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runController.enabled=false "${mgplan[@]}" \
  --set broker.modelGateway.service=model-gateway)"
out="$mgp"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.human.intake.plan.url == "http://model-gateway.agent-system.svc.corp.local:8082/clients/planner/v1" and .human.intake.plan.apiKeyFile == "/var/run/secrets/maku/model-gateway/planner")' 'modelGateway: the planner gets the clients listener URL and its token file'
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.human.slackIntents == null)' 'modelGateway: an absent slackIntents stays absent'
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.volumes[] | select(.name == "model-gateway") | (.projected.sources | map(.serviceAccountToken | .path + "=" + .audience + "@" + (.expirationSeconds | tostring)) | join(",")) == "planner=maku-gateway.client.planner@3600,classifier=maku-gateway.client.classifier@3600"' 'modelGateway: one projected token per client, 3600 s, read at use'
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.containers[0].volumeMounts[] | select(.name == "model-gateway") | (.mountPath == "/var/run/secrets/maku/model-gateway" and .readOnly == true)' 'modelGateway: the tokens are mounted read-only'
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.egress[] | select(.toEndpoints[0].matchLabels."gateway.networking.k8s.io/gateway-name" == "model-gateway") | (.toEndpoints[0].matchLabels."io.kubernetes.pod.namespace" == "agent-system" and (.toPorts[0].ports | map(.port) | join(",")) == "8082")' 'modelGateway: the broker CNP reaches the gateway pods on the clients port'
brokercfg "$mgp" >"$mgtmp/planner-gateway.yaml"
mgo="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runController.enabled=false "${mgplan[@]}" \
  --set broker.modelGateway.service=model-gateway \
  --set broker.human.intake.plan.url=http://llm.other-gateway.svc.cluster.local:8080/v1 \
  --set broker.human.intake.plan.apiKeyFile=/etc/room-broker/planner/key)"
out="$mgo"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.human.intake.plan.url == "http://llm.other-gateway.svc.cluster.local:8080/v1" and .human.intake.plan.apiKeyFile == "/etc/room-broker/planner/key")' 'modelGateway: a block that sets its url and apiKeyFile keeps both (D25)'
mgs="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" "${mgplan[@]}" "${mgsi[@]}" \
  --set broker.modelGateway.service=model-gateway --set broker.modelGateway.namespace=gateways --set broker.modelGateway.port=9082 \
  --set broker.modelGateway.audiencePrefix=mgw --set networkPolicies.mode=kubernetes)"
out="$mgs"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.human.slackIntents.url == "http://model-gateway.gateways.svc.corp.local:9082/clients/classifier/v1" and .human.slackIntents.apiKeyFile == "/var/run/secrets/maku/model-gateway/classifier" and .human.intake.plan.url == "http://model-gateway.gateways.svc.corp.local:9082/clients/planner/v1")' 'modelGateway: an existing slackIntents gets the classifier route; namespace and port follow the values'
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.volumes[] | select(.name == "model-gateway") | (.projected.sources | map(.serviceAccountToken.audience) | join(",")) == "mgw.client.planner,mgw.client.classifier"' 'modelGateway: the audiences follow audiencePrefix'
ykind NetworkPolicy 'select(.metadata.name=="room-broker") | .spec.egress[] | select(.to[0].podSelector.matchLabels."gateway.networking.k8s.io/gateway-name" == "model-gateway") | (.to[0].namespaceSelector.matchLabels."kubernetes.io/metadata.name" == "gateways" and (.ports | map(.port) | join(",")) == "9082")' 'modelGateway: the broker NetworkPolicy reaches the gateway pods on the clients port'
brokercfg "$mgs" >"$mgtmp/slackintents-gateway.yaml"
# unset modelGateway: no volume, no egress, no derived key
out="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runController.enabled=false "${mgplan[@]}" \
  --set broker.human.intake.plan.url=http://gw.ai.svc:8080/v1 --set broker.human.intake.plan.apiKeyFile=/k)"
ynone 'select(.kind=="Deployment") | .spec.template.spec.volumes[] | select(.name == "model-gateway")' 'no modelGateway: no token volume'
ynone 'select(.kind=="CiliumNetworkPolicy") | .spec.egress[] | select(.toEndpoints[0].matchLabels."gateway.networking.k8s.io/gateway-name")' 'no modelGateway: no egress to a gateway'
bad '/broker/modelGateway/port' --set broker.modelGateway.service=model-gateway --set broker.modelGateway.port=0
# A url without its apiKeyFile (or the reverse) would pair the gateway's token with another endpoint,
# or another key with the gateway: the pair is filled only when both are unset, else the render fails.
mgbad() { # mgbad 'expected message' --set ...: the oidc-mode render with modelGateway on must fail with it
  local want="$1" err; shift
  if err="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runController.enabled=false "${mgplan[@]}" \
      --set broker.modelGateway.service=model-gateway "$@" 2>&1 >/dev/null)"; then
    echo "FAIL: $* must not render" >&2; fail=1
  elif ! grep -qF -- "$want" <<<"$err"; then
    echo "FAIL: $* failed, but not with \"$want\": $err" >&2; fail=1
  fi
}
mgbad 'broker.human.intake.plan.url and broker.human.intake.plan.apiKeyFile: set both or neither' \
  --set broker.human.intake.plan.url=https://api.openai.com/v1
mgbad 'broker.human.intake.plan.url and broker.human.intake.plan.apiKeyFile: set both or neither' \
  --set broker.human.intake.plan.apiKeyFile=/etc/planner/key
mgbad 'broker.human.slackIntents.url and broker.human.slackIntents.apiKeyFile: set both or neither' "${mgsi[@]}" \
  --set broker.human.slackIntents.url=https://api.openai.com/v1
mgbad 'broker.human.slackIntents.url and broker.human.slackIntents.apiKeyFile: set both or neither' "${mgsi[@]}" \
  --set broker.human.slackIntents.apiKeyFile=/etc/classifier/key

# sandbox (D18, plan PR8): the new keys map onto the run-controller's. No sandbox key: the config
# is the old one, byte for byte (runtimeClassName: gvisor), plus PR B's unsafeSharedGitToken (the
# quickstart's one PAT).
rcconf() { yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml"' - <<<"$1"; }
rcconf "$q" | diff -u tests/golden/rc-quickstart.yaml - >&2 || { echo 'FAIL: with no sandbox key the controller config must be the one before step 2 (tests/golden/rc-quickstart.yaml)' >&2; fail=1; }
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set sandbox.runtimeClassName=kata)"
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (.runtimeClassName == "kata" and .allowUnsandboxed == false)' 'sandbox.runtimeClassName wins over runController.runtimeClassName'
ykind RuntimeClass '(.metadata.name == "kata" and .handler == "runsc")' 'createRuntimeClass renders the class under sandbox.runtimeClassName'
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set sandbox.unsafeAllowRunc=true --set runController.createRuntimeClass=false)"
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (has("runtimeClassName") == false and .allowUnsandboxed == true)' 'sandbox.unsafeAllowRunc: no runtimeClassName, allowUnsandboxed'
ynone 'select(.kind == "RuntimeClass")' 'sandbox.unsafeAllowRunc renders no RuntimeClass'
ynone 'select(.kind == "ClusterRole" and .metadata.name == "maku-maku-run-controller-runtimeclass")' 'sandbox.unsafeAllowRunc: no RuntimeClass ClusterRole (the preflight checks nothing)'
# the preflight (task 2) reads the resolved class, uncached, by name: get only, that one name
out="$q"
ykind ClusterRole 'select(.metadata.name == "maku-maku-run-controller-runtimeclass") | ((.rules | length) == 1 and .rules[0].apiGroups[0] == "node.k8s.io" and (.rules[0].resources | join(",")) == "runtimeclasses" and (.rules[0].resourceNames | join(",")) == "gvisor" and (.rules[0].verbs | join(",")) == "get")' 'the preflight gets the resolved RuntimeClass by name, nothing else'
ykind ClusterRoleBinding 'select(.metadata.name == "maku-maku-run-controller-runtimeclass") | (.roleRef.name == "maku-maku-run-controller-runtimeclass" and .subjects[0].name == "run-controller" and .subjects[0].namespace == "maku")' 'the RuntimeClass ClusterRole is bound to the run-controller'
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set sandbox.runtimeClassName=kata)"
ykind ClusterRole 'select(.metadata.name == "maku-maku-run-controller-runtimeclass") | (.rules[0].resourceNames | join(",")) == "kata"' 'the RuntimeClass ClusterRole names sandbox.runtimeClassName'
bad 'sandbox.unsafeAllowRunc and sandbox.runtimeClassName: choose one' --set sandbox.unsafeAllowRunc=true --set sandbox.runtimeClassName=kata
bad "at '/sandbox/unsafeAllowRunc': got string, want boolean" --set-string sandbox.unsafeAllowRunc=yes
bad 'additional properties' --set sandbox.runtimeClass=kata

# the chart's runs namespace enforces Pod Security restricted under the run-pod policy (the XR
# install's namespace is unchanged: tests/golden/xr.yaml)
out="$q"
ykind Namespace 'select(.metadata.name == "agents") | .metadata.labels."pod-security.kubernetes.io/enforce" == "restricted"' 'the runs namespace enforces Pod Security restricted'

# runsQuota (D16, plan PR10): one ResourceQuota on the runs namespace, pods, CPU and memory only
out="$q"
ykind ResourceQuota '(.metadata.name == "runs" and .metadata.namespace == "agents" and (.spec.hard | keys | sort | join(",")) == "limits.cpu,limits.memory,pods,requests.cpu,requests.memory")' 'one ResourceQuota runs in the runs namespace, pods, CPU and memory only'
ykind ResourceQuota '(.spec.hard.pods == "25" and .spec.hard."requests.cpu" == "16" and .spec.hard."requests.memory" == "32Gi" and .spec.hard."limits.cpu" == "32" and .spec.hard."limits.memory" == "64Gi")' 'the quota defaults: ten small runs with headroom'
[[ $(yq ea '[select(.kind == "ResourceQuota")] | length' - <<<"$q") == 1 ]] || { echo 'FAIL: exactly one ResourceQuota' >&2; fail=1; }
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set runsQuota.enabled=false)"
ynone 'select(.kind == "ResourceQuota")' 'runsQuota.enabled=false renders no ResourceQuota'
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set runsQuota.hard.pods=5)"
ykind ResourceQuota '(.spec.hard.pods == "5" and .spec.hard."limits.memory" == "64Gi")' 'one quota key overrides alone'
out="$p"
ynone 'select(.kind == "ResourceQuota")' 'runController.enabled=false renders no ResourceQuota'
bad "at '/runsQuota/enabled': got string, want boolean" --set-string runsQuota.enabled=yes
bad 'additional properties' --set runsQuota.hard.services=1
bad 'does not match pattern' --set runsQuota.hard.pods=lots
# the schema takes resource.Quantity's grammar: binary and decimal suffixes up to E, and exponents
helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set runsQuota.hard.limits\\.memory=1.5Pi --set runsQuota.hard.requests\\.memory=1Ei \
  --set runsQuota.hard.requests\\.cpu=2e3 --set runsQuota.hard.limits\\.cpu=1P --set runsQuota.hard.pods=.5k >/dev/null ||
  { echo 'FAIL: the quota schema refuses valid Kubernetes quantities (Pi, Ei, P, exponents)' >&2; fail=1; }

# runPodPolicy (D17): its CEL is proven in charts/maku/tests/admission (envtest); here, its shape,
# its binding, and what the values put in it.
vap() { yq 'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == "maku-maku-run-pods")' - <<<"$1"; }
out="$q"
ykind ValidatingAdmissionPolicy 'select(.metadata.name == "maku-maku-run-pods") | (.spec.failurePolicy == "Fail" and (.spec.matchConstraints.resourceRules | map(.resources | join(",")) | join(";")) == "pods;pods/ephemeralcontainers")' 'the run-pod policy matches pods and pods/ephemeralcontainers, failing closed'
ykind ValidatingAdmissionPolicy 'select(.metadata.name == "maku-maku-run-pods") | (.spec.variables[] | select(.name == "images") | .expression) == "['"'"'ghcr.io/smana/maku-harness'"'"', '"'"'ghcr.io/smana/maku-bridge'"'"', '"'"'ghcr.io/smana/maku-run-controller'"'"']"' 'the image allow-list is the run-controller'"'"'s three repositories'
ykind ValidatingAdmissionPolicy 'select(.metadata.name == "maku-maku-run-pods") | (.spec.variables[] | select(.name == "pinned") | .expression) == "true"' 'images are pinned by default'
ykind ValidatingAdmissionPolicy 'select(.metadata.name == "maku-maku-run-pods") | (.spec.validations | map(select(.message == "a run pod runs under RuntimeClass \"gvisor\" (sandbox.runtimeClassName)")) | length) == 1' 'the policy holds the resolved RuntimeClass'
ykind ValidatingAdmissionPolicy 'select(.metadata.name == "maku-maku-run-pods") | (.spec.validations | map(select(.expression | test("system:serviceaccount:maku:run-controller"))) | length) == 1' 'only the run-controller of this release creates run pods'
ykind ValidatingAdmissionPolicyBinding 'select(.spec.policyName == "maku-maku-run-pods") | (.metadata.name == "maku-maku-run-pods" and (.spec.validationActions | join(",")) == "Deny" and .spec.matchResources.namespaceSelector.matchLabels."kubernetes.io/metadata.name" == "agents")' 'the run-pod binding denies, in the runs namespace'
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set sandbox.unsafeAllowRunc=true --set runController.createRuntimeClass=false --set 'runPodPolicy.extraImages={docker.io/library/busybox}' --set e2e.unpinnedImages=true)"
ykind ValidatingAdmissionPolicy 'select(.metadata.name == "maku-maku-run-pods") | (.spec.validations | map(select(.message | test("RuntimeClass"))) | length) == 0' 'unsafeAllowRunc: the policy holds no RuntimeClass rule'
ykind ValidatingAdmissionPolicy 'select(.metadata.name == "maku-maku-run-pods") | (.spec.variables[] | select(.name == "images") | .expression | test("'"'"'docker.io/library/busybox'"'"'\]$"))' 'runPodPolicy.extraImages joins the allow-list'
ykind ValidatingAdmissionPolicy 'select(.metadata.name == "maku-maku-run-pods") | (.spec.variables[] | select(.name == "pinned") | .expression) == "false"' 'e2e.unpinnedImages lifts the digest rule'
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set runPodPolicy.enabled=false)"
ynone 'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == "maku-maku-run-pods")' 'runPodPolicy.enabled=false renders no run-pod policy'
out="$p"
ynone 'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == "maku-agent-system-run-pods")' 'runController.enabled=false renders no run-pod policy'
bad "at '/runPodPolicy/extraImages/0': 'not' failed" --set 'runPodPolicy.extraImages={busybox:1.37}'

# agentRunPolicies (D17, task 5): opt-in, each only when its value is set, with or without the run
# controller; their CEL is proven in charts/maku/tests/admission (envtest)
out="$q"
ynone 'select(.kind == "ValidatingAdmissionPolicy" and (.metadata.name | test("agentrun-(creators|factory-patch-limit)$")))' 'no AgentRun creator or patch-limit policy unless set'
out="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runs.group=cloud.ogenki.io --set runController.enabled=false \
       --set 'agentRunPolicies.creators={system:serviceaccount:agent-system:agent-factory}' --set agentRunPolicies.factoryServiceAccount=agent-factory)"
ykind ValidatingAdmissionPolicy 'select(.metadata.name == "maku-agent-system-agentrun-creators") | (.spec.matchConstraints.resourceRules[0].apiGroups[0] == "cloud.ogenki.io" and (.spec.matchConstraints.resourceRules[0].operations | join(",")) == "CREATE" and .spec.validations[0].expression == "request.userInfo.username in ['"'"'system:serviceaccount:agent-system:agent-factory'"'"']")' 'the creator policy names the listed users, on runs.group, for the XR install too'
ykind ValidatingAdmissionPolicy 'select(.metadata.name == "maku-agent-system-agentrun-factory-patch-limit") | (.spec.matchConditions[0].expression == "request.userInfo.username == '"'"'system:serviceaccount:agent-system:agent-factory'"'"'" and (.spec.matchConstraints.resourceRules[0].operations | join(",")) == "UPDATE")' 'the patch-limit policy holds the factory ServiceAccount of the release namespace'
ykind ValidatingAdmissionPolicyBinding 'select(.metadata.name | test("agentrun-(creators|factory-patch-limit)$")) | (.spec.policyName == .metadata.name and (.spec.validationActions | join(",")) == "Deny" and has("spec") and (.spec | has("matchResources")) == false)' 'both AgentRun bindings deny in every namespace, as the Kyverno originals'
out="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runs.group=cloud.ogenki.io --set runController.enabled=false \
       --set 'agentRunPolicies.creators={system:serviceaccount:agent-system:agent-factory}' --set agentRunPolicies.factoryServiceAccount=agent-factory \
       --set 'agentRunPolicies.namespaces={agents,agents-2}')"
ykind ValidatingAdmissionPolicyBinding 'select(.metadata.name | test("agentrun-(creators|factory-patch-limit)$")) | (.spec.matchResources.namespaceSelector.matchExpressions[0] | (.key == "kubernetes.io/metadata.name" and .operator == "In" and (.values | join(",")) == "agents,agents-2"))' 'agentRunPolicies.namespaces narrows both bindings'
bad 'does not match pattern' --set agentRunPolicies.factoryServiceAccount=Agent_Factory
bad 'does not match pattern' --set-json 'agentRunPolicies.creators=["system:serviceaccount:x:y'"'"' || true || '"'"'"]'
bad 'cannot go into the run-pod policy' --set-json 'runController.images.harness.repository="ghcr.io/x'"'"']+['"'"'y"'

# install.unsafe (D18, plan PR9): the chart lists its unsafe settings in the broker's config. The
# quickstart shares its one PAT across roles (PR B), so it lists shared_git_token; without that flag
# it lists none, and the render keeps its old config
unsafe() { yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | .install.unsafe // [] | join(",")' - <<<"$1"; }
[[ $(unsafe "$q") == "shared_git_token,model_key_in_sandbox" ]] || { echo "FAIL: the default quickstart (gVisor, Cilium) lists only shared_git_token and model_key_in_sandbox, got '$(unsafe "$q")'" >&2; fail=1; }
[[ $(unsafe "$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set runController.unsafeSharedGitToken=false)") == "model_key_in_sandbox" ]] ||
  { echo "FAIL: the quickstart with a read Secret lists only its model key in the sandbox" >&2; fail=1; }
uns="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set runController.allowUnsandboxed=true --set runController.runtimeClassName= --set runController.createRuntimeClass=false)"
[[ $(unsafe "$uns") == "unsafe_allow_runc,shared_git_token,model_key_in_sandbox" ]] || { echo "FAIL: ALLOW_UNSANDBOXED's flags list unsafe_allow_runc (and the quickstart's shared_git_token), got '$(unsafe "$uns")'" >&2; fail=1; }
[[ $(unsafe "$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set sandbox.unsafeAllowRunc=true --set runController.createRuntimeClass=false)") == "unsafe_allow_runc,shared_git_token,model_key_in_sandbox" ]] ||
  { echo "FAIL: sandbox.unsafeAllowRunc lists unsafe_allow_runc" >&2; fail=1; }
[[ $(unsafe "$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set runController.allowUnsandboxed=true)") == "shared_git_token,model_key_in_sandbox" ]] ||
  { echo "FAIL: allowUnsandboxed with a RuntimeClass still sandboxes: only the quickstart's shared_git_token listed" >&2; fail=1; }
[[ $(unsafe "$nonp") == "network_policy_none,shared_git_token,model_key_in_sandbox" ]] || { echo "FAIL: networkPolicies.enabled=false lists network_policy_none (and the quickstart's shared_git_token), got '$(unsafe "$nonp")'" >&2; fail=1; }
[[ $(unsafe "$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set runController.unsafeSharedGitToken=true)") == "shared_git_token,model_key_in_sandbox" ]] ||
  { echo "FAIL: runController.unsafeSharedGitToken lists shared_git_token" >&2; fail=1; }
[[ $(unsafe "$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runs.group=cloud.ogenki.io --set runController.enabled=false --set networkPolicies.enabled=false)") == "" ]] ||
  { echo "FAIL: without the run-controller nothing is listed (the XR install renders as before)" >&2; fail=1; }

# networkPolicies.mode kubernetes (v4 D6, plan PR6): plain NetworkPolicies and the egress proxy. No
# CiliumNetworkPolicy; default-deny in the runs namespace; the controller creates a NetworkPolicy per
# run and points its harnesses at the proxy. The old key keeps its meaning.
kn="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set networkPolicies.mode=kubernetes)"
out="$kn"
ynone 'select(.kind == "CiliumNetworkPolicy")' 'mode kubernetes renders no CiliumNetworkPolicy'
ykind NetworkPolicy 'select(.metadata.name=="default-deny") | (.metadata.namespace == "agents" and (.spec.podSelector | length) == 0 and (.spec.policyTypes | join(",")) == "Ingress,Egress" and (.spec | has("egress") | not) and (.spec | has("ingress") | not))' 'mode kubernetes: default-deny in the runs namespace, both directions, no rule'
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (.networkPolicy == "kubernetes" and .egressProxy.url == "http://egress-proxy.maku.svc.cluster.local:3128" and .egressProxy.namespace == "maku" and .egressProxy.labels."app.kubernetes.io/name" == "egress-proxy" and .dns.namespace == "kube-system" and .dns.labels."k8s-app" == "kube-dns" and (.egressProxy.denyCIDRs | length) == 5)' 'mode kubernetes: the controller config names the proxy and DNS'
ykind Role 'select(.metadata.namespace=="agents" and .metadata.name=="run-controller") | .rules[] | select(.resources[]=="networkpolicies") | (.apiGroups[0] == "networking.k8s.io" and (.verbs | sort | join(",")) == "create,get")' 'mode kubernetes: the controller gets and creates NetworkPolicies'
# The network preflight (plan Task 14): its probe pod is the controller's own image, which the run-pod
# policy already allows; the controller lists events to name why a probe did not start.
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | .images.probe == "ghcr.io/smana/maku-run-controller:v0.0.0@sha256:0000"' 'mode kubernetes: images.probe is the run-controller image'
ykind ValidatingAdmissionPolicy 'select(.metadata.name=="maku-maku-run-pods") | .spec.variables[] | select(.name == "images") | .expression | test("'"'"'ghcr.io/smana/maku-run-controller'"'"'")' 'the run-pod policy allows the run-controller image, the probe pod'"'"'s'
ykind Role 'select(.metadata.namespace=="agents" and .metadata.name=="run-controller") | .rules[] | select(.resources[]=="events") | (.verbs | sort | join(",")) == "create,list,patch"' 'mode kubernetes: the controller lists events (a probe pod that did not start)'
ynone 'select(.kind=="Role") | .rules[] | select(.resources[]=="ciliumnetworkpolicies")' 'mode kubernetes: no ciliumnetworkpolicies verb'
[[ $(unsafe "$kn") == "shared_git_token,model_key_in_sandbox" ]] || { echo "FAIL: mode kubernetes is not network_policy_none, got '$(unsafe "$kn")'" >&2; fail=1; }
out="$q"
ynone 'select(.kind == "NetworkPolicy")' 'mode cilium (the default) renders no NetworkPolicy'
ynone 'select(.kind=="Role") | .rules[] | select(.resources[]=="networkpolicies")' 'mode cilium: no networkpolicies verb'
out="$nonp"
ynone 'select(.kind == "NetworkPolicy")' 'networkPolicies.enabled=false (none) renders no NetworkPolicy'
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set networkPolicies.mode=none)"
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | .networkPolicy == "none"' 'mode none: the controller config says none'
ynone 'select(.kind == "CiliumNetworkPolicy" or .kind == "NetworkPolicy")' 'mode none: no policy at all'
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set networkPolicies.mode=cilium)"
ykind CiliumNetworkPolicy 'select(.metadata.name=="default-deny") | .metadata.namespace == "agents"' 'mode cilium: the default-deny CNP'
# runsNamespaceDefaultDeny: false, for a platform that owns agents/default-deny: no default-deny in
# either mode, and every other policy and the Namespace as before.
for m in cilium kubernetes; do
  out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set networkPolicies.mode=$m --set networkPolicies.runsNamespaceDefaultDeny=false)"
  ynone 'select(.metadata.name == "default-deny")' "mode $m, runsNamespaceDefaultDeny false: no default-deny"
  ykind Namespace '.metadata.name == "agents"' "mode $m, runsNamespaceDefaultDeny false: the runs namespace still renders"
  ykind "$([[ $m == cilium ]] && echo CiliumNetworkPolicy || echo NetworkPolicy)" 'select(.metadata.name=="room-broker") | .metadata.namespace == null' "mode $m, runsNamespaceDefaultDeny false: the broker's policy still renders"
  out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set networkPolicies.mode=$m --set networkPolicies.runsNamespaceDefaultDeny=true)"
  ykind "$([[ $m == cilium ]] && echo CiliumNetworkPolicy || echo NetworkPolicy)" 'select(.metadata.name=="default-deny") | .metadata.namespace == "agents"' "mode $m, runsNamespaceDefaultDeny true: the default-deny"
done
bad "at '/networkPolicies/runsNamespaceDefaultDeny'" --set-string networkPolicies.runsNamespaceDefaultDeny=no
bad 'networkPolicies.mode cilium and networkPolicies.enabled false: choose one' --set networkPolicies.mode=cilium --set networkPolicies.enabled=false
bad 'networkPolicies.mode kubernetes needs runController.enabled' --set networkPolicies.mode=kubernetes --set runController.enabled=false
# the proxy trusts the egress annotation because the run-pod policy alone lets the run-controller write it
bad 'networkPolicies.mode kubernetes needs runPodPolicy.enabled' --set networkPolicies.mode=kubernetes --set runPodPolicy.enabled=false
bad "at '/networkPolicies/mode'" --set networkPolicies.mode=calico

# The egress proxy and the components' NetworkPolicies, kubernetes mode only (plan Task 13, PR13).
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set networkPolicies.mode=kubernetes \
  --set broker.roomTools.enabled=true --set broker.roomTools.auth=tokenreview --set broker.factoryURL=https://agent-factory.maku.svc:8082 \
  --set-json 'networkPolicies.kubernetes.metricsFrom=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"monitoring"}}}]' \
  --set-json 'egressProxy.extraPorts=[8443]')"
npsel() { echo "select(.kind==\"NetworkPolicy\" and .metadata.name==\"$1\") | .spec"; }
ykind NetworkPolicy "$(npsel egress-proxy) | (.podSelector.matchLabels.\"app.kubernetes.io/name\" == \"egress-proxy\" and (.policyTypes | join(\",\")) == \"Ingress,Egress\")" 'the proxy NetworkPolicy selects the proxy, both directions'
ykind NetworkPolicy "$(npsel egress-proxy) | .ingress[] | select(.ports[0].port == 3128) | (((.from | length) == 1) and .from[0].namespaceSelector.matchLabels.\"kubernetes.io/metadata.name\" == \"agents\" and .from[0].podSelector.matchLabels.\"app.kubernetes.io/managed-by\" == \"run-controller\")" 'the proxy takes 3128 only from the run pods'
ykind NetworkPolicy "$(npsel egress-proxy) | .ingress[] | select(.ports[0].port == 9090) | .from[0].namespaceSelector.matchLabels.\"kubernetes.io/metadata.name\" == \"monitoring\"" 'the proxy takes 9090 only from metricsFrom'
ykind NetworkPolicy "$(npsel egress-proxy) | (.ingress | map(.ports[].port) | sort | join(\",\")) == \"3128,9090\"" 'the proxy takes nothing else'
ykind NetworkPolicy "$(npsel egress-proxy) | .egress[] | select(.to[0].ipBlock.cidr == \"0.0.0.0/0\") | ((.to[0].ipBlock.except | join(\",\")) == \"10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,100.64.0.0/10,169.254.0.0/16\" and (.ports | map(.port) | join(\",\")) == \"443,8443\")" 'the proxy dials the public internet on 443 and extraPorts, never privateCIDRs'
ykind NetworkPolicy "$(npsel egress-proxy) | .egress[] | select(.ports[0].port == 53) | .to[0].podSelector.matchLabels.\"k8s-app\" == \"kube-dns\"" 'the proxy resolves through kube-dns'
ykind NetworkPolicy "$(npsel egress-proxy) | .egress[] | select(has(\"to\") | not) | (.ports | map(.port) | join(\",\")) == \"443,6443\"" 'the proxy reaches the apiserver on apiServerPorts (any address without apiServerCIDRs)'
ykind NetworkPolicy "$(npsel room-broker) | .ingress[] | select(.from[0].namespaceSelector.matchLabels.\"kubernetes.io/metadata.name\" == \"agents\") | (.ports | map(.port) | join(\",\")) == \"8443,8090\"" 'the broker takes 8443 and, under tokenreview, 8090 from the runs'
ykind NetworkPolicy "$(npsel room-broker) | .ingress[] | select(.from[0].podSelector.matchLabels.\"app.kubernetes.io/name\" == \"agent-factory\") | (.ports[0].port == 8443 and .from[0].namespaceSelector.matchLabels.\"kubernetes.io/metadata.name\" == \"maku\")" 'the broker takes 8443 from the factory'
ykind NetworkPolicy "$(npsel room-broker) | .ingress[] | select(.ports[0].port == 9090) | .from[0].namespaceSelector.matchLabels.\"kubernetes.io/metadata.name\" == \"monitoring\"" 'the broker takes 9090 only from metricsFrom'
ykind NetworkPolicy "$(npsel room-broker) | .egress[] | select(.ports[0].port == 5432) | .to[0].podSelector.matchExpressions[0].key == \"cnpg.io/cluster\"" 'the broker reaches the database label on 5432'
ykind NetworkPolicy "$(npsel room-broker) | .egress[] | select(.ports[0].port == 8082) | .to[0].podSelector.matchLabels.\"app.kubernetes.io/name\" == \"agent-factory\"" 'the broker reaches the factory port'
ykind NetworkPolicy "$(npsel run-controller) | ((.egress | map(.ports[0].port) | sort | join(\",\")) == \"53,443\" and (.ingress | map(.ports[].port) | join(\",\")) == \"9090\")" 'the run-controller reaches DNS and the apiserver only, and takes only metrics'
ykind Deployment 'select(.metadata.name=="egress-proxy") | .spec.template.spec | (.containers[0].image == "ghcr.io/smana/maku-run-controller:v0.0.0@sha256:0000" and (.containers[0].args | join(",")) == "egress-proxy" and .containers[0].securityContext.readOnlyRootFilesystem == true and .securityContext.runAsNonRoot == true and .serviceAccountName == "egress-proxy" and .volumes[0].configMap.name == "run-controller-config")' 'the proxy is the run-controller image by digest, restricted, reading the controller config'
ykind Deployment 'select(.metadata.name=="egress-proxy") | .spec.replicas == 2' 'two proxy replicas'
ykind Deployment 'select(.metadata.name=="egress-proxy") | (.spec.template.spec.containers[0].env | map(select(.name | test("^MAX_TUNNELS"))) | map(.name + "=" + .value) | join(",")) == "MAX_TUNNELS=1024,MAX_TUNNELS_PER_CLIENT=64"' 'the proxy caps its tunnels, in all and per client'
ykind PodDisruptionBudget 'select(.metadata.name=="egress-proxy") | .spec.minAvailable == 1' 'the proxy PDB'
ykind Service 'select(.metadata.name=="egress-proxy") | (.spec.ports | map(.port) | join(",")) == "3128"' 'the proxy Service on 3128'
ykind Role 'select(.metadata.name=="egress-proxy") | (.metadata.namespace == "agents" and (.rules | length) == 1 and (.rules[0].resources | join(",")) == "pods" and (.rules[0].verbs | sort | join(",")) == "get,list,watch")' 'the proxy watches pods in the runs namespace, nothing else'
for r in "$q" "$nonp"; do
  out="$r"
  ynone 'select(.metadata.name=="egress-proxy" or .kind=="NetworkPolicy" or .kind=="PodDisruptionBudget")' 'modes cilium and none: no proxy, no NetworkPolicy'
done

# runController.model.mode gateway (v4 step 4, GW3): runs call the model gateway with their own token.
gw="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set runController.model.mode=gateway \
  --set-json 'runController.model.gateway.aliases={"public":["agent-default","tier-light"],"internal":["tier-standard"]}')"
out="$gw"
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | ((has("modelSecret") | not) and .model.mode == "gateway" and .model.gateway.url == "http://model-gateway.maku.svc.cluster.local" and .model.gateway.ports.public == 8080 and .model.gateway.ports.internal == 8081 and .model.gateway.audiencePrefix == "maku-gateway" and (.model.gateway.aliases.public | join(",")) == "agent-default,tier-light" and (.model.gateway.aliases.internal | join(",")) == "tier-standard" and .model.gateway.serviceAccount.namespace == "maku" and .model.gateway.serviceAccount.name == "run-controller")' 'gateway mode: no modelSecret, the gateway in the release namespace by default, its ports, prefix, aliases and the controller'"'"'s own ServiceAccount'
ykind Role 'select(.metadata.namespace=="agents" and .metadata.name=="run-controller") | .rules[] | select(.resources[]=="secrets") | (.resourceNames | join(",")) == "agent-git"' 'gateway mode: the controller may not read the model Secret'
ykind Role 'select(.metadata.name=="run-controller-token") | (.metadata.namespace == null and (.rules | length) == 1 and (.rules[0].resources | join(",")) == "serviceaccounts/token" and (.rules[0].resourceNames | join(",")) == "run-controller" and (.rules[0].verbs | join(",")) == "create")' 'gateway mode: the preflight requests its own ServiceAccount token, by name, nothing else'
ykind ValidatingAdmissionPolicy '.spec.validations[] | select(.message | test("projected ServiceAccount tokens")) | .expression | (test("startsWith\\('"'"'maku-gateway[.]public[.]'"'"'\\)") and test("startsWith\\('"'"'maku-gateway[.]internal[.]'"'"'\\)") and (test("startsWith\\('"'"'maku-gateway[.]'"'"'\\)") | not))' 'gateway mode: the run-pod policy holds token audiences to the broker, the room tools and the gateway'"'"'s two run listeners'
ykind ValidatingAdmissionPolicy '.spec.validations[] | select(.message | test("never reads the model Secret agent-model")) | .expression | (test("secretKeyRef") and test("envFrom") and test("projected"))' 'gateway mode: the run-pod policy refuses the model Secret by volume, projected source, env and envFrom'
ykind Deployment 'select(.metadata.name=="run-controller") | (.spec.template.spec.containers[0].env | map(select(.name == "POD_NAME" or .name == "POD_UID") | .valueFrom.fieldRef.fieldPath) | join(",")) == "metadata.name,metadata.uid"' 'gateway mode: the controller knows its own pod, for the preflight'"'"'s bound token'
gwext="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set runController.model.mode=gateway \
  --set runController.model.gateway.service=agentgateway --set runController.model.gateway.namespace=ai \
  --set runController.model.gateway.ports.public=9080 --set runController.model.gateway.audiencePrefix=cnref.gw \
  --set-json 'runController.model.gateway.aliases={"public":["tier-light"]}')"
out="$gwext"
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (.model.gateway.url == "http://agentgateway.ai.svc.cluster.local" and .model.gateway.ports.public == 9080 and .model.gateway.audiencePrefix == "cnref.gw" and (.model.gateway.aliases.internal | length) == 0)' 'gateway mode: an external gateway by service and namespace'
for r in "$q" "$nonp"; do
  out="$r"
  ynone 'select(.metadata.name=="run-controller-token")' 'secret mode: no token Role'
  ynone 'select(.kind=="Deployment" and .metadata.name=="run-controller") | .spec.template.spec.containers[0].env[] | select(.name == "POD_NAME")' 'secret mode: the controller Deployment is unchanged'
  ynone 'select(.kind=="ValidatingAdmissionPolicy") | .spec.validations[] | select(.message | test("ServiceAccount tokens|never reads the model Secret"))' 'secret mode: the run-pod policy is unchanged'
done
bad 'runController.model.gateway.aliases.public is required' --set runController.model.mode=gateway

# v4 step 8 (D7, R4): the gateway is the model path. model.mode defaults to gateway; secret puts the
# provider key in the sandbox, so it is demo-only (demoValues, which values-quickstart.yaml sets) and
# on the banner as model_key_in_sandbox.
[[ "$(yq '.runController.model.mode' values.yaml)" == gateway ]] || { echo "FAIL: runController.model.mode does not default to gateway" >&2; fail=1; }
[[ "$(yq '.demoValues' values.yaml)" == false ]] || { echo "FAIL: demoValues does not default to false" >&2; fail=1; }
out="$ctl"
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (.model.mode == "gateway" and (has("modelSecret") | not))' 'the default model mode is gateway'
ykind ConfigMap 'select(.metadata.name == "room-broker-config") | .data."config.yaml" | from_yaml | (has("install") | not)' 'gateway mode: nothing on the banner'
out="$q"
ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | .modelSecret == "agent-model"' 'the quickstart keeps model.mode secret (a laptop without a gateway)'
ykind ConfigMap 'select(.metadata.name == "room-broker-config") | (.data."config.yaml" | from_yaml | .install.unsafe | join(",")) == "shared_git_token,model_key_in_sandbox"' 'model.mode secret is on the banner as model_key_in_sandbox'
bad 'runController.model.mode secret is demo-only (v4 D7): the provider key would sit in the sandbox' --set demoValues=false
if err="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set runController.model.mode=secret 2>&1 >/dev/null)"; then
  echo "FAIL: model.mode secret without demoValues must not render" >&2; fail=1
elif ! grep -qF 'runController.model.mode secret is demo-only' <<<"$err"; then
  echo "FAIL: model.mode secret without demoValues failed, but not for being demo-only: $err" >&2; fail=1
fi
out="$p"
ynone 'select(.kind == "ConfigMap" and .metadata.name == "room-broker-config") | .data."config.yaml" | from_yaml | select(has("install")) | .install' 'no run-controller: the model mode is not read, and nothing is on the banner'
bad "at '/demoValues'" --set demoValues=yes
bad "at '/runController/model/mode'" --set runController.model.mode=direct
bad "at '/runController/model/gateway/aliases/public/0'" --set runController.model.mode=gateway --set-json 'runController.model.gateway.aliases={"public":["gpt-4"]}'

# v4 D8: broker.oidc.existingSecret mounts the OIDC ids and fills the file paths the human block
# leaves unset; a path the block sets wins. projectIDFile is the zitadel preset's alone.
oidcv=(--set broker.runIssuer.mode=jwks --set broker.runIssuer.issuer=https://oidc --set broker.runIssuer.jwksURL=https://oidc/keys
       --set broker.database.brokerSecret=dsn --set broker.publicURL=https://rooms.example.com
       --set broker.human.issuer=https://idp.example.com --set broker.human.origin=https://rooms.example.com
       --set broker.human.groups.admin=a --set broker.human.groups.member=m --set runController.enabled=false)
ozit="$(helm template ap . -n agent-system "${imgs[@]}" "${oidcv[@]}" --set broker.oidc.existingSecret=room-broker-oidc)"
out="$ozit"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.human.clientIDFile == "/etc/room-broker/oidc/web-client-id" and .human.roomctlClientIDFile == "/etc/room-broker/oidc/cli-client-id" and .human.projectIDFile == "/etc/room-broker/oidc/project-id")' 'broker.oidc fills the three id files under the zitadel preset'
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.volumes[] | select(.name == "oidc") | .secret.secretName == "room-broker-oidc"' 'broker.oidc names its Secret'
ykind Deployment 'select(.metadata.name=="room-broker") | .spec.template.spec.containers[0].volumeMounts[] | select(.name == "oidc") | (.mountPath == "/etc/room-broker/oidc" and .readOnly == true)' 'broker.oidc mounts at /etc/room-broker/oidc'
ogen="$(helm template ap . -n agent-system "${imgs[@]}" "${oidcv[@]}" --set broker.oidc.existingSecret=room-broker-oidc \
  --set broker.human.preset=generic --set broker.human.rolesClaim=groups --set broker.human.clientIDFile=/c --set broker.oidc.keys.cli=maku)"
out="$ogen"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.human.clientIDFile == "/c" and .human.roomctlClientIDFile == "/etc/room-broker/oidc/maku" and (.human | has("projectIDFile") | not))' 'a set clientIDFile wins; generic takes no projectIDFile; a renamed key is followed'
out="$(helm template ap . -n agent-system "${imgs[@]}" "${oidcv[@]}" --set broker.human.clientIDFile=/c --set broker.human.projectIDFile=/p)"
ynone 'select(.kind=="Deployment") | .spec.template.spec.volumes[] | select(.name == "oidc")' 'no broker.oidc, no oidc volume'
if helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" --set broker.oidc.existingSecret=x >/dev/null 2>&1; then
  echo 'FAIL: broker.oidc in local mode must be refused' >&2; fail=1; fi

# v4 step 6 (D21, EX15): runController.tracing hands runs the OTLP/HTTP endpoint and opens the
# collector's port in each run's own policy; unset, the controller config carries no tracing block
for r in "$q" "$ctl" "$kn"; do
  out="$r"
  ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (has("tracing") | not)' 'tracing unset: the controller config has no tracing block'
done
trc=(--set runController.tracing.otlpEndpoint=http://collector.obs.svc.cluster.local:4318
     --set runController.tracing.collector.namespace=obs --set-json 'runController.tracing.collector.labels={"app.kubernetes.io/name":"collector"}')
tr="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" "${trc[@]}")"
trk="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" "${trc[@]}" --set networkPolicies.mode=kubernetes)"
for r in "$tr" "$trk"; do
  out="$r"
  ykind ConfigMap 'select(.metadata.name=="run-controller-config") | .data."config.yaml" | from_yaml | (.tracing.otlpEndpoint == "http://collector.obs.svc.cluster.local:4318" and .tracing.collector.namespace == "obs" and (.tracing.collector.labels | to_entries | map(.key + "=" + .value) | join(",")) == "app.kubernetes.io/name=collector")' 'tracing: the controller config carries the endpoint and the collector selector'
done
bad "tracing.collector: name the collector's pods (namespace, labels) so the run's policy can reach it" --set runController.tracing.otlpEndpoint=http://collector.obs.svc.cluster.local:4318
bad "tracing.collector: name the collector's pods (namespace, labels) so the run's policy can reach it" --set runController.tracing.otlpEndpoint=http://collector.obs.svc.cluster.local:4318 --set runController.tracing.collector.namespace=obs
bad 'runController.tracing.collector is set without runController.tracing.otlpEndpoint' --set runController.tracing.collector.namespace=obs
bad 'runController.tracing.collector.namespace "other" is not the endpoint'"'"'s namespace "obs"' "${trc[@]}" --set runController.tracing.collector.namespace=other
bad "at '/runController/tracing/otlpEndpoint'" --set runController.tracing.otlpEndpoint=collector.obs.svc.cluster.local:4317
bad 'runController.tracing.otlpEndpoint "http://collector.obs.svc.other.local:4318" is not in clusterDomain "cluster.local"' "${trc[@]}" --set runController.tracing.otlpEndpoint=http://collector.obs.svc.other.local:4318
bad 'runController.tracing.collector.labels: "io.kubernetes.pod.namespace" is not a pod label' "${trc[@]}" --set-json 'runController.tracing.collector.labels={"app.kubernetes.io/name":"collector","io.kubernetes.pod.namespace":"kube-system"}'
bad "at '/runController/tracing/otlpEndpoint'" --set runController.tracing.otlpEndpoint=http://collector.obs.svc.cluster.local
bad "at '/runController/tracing/otlpEndpoint'" --set runController.tracing.otlpEndpoint=https://otel.example.com:4318

# v4 D14: the DSNs are env, read once: broker.reloader annotates the Deployment for Stakater Reloader
out="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set broker.database.ownerSecret=owner-dsn --set broker.reloader=true)"
ykind Deployment 'select(.metadata.name=="room-broker") | .metadata.annotations."secret.reloader.stakater.com/reload" == "dsn,owner-dsn"' 'broker.reloader names both DSN Secrets'
out="$p"
ynone 'select(.kind=="Deployment" and .metadata.name=="room-broker") | .metadata.annotations | select(. != null)' 'no reloader, no Deployment annotation'

# v4 D24 (step 7 PR A): the preflight runner, off by default; on, eight objects from one pod template.
out="$q"
ynone 'select(.metadata.labels."app.kubernetes.io/name" == "preflight")' 'preflight off renders no preflight object'
pf="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set networkPolicies.mode=kubernetes \
  --set broker.database.ownerSecret=owner-dsn --set preflight.enabled=true --set preflight.strict=true \
  --set-json 'preflight.extraCRDs=[{"name":"gateways.gateway.networking.k8s.io","version":"v1"}]')"
out="$pf"
kinds="$(yq ea '[select(.metadata.labels."app.kubernetes.io/name" == "preflight") | .kind] | sort | join(",")' - <<<"$pf")"
[[ $kinds == "ClusterRole,ClusterRoleBinding,ConfigMap,CronJob,Job,NetworkPolicy,Role,RoleBinding,ServiceAccount" ]] ||
  { echo "FAIL: preflight renders $kinds" >&2; fail=1; }
job="$(yq ea 'select(.kind == "Job" and .metadata.labels."app.kubernetes.io/name" == "preflight") | .spec.template' - <<<"$pf")"
cron="$(yq ea 'select(.kind == "CronJob" and .metadata.labels."app.kubernetes.io/name" == "preflight") | .spec.jobTemplate.spec.template' - <<<"$pf")"
[[ -n $job && $job == "$cron" ]] || { echo "FAIL: the preflight Job's and CronJob's pod templates differ" >&2; fail=1; }
ykind Job 'select(.metadata.labels."app.kubernetes.io/name" == "preflight") | (.metadata.annotations."helm.sh/hook" == "test" and .metadata.annotations."helm.sh/hook-delete-policy" == "before-hook-creation,hook-succeeded" and .spec.backoffLimit == 0 and .spec.activeDeadlineSeconds == 300)' 'the test hook: deleted before a rerun and on success, never retried, bounded by preflight.timeout'
ykind CronJob 'select(.metadata.labels."app.kubernetes.io/name" == "preflight") | (.metadata.name == "ap-preflight" and .spec.suspend == true and .spec.jobTemplate.spec.ttlSecondsAfterFinished == 86400)' 'the CronJob <release>-preflight is suspended: maku preflight and Argo CD users create a Job from it'
ykind Job 'select(.metadata.labels."app.kubernetes.io/name" == "preflight") | .spec.template.spec | (.serviceAccountName == "ap-preflight" and .restartPolicy == "Never" and .enableServiceLinks == false and .securityContext.runAsNonRoot == true and .securityContext.seccompProfile.type == "RuntimeDefault")' 'the pod: its own ServiceAccount, never restarted, no service links, non-root under RuntimeDefault'
ykind Job 'select(.metadata.labels."app.kubernetes.io/name" == "preflight") | .spec.template.spec.containers[0] | [.securityContext.allowPrivilegeEscalation == false, .securityContext.readOnlyRootFilesystem, .securityContext.capabilities.drop[0] == "ALL", .resources.limits.memory != null, (.args | join(" ")) == "preflight --config /etc/maku-preflight/config.json --strict", (.image | test("maku-run-controller:"))] | all' 'the container: restricted, with limits, the run-controller image running preflight'
ynone 'select(.kind == "Role" and .metadata.labels."app.kubernetes.io/name" == "preflight") | .rules[] | select(.resourceNames == null or (.resourceNames | length) == 0)' 'the preflight Role never grants get on every Secret: resourceNames is never empty'
ykind Role 'select(.metadata.labels."app.kubernetes.io/name" == "preflight") | (.rules[0].resources[0] == "secrets" and (.rules[0].verbs | join(",")) == "get" and (.rules[0].resourceNames | join(",")) == "broker-ca,broker-cert,dsn,owner-dsn")' 'the Role gets exactly the registry Secrets the values enable, by name'
ykind ConfigMap 'select(.metadata.labels."app.kubernetes.io/name" == "preflight") | .data."config.json" | from_json | ((.cluster.secrets | map(.name) | sort | join(",")) == "broker-ca,broker-cert,dsn,owner-dsn" and (.cluster.secrets[] | select(.name == "dsn") | (.keys | join(","))) == "uri" and .cluster.runtimeClass == "gvisor" and .cluster.minKubernetes == "1.30" and (.cluster.crds | length) == 7 and (.components | length) == 0 and .timeout == "300s")' 'the config: the same Secrets with their keys, the RuntimeClass, the six CRDs and the extra one'
ykind ClusterRole 'select(.metadata.labels."app.kubernetes.io/name" == "preflight") | (.metadata.name == "maku-agent-system-ap-preflight" and (.rules[] | select(.resources[0] == "customresourcedefinitions") | (.resourceNames | length) == 7) and (.rules[] | select(.resources[0] == "runtimeclasses") | .resourceNames[0] == "gvisor") and (.rules[] | select(.nonResourceURLs != null) | .nonResourceURLs[0] == "/version"))' 'the ClusterRole: the named CRDs, the RuntimeClass and /version, under a release-unique name'
ykind NetworkPolicy 'select(.metadata.labels."app.kubernetes.io/name" == "preflight") | ((.spec.egress | length) == 3 and (.spec.egress[2].ports[0].port == 9090))' 'the preflight egress: DNS, the API server, the components on 9090'
ykind NetworkPolicy 'select(.metadata.name == "room-broker") | .spec.ingress[] | select(.from[0].podSelector.matchLabels."app.kubernetes.io/name" == "preflight") | .ports[0].port == 9090' 'the broker admits the preflight on 9090'
cnp="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set preflight.enabled=true)"
out="$cnp"
ykind CiliumNetworkPolicy 'select(.metadata.labels."app.kubernetes.io/name" == "preflight") | (.spec.egress | length) == 3' 'cilium mode: a CNP in place of the NetworkPolicy'
ynone 'select(.kind == "NetworkPolicy" and .metadata.labels."app.kubernetes.io/name" == "preflight")' 'cilium mode renders no NetworkPolicy for the preflight'
out="$(helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" --set preflight.enabled=true)"
ykind ConfigMap 'select(.metadata.labels."app.kubernetes.io/name" == "preflight") | .data."config.json" | from_json | (.cluster.unsafeAllowRunc == (.cluster.runtimeClass == ""))' 'the quickstart: the sandbox as the chart resolves it'

# both rendered configs load through the broker's real loader
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp" "$mgtmp"' EXIT
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$ozit" >"$tmp/oidc-zitadel.yaml"
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$ogen" >"$tmp/oidc-generic.yaml"
go run ./tests/loadconfig broker "$tmp/oidc-zitadel.yaml" "$tmp/oidc-generic.yaml" || fail=1
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$q" >"$tmp/quickstart.yaml"
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$p" >"$tmp/production.yaml"
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$fwd" >"$tmp/forwarded.yaml"
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$fca" >"$tmp/factory-ca.yaml"
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$fins" >"$tmp/factory-insecure.yaml"
yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml"' - <<<"$q" >"$tmp/rc-quickstart.yaml"
yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml"' - <<<"$ctl" >"$tmp/rc-production.yaml"
yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml"' - <<<"$pr" >"$tmp/rc-perrun.yaml"
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$uns" >"$tmp/unsafe.yaml"
# v4 step 5 PR C (D8): the GitHub account link by Secret. broker.githubLink.existingSecret mounts the
# client at /etc/room-broker/github-link and fills human.githubLink where the human block leaves it
# unset; the broker's egress reaches GitHub's site and API on 443.
ghl=(--set broker.githubLink.existingSecret=gh-link)
gl="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" "${ghl[@]}")" || { echo "FAIL: broker.githubLink does not render" >&2; fail=1; gl=''; }
out="$gl"
ykind Deployment 'select(.metadata.name=="room-broker") | ((.spec.template.spec.volumes[] | select(.name == "github-link") | .secret.secretName == "gh-link") and (.spec.template.spec.containers[0].volumeMounts[] | select(.name == "github-link") | (.mountPath == "/etc/room-broker/github-link" and .readOnly)))' 'broker.githubLink.existingSecret is mounted read-only at /etc/room-broker/github-link'
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | .human.githubLink | (.clientIDFile == "/etc/room-broker/github-link/client_id" and .clientSecretFile == "/etc/room-broker/github-link/client_secret" and .activeWindow == "168h" and (has("webURL") | not))' 'the chart fills human.githubLink from the Secret'
# fqdns: the GitHub hosts the broker's CNP names, comma-joined
fqdns() { yq ea 'select(.kind == "CiliumNetworkPolicy" and .metadata.name == "room-broker")' - <<<"$out" | yq '[.spec.egress[] | select(.toFQDNs != null) | .toFQDNs[].matchName] | join(",")' -; }
[[ "$(fqdns)" == "github.com,api.github.com" ]] || { echo "FAIL: mode cilium: the broker may reach github.com and api.github.com, got '$(fqdns)'" >&2; fail=1; }
ykind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | .spec.egress[] | select(.toFQDNs != null) | .toPorts[0].ports[0].port == "443"' 'only on 443'
gl2="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" "${ghl[@]}" --set broker.human.githubLink.clientIDFile=/mine \
  --set broker.githubLink.webURL=https://ghes.example.test --set broker.githubLink.activeWindow=24h)"
out="$gl2"
ykind ConfigMap 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | .human.githubLink | (.clientIDFile == "/mine" and .clientSecretFile == "/etc/room-broker/github-link/client_secret" and .webURL == "https://ghes.example.test" and .activeWindow == "24h")' 'a set human.githubLink.clientIDFile wins; webURL and activeWindow reach the config'
[[ "$(fqdns)" == "ghes.example.test" ]] || { echo "FAIL: a GHES: its one host, got '$(fqdns)'" >&2; fail=1; }
gk="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" "${ghl[@]}" --set networkPolicies.mode=kubernetes)" || { echo "FAIL: broker.githubLink in mode kubernetes does not render" >&2; fail=1; gk=''; }
out="$gk"
n443="$(yq ea 'select(.kind == "NetworkPolicy" and .metadata.name == "room-broker")' - <<<"$out" | yq '[.spec.egress[] | select(.ports[0].port == 443 and .to[0].ipBlock.cidr == "0.0.0.0/0")] | length' -)"
[[ $n443 == 1 ]] || { echo "FAIL: mode kubernetes: 443 to public addresses (NetworkPolicy names no host), got $n443 rules" >&2; fail=1; }
ykind NetworkPolicy 'select(.metadata.name=="room-broker") | .spec.egress[] | select(.ports[0].port == 443 and .to[0].ipBlock.cidr == "0.0.0.0/0") | (.to[0].ipBlock.except | contains(["10.0.0.0/8", "169.254.0.0/16"]))' 'the private ranges excluded'
# human.githubLink set directly, with no Secret: the link is on, so the egress is too
out="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set broker.human.githubLink.clientIDFile=/a --set broker.human.githubLink.clientSecretFile=/b)"
[[ "$(fqdns)" == "github.com,api.github.com" ]] || { echo "FAIL: human.githubLink set directly renders no GitHub egress, got '$(fqdns)'" >&2; fail=1; }
ynone 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.name == "github-link") | .name' 'human.githubLink set directly mounts no Secret'
out="$p"
ynone 'select(.kind == "Deployment") | .spec.template.spec.volumes[] | select(.name == "github-link") | .name' 'no githubLink, no mount'
ynone 'select(.kind == "CiliumNetworkPolicy") | .spec.egress[] | select(.toFQDNs != null) | .toPorts' 'no githubLink, no GitHub egress'
bad 'broker.githubLink.existingSecret: mode local has no GitHub account to link' --set broker.githubLink.existingSecret=gh-link
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$gl" >"$tmp/githublink.yaml"
go run ./tests/loadconfig broker "$tmp/githublink.yaml" || fail=1
go run ./tests/loadconfig broker "$tmp/quickstart.yaml" "$tmp/production.yaml" "$tmp/forwarded.yaml" "$tmp/factory-ca.yaml" "$tmp/factory-insecure.yaml" "$tmp/unsafe.yaml" || fail=1
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$si" >"$tmp/slackintents.yaml"
go run ./tests/loadconfig broker-slackintents "$tmp/slackintents.yaml" || fail=1
go run ./tests/loadconfig broker-planner-gateway "$mgtmp/planner-gateway.yaml" || fail=1
go run ./tests/loadconfig broker-slackintents-gateway "$mgtmp/slackintents-gateway.yaml" || fail=1
go run ./tests/loadconfig broker-roomtools-off "$tmp/quickstart.yaml" || fail=1
go run ./tests/loadconfig broker-roomtools-on "$tmp/production.yaml" || fail=1
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$rt" >"$tmp/roomtools-tokenreview.yaml"
yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml"' - <<<"$rt" >"$tmp/rc-roomtools.yaml"
go run ./tests/loadconfig broker-roomtools-tokenreview "$tmp/roomtools-tokenreview.yaml" || fail=1
yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml"' - <<<"$nonp" >"$tmp/rc-none.yaml"
yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml"' - <<<"$kn" >"$tmp/rc-kubernetes.yaml"
yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml"' - <<<"$tr" >"$tmp/rc-tracing.yaml"
yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml"' - <<<"$trk" >"$tmp/rc-tracing-kubernetes.yaml"
go run ./tests/loadconfig run-controller "$tmp/rc-quickstart.yaml" "$tmp/rc-production.yaml" "$tmp/rc-none.yaml" "$tmp/rc-roomtools.yaml" "$tmp/rc-kubernetes.yaml" "$tmp/rc-perrun.yaml" || fail=1
go run ./tests/loadconfig run-controller-tracing "$tmp/rc-tracing.yaml" "$tmp/rc-tracing-kubernetes.yaml" || fail=1
yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml"' - <<<"$gw" >"$tmp/rc-gateway.yaml"
yq 'select(.metadata.name=="run-controller-config") | .data."config.yaml"' - <<<"$gwext" >"$tmp/rc-gateway-external.yaml"
go run ./tests/loadconfig run-controller-gateway "$tmp/rc-gateway.yaml" "$tmp/rc-gateway-external.yaml" || fail=1

# v4 D9: bring your own Postgres. The quickstart renders no retention job; retentionSecret renders the
# CronJob reading that Secret's key as ROOMS_DATABASE_URL and nothing else, secretKey moves all three
# refs, caSecret mounts in the migrate init container, the broker and the job, and egress.cidrs points
# the broker's and the job's database egress at an external database in both network modes.
out="$q"
ynone 'select(.kind == "CronJob")' 'the quickstart renders no retention CronJob'
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set broker.database.retentionSecret=r)"
ykind CronJob 'select(.metadata.name == "room-broker-retention") | .spec.jobTemplate.spec.template.spec | ((.containers | length) == 1 and (.containers[0].args | join(",")) == "retention" and (.containers[0].env | ((length == 1) and .[0].name == "ROOMS_DATABASE_URL" and .[0].valueFrom.secretKeyRef.name == "r" and .[0].valueFrom.secretKeyRef.key == "uri")))' 'retentionSecret: the CronJob reads its uri as ROOMS_DATABASE_URL and nothing else'
ykind CronJob 'select(.metadata.name == "room-broker-retention") | (.spec.schedule == "17 3 * * *" and .spec.concurrencyPolicy == "Forbid" and .spec.jobTemplate.spec.activeDeadlineSeconds == 900 and .spec.jobTemplate.spec.template.spec.automountServiceAccountToken == false and (.spec.jobTemplate.spec.template.spec.containers[0].image | test("^ghcr[.]io/smana/maku-broker:")) and .spec.jobTemplate.spec.template.spec.containers[0].securityContext.readOnlyRootFilesystem == true and .spec.jobTemplate.spec.template.spec.containers[0].resources.limits.memory == "64Mi")' 'the CronJob: ANR schedule, Forbid, 900 s, the broker image, no token, restricted, limited'
ykind CiliumNetworkPolicy 'select(.metadata.name == "room-broker-retention") | ((.spec.ingress | length) == 1 and (.spec.ingress[0] | length) == 0 and (.spec.egress | length) == 2 and .spec.egress[1].toPorts[0].ports[0].port == "5432")' 'the CronJob has its own CNP: no ingress, DNS and the database'
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set broker.database.retentionSecret=r --set broker.database.secretKey=dsn --set broker.database.caSecret=db-ca)"
got=$(yq ea '[.. | select(has("secretKeyRef")) | .secretKeyRef | select(.name == "rooms-owner-dsn" or .name == "rooms-broker-dsn" or .name == "r") | .key] | join(",")' - <<<"$out")
[[ $got == "dsn,dsn,dsn" ]] || { echo "FAIL: secretKey dsn: the three DSN refs read '$got', want dsn,dsn,dsn" >&2; fail=1; }
got=$(yq ea '[.. | select(has("volumeMounts")) | .volumeMounts[] | select(.name == "db-ca") | .mountPath] | join(",")' - <<<"$out")
[[ $got == "/etc/room-broker/db-ca,/etc/room-broker/db-ca,/etc/room-broker/db-ca" ]] || { echo "FAIL: caSecret mounts in '$got', want the init container, the broker and the CronJob" >&2; fail=1; }
got=$(yq ea '[.. | select(has("volumes")) | .volumes[] | select(.name == "db-ca") | .secret.secretName + ":" + .secret.items[0].key] | join(",")' - <<<"$out")
[[ $got == "db-ca:ca.crt,db-ca:ca.crt" ]] || { echo "FAIL: caSecret volumes '$got', want db-ca's ca.crt in the broker and the CronJob" >&2; fail=1; }
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set broker.database.retentionSecret=r --set 'broker.database.egress.cidrs={10.20.0.0/16}')"
ykind CiliumNetworkPolicy 'select(.metadata.name == "room-broker" or .metadata.name == "room-broker-retention") | (.spec.egress | map(select(.toPorts[0].ports[0].port == "5432")) | ((length == 1) and (.[0].toCIDR | join(",")) == "10.20.0.0/16"))' 'egress.cidrs: the broker and the job reach the database at the CIDR (cilium)'
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set networkPolicies.mode=kubernetes --set broker.database.retentionSecret=r --set 'broker.database.egress.cidrs={10.20.0.0/16}')"
ykind NetworkPolicy 'select(.metadata.name == "room-broker" or .metadata.name == "room-broker-retention") | (.spec.egress | map(select(.ports[0].port == 5432)) | ((length == 1) and .[0].to[0].ipBlock.cidr == "10.20.0.0/16"))' 'egress.cidrs: the broker and the job reach the database at the CIDR (kubernetes)'
ykind NetworkPolicy 'select(.metadata.name == "room-broker-retention") | ((.spec.policyTypes | join(",")) == "Ingress,Egress" and (.spec | has("ingress") | not) and (.spec.egress | length) == 2)' 'the job: no ingress, DNS and the database (kubernetes)'
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set networkPolicies.mode=kubernetes --set broker.database.retentionSecret=r --set 'broker.database.podLabels.app=db')"
ykind NetworkPolicy 'select(.metadata.name == "room-broker-retention") | .spec.egress[1].to[0].podSelector.matchLabels.app == "db"' 'the job reaches the database by the broker'"'"'s podLabels'
# an IPv6 CIDR renders quoted in both modes (a bare "fd00::/8" is a YAML scalar only by luck)
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set broker.database.retentionSecret=r --set 'broker.database.egress.cidrs={fd00:db::/64}')"
ykind CiliumNetworkPolicy 'select(.metadata.name == "room-broker-retention") | .spec.egress[1].toCIDR[0] == "fd00:db::/64"' 'egress.cidrs: an IPv6 CIDR (cilium)'
grep -qF -- '- "fd00:db::/64"' <<<"$out" || { echo 'FAIL: the IPv6 CIDR is not quoted (cilium)' >&2; fail=1; }
out="$(helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set networkPolicies.mode=kubernetes --set broker.database.retentionSecret=r --set 'broker.database.egress.cidrs={fd00:db::/64}')"
ykind NetworkPolicy 'select(.metadata.name == "room-broker-retention") | .spec.egress[1].to[0].ipBlock.cidr == "fd00:db::/64"' 'egress.cidrs: an IPv6 CIDR (kubernetes)'
grep -qF -- '{cidr: "fd00:db::/64"}' <<<"$out" || { echo 'FAIL: the IPv6 CIDR is not quoted (kubernetes)' >&2; fail=1; }
if helm template ap . -n maku -f values-quickstart.yaml "${imgs[@]}" --set 'broker.database.egress.cidrs={db.example.com}' >/dev/null 2>&1; then
  echo 'FAIL: a database egress CIDR that is not one must be refused' >&2; fail=1; fi

# a bare tag is refused unless e2e.unpinnedImages
if helm template ap . -n x -f values-quickstart.yaml "${imgs[@]}" --set broker.image.tag=v1 >/dev/null 2>&1; then
  echo 'FAIL: a bare image tag must be refused' >&2; fail=1; fi
# v4 step 6 Task 12 (D19): broker.webProxy opens the human listener, :8080, to the web front door's
# pods alone, in both policy modes; off (the default), no rule admits :8080.
for m in cilium kubernetes; do
  out="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set networkPolicies.mode=$m)"
  jnone 'select(.kind == "NetworkPolicy" or .kind == "CiliumNetworkPolicy") | select(.metadata.name == "room-broker") | .spec.ingress[] | select(((.ports // []) | map(.port) | any(. == 8080)) or ((.toPorts // []) | map(.ports[].port) | any(. == "8080")))' "mode $m: without broker.webProxy a rule admits :8080"
  out="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set networkPolicies.mode=$m --set broker.webProxy=true)"
done
jkind NetworkPolicy 'select(.metadata.name == "room-broker") | [.spec.ingress[] | select((.ports // []) | map(.port) | any(. == 8080))] == [{"from": [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "rooms-web-proxy"}}}], "ports": [{"port": 8080, "protocol": "TCP"}]}]' 'mode kubernetes: broker.webProxy admits :8080 from the rooms-web-proxy pods alone'
out="$(helm template ap . -n agent-system "${imgs[@]}" "${prod[@]}" --set broker.webProxy=true)"
jkind CiliumNetworkPolicy 'select(.metadata.name == "room-broker") | [.spec.ingress[] | select((.toPorts // []) | map(.ports[].port) | any(. == "8080"))] == [{"fromEndpoints": [{"matchLabels": {"io.kubernetes.pod.namespace": "agent-system", "app.kubernetes.io/name": "rooms-web-proxy"}}], "toPorts": [{"ports": [{"port": "8080", "protocol": "TCP"}]}]}]' 'mode cilium: broker.webProxy admits :8080 from the rooms-web-proxy pods alone'
# v4 step 6 Task 13 (EX9): the broker's parity keys, rendered verbatim. Unset (every render above),
# none appears; tests/parity-values.yaml is cloud-native-ref gcp-0's three blocks.
ynone 'select(.metadata.name=="room-broker-config") | .data."config.yaml" | from_yaml | (.webhooks // .suspend // .policyManagement // .coordination) // ""' 'no parity key set, yet one is in the config'
pv=("${imgs[@]}" "${prod[@]}" -f tests/parity-values.yaml)
for m in cilium kubernetes; do
  out="$(helm template ap . -n agent-system "${pv[@]}" --set networkPolicies.mode=$m)" || { echo "FAIL: the parity values do not render in mode $m" >&2; fail=1; out=''; }
  [[ $m == cilium ]] && pc="$out" || pk="$out"
done
out="$pc"
cfg="$(yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$out")"
[[ $(yq -o=json -I0 '[.webhooks[] | .name + "=" + .secretFile]' <<<"$cfg") == '["slack-app=/etc/room-broker/webhooks/slack-app/signing-key","runlore=/etc/runlore-key/signing-key","pager=/etc/room-broker/webhooks/pager/hmac"]' ]] ||
  { echo "FAIL: each webhook's secretFile: the mount's path, or the set one ($(yq -o=json -I0 '.webhooks' <<<"$cfg"))" >&2; fail=1; }
[[ $(yq -o=json -I0 '[.webhooks[] | keys | .[]] | unique' <<<"$cfg") == '["events","generation","name","network","repositories","secretFile","url"]' ]] ||
  { echo "FAIL: a chart-only webhook key (existingSecret, key, networkPolicy) reached the config: $(yq -o=json -I0 '[.webhooks[] | keys | .[]] | unique' <<<"$cfg")" >&2; fail=1; }
yq -o=json -I0 '[.suspend, .policyManagement, .coordination]' <<<"$cfg" | jq -e '. == [{"enabled":true,"after":"5m"},{"promotion":true,"operators":["Smana"]},{"mode":"off","maxChildren":2}]' >/dev/null ||
  { echo "FAIL: suspend, policyManagement and coordination are not verbatim" >&2; fail=1; }
# defaultMode 0440: yq's JSON spells the YAML octal as 440.
jkind Deployment 'select(.metadata.name=="room-broker") | ([.spec.template.spec.volumes[] | select(.name | startswith("webhook-")) | {(.name): .secret}] | add) == {"webhook-slack-app": {"secretName": "room-broker-webhook-slack-app", "defaultMode": 440}, "webhook-pager": {"secretName": "pager-key", "defaultMode": 440}}' 'one whole-Secret volume (0440) per webhook with existingSecret'
jkind Deployment 'select(.metadata.name=="room-broker") | [.spec.template.spec.containers[0].volumeMounts[] | select(.name | startswith("webhook-")) | .name + ":" + .mountPath + ":" + (.readOnly | tostring)] == ["webhook-slack-app:/etc/room-broker/webhooks/slack-app:true", "webhook-pager:/etc/room-broker/webhooks/pager:true"]' 'mounted read-only at /etc/room-broker/webhooks/<name>'
jkind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | [.spec.egress[] | select(.toPorts[0].ports[0].port == "8081")] == [{"toEndpoints": [{"matchLabels": {"io.kubernetes.pod.namespace": "agent-system", "app.kubernetes.io/name": "factory-slack-app"}}], "toPorts": [{"ports": [{"port": "8081", "protocol": "TCP"}]}]}]' 'cilium: the Slack App subscriber opens :8081 on the App pods, by default'
jkind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | [.spec.egress[] | select(.toPorts[0].ports[0].port == "8080")] == [{"toEndpoints": [{"matchLabels": {"io.kubernetes.pod.namespace": "runlore", "app.kubernetes.io/name": "runlore"}}], "toPorts": [{"ports": [{"port": "8080", "protocol": "TCP"}]}]}]' "cilium: a cluster subscriber opens its URL's port to its networkPolicy.to only"
jkind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | [.spec.egress[] | select(.toFQDNs == [{"matchName": "hooks.example.com"}])] | length == 1 and .[0].toPorts == [{"ports": [{"port": "443", "protocol": "TCP"}]}]' 'cilium: an internet subscriber opens 443 to its host'
out="$pk"
jkind NetworkPolicy 'select(.metadata.name=="room-broker") | [.spec.egress[] | select(.ports == [{"port": 8081, "protocol": "TCP"}])] == [{"to": [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "agent-system"}}, "podSelector": {"matchLabels": {"app.kubernetes.io/name": "factory-slack-app"}}}], "ports": [{"port": 8081, "protocol": "TCP"}]}]' 'kubernetes: the Slack App subscriber opens :8081 on the App pods'
jkind NetworkPolicy 'select(.metadata.name=="room-broker") | [.spec.egress[] | select(.ports == [{"port": 443, "protocol": "TCP"}] and (.to[0].ipBlock.cidr // "") == "0.0.0.0/0")] | length == 1 and (.[0].to[0].ipBlock.except | index("10.0.0.0/8") != null)' 'kubernetes: an internet subscriber opens 443 to public addresses, privateCIDRs excepted'
msg="$(helm template ap . -n agent-system "${pv[@]}" --set networkPolicies.mode=kubernetes --set-json 'broker.webhooks[1].networkPolicy.to=[]' 2>&1 >/dev/null || true)"
grep -qF 'broker.webhooks[runlore].networkPolicy.to is empty: name the subscriber' <<<"$msg" || { echo "FAIL: a cluster subscriber with no peers does not fail (got: $msg)" >&2; fail=1; }
# the review of #556: a NetworkPolicy peer in cilium mode is refused; a long name's volume is cut to 63
msg="$(helm template ap . -n agent-system "${pv[@]}" --set networkPolicies.mode=cilium --set-json 'broker.webhooks[1].networkPolicy.to=[{"podSelector":{"matchLabels":{"app":"runlore"}}}]' 2>&1 >/dev/null || true)"
grep -qF 'broker.webhooks[runlore].networkPolicy.to holds a NetworkPolicy peer' <<<"$msg" || { echo "FAIL: a podSelector in cilium mode does not fail (got: $msg)" >&2; fail=1; }
long=a23456789012345678901234567890123456789012345678901234567890b
out="$(helm template ap . -n agent-system "${pv[@]}" --set "broker.webhooks[2].name=$long")"
jkind Deployment 'select(.metadata.name=="room-broker") | [.spec.template.spec.volumes[] | select(.secret.secretName == "pager-key") | .name] | (length == 1 and (.[0] | length) <= 63 and (.[0] | startswith("webhook-a2345")))' 'a long webhook name'"'"'s volume is cut to 63 characters'
jkind Deployment 'select(.metadata.name=="room-broker") | [.spec.template.spec.containers[0].volumeMounts[] | select(.mountPath == "/etc/room-broker/webhooks/'"$long"'") | .name | length] == [63]' 'its mount names the same volume, at /etc/room-broker/webhooks/<name>'
out="$(helm template ap . -n agent-system "${pv[@]}" --set 'broker.webhooks[0].url=http://factory-slack-app:8081/webhooks')"
jkind CiliumNetworkPolicy 'select(.metadata.name=="room-broker") | [.spec.egress[] | select(.toPorts[0].ports[0].port == "8081") | .toEndpoints[0].matchLabels."io.kubernetes.pod.namespace"] == ["agent-system"]' "a short-name App URL defaults to the App's pods in the release namespace"
mkdir -p "$tmp/parity"
yq 'select(.metadata.name=="room-broker-config") | .data."config.yaml"' - <<<"$pc" >"$tmp/parity/broker.yaml"
go run ./tests/loadconfig broker-parity "$tmp/parity/broker.yaml" || fail=1
helm template ap . -n x -f values-quickstart.yaml --set e2e.unpinnedImages=true --set broker.image.tag=e2e --set runController.image.tag=e2e \
  --set runController.images.harness.tag=e2e --set runController.images.bridge.tag=e2e \
  | grep -q 'imagePullPolicy: Never' || { echo 'FAIL: e2e.unpinnedImages renders Never' >&2; fail=1; }
[ "$fail" -eq 0 ] && echo PASS
exit "$fail"
