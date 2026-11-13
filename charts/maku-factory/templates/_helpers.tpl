{{- define "agent-factory.labels" -}}
app.kubernetes.io/name: agent-factory
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end -}}

{{/* The image by digest: a bare tag can move under a running factory. e2e.unpinnedImages lifts it for locally loaded images. */}}
{{- define "agent-factory.image" -}}
{{- $tag := required "image.tag is <version>@sha256:<digest>" .Values.image.tag -}}
{{- if and (not (.Values.e2e).unpinnedImages) (not (regexMatch "^[A-Za-z0-9_.-]+@sha256:[0-9a-f]+$" $tag)) -}}
{{- fail "image.tag is <version>@sha256:<digest>, never a bare tag" -}}
{{- end -}}
{{ .Values.image.repository }}:{{ $tag }}
{{- end -}}

{{/* af.apps (v4 D12, plan PR9): the Apps' Secrets, resolved from apps.* over the older keys, as JSON
for fromJson. factory: the factory App's Secret. agents: the agents App's Secret under its new key,
mounted at /etc/agent-factory-agents. runTokens: runTokens.appSecret, the alias, mounted where it
always was when apps.agents is unset. merger: the merger App's Secret, empty when folded. */}}
{{- define "af.apps" -}}
{{- $v := .Values -}}
{{- $factory := dig "factory" "existingSecret" "" ($v.apps | default dict) | default $v.githubSecret -}}
{{- $agents := dig "agents" "existingSecret" "" ($v.apps | default dict) -}}
{{- $alias := $v.runTokens.appSecret | default "" -}}
{{- if and $agents $alias (ne $agents $alias) -}}
{{- fail (printf "apps.agents.existingSecret and runTokens.appSecret name different Secrets (%q, %q): set apps.agents.existingSecret alone" $agents $alias) -}}
{{- end -}}
{{- $runTokens := "" -}}
{{- if and $v.runTokens.enabled (not $agents) -}}
{{- $runTokens = required "runTokens.enabled needs apps.agents.existingSecret or runTokens.appSecret" $alias -}}
{{- end -}}
{{- $mode := dig "merger" "mode" "separate" ($v.apps | default dict) -}}
{{- if not (has $mode (list "separate" "factory")) -}}
{{- fail (printf "apps.merger.mode is separate or factory, not %q" $mode) -}}
{{- end -}}
{{- $merger := $v.mergerSecret | default "agent-factory-merger" -}}
{{- if eq $mode "factory" -}}
{{- if $v.mergerSecret -}}
{{- fail "apps.merger.mode factory and mergerSecret: a merger folded into the factory App has no Secret of its own; unset mergerSecret" -}}
{{- end -}}
{{- $merger = "" -}}
{{- end -}}
{{- dict "factory" $factory "agents" $agents "runTokens" $runTokens "merger" $merger | toJson -}}
{{- end -}}
