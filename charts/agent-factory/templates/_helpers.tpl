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
{{- include "afumbrella.identity" . -}}
{{- end }}

{{/* The humans' identity agreement (v4 D8, PR2): the broker forwards each human's token to the
factory, which verifies it again, so both must read the same issuer, preset, token use, roles claim,
group names, audience and sponsored client claim. The client ids and an audienceFile are files,
read at use, so a render cannot compare them. Checked once the factory takes human tokens (config.api.humanIssuer set) and the
broker is in oidc mode; each refusal names both keys. Empty values compare as their defaults. */}}
{{- define "afumbrella.identity" -}}
{{- $h := .Values.maku.broker.human | default dict -}}
{{- $a := dig "config" "api" dict (index .Values "maku-factory") -}}
{{- if and .Values.maku.enabled (index .Values "maku-factory" "enabled") (ne ($h.mode | default "oidc") "local") $a.humanIssuer -}}
{{- $hp := $h.preset | default "zitadel" -}}
{{- $ap := $a.preset | default "zitadel" -}}
{{- $use := dict "zitadel" "access" "generic" "id" -}}
{{- $hg := $h.groups | default dict -}}
{{- $ag := $a.groups | default dict -}}
{{- range $pair := list
  (list "preset" "preset" $hp $ap)
  (list "issuer" "humanIssuer" ($h.issuer | default "") $a.humanIssuer)
  (list "tokenUse" "tokenUse" ($h.tokenUse | default (get $use $hp) | default "") ($a.tokenUse | default (get $use $ap) | default ""))
  (list "rolesClaim" "rolesClaim" ($h.rolesClaim | default "") ($a.rolesClaim | default ""))
  (list "groups.admin" "groups.admin" ($hg.admin | default "") ($ag.admin | default "agents-admin"))
  (list "groups.member" "groups.member" ($hg.member | default "") ($ag.member | default "agents-member"))
  (list "audience" "audience" ($h.audience | default "") ($a.audience | default ""))
  (list "mcp.sponsoredClientClaim" "sponsoredClientClaim" (dig "mcp" "sponsoredClientClaim" "" $h) ($a.sponsoredClientClaim | default "")) -}}
{{- if ne (index $pair 2) (index $pair 3) -}}
{{- fail (printf "maku.broker.human.%s %q and maku-factory.config.api.%s %q must be equal: the factory verifies the token the broker forwards" (index $pair 0) (index $pair 2) (index $pair 1) (index $pair 3)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
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
{{- include "afumbrella.runsGateway" . -}}
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
{{- $factory := index .Values "maku-factory" | default dict -}}
{{- if and $factory.enabled (eq (dig "config" "runs" "group" "" $factory) "maku.ogenki.io") (not $identity) -}}
{{- fail "maku.runController.model.mode gateway needs maku-factory.config.runs.gatewayIdentity: true: the factory's runs then carry a gateway identity, which the meter prices (GW10)" -}}
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

{{/* maku's networkPolicies mode, as its ap.netMode reads it: mode wins; unset, enabled decides
(true, the default: cilium). maku's own helper validates the value. */}}
{{- define "afumbrella.makuNetMode" -}}
{{- $np := (.Values.maku | default dict).networkPolicies | default dict -}}
{{- $np.mode | default (ternary "cilium" "none" (ne (toString $np.enabled) "false")) -}}
{{- end }}

{{/* Monitoring (plan v4 step 6, EX12). A scrape object whose target sits behind a default-deny
policy that does not admit the scraper reads nothing but `up == 0`, and the Down alerts fire forever:
the render fails instead. maku mode kubernetes: the broker's, the run-controller's and the egress
proxy's NetworkPolicies admit :9090 from metricsFrom. maku mode cilium: the umbrella's CNP
maku-metrics admits cilium.fromEndpoints. maku-gateway: its policy admits metrics.allowFrom on
:15020, unless its mode is none. */}}
{{- define "afumbrella.monitoring" -}}
{{- $sm := dig "serviceMonitor" dict (.Values.monitoring | default dict) -}}
{{- $maku := .Values.maku | default dict -}}
{{- if $sm.enabled -}}
{{- $mode := include "afumbrella.makuNetMode" . -}}
{{- if and $maku.enabled (eq $mode "kubernetes") (not (dig "networkPolicies" "kubernetes" "metricsFrom" list $maku)) -}}
{{- fail "monitoring.serviceMonitor.enabled in maku.networkPolicies.mode kubernetes needs maku.networkPolicies.kubernetes.metricsFrom: the scraper could not reach :9090 (name your Prometheus pods there)" -}}
{{- end -}}
{{- if and $maku.enabled (eq $mode "cilium") (not (dig "cilium" "fromEndpoints" list $sm)) -}}
{{- fail "monitoring.serviceMonitor.enabled in maku.networkPolicies.mode cilium needs monitoring.serviceMonitor.cilium.fromEndpoints: the scraper could not reach :9090 (select your Prometheus pods there)" -}}
{{- end -}}
{{- $gw := index .Values "maku-gateway" | default dict -}}
{{- $gwMode := dig "networkPolicy" "mode" "" $gw | default "kubernetes" -}}
{{- if and $gw.enabled (ne $gwMode "none") (not (dig "metrics" "allowFrom" list $gw)) -}}
{{- fail (printf "monitoring.serviceMonitor.enabled with maku-gateway.networkPolicy.mode %q needs maku-gateway.metrics.allowFrom: the scraper could not reach :15020 (name your Prometheus pods there)" $gwMode) -}}
{{- end -}}
{{- end -}}
{{- end }}
