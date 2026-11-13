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
{{- include "afumbrella.gateway" . -}}
{{- end }}

{{/* The model gateway's agreement with maku (plan v4 step 4, Task 5): the gateway admits the runs and
the broker maku runs, so both must name the same ones. */}}
{{- define "afumbrella.gateway" -}}
{{- $gw := index .Values "maku-gateway" -}}
{{- if $gw.enabled -}}
{{- $mr := .Values.maku.runs -}}
{{- if or (ne $gw.runs.namespace $mr.namespace) (ne $gw.runs.namePrefix $mr.namePrefix) -}}
{{- fail (printf "maku-gateway.runs (namespace %q, namePrefix %q) must equal maku.runs (namespace %q, namePrefix %q): the gateway admits only the runs maku starts" $gw.runs.namespace $gw.runs.namePrefix $mr.namespace $mr.namePrefix) -}}
{{- end -}}
{{- if ne $gw.broker.serviceAccount "room-broker" -}}
{{- fail (printf "maku-gateway.broker.serviceAccount %q must be the broker's ServiceAccount, room-broker (maku's broker-deployment)" $gw.broker.serviceAccount) -}}
{{- end -}}
{{- if ne ($gw.broker.namespace | default .Release.Namespace) .Release.Namespace -}}
{{- fail (printf "maku-gateway.broker.namespace %q must be the release namespace %q, where the umbrella runs the broker" $gw.broker.namespace .Release.Namespace) -}}
{{- end -}}
{{- end -}}
{{- include "afumbrella.runsGateway" . -}}
{{- end }}

{{/* The run-controller's model.mode gateway (plan v4 step 4, Task 10): runs call either this release's
maku-gateway, which must then serve exactly the aliases, ports and prefix the run-controller's config
names, or an external gateway named by maku.runController.model.gateway.service (ANR's form). */}}
{{- define "afumbrella.runsGateway" -}}
{{- $gw := index .Values "maku-gateway" -}}
{{- $m := dig "runController" "model" dict .Values.maku -}}
{{- $mode := $m.mode | default "secret" -}}
{{- $g := $m.gateway | default dict -}}
{{- $identity := dig "config" "runs" "gatewayIdentity" false (index .Values "maku-factory") -}}
{{- if and $identity (ne $mode "gateway") -}}
{{- fail (printf "maku-factory.config.runs.gatewayIdentity is true but maku.runController.model.mode is %q: the runs carry a gateway identity only in model.mode gateway" $mode) -}}
{{- end -}}
{{- if eq $mode "gateway" -}}
{{- if and (not $gw.enabled) (not $g.service) -}}
{{- fail "maku.runController.model.mode gateway needs maku-gateway.enabled, or maku.runController.model.gateway.service naming an external gateway" -}}
{{- end -}}
{{- if $gw.enabled -}}
{{- if and $g.service (ne $g.service $gw.name) -}}
{{- fail (printf "maku.runController.model.gateway.service %q must be empty or maku-gateway.name %q: maku-gateway is enabled" $g.service $gw.name) -}}
{{- end -}}
{{- if and $g.namespace (ne $g.namespace .Release.Namespace) -}}
{{- fail (printf "maku.runController.model.gateway.namespace %q must be empty or the release namespace %q, where maku-gateway runs" $g.namespace .Release.Namespace) -}}
{{- end -}}
{{- if not $g.service -}}
{{- if ne $gw.name "model-gateway" -}}
{{- fail (printf "maku.runController.model.gateway.service is empty, which means model-gateway, but maku-gateway.name is %q: set the service to it" $gw.name) -}}
{{- end -}}
{{- end -}}
{{- $ports := $g.ports | default dict -}}
{{- if or (ne (int ($ports.public | default 8080)) (int $gw.listeners.public)) (ne (int ($ports.internal | default 8081)) (int $gw.listeners.internal)) -}}
{{- fail (printf "maku.runController.model.gateway.ports (%v, %v) must equal maku-gateway.listeners public and internal (%v, %v)" ($ports.public | default 8080) ($ports.internal | default 8081) $gw.listeners.public $gw.listeners.internal) -}}
{{- end -}}
{{- if ne ($g.audiencePrefix | default "maku-gateway") $gw.audiencePrefix -}}
{{- fail (printf "maku.runController.model.gateway.audiencePrefix %q must equal maku-gateway.audiencePrefix %q: the run's token audience is what the gateway checks" ($g.audiencePrefix | default "maku-gateway") $gw.audiencePrefix) -}}
{{- end -}}
{{- $aliases := $g.aliases | default dict -}}
{{- range $class := list "public" "internal" -}}
{{- $want := keys (index $gw.aliases $class | default dict) | sortAlpha -}}
{{- $got := index $aliases $class | default list | sortAlpha -}}
{{- if ne (join "," $got) (join "," $want) -}}
{{- fail (printf "maku.runController.model.gateway.aliases.%s [%s] must be maku-gateway.aliases.%s's keys [%s]: a run is refused unless its listener serves its alias" $class (join ", " $got) $class (join ", " $want)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end }}
