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
rejects 'plannerInternal on a public-only provider (v5 P0-T1)' 'clients.plannerInternal names provider stub, whose dataClasses ([public]) exclude internal' \
  "${minimal[@]}" --set clients.plannerInternal.enabled=true --set clients.plannerInternal.provider=stub --set clients.plannerInternal.model=m
out="$(render "${minimal[@]}" --set 'providers.stub.dataClasses={public,internal}' \
  --set clients.plannerInternal.enabled=true --set clients.plannerInternal.provider=stub --set clients.plannerInternal.model=internal-m)"
yeq 'select(.kind == "HTTPRoute" and .metadata.name == "maku-clients-planner-internal") | (.spec.parentRefs[0].sectionName + " " + .spec.rules[0].matches[0].path.value + " " + .spec.rules[0].backendRefs[0].name)' \
  'clients /clients/planner-internal/v1/chat/completions stub-planner-internal' 'plannerInternal renders its route and backend on clients'
yeq 'select(.kind == "AgentgatewayPolicy" and .metadata.name == "maku-clients-planner-internal") | .spec.traffic.authorization.policy.matchExpressions[0]' \
  '"maku-gateway.client.planner-internal" in jwt.aud' 'plannerInternal pins its own audience'
# golden NAME ARGS...: the render equals tests/golden/NAME.yaml (UPDATE_GOLDEN=1 rewrites it; review the diff).
golden() {
  local name=$1; shift
  out="$(render "$@")"
  if [[ ${UPDATE_GOLDEN:-} == 1 ]]; then printf '%s\n' "$out" >"tests/golden/$name.yaml"; fi
  diff -u "tests/golden/$name.yaml" - <<<"$out" >&2 || { echo "FAIL: the $name render differs from tests/golden/$name.yaml" >&2; fail=1; }
}
for f in minimal cnref-like; do
  helm lint --strict . -f "tests/values-$f.yaml" >/dev/null || { echo "FAIL: helm lint --strict with tests/values-$f.yaml" >&2; fail=1; }
  golden "$f" -f "tests/values-$f.yaml"
  # no provider key is ever rendered
  yeq '[select(.kind == "Secret")] | length' 0 "the $f values render no Secret"
done

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
# source inline: the issuer's public keys in the values, no anonymous access to the apiserver
jwks='{"keys":[{"kty":"RSA","kid":"k1","n":"AQAB","e":"AQAB","use":"sig","alg":"RS256"}]}'
inline="$(mktemp)"; JWKS="$jwks" yq -n '.identity.jwks = {"source": "inline", "inline": strenv(JWKS)}' >"$inline"
out="$(render "${minimal[@]}" -f "$inline")"; rm -f "$inline"
yeq "$(pol listener-public) | .spec.traffic.jwtAuthentication.providers[0].jwks.inline" "$jwks" 'source inline puts the keyset in the policy'
yeq '[select(.kind == "ClusterRoleBinding" or (.kind == "AgentgatewayBackend" and .metadata.name == "model-gateway-jwks"))] | length' 0 'source inline: no discovery binding, no apiserver backend'
rejects 'source inline without a keyset' 'identity.jwks.inline is required with identity.jwks.source inline' "${minimal[@]}" --set identity.jwks.source=inline
rejects 'an image without a digest' 'image.digest is required' "${minimal[@]}" --set image.digest=
out="$(render "${minimal[@]}" --set replicas=1)"
yeq '[select(.kind == "AgentgatewayParameters") | .spec.podDisruptionBudget] | map(select(. != null)) | length' 0 'one replica: no PDB (it would block every drain)'

