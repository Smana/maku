#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Renders maku-gateway and asserts its contract (docs/gateway.md; plan v4 step 4, GW2 to GW13): off
# by default, strict values, each provider's key read from a Secret by name, the presets' quirks
# that values cannot undo, each caller's token verified per listener and per alias, and the catalog
# ANR generates today. The golden minimal.yaml pins the smallest render; UPDATE_GOLDEN=1 rewrites it.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
fail=0
out=''
minimal=(-f tests/values-minimal.yaml)
cnref=(-f tests/values-cnref-like.yaml)
render() { helm template mgw . -n maku "$@"; }
# rejects CASE WANT ARGS...: the render fails, and its error contains WANT.
rejects() {
  local name=$1 want=$2 msg; shift 2
  if msg="$(render "$@" 2>&1 >/dev/null)"; then
    echo "FAIL: $name renders; it must fail with: $want" >&2; fail=1; return
  fi
  grep -qF -- "$want" <<<"$msg" || { echo "FAIL: $name fails without '$want' (got: $msg)" >&2; fail=1; }
}
# ykind KIND 'expr' 'message': every KIND document's expr is true, and there is at least one.
ykind() {
  yq ea -e "[select(.kind == \"$1\") | ($2)] | (length > 0 and all)" - <<<"$out" >/dev/null 2>&1 || { echo "FAIL: $3" >&2; fail=1; }
}
# yone 'expr' 'message': the query over every document yields exactly one true.
yeq() {
  local got
  got="$(yq ea "$1" - <<<"$out" 2>/dev/null)" || got="<yq error>"
  [[ $got == "$2" ]] || { echo "FAIL: $3 (got '$got', want '$2')" >&2; fail=1; }
}

# ---- Task 2: values, schema and presets (GW6, GW7, D14)
[[ -z "$(render | grep -E '^kind:' || true)" ]] || { echo "FAIL: the default values render objects" >&2; fail=1; }
rejects 'enabled without an issuer' 'identity.issuer is required: the gateway verifies each caller'"'"'s token' --set enabled=true
rejects 'an alias outside the AgentRun enum' 'tier-huge' "${minimal[@]}" --set aliases.public.tier-huge.provider=stub --set aliases.public.tier-huge.model=m
rejects 'a provider alias on internal' 'kimi-frontier' "${minimal[@]}" --set aliases.internal.kimi-frontier.provider=stub --set aliases.internal.kimi-frontier.model=m
rejects 'an internal alias on a public-only provider (GW6)' 'aliases.internal.tier-light names provider moonshot, whose dataClasses ([public]) exclude internal' \
  "${minimal[@]}" --set providers.moonshot.preset=moonshot --set providers.moonshot.existingSecret=k \
  --set aliases.internal.tier-light.provider=moonshot --set aliases.internal.tier-light.model=k3.1-pro
rejects 'a transformation the preset sets (GW7)' 'providers.mistral.transformations.reasoning_effort: the mistral preset sets it (' \
  "${minimal[@]}" --set providers.mistral.preset=mistral --set providers.mistral.existingSecret=k \
  --set 'providers.mistral.transformations[0].field=reasoning_effort' --set 'providers.mistral.transformations[0].expression="high"'
rejects 'a transformation of the forced model' 'providers.stub.transformations.model: the chart sets it' \
  "${minimal[@]}" --set 'providers.stub.transformations[0].field=model' --set 'providers.stub.transformations[0].expression="x"'
rejects 'a classifier on a model that always reasons (GW7)' 'clients.classifier: zai/glm-5.3 always reasons' \
  "${minimal[@]}" --set providers.zai.preset=zai --set providers.zai.existingSecret=k \
  --set clients.classifier.enabled=true --set clients.classifier.provider=zai --set clients.classifier.model=glm-5.3
rejects 'a classifier on a moonshot model (every model reasons)' 'clients.classifier: moonshot/k3.1-turbo always reasons' \
  "${minimal[@]}" --set providers.moonshot.preset=moonshot --set providers.moonshot.existingSecret=k \
  --set clients.classifier.enabled=true --set clients.classifier.provider=moonshot --set clients.classifier.model=k3.1-turbo
rejects 'a provider without existingSecret (D14)' 'providers.other.existingSecret is required' \
  "${minimal[@]}" --set providers.other.preset=openai
