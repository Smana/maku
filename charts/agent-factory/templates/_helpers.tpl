{{/* Helper names are global across subcharts: these are `afumbrella.*` because maku-factory defines `agent-factory.labels`, and a same-named parent define silently replaces it (the factory's Deployment selector lost its name label). */}}
{{/* Labels every umbrella object carries. */}}
{{- define "afumbrella.labels" -}}
app.kubernetes.io/part-of: agent-factory
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* The broker Secret names, from the maku chart's own values (a subchart value is read here as .Values.maku). */}}
{{- define "afumbrella.tlsSecret" -}}{{ .Values.maku.broker.tls.secretName | default "room-broker-tls" }}{{- end }}
{{- define "afumbrella.caSecret" -}}{{ .Values.maku.broker.tls.caSecretName | default "room-broker-ca" }}{{- end }}
{{- define "afumbrella.factoryCASecret" -}}{{ index .Values "maku-factory" "brokerCASecret" | default "openbao-ca" }}{{- end }}

{{/* The profile gate, included first by every umbrella template (the skeleton values hold no tls:, postgres: or networkPolicies:). The demo is the only profile until stage 2 adds production's values, and it needs values-demo.yaml (its `demoValues` marker): a subchart reads only the values Helm hands it, so `--set profile=demo` alone would render half a demo. */}}
{{- define "afumbrella.profile" -}}
{{- $profile := .Values.profile | default "" -}}
{{- if eq $profile "production" -}}
{{- fail "profile production: its seams arrive in rollout steps 2 to 6 (docs/superpowers/specs/2026-11-12-v4-production-path-design.md); use -f values-demo.yaml for the demo" -}}
{{- end -}}
{{- if ne $profile "demo" -}}
{{- fail (printf "profile is required: demo (-f values-demo.yaml), or production (rollout steps 2 to 6); got %q" $profile) -}}
{{- end -}}
{{- if not .Values.demoValues -}}
{{- fail "profile demo needs -f values-demo.yaml (the demo's defaults moved there in v0.4x)" -}}
{{- end -}}
{{- $d := .Values.clusterDomain -}}
{{- if or (ne $d (.Values.maku.clusterDomain | default "cluster.local")) (ne $d (index .Values "maku-factory" "clusterDomain" | default "cluster.local")) -}}
{{- fail (printf "clusterDomain, maku.clusterDomain and maku-factory.clusterDomain must be equal (got %q, %q, %q): the broker certificate's names and the factory's broker URL are built from them" $d .Values.maku.clusterDomain (index .Values "maku-factory" "clusterDomain")) -}}
{{- end -}}
{{- end }}