# ---- Task 4: providers, alias routes, client routes and the catalog (GW5, GW7, GW8, GW12)
out="$(render "${cnref[@]}")"
be() { echo "select(.kind == \"AgentgatewayBackend\" and .metadata.name == \"$1\")"; }
route() { echo "select(.kind == \"HTTPRoute\" and .metadata.name == \"$1\")"; }
rpol() { echo "select(.kind == \"AgentgatewayPolicy\" and .spec.targetRefs[0].kind == \"HTTPRoute\" and .spec.targetRefs[0].name == \"$1\")"; }
k='moonshot-kimi-frontier'
yeq "$(be $k) | .spec.ai.provider.custom.providerOverride + \" \" + .spec.ai.provider.host + \":\" + (.spec.ai.provider.port | tostring) + .spec.ai.provider.custom.formats[0].path" \
  'moonshot api.kimi.ai:443/coding/v1/chat/completions' 'the moonshot preset: the Kimi Code host and path'
yeq "$(be $k) | .spec.policies.ai.modelAliases | to_entries | map(.key + \"=\" + .value) | join(\",\")" 'kimi-frontier=k3.1-pro' 'kimi-frontier maps to k3.1-pro'
yeq "$(be $k) | [.spec.policies.ai.transformations[] | .field + \"=\" + .expression] | join(\",\")" \
  'model="kimi-frontier",temperature=1,top_p=null' 'the model forced to the alias, then the preset temperature and top_p'
yeq "$(be $k) | .spec.policies.auth.secretRef.name + \"/\" + .spec.policies.auth.secretRef.key + \" \" + .spec.policies.tls.sni" \
  'agents-moonshot-api-key/apiKey api.kimi.ai' "the key by Secret name, TLS to the provider's host"
yeq "$(be mistral-mistral-light) | [.spec.policies.ai.transformations[] | .field + \"=\" + .expression] | join(\",\")" \
  'model="mistral-light",reasoning_effort="none"' "the mistral preset's reasoning_effort"
yeq "$(be anthropic-tier-light) | (.spec.ai.provider | keys | join(\",\")) + \" \" + .spec.policies.ai.modelAliases.\"tier-light\"" \
  'anthropic claude-haiku-5-5' 'the native anthropic provider'
yeq "[select(.kind == \"AgentgatewayBackend\") | .metadata.name] | sort | join(\",\")" \
  'anthropic-agent-default,anthropic-tier-frontier,anthropic-tier-light,anthropic-tier-standard,mistral-mistral-light,mistral-mistral-standard,moonshot-kimi-frontier,zai-agent-default,zai-planner,zai-tier-frontier,zai-tier-light,zai-tier-standard' \
  'one backend per alias row and per client'
r='maku-models-public-kimi-frontier'
# Exact paths only: with the gateway's key, a caller reaches the provider's chat endpoint and nothing else.
yeq "$(route $r) | .spec.parentRefs[0].name + \"/\" + .spec.parentRefs[0].sectionName + \" \" + ([.spec.rules[] | .matches[0].path.type + \" \" + .matches[0].path.value + \">\" + .filters[0].urlRewrite.path.type + \" \" + .filters[0].urlRewrite.path.replaceFullPath + \">\" + .backendRefs[0].name] | join(\",\"))" \
  'model-gateway/public Exact /m/kimi-frontier/v1/chat/completions>ReplaceFullPath /v1/chat/completions>moonshot-kimi-frontier' \
  'the alias route matches its chat path exactly, rewritten to the backend route'
yeq "$(route maku-models-internal-tier-light) | [.spec.rules[] | .matches[0].path.value + \">\" + .filters[0].urlRewrite.path.replaceFullPath] | join(\",\")" \
  '/m/tier-light/v1/chat/completions>/v1/chat/completions,/m/tier-light/v1/messages>/v1/messages' 'an anthropic alias also serves Messages, exactly'
yeq "[select(.kind == \"HTTPRoute\") | .spec.rules[].matches[].path.type] | unique | join(\",\")" 'Exact' 'no route matches a prefix'
yeq "$(rpol $r) | .spec.traffic.authorization.action + \" \" + (.spec.traffic.authorization.policy.matchExpressions | join(\",\"))" \
  'Require "maku-gateway.public.kimi-frontier" in jwt.aud,jwt.sub.matches("^system:serviceaccount:agents:run-[a-z2-7]{8}$")' \
  "the route requires the alias's audience and, again, a run's subject"
