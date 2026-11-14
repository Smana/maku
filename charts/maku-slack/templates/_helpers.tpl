{{/* Labels every object carries; app.kubernetes.io/name is what the broker's and the gateway's policies select. */}}
{{- define "mslack.labels" -}}
app.kubernetes.io/name: factory-slack-app
app.kubernetes.io/part-of: agent-factory
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* The image, pinned by digest unless e2e.unpinnedImages. */}}
{{- define "mslack.image" -}}
{{- $t := .Values.image.tag | default "" -}}
{{- if not .Values.e2e.unpinnedImages -}}
{{- if not (regexMatch "^[^@\\s]+@sha256:[0-9a-f]{64}$" $t) -}}
{{- fail (printf "image.tag is <version>@sha256:<digest>, never a bare tag (got %q)" $t) -}}
{{- end -}}
{{- end -}}
{{- printf "%s:%s" .Values.image.repository $t -}}
{{- end }}

{{/* The App's config file (internal/slackapp's Config), as YAML: the derived files, addresses and
namespace, then config verbatim. A derived key set in config fails, naming it. */}}
{{- define "mslack.config" -}}
{{- $v := .Values -}}
{{- if not $v.roomsURL }}{{ fail "roomsURL is required: the rooms' web address, for the links the App posts" }}{{ end -}}
{{- if not $v.issuerURL }}{{ fail "issuerURL is required: the App signs in there with its principal Secret's client" }}{{ end -}}
{{- $c := dict
  "signingSecretFile" "/etc/factory-slack-app/slack/signing-secret"
  "botTokenFile" "/etc/factory-slack-app/slack/bot-token"
  "botUserIDFile" "/etc/factory-slack-app/slack/bot-user-id"
  "webhookKeyFile" "/etc/factory-slack-app/webhook/signing-key"
  "clientIDFile" "/etc/factory-slack-app/principal/client-id"
  "clientSecretFile" "/etc/factory-slack-app/principal/client-secret"
  "issuerURL" $v.issuerURL
  "brokerURL" ($v.brokerURL | default (printf "http://room-broker.%s.svc.%s:8080" .Release.Namespace $v.clusterDomain))
  "roomsURL" $v.roomsURL
  "namespace" .Release.Namespace -}}
{{- if eq ($v.preset | default "zitadel") "zitadel" -}}
{{- $_ := set $c "projectIDFile" "/etc/factory-slack-app/principal/project-id" -}}
{{- end -}}
{{- with $v.preset }}{{ $_ := set $c "preset" . }}{{ end -}}
{{- with $v.tokenURL }}{{ $_ := set $c "tokenURL" . }}{{ end -}}
{{- with $v.scopes }}{{ $_ := set $c "scopes" . }}{{ end -}}
{{- with $v.audience }}{{ $_ := set $c "audience" . }}{{ end -}}
{{- range $k := keys ($v.config | default dict) | sortAlpha -}}
{{- if or (hasKey $c $k) (has $k (list "projectIDFile" "preset" "tokenURL" "scopes" "audience" "eventsAddr" "webhooksAddr" "opsAddr")) -}}
{{- fail (printf "config.%s: the chart derives it (set the chart's own value instead)" $k) -}}
{{- end -}}
{{- end -}}
{{- toYaml (merge $c ($v.config | default dict)) -}}
{{- end }}
