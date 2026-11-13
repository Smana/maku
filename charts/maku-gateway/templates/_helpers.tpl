{{/* Helper names are global across an umbrella's subcharts: every one here is `mgw.*`. */}}

{{/* Labels every object carries. */}}
{{- define "mgw.labels" -}}
app.kubernetes.io/name: maku-gateway
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: maku
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end -}}

{{/* The broker's namespace: broker.namespace, else the release namespace. */}}
{{- define "mgw.brokerNamespace" -}}
{{- .Values.broker.namespace | default .Release.Namespace -}}
{{- end -}}

{{/* A string as a CEL string literal's body: backslashes and quotes escaped. */}}
{{- define "mgw.celString" -}}
{{- . | replace "\\" "\\\\" | replace "\"" "\\\"" -}}
{{- end -}}

{{/* A run's token subject, as an RE2 pattern (GW3): the runs namespace and name prefix, quoted. */}}
{{- define "mgw.runSubPattern" -}}
{{- printf "^system:serviceaccount:%s:%s[a-z2-7]{8}$" (regexQuoteMeta .Values.runs.namespace) (regexQuoteMeta .Values.runs.namePrefix) -}}
{{- end -}}

{{/* The broker's token subject (GW8). */}}
{{- define "mgw.brokerSub" -}}
{{- printf "system:serviceaccount:%s:%s" (include "mgw.brokerNamespace" .) .Values.broker.serviceAccount -}}
{{- end -}}

{{/* A provider's dataClasses: its own, else [public] (GW6: fail closed). */}}
{{- define "mgw.dataClasses" -}}
{{- (index .root.Values.providers .name).dataClasses | default (list "public") | toJson -}}
{{- end -}}

{{/* The catalog and telemetry key of providers.<name>: the native provider's own name, else <name>
(each custom backend sets providerOverride to it). */}}
{{- define "mgw.catalogKey" -}}
{{- $p := index .root.Values.providers .name -}}
{{- if has $p.preset (list "openai" "anthropic") -}}{{ $p.preset }}{{- else -}}{{ .name }}{{- end -}}
{{- end -}}

{{/*
Every backend the values call for, as a YAML list of {name, provider, alias, model, class}: one per
alias row (<provider>-<alias>; a provider serving an alias on both listeners shares one backend, so
it must map it to one model) and one per enabled client (<provider>-<client>, alias tier-frontier
for the planner and tier-light for the classifier).
*/}}
{{- define "mgw.backends" -}}
{{- $seen := dict -}}
{{- $out := list -}}
{{- range $class := list "public" "internal" -}}
{{- range $alias, $t := (index $.Values.aliases $class) | default dict -}}
{{- $name := printf "%s-%s" $t.provider $alias -}}
{{- if hasKey $seen $name -}}
{{- if ne (get $seen $name) $t.model -}}
{{- fail (printf "aliases.public.%s and aliases.internal.%s both use provider %s with different models (%s, %s): one provider serves one model per alias" $alias $alias $t.provider (get $seen $name) $t.model) -}}
{{- end -}}
{{- else -}}
{{- $_ := set $seen $name $t.model -}}
{{- $out = append $out (dict "name" $name "provider" $t.provider "alias" $alias "model" $t.model) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $client, $alias := dict "planner" "tier-frontier" "classifier" "tier-light" -}}
{{- $c := index $.Values.clients $client -}}
{{- if $c.enabled -}}
{{- $out = append $out (dict "name" (printf "%s-%s" $c.provider $client) "provider" $c.provider "alias" $alias "model" $c.model) -}}
{{- end -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{/* A USD rate as the catalog writes it: plain decimal, at least two places (1.4 -> "1.40", 0.075 -> "0.075"). */}}
{{- define "mgw.usd" -}}
{{- $s := printf "%v" . -}}
{{- if not (contains "." $s) -}}{{- $s = printf "%s.00" $s -}}
{{- else if eq (len (last (splitList "." $s))) 1 -}}{{- $s = printf "%s0" $s -}}
{{- end -}}
{{- $s -}}
{{- end -}}

{{/*
The model catalog ConfigMap (GW12): (dict "root" <a context with .Values, .Release, .Chart> "prices"
<meter.prices rows>). Each row goes under the catalog key of every provider serving that model, its
rates as strings, an absent cacheWrite absent: what ANR's gen-prices.py renders.
*/}}
{{- define "mgw.catalog" -}}
{{- $root := .root -}}
{{- $providers := dict -}}
{{- range $b := include "mgw.backends" $root | fromYamlArray -}}
{{- with index $.prices $b.model -}}
{{- $row := . -}}
{{- $key := include "mgw.catalogKey" (dict "root" $root "name" $b.provider) -}}
{{- if not (hasKey $providers $key) -}}{{- $_ := set $providers $key (dict "models" dict) -}}{{- end -}}
{{- $rates := dict -}}
{{- range $f := list "input" "output" "cacheRead" "cacheWrite" -}}
{{- if hasKey $row $f -}}{{- $_ := set $rates $f (include "mgw.usd" (index $row $f)) -}}{{- end -}}
{{- end -}}
{{- $_ := set (index $providers $key).models $b.model (dict "rates" $rates) -}}
{{- end -}}
{{- end -}}
# The prices agentgateway merges over its built-in catalog to price each response (the access log's
# `cost`), from the factory's meter.prices rows: edit a price there.
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ $root.Values.name }}-model-catalog
  labels: {{- include "mgw.labels" $root | nindent 4 }}