yeq "[select(.kind == \"HTTPRoute\") | .metadata.name] | sort | join(\",\")" \
  'maku-clients-planner,maku-models-internal-agent-default,maku-models-internal-list,maku-models-internal-tier-frontier,maku-models-internal-tier-light,maku-models-internal-tier-standard,maku-models-public-agent-default,maku-models-public-kimi-frontier,maku-models-public-list,maku-models-public-mistral-light,maku-models-public-mistral-standard,maku-models-public-tier-frontier,maku-models-public-tier-light,maku-models-public-tier-standard' \
  "a route per listener's alias rows, none for an alias the listener does not serve"
yeq "[select(.kind == \"AgentgatewayPolicy\" and .spec.targetRefs[0].kind == \"HTTPRoute\" and .spec.traffic.authorization != null)] | length" 12 'every alias and client route requires its audience'
yeq "$(route maku-models-public-list) | [.spec.rules[0].matches[] | .path.type + \" \" + .path.value] | join(\",\")" \
  'Exact /m/agent-default/v1/models,Exact /m/kimi-frontier/v1/models,Exact /m/mistral-light/v1/models,Exact /m/mistral-standard/v1/models,Exact /m/tier-frontier/v1/models,Exact /m/tier-light/v1/models,Exact /m/tier-standard/v1/models' \
  "a model listing is matched exactly, per alias"
yeq "$(rpol maku-models-public-list) | (.spec.traffic.directResponse.status | tostring) + \" \" + .spec.traffic.directResponse.body" \
  '404 {"error":"model listing is not served on the model gateway"}' 'and answered 404'
yeq "$(route maku-clients-planner) | .spec.parentRefs[0].sectionName + \" \" + .spec.rules[0].matches[0].path.value + \" \" + .spec.rules[0].backendRefs[0].name" \
  'clients /clients/planner/v1/chat/completions zai-planner' "the planner's route on the clients listener"
yeq "$(be zai-planner) | ([.spec.policies.ai.transformations[] | .field + \"=\" + .expression] | join(\",\")) + \" \" + .spec.policies.ai.modelAliases.\"tier-frontier\"" \
  'model="tier-frontier" glm-5.3' 'the planner forced to tier-frontier, mapped to glm-5.3'
yeq "$(rpol maku-clients-planner) | .spec.traffic.authorization.policy.matchExpressions | join(\",\")" \
  '"maku-gateway.client.planner" in jwt.aud,jwt.sub == "system:serviceaccount:maku:room-broker"' "the planner's route requires its audience and the broker's subject"
cat="$(yq ea 'select(.kind == "ConfigMap" and .metadata.name == "model-gateway-model-catalog") | .data."catalog.json"' - <<<"$out")"
want="$(yq '.data."catalog.json"' tests/golden/anr-model-catalog.yaml)"
[[ "$(jq -S . <<<"$cat")" == "$(jq -S . <<<"$want")" ]] || { echo "FAIL: the catalog differs from ANR's model-catalog.yaml:" >&2; diff <(jq -S . <<<"$want") <(jq -S . <<<"$cat") >&2; fail=1; }
rejects 'a price no alias serves (GW12)' 'modelCatalog.prices.gpt-9: no alias or client serves this model' \
  "${cnref[@]}" --set modelCatalog.prices.gpt-9.input=1 --set modelCatalog.prices.gpt-9.cacheRead=1 --set modelCatalog.prices.gpt-9.output=1
