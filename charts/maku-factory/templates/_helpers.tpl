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

{{/* af.apps (v4 D12): the Apps' Secrets from apps.*, as JSON for fromJson. factory: the factory
App's Secret. agents: the agents App's Secret, mounted at /etc/agent-factory-agents. merger: the
merger App's Secret, empty when folded. The older githubSecret, mergerSecret and runTokens.appSecret
are removed (v4 step 8): the schema refuses them, and so does this helper under
--skip-schema-validation. */}}
{{- define "af.apps" -}}
{{- $v := .Values -}}
{{- if hasKey $v "githubSecret" -}}
{{- fail "githubSecret was removed in v4 step 8: set apps.factory.existingSecret (docs/upgrading.md#the-old-app-keys)" -}}
{{- end -}}
{{- if hasKey $v "mergerSecret" -}}
{{- fail "mergerSecret was removed in v4 step 8: set apps.merger.existingSecret (docs/upgrading.md#the-old-app-keys)" -}}
{{- end -}}
{{- if hasKey ($v.runTokens | default dict) "appSecret" -}}
{{- fail "runTokens.appSecret was removed in v4 step 8: set apps.agents.existingSecret (docs/upgrading.md#the-old-app-keys)" -}}
{{- end -}}
{{- $apps := $v.apps | default dict -}}
{{- $factory := required "apps.factory.existingSecret: the factory App's Secret" (dig "factory" "existingSecret" "" $apps) -}}
{{- $agents := dig "agents" "existingSecret" "" $apps -}}
{{- if and ($v.runTokens).enabled (not $agents) -}}
{{- fail "runTokens.enabled needs apps.agents.existingSecret: the agents App it mints from" -}}
{{- end -}}
{{- $mode := dig "merger" "mode" "separate" $apps -}}
{{- if not (has $mode (list "separate" "factory")) -}}
{{- fail (printf "apps.merger.mode is separate or factory, not %q" $mode) -}}
{{- end -}}
{{- $mergerSet := dig "merger" "existingSecret" "" $apps -}}
{{- $merger := $mergerSet | default "agent-factory-merger" -}}
{{- if eq $mode "factory" -}}
{{- if $mergerSet -}}
{{- fail "apps.merger.mode factory and apps.merger.existingSecret: a merger folded into the factory App has no Secret of its own; unset apps.merger.existingSecret" -}}
{{- end -}}
{{- $merger = "" -}}
{{- end -}}
{{- /* One Secret is one App (review of #510): the agents App is never the factory's nor the merger's,
the gate ruleset's bypass actor, and a separate merger is never the factory's. */ -}}
{{- if and $agents (eq $agents $factory) -}}
{{- fail (printf "apps.agents.existingSecret names the factory App's Secret %q: the agents App must be a second App (D12)" $factory) -}}
{{- end -}}
{{- if and $agents (eq $agents $merger) -}}
{{- fail (printf "apps.agents.existingSecret names the merger App's Secret %q: the merger bypasses the merge gate's ruleset, so a run token could merge past maku/merge-gate" $merger) -}}
{{- end -}}
{{- if and $merger (eq $merger $factory) -}}
{{- fail (printf "apps.merger.existingSecret names the factory App's Secret %q: set apps.merger.mode factory to fold the merger into the factory App" $factory) -}}
{{- end -}}
{{- if and $v.demo $agents (not ($v.runTokens).enabled) -}}
{{- fail "demo: apps.agents.existingSecret needs runTokens.enabled: without it the demo mints from the factory App" -}}
{{- end -}}
{{- dict "factory" $factory "agents" $agents "merger" $merger | toJson -}}
{{- end -}}
