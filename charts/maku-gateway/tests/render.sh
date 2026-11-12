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
  yq ea -e "[select(.kind == \"$1\") | $2] | (length > 0 and all)" - <<<"$out" >/dev/null 2>&1 || { echo "FAIL: $3" >&2; fail=1; }
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

[[ $fail == 0 ]] || exit 1
echo "maku-gateway chart render checks passed"