out="$(render "${cnref[@]}" --set modelCatalog.umbrellaPrices=true)"
yeq '[select(.kind == "ConfigMap" and .metadata.name == "model-gateway-model-catalog")] | length' 1 'umbrellaPrices with prices of its own: the chart renders its catalog'
out="$(render "${cnref[@]}" --set modelCatalog.umbrellaPrices=true --set-json 'modelCatalog.prices={}')"
yeq '[select(.kind == "ConfigMap" and .metadata.name == "model-gateway-model-catalog")] | length' 0 'umbrellaPrices without prices: the umbrella renders the catalog (GW12)'
yeq '[select(.kind == "AgentgatewayParameters") | .spec.modelCatalog.sources[0].configMap.name] | join(",")' 'model-gateway-model-catalog' 'and the parameters still read it'
out="$(render "${cnref[@]}" --set modelCatalog.enabled=false)"
yeq '[select(.kind == "ConfigMap")] | length' 0 'modelCatalog.enabled false: no catalog'
yeq '[select(.kind == "AgentgatewayParameters") | .spec.modelCatalog] | map(select(. != null)) | length' 0 'and no catalog source'
out="$(render "${minimal[@]}" --set 'providers.stub.transformations[0].field=max_tokens' --set-string 'providers.stub.transformations[0].expression=64')"
yeq "$(be stub-tier-light) | ([.spec.policies.ai.transformations[] | .field + \"=\" + .expression] | join(\",\")) + \" \" + (.spec.policies.tls // \"plaintext\")" \
  'model="tier-light",max_tokens=64 plaintext' "a user's transformation appends; tls false is plaintext"
rejects 'one provider, one alias, two models across listeners' 'one provider serves one model per alias' \
  "${minimal[@]}" --set 'providers.stub.dataClasses={public,internal}' --set aliases.internal.tier-light.provider=stub --set aliases.internal.tier-light.model=other

# ---- Task 5: the proxy's network (GW13)
out="$(render "${cnref[@]}")"
np='select(.kind == "NetworkPolicy" and .metadata.name == "model-gateway")'
yeq "$np | .spec.podSelector.matchLabels.\"gateway.networking.k8s.io/gateway-name\" + \" \" + (.spec.policyTypes | join(\",\"))" 'model-gateway Ingress,Egress' 'default deny on the proxy pods'
yeq "$np | .spec.ingress[0] | .from[0].namespaceSelector.matchLabels.\"kubernetes.io/metadata.name\" + \" \" + .from[0].podSelector.matchExpressions[0].key + \" \" + ([.ports[].port] | join(\",\"))" \
  'agents agents.ogenki.io/run-id 8080,8081' 'run pods reach public and internal'
yeq "$np | .spec.ingress[1] | .from[0].namespaceSelector.matchLabels.\"kubernetes.io/metadata.name\" + \" \" + .from[0].podSelector.matchLabels.\"app.kubernetes.io/name\" + \" \" + ([.ports[].port] | join(\",\"))" \
  'maku room-broker 8082' 'the broker reaches clients'
yeq "$np | .spec.ingress | length" 2 'no metrics scraper unless metrics.allowFrom'
yeq "$np | [.spec.egress[] | select(.to[0].namespaceSelector.matchLabels.\"kubernetes.io/metadata.name\" == \"kube-system\") | .ports[].port] | join(\",\")" '53,53' 'DNS'
yeq "$np | [.spec.egress[] | select(.to[0].podSelector.matchLabels.\"app.kubernetes.io/name\" == \"agentgateway\") | .to[0].namespaceSelector.matchLabels.\"kubernetes.io/metadata.name\" + \":\" + (.ports[0].port | tostring)] | join(\",\")" 'maku:9978' "the controller's xDS"
yeq "$np | [.spec.egress[] | select(.to[0].ipBlock.cidr == \"0.0.0.0/0\") | (.to[0].ipBlock.except | join(\",\")) + \" \" + (.ports[0].port | tostring)] | unique | join(\";\")" \
  '10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,100.64.0.0/10,169.254.0.0/16 443' "providers on 443, never the cluster's own ranges"
