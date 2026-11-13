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
{{- /* v4 D10: per-run tokens are two halves. The factory mints and writes <run>-git, the run-controller mounts it: one without the other leaves every run Pending. */ -}}
{{- $perRun := dig "runTokens" "perRun" false (index .Values "maku-factory") -}}
{{- $gitTokens := dig "runController" "gitTokens" "shared" .Values.maku -}}
{{- if ne (toString $perRun) (toString (eq $gitTokens "perRun")) -}}
{{- fail (printf "maku-factory.runTokens.perRun (%v) and maku.runController.gitTokens (%q) go together: the factory writes each run's token Secret, the run-controller mounts it; set both (perRun) or neither" $perRun $gitTokens) -}}
{{- end -}}
{{- if $perRun -}}
{{- $creators := dig "agentRunPolicies" "creators" list .Values.maku -}}
{{- if not $creators -}}
{{- fail "maku-factory.runTokens.perRun needs maku.agentRunPolicies.creators: the factory mints a token for every run it can account for, so only the factory may create AgentRuns (plan PR4)" -}}
{{- end -}}
{{- $factory := printf "system:serviceaccount:%s:agent-factory" .Release.Namespace -}}
{{- if not (has $factory $creators) -}}
{{- fail (printf "maku.agentRunPolicies.creators %v does not name this release's factory, %s: it could not create a run" $creators $factory) -}}
{{- end -}}
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
{{- include "afumbrella.gatewayClients" . -}}
{{- end -}}
{{- end }}

{{/* The broker's clients on the gateway (plan v4 step 4, Task 12, GW8). A gateway that serves the
planner or the classifier needs the broker pointed at it (maku.broker.modelGateway), on its clients
port and audience prefix. A broker that derives a client's route from that gateway needs the gateway
to serve that client: otherwise every plan or classification would answer 404. */}}
{{- define "afumbrella.gatewayClients" -}}
{{- $gw := index .Values "maku-gateway" -}}
{{- $mg := .Values.maku.broker.modelGateway | default dict -}}
{{- $human := .Values.maku.broker.human | default dict -}}
{{- $clients := $gw.clients | default dict -}}
{{- $served := list -}}
{{- range $c := list "planner" "classifier" -}}
{{- if (index $clients $c | default dict).enabled -}}{{- $served = append $served $c -}}{{- end -}}
{{- end -}}
{{- if and $served (ne ($mg.service | default "") $gw.name) -}}
{{- fail (printf "maku-gateway.clients.%s.enabled needs maku.broker.modelGateway.service %q (got %q): the broker calls the gateway's clients listener" (first $served) $gw.name ($mg.service | default "")) -}}
{{- end -}}
{{- if eq ($mg.service | default "") $gw.name -}}
{{- if ne ($mg.namespace | default .Release.Namespace) .Release.Namespace -}}
{{- fail (printf "maku.broker.modelGateway.namespace %q must be the release namespace %q, where the umbrella runs maku-gateway" $mg.namespace .Release.Namespace) -}}
{{- end -}}
{{- if ne (int $mg.port) (int $gw.listeners.clients) -}}
{{- fail (printf "maku.broker.modelGateway.port %v must equal maku-gateway.listeners.clients %v" $mg.port $gw.listeners.clients) -}}
{{- end -}}
{{- if ne $mg.audiencePrefix $gw.audiencePrefix -}}
{{- fail (printf "maku.broker.modelGateway.audiencePrefix %q must equal maku-gateway.audiencePrefix %q: the gateway checks each client's audience" $mg.audiencePrefix $gw.audiencePrefix) -}}
{{- end -}}
{{- $plan := dig "intake" "plan" dict $human -}}
{{- if and $plan.enabled (not $plan.url) (not (has "planner" $served)) -}}
{{- fail "maku.broker.human.intake.plan routes through maku-gateway (no url set), but maku-gateway.clients.planner.enabled is false: give the planner its provider and model there" -}}
{{- end -}}
{{- if and $human.slackIntents (not $human.slackIntents.url) (not (has "classifier" $served)) -}}
{{- fail "maku.broker.human.slackIntents routes through maku-gateway (no url set), but maku-gateway.clients.classifier.enabled is false: give the classifier its provider and model there" -}}
{{- end -}}
{{- end -}}
{{- end }}