data:
  catalog.json: |
    {{- dict "providers" $providers | toPrettyJson | nindent 4 }}
{{- end -}}

{{/* A misconfiguration is a render error naming the key, never a gateway that answers wrong. */}}
{{- define "mgw.validate" -}}
{{- $v := .Values -}}
{{- if not $v.identity.issuer -}}
{{- fail "identity.issuer is required: the gateway verifies each caller's token against this cluster's ServiceAccount issuer" -}}
{{- end -}}
{{- $jwks := $v.identity.jwks -}}
{{- if and (eq $jwks.source "url") (not $jwks.url) -}}
{{- fail "identity.jwks.url is required with identity.jwks.source url: the issuer's public JWKS" -}}
{{- end -}}
{{- if and (eq $jwks.source "inline") (not $jwks.inline) -}}
{{- fail "identity.jwks.inline is required with identity.jwks.source inline: the issuer's public JWKS (kubectl get --raw /openid/v1/jwks)" -}}
{{- end -}}
{{- if and (eq $jwks.source "apiserver") (not $jwks.allowAnonymousDiscovery) -}}
{{- fail "identity.jwks.source apiserver needs identity.jwks.allowAnonymousDiscovery: true: agentgateway's controller fetches the apiserver's JWKS anonymously (probe P1), so system:unauthenticated must hold system:service-account-issuer-discovery" -}}
{{- end -}}
{{- if not $v.image.digest -}}
{{- fail "image.digest is required: the gateway proxy's image is pinned by digest" -}}
{{- end -}}
{{- $presets := include "mgw.presets" . | fromYaml -}}
{{- range $name, $p := $v.providers -}}
{{- if not $p.existingSecret -}}
{{- fail (printf "providers.%s.existingSecret is required: the chart reads each provider's key from a Secret you create in the release namespace, never from a value (D14)" $name) -}}
{{- end -}}
{{- $preset := index $presets $p.preset -}}
{{- if eq $p.preset "custom" -}}
{{- if not (and $p.host $p.port $p.path) -}}
{{- fail (printf "providers.%s: preset custom needs host, port and path (its Chat Completions endpoint)" $name) -}}
{{- end -}}
{{- else -}}
{{- range $k := list "host" "port" "path" "tls" -}}
{{- if hasKey $p $k -}}
{{- fail (printf "providers.%s.%s: only preset custom takes an endpoint; the %s preset sets it" $name $k $p.preset) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $t := $p.transformations | default list -}}
{{- if eq $t.field "model" -}}
{{- fail (printf "providers.%s.transformations.model: the chart sets it (each route forces its alias's model, GW5)" $name) -}}
{{- end -}}
{{- with index $preset.fields $t.field -}}
{{- fail (printf "providers.%s.transformations.%s: the %s preset sets it (%s); use preset custom to own it" $name $t.field $p.preset .why) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $class := list "public" "internal" -}}
{{- range $alias, $t := (index $v.aliases $class) | default dict -}}
{{- if not (hasKey $v.providers $t.provider) -}}
{{- fail (printf "aliases.%s.%s names provider %s, which providers does not define" $class $alias $t.provider) -}}
{{- end -}}
{{- $classes := include "mgw.dataClasses" (dict "root" $ "name" $t.provider) | fromJsonArray -}}
{{- if not (has $class $classes) -}}
{{- fail (printf "aliases.%s.%s names provider %s, whose dataClasses ([%s]) exclude %s: add %s to providers.%s.dataClasses, or map the alias to another provider" $class $alias $t.provider (join ", " $classes) $class $class $t.provider) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $client := list "planner" "classifier" -}}
{{- $c := index $v.clients $client -}}
{{- if $c.enabled -}}
{{- if not (and $c.provider $c.model) -}}
{{- fail (printf "clients.%s needs provider and model when enabled" $client) -}}
{{- end -}}
{{- if not (hasKey $v.providers $c.provider) -}}
{{- fail (printf "clients.%s names provider %s, which providers does not define" $client $c.provider) -}}
{{- end -}}
{{- $reasons := (index $presets (index $v.providers $c.provider).preset).alwaysReasons -}}
{{- if and (eq $client "classifier") (or (has "*" $reasons) (has $c.model $reasons)) -}}
{{- fail (printf "clients.classifier: %s/%s always reasons: the classifier's 64 tokens would be spent reasoning and its reply would be empty; choose a model that answers without reasoning" $c.provider $c.model) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $backends := include "mgw.backends" . | fromYamlArray -}}
{{- if $v.modelCatalog.enabled -}}
{{- $served := list -}}
{{- range $backends -}}{{- $served = append $served .model -}}{{- end -}}
{{- range $model, $_ := $v.modelCatalog.prices -}}
{{- if not (has $model $served) -}}
{{- fail (printf "modelCatalog.prices.%s: no alias or client serves this model, so the gateway cannot price it under a provider" $model) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