out="$(render "${minimal[@]}" --set 'metrics.allowFrom[0].namespaceSelector.matchLabels.team=obs')"
yeq "$np | .spec.ingress[2].from[0].namespaceSelector.matchLabels.team + \" \" + (.spec.ingress[2].ports[0].port | tostring)" 'obs 15020' 'metrics.allowFrom on 15020'
yeq "$np | [.spec.egress[] | select(.to[0].namespaceSelector.matchLabels.\"kubernetes.io/metadata.name\" == \"models\") | .ports[0].port] | join(\",\")" '8080' "an in-cluster provider's peers, on its port"
out="$(render "${minimal[@]}" --set networkPolicy.mode=none)"
yeq '[select(.kind == "NetworkPolicy" or .kind == "CiliumNetworkPolicy")] | length' 0 'mode none: no policy'
out="$(render "${cnref[@]}" --set networkPolicy.mode=cilium)"
yeq '[select(.kind == "NetworkPolicy")] | length' 0 'mode cilium: no NetworkPolicy'
yeq "select(.kind == \"CiliumNetworkPolicy\") | [.spec.egress[] | .toFQDNs[]?.matchName] | sort | join(\",\")" 'api.anthropic.com,api.kimi.ai,api.mistral.ai,api.z.ai' "mode cilium: every provider's host by FQDN"
yeq "select(.kind == \"CiliumNetworkPolicy\") | .spec.ingress | length" 2 'mode cilium: no scraper unless metrics.allowFrom'
# metrics.allowFrom's NetworkPolicy peers, translated to Cilium endpoint selectors (cnref runs Cilium)
out="$(render "${cnref[@]}" --set networkPolicy.mode=cilium \
  --set 'metrics.allowFrom[0].namespaceSelector.matchLabels.kubernetes\.io/metadata\.name=observability' \
  --set 'metrics.allowFrom[0].podSelector.matchLabels.app\.kubernetes\.io/name=vmagent' \
  --set 'metrics.allowFrom[1].podSelector.matchLabels.app=scraper')"
yeq "select(.kind == \"CiliumNetworkPolicy\") | .spec.ingress[2] | ([.fromEndpoints[] | .matchLabels | to_entries | map(.key + \"=\" + .value) | join(\"+\")] | join(\",\")) + \" \" + .toPorts[0].ports[0].port" \
  'k8s:app.kubernetes.io/name=vmagent+k8s:io.cilium.k8s.namespace.labels.kubernetes.io/metadata.name=observability,k8s:app=scraper+k8s:io.kubernetes.pod.namespace=maku 15020' \
  'mode cilium: metrics.allowFrom becomes fromEndpoints on 15020'
out="$(render "${cnref[@]}" --set networkPolicy.mode=cilium --set-json 'metrics.allowFrom=[{"namespaceSelector":{},"podSelector":{"matchLabels":{"app":"vm"}}}]')"
yeq "select(.kind == \"CiliumNetworkPolicy\") | .spec.ingress[2].fromEndpoints[0] | (.matchExpressions[0].key + \" \" + .matchExpressions[0].operator) + \" \" + (.matchLabels | to_entries | map(.key + \"=\" + .value) | join(\"+\"))" \
  'k8s:io.kubernetes.pod.namespace Exists k8s:app=vm' 'mode cilium: namespaceSelector {} is every namespace, not the release namespace'
rejects 'mode cilium with an ipBlock scraper' 'metrics.allowFrom[0]: mode cilium translates namespaceSelector and podSelector matchLabels only' \
  "${cnref[@]}" --set networkPolicy.mode=cilium --set 'metrics.allowFrom[0].ipBlock.cidr=10.0.0.0/8'

# ---- Gemini: the gemini and vertex presets (Gemini design G1, G5; probe P7)
vtx=(--set providers.vertex.preset=vertex --set providers.vertex.projectId=ogenki-435905 --set providers.vertex.region=eu)
gal=(--set aliases.public.gemini-standard.provider=vertex --set aliases.public.gemini-standard.model=gemini-4-neon)
rejects 'vertex with existingSecret' "providers.vertex.existingSecret: the vertex preset authenticates as the proxy's own Google identity" \
  "${minimal[@]}" "${vtx[@]}" --set providers.vertex.existingSecret=k
