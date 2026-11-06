{{/* Labels every umbrella object carries. */}}
{{- define "agent-factory.labels" -}}
app.kubernetes.io/part-of: agent-factory
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* The broker Secret names, from the maku chart's own values (a subchart value is read here as .Values.maku). */}}
{{- define "agent-factory.tlsSecret" -}}{{ .Values.maku.broker.tls.secretName | default "room-broker-tls" }}{{- end }}
{{- define "agent-factory.caSecret" -}}{{ .Values.maku.broker.tls.caSecretName | default "room-broker-ca" }}{{- end }}
{{- define "agent-factory.factoryCASecret" -}}{{ index .Values "maku-factory" "brokerCASecret" | default "openbao-ca" }}{{- end }}
