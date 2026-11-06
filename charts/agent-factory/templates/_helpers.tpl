{{/* Labels every umbrella object carries. */}}
{{- define "agent-factory.labels" -}}
app.kubernetes.io/part-of: agent-factory
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* The broker Secret names, from the maku chart's own values (a subchart value is read here as .Values.maku). */}}
{{- define "agent-factory.tlsSecret" -}}{{ .Values.maku.broker.tls.secretName | default "room-broker-tls" }}{{- end }}
{{- define "agent-factory.caSecret" -}}{{ .Values.maku.broker.tls.caSecretName | default "room-broker-ca" }}{{- end }}
{{- define "agent-factory.factoryCASecret" -}}{{ index .Values "maku-factory" "brokerCASecret" | default "openbao-ca" }}{{- end }}

{{/* The profile gate: the demo is the only profile until stage 2 adds production's values. */}}
{{- define "agent-factory.profile" -}}
{{- if ne (.Values.profile | default "") "demo" -}}
{{- fail (printf "profile is required: demo, or production values (stage 2); got %q" (.Values.profile | default "")) -}}
{{- end -}}
{{- $d := .Values.clusterDomain -}}
{{- if or (ne $d (.Values.maku.clusterDomain | default "cluster.local")) (ne $d (index .Values "maku-factory" "clusterDomain" | default "cluster.local")) -}}
{{- fail (printf "clusterDomain, maku.clusterDomain and maku-factory.clusterDomain must be equal (got %q, %q, %q): the broker certificate's names and the factory's broker URL are built from them" $d .Values.maku.clusterDomain (index .Values "maku-factory" "clusterDomain")) -}}
{{- end -}}
{{- end }}