rejects 'vertex without projectId' 'providers.vertex: preset vertex needs projectId and region' \
  "${minimal[@]}" --set providers.vertex.preset=vertex --set providers.vertex.region=eu
rejects 'vertex without region' 'providers.vertex: preset vertex needs projectId and region' \
  "${minimal[@]}" --set providers.vertex.preset=vertex --set providers.vertex.projectId=ogenki-435905
rejects 'projectId on another preset' 'providers.mistral.projectId: only preset vertex takes a Google project and region' \
  "${minimal[@]}" --set providers.mistral.preset=mistral --set providers.mistral.existingSecret=k --set providers.mistral.projectId=ogenki-435905
rejects 'a region that is none' 'region' "${minimal[@]}" "${vtx[@]}" --set providers.vertex.region=mars
rejects 'an endpoint on vertex' 'providers.vertex.host: only preset custom takes an endpoint' "${minimal[@]}" "${vtx[@]}" --set providers.vertex.host=x.test
rejects 'gemini without existingSecret (D14)' 'providers.gemini.existingSecret is required' "${minimal[@]}" --set providers.gemini.preset=gemini
rejects 'a classifier on gemini (Gemini 4 always reasons)' 'clients.classifier: vertex/gemini-4-hydrogen always reasons' \
  "${minimal[@]}" "${vtx[@]}" --set clients.classifier.enabled=true --set clients.classifier.provider=vertex --set clients.classifier.model=gemini-4-hydrogen
rejects 'the vertex preset owns thinking_config' 'providers.vertex.transformations.thinking_config: the vertex preset sets it (' \
  "${minimal[@]}" "${vtx[@]}" --set 'providers.vertex.transformations[0].field=thinking_config' --set-string 'providers.vertex.transformations[0].expression=x'
out="$(render "${minimal[@]}" "${vtx[@]}" "${gal[@]}" --set aliases.public.gemini-light.provider=gemini --set aliases.public.gemini-light.model=gemini-4-hydrogen \
  --set providers.gemini.preset=gemini --set providers.gemini.existingSecret=agents-gemini-api-key \
  --set-json 'modelCatalog.prices.gemini-4-neon={"input": 3, "output": 15, "cacheRead": 0.3}')"
v='vertex-gemini-standard'
yeq "$(be $v) | (.spec.ai.provider | keys | join(\",\")) + \" \" + .spec.ai.provider.vertexai.projectId + \" \" + .spec.ai.provider.vertexai.region" \
  'vertexai ogenki-435905 eu' 'the vertex preset: the native vertexai provider, its project and region'
yeq "$(be $v) | (.spec.policies.auth | keys | join(\",\")) + \" \" + .spec.policies.auth.gcp.type + \" \" + ((.spec.policies.tls // \"no-tls-policy\") | tostring)" \
  'gcp AccessToken no-tls-policy' 'vertex: the gcp AccessToken auth, no secretRef, the native TLS'
yeq "$(be $v) | [.spec.policies.ai.transformations[] | .field] | join(\",\")" 'model,thinking_config' 'vertex: the model forced, then the thinking level'
yeq "$(be $v) | .spec.policies.ai.transformations[1].expression" \
  '{"thinkingLevel": llmRequest.reasoning_effort in ["none", "minimal", "low"] ? "low" : llmRequest.reasoning_effort == "medium" ? "medium" : "high"}' \
  'vertex: reasoning_effort becomes a thinking level, never a budget'
yeq "$(be $v) | .spec.policies.ai.modelAliases.\"gemini-standard\"" 'gemini-4-neon' 'gemini-standard maps to gemini-4-neon'
g='gemini-gemini-light'
yeq "$(be $g) | .spec.ai.provider.custom.providerOverride + \" \" + .spec.ai.provider.host + \":\" + (.spec.ai.provider.port | tostring) + .spec.ai.provider.custom.formats[0].path + \" \" + .spec.policies.auth.secretRef.name" \
  'gemini generativelanguage.googleapis.com:443/v1beta/openai/chat/completions agents-gemini-api-key' 'the gemini preset: the OpenAI-compatible endpoint and its key'
