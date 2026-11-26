{{/*
The provider presets (plan v4 step 4, GW7; docs/gateway.md, "Presets"): what each provider needs on
the wire, held here so a values file cannot silently undo it. Each preset is
  provider       the AgentgatewayBackend's ai.provider kind: openai, anthropic, vertexai (native) or custom
  host/port/path the custom provider's Chat Completions endpoint, over TLS
  auth           secret (the default: the key from existingSecret) or gcp (the proxy's own Google
                 identity, ambient credentials: no Secret at all)
  fields         the transformations always rendered: {field: {expression, why}}
  alwaysReasons  the models that reason on every call ("*": every model of the provider)
Probe P4 (charts/maku-gateway/tests/probe) checks the mistral and moonshot transformations on the wire,
and P7 the gemini and vertex presets.
*/}}
{{- define "mgw.presets" -}}
openai:
  provider: openai
  # Native: the controller knows the endpoint; the host is for the network policy only.
  host: api.openai.com
  port: 443
  fields: {}
  alwaysReasons: []
anthropic:
  # Native: Chat Completions in, Messages out; the key moves to x-api-key and anthropic-version is set.
  provider: anthropic
  host: api.anthropic.com
  port: 443
  fields: {}
  alwaysReasons: []
zai:
  provider: custom
  host: api.z.ai
  port: 443
  path: /api/paas/v4/chat/completions
  fields: {}
  # glm-5.3 reasons on every call: the planner sets reasoningEffort low beside it (Maku #372).
  alwaysReasons: [glm-5.3]
mistral:
  provider: custom
  host: api.mistral.ai
  port: 443
  path: /v1/chat/completions
  fields:
    reasoning_effort:
      expression: '"none"'
      why: agentgateway 1.6 and 1.7 cannot parse the list content Mistral's reasoning answers with, a 502 (ANR live run uyqiqv6e), and the planner sends low
  alwaysReasons: []
moonshot:
  # The Kimi Code plan's host: api.moonshot.ai answers 401 for its keys.
  provider: custom
  host: api.kimi.ai
  port: 443
  path: /coding/v1/chat/completions
  fields:
    temperature:
      expression: "1"
      why: K3.1 Pro fixes temperature at 1, and the planner and the classifier send 0
    top_p:
      expression: "null"
      why: K3.1 Pro fixes top_p
  # reasoning_content spends max_tokens before any content.
  alwaysReasons: ["*"]
gemini:
  # The Gemini API's OpenAI-compatible endpoint, with an AI Studio key. A tool call's thought
  # signature rides in tool_calls[].extra_content, which the caller must send back (probe P7).
  provider: custom
  host: generativelanguage.googleapis.com
  port: 443
  path: /v1beta/openai/chat/completions
  fields: {}
  # Gemini 4 cannot stop thinking.
  alwaysReasons: ["*"]
vertex:
  # Vertex AI, native: agentgateway turns Chat Completions into generateContent on the region's host
  # (mgw.vertexHost) and carries each thought signature inside the tool call's id, so any caller that
  # echoes tool_call ids round-trips it (probe P7). The proxy authenticates as its own Google identity
  # (GKE Workload Identity): no key exists.
  provider: vertexai
  port: 443
  auth: gcp
  fields:
    thinking_config:
      expression: '{"thinkingLevel": llmRequest.reasoning_effort in ["none", "minimal", "low"] ? "low" : llmRequest.reasoning_effort == "medium" ? "medium" : "high"}'
      why: agentgateway 1.6 turns reasoning_effort into a thinkingBudget for every model but gemini-3, and Gemini 4 answers 400 to a budget; without reasoning_effort the expression fails and no thinking config is sent
  alwaysReasons: ["*"]
custom:
  provider: custom
  fields: {}
  alwaysReasons: []
{{- end -}}

{{/* The preset of providers.<name>: (dict "root" . "name" <name>) -> YAML. */}}
{{- define "mgw.preset" -}}
{{- $p := index .root.Values.providers .name -}}
{{- index (include "mgw.presets" . | fromYaml) $p.preset | toYaml -}}
{{- end -}}