rejects 'an unknown provider key' "additional properties 'apiKey' not allowed" "${minimal[@]}" --set providers.stub.apiKey=sk-plain
rejects 'an unknown preset' 'value must be one of' "${minimal[@]}" --set providers.stub.preset=bedrock
rejects 'an alias naming no provider' 'aliases.public.tier-standard names provider nope, which providers does not define' \
  "${minimal[@]}" --set aliases.public.tier-standard.provider=nope --set aliases.public.tier-standard.model=m
rejects 'a custom provider without its endpoint' 'providers.bare: preset custom needs host, port and path' \
  "${minimal[@]}" --set providers.bare.preset=custom --set providers.bare.existingSecret=k
rejects 'an endpoint on a preset that sets it' 'providers.zai.host: only preset custom takes an endpoint' \
  "${minimal[@]}" --set providers.zai.preset=zai --set providers.zai.existingSecret=k --set providers.zai.host=x.test
rejects 'a client without its model' 'clients.planner needs provider and model' "${minimal[@]}" --set clients.planner.enabled=true
helm lint --strict . "${minimal[@]}" >/dev/null || { echo "FAIL: helm lint --strict with the minimal values" >&2; fail=1; }
out="$(render "${minimal[@]}")"
if [[ ${UPDATE_GOLDEN:-} == 1 ]]; then printf '%s\n' "$out" >tests/golden/minimal.yaml; fi
diff -u tests/golden/minimal.yaml - <<<"$out" >&2 || { echo "FAIL: the minimal render differs from tests/golden/minimal.yaml" >&2; fail=1; }
# no provider key is ever rendered: no Secret, no plaintext key field
yeq '[select(.kind == "Secret")] | length' 0 'the chart renders no Secret'
out="$(render "${cnref[@]}")"
yeq '[select(.kind == "Secret")] | length' 0 'the cnref-like values render no Secret'

# ---- Task 3: the Gateway, its parameters and the identity policies (GW3, GW4, GW8)
out="$(render "${minimal[@]}" --set runs.namespace=runs-x --set runs.namePrefix=r.n-)"
pol() { echo "select(.kind == \"AgentgatewayPolicy\" and .metadata.name == \"model-gateway-$1\")"; }
ykind Gateway '(.metadata.name == "model-gateway") and (.spec.gatewayClassName == "agentgateway")' 'the Gateway model-gateway on class agentgateway'
yeq 'select(.kind == "Gateway") | [.spec.listeners[] | .name + ":" + (.port | tostring)] | join(",")' 'public:8080,internal:8081,clients:8082' 'three listeners'
yeq 'select(.kind == "Gateway") | [.spec.listeners[] | .allowedRoutes.namespaces.from] | unique | join(",")' 'Same' 'routes from the release namespace only'
yeq 'select(.kind == "Gateway") | .spec.infrastructure.parametersRef.name' 'model-gateway' 'the Gateway names its parameters'
ykind AgentgatewayParameters '(.spec.image.digest == "sha256:9d3e6044ddcdc0878b1787f77bd401252b95e22684203fb5e874c4c42d2ed90c") and (.spec.image.tag == "v1.6.0")' 'the proxy image by digest'
ykind AgentgatewayParameters '(.spec.deployment.spec.template.spec.securityContext.seccompProfile.type == "RuntimeDefault") and (.spec.deployment.spec.template.spec.containers[0].livenessProbe.httpGet.port == 15021)' 'seccomp and the liveness probe'
ykind AgentgatewayParameters '(.spec.shutdown.min == 10) and (.spec.shutdown.max == 660) and (.spec.service.spec.type == "ClusterIP") and (.spec.podDisruptionBudget.spec.minAvailable == 1)' 'shutdown window, ClusterIP, PDB'
ykind AgentgatewayParameters '(.spec.resources.limits.memory == "1Gi") and (.spec.deployment.spec.replicas == 2)' 'limits and two replicas'
ykind AgentgatewayParameters '(.spec.deployment.spec.template.spec.topologySpreadConstraints | map(.topologyKey) | join(",")) == "topology.kubernetes.io/zone,kubernetes.io/hostname"' 'zone and host spread'
ykind AgentgatewayParameters '.spec.modelCatalog.sources[0].configMap.name == "model-gateway-model-catalog"' 'the catalog source'
for l in public internal; do
  yeq "$(pol "listener-$l") | .spec.traffic.authorization.policy.matchExpressions | join(\" && \")" \
    "jwt.sub.matches(\"^system:serviceaccount:runs-x:r\\\\.n-[a-z2-7]{8}\$\") && jwt.aud.exists(a, a.startsWith(\"maku-gateway.$l.\"))" \
    "listener-$l requires the run sub (QuoteMeta'd) and its audience prefix"
  yeq "$(pol "listener-$l") | .spec.targetRefs[0].sectionName" "$l" "listener-$l targets its listener"
  yeq "$(pol "listener-$l") | .spec.traffic.timeouts.request" 600s "listener-$l: the runs' 600s timeout"