yeq "[select(.kind == \"Secret\")] | length" 0 'gemini and vertex render no Secret'
cat="$(yq ea 'select(.kind == "ConfigMap" and .metadata.name == "model-gateway-model-catalog") | .data."catalog.json"' - <<<"$out")"
[[ "$(jq -c '.providers."gcp.vertex_ai".models."gemini-4-neon".rates' <<<"$cat")" == '{"cacheRead":"0.30","input":"3.00","output":"15.00"}' ]] \
  || { echo "FAIL: vertex prices key under gcp.vertex_ai, agentgateway's name for its native provider (got: $cat)" >&2; fail=1; }
yeq "$np | [.spec.egress[] | select(.to[0].ipBlock.cidr == \"169.254.169.254/32\") | ([.to[].ipBlock.cidr] | join(\",\")) + \" \" + ([.ports[].port] | join(\",\"))] | join(\";\")" \
  '169.254.169.254/32,169.254.169.252/32 80,988' 'vertex: the GKE metadata server, for the Workload Identity token'
out="$(render "${minimal[@]}" "${vtx[@]}" "${gal[@]}" --set networkPolicy.mode=cilium)"
yeq "select(.kind == \"CiliumNetworkPolicy\") | [.spec.egress[] | .toFQDNs[]?.matchName] | join(\",\")" 'aiplatform.eu.rep.googleapis.com,stub.models.svc.cluster.local' 'vertex on eu dials the eu restricted endpoint'
yeq "select(.kind == \"CiliumNetworkPolicy\") | [.spec.egress[] | select(.toCIDR) | (.toCIDR | join(\",\")) + \" \" + ([.toPorts[0].ports[].port] | join(\",\"))] | join(\";\")" \
  '169.254.169.254/32,169.254.169.252/32 80,988' 'mode cilium: the metadata server by CIDR'
for r in global:aiplatform.googleapis.com us:aiplatform.us.rep.googleapis.com europe-west4:europe-west4-aiplatform.googleapis.com; do
  out="$(render "${minimal[@]}" "${vtx[@]}" "${gal[@]}" --set networkPolicy.mode=cilium --set "providers.vertex.region=${r%%:*}")"
  yeq "select(.kind == \"CiliumNetworkPolicy\") | [.spec.egress[] | .toFQDNs[]?.matchName] | join(\",\")" "${r#*:},stub.models.svc.cluster.local" "vertex on ${r%%:*} dials ${r#*:}"
done
out="$(render "${cnref[@]}" --set networkPolicy.mode=cilium)"
yeq "[select(.kind == \"CiliumNetworkPolicy\") | .spec.egress[] | select(.toCIDR)] | length" 0 'no gcp provider: no metadata server'
# A Flux postBuild variable may stand for the project: the release renders it as written, and Flux
# substitutes the rendered objects (a GitOps install renders before substitution). Never for the
# region: the chart derives the host and the network policy from it at render time.
out="$(render "${minimal[@]}" "${gal[@]}" "${vtx[@]}" --set-string 'providers.vertex.projectId=${project_id}')"
yeq "$(be vertex-gemini-standard) | .spec.ai.provider.vertexai.projectId" '${project_id}' \
  'vertex: a ${variable} projectId renders as written, for Flux to substitute'
rejects 'a projectId that is neither a project nor a ${variable}' 'projectId' "${minimal[@]}" "${gal[@]}" "${vtx[@]}" \
  --set-string 'providers.vertex.projectId=${project id}'
rejects 'a ${variable} region (the host is derived from it at render time)' 'region' "${minimal[@]}" "${gal[@]}" "${vtx[@]}" \
  --set-string 'providers.vertex.region=${region}'

[[ $fail == 0 ]] || exit 1
echo "maku-gateway chart render checks passed"
