{{/* One generated CRD (files/*.yaml, a byte copy of config/crd) with the chart's metadata merged in: the part-of label, and the keep annotation unless keep is false. */}}
{{- define "afcrds.crd" -}}
{{- $crd := .crd -}}
{{- $meta := $crd.metadata | default dict -}}
{{- $labels := $meta.labels | default dict -}}
{{- $_ := set $labels "app.kubernetes.io/part-of" "agent-factory" -}}
{{- $_ := set $labels "app.kubernetes.io/managed-by" .root.Release.Service -}}
{{- $_ := set $meta "labels" $labels -}}
{{- $annotations := $meta.annotations | default dict -}}
{{- if .root.Values.keep -}}
{{- $_ := set $annotations "helm.sh/resource-policy" "keep" -}}
{{- end -}}
{{- if $annotations -}}
{{- $_ := set $meta "annotations" $annotations -}}
{{- end -}}
{{- $_ := set $crd "metadata" $meta -}}
{{- toYaml $crd -}}
{{- end }}