done
yeq "$(pol listener-clients) | .spec.traffic.authorization.policy.matchExpressions | join(\" && \")" \
  'jwt.sub == "system:serviceaccount:maku:room-broker" && jwt.aud.exists(a, a.startsWith("maku-gateway.client."))' 'the clients listener requires the broker sub'
yeq "$(pol listener-clients) | .spec.traffic.timeouts.request" 60s 'the clients 60s timeout'
yeq "[select(.kind == \"AgentgatewayPolicy\") | .spec.traffic.jwtAuthentication | select(. != null)] | length" 3 'three listeners verify a token'
yeq "[select(.kind == \"AgentgatewayPolicy\") | .spec.traffic.jwtAuthentication | select(. != null) | select(.preserveToken == true)] | length" 0 'no listener preserves the token'
yeq "[select(.kind == \"AgentgatewayPolicy\") | .spec.traffic.jwtAuthentication | select(. != null) | .mode + \" \" + (.providers[0].validation.requiredClaims | join(\",\")) + \" \" + .providers[0].jwks.remote.url] | unique | join(\";\")" \
  'Strict exp,sub https://issuer.example.test/openid/v1/jwks' 'strict, exp and sub required, the JWKS URL'
yeq "[select(.kind == \"AgentgatewayPolicy\") | .spec.traffic.transformation.request.set[]? | select(.name == \"x-ar-agent\") | .value] | unique | join(\",\")" 'jwt.sub' 'x-ar-agent is the verified sub'
yeq "$(pol frontend) | [.spec.frontend.metrics.attributes.add[] | .name + \"=\" + .expression] | join(\",\")" 'ar_agent=jwt.sub' 'metrics labelled ar_agent'
yeq "$(pol frontend) | [.spec.frontend.accessLog.attributes.add[] | .name + \"=\" + .expression] | join(\",\")" 'x_ar_agent=jwt.sub,cost=flatten(llm.cost)' 'the access log carries x_ar_agent and cost'
yeq "$(pol frontend) | .spec.frontend.http.maxBufferSize" 8Mi 'the 8Mi buffer'
yeq "$(pol strip-identity-headers) | .spec.traffic.phase + \" \" + (.spec.traffic.transformation.request.remove | join(\",\"))" \
  'PreRouting x-ar-agent,x-ar-human,x-ai-gateway-client-id,agent-session-id' 'the identity headers are stripped before routing'
yeq '[select(.kind == "ClusterRoleBinding")] | length' 0 'source url renders no discovery binding'
out="$(render "${minimal[@]}" --set identity.jwks.source=apiserver --set identity.jwks.allowAnonymousDiscovery=true --set clusterDomain=k.test)"
yeq "$(pol listener-public) | .spec.traffic.jwtAuthentication.providers[0].jwks.remote | .backendRef.kind + \"/\" + .backendRef.name + \" \" + .jwksPath" \
  'AgentgatewayBackend/model-gateway-jwks openid/v1/jwks' 'source apiserver reads the JWKS through a backend'
ykind AgentgatewayBackend 'select(.metadata.name == "model-gateway-jwks") | ((.spec.static.host == "kubernetes.default.svc.k.test") and (.spec.static.port == 443) and (.spec.policies.tls.caCertificateRefs[0].name == "kube-root-ca.crt"))' 'the apiserver backend, verified with kube-root-ca.crt'
ykind ClusterRoleBinding '(.roleRef.name == "system:service-account-issuer-discovery") and (.subjects[0].name == "system:unauthenticated")' 'the anonymous discovery binding'
rejects 'source apiserver without the binding' 'identity.jwks.source apiserver needs identity.jwks.allowAnonymousDiscovery: true' "${minimal[@]}" --set identity.jwks.source=apiserver
rejects 'source url without a url' 'identity.jwks.url is required' "${minimal[@]}" --set identity.jwks.url=
rejects 'an image without a digest' 'image.digest is required' "${minimal[@]}" --set image.digest=
out="$(render "${minimal[@]}" --set replicas=1)"
yeq '[select(.kind == "AgentgatewayParameters") | .spec.podDisruptionBudget] | map(select(. != null)) | length' 0 'one replica: no PDB (it would block every drain)'

[[ $fail == 0 ]] || exit 1
echo "maku-gateway chart render checks passed"
