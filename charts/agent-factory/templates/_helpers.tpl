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

{{/* The profile gate, included first by every umbrella template, and alone by templates/gate/renders/before/subcharts.yaml, which
Helm renders before any subchart's template (it is deeper). The skeleton values hold no tls: or networkPolicies:, and postgres: only its enabled: false.
  - demo needs values-demo.yaml (its `demoValues` marker): a subchart reads only the values Helm hands it, so `--set profile=demo`
    alone would render half a demo.
  - production (v4 EX16) checks the seams of each enabled component, and fails once, naming every missing seam (afumbrella.production). */}}
{{- define "afumbrella.profile" -}}
{{- $profile := .Values.profile | default "" -}}
{{- /* v4 D9: the bundled single-replica Postgres has no backup, no HA and no TLS. Production lists it with its other seams. */ -}}
{{- if and (dig "enabled" false (.Values.postgres | default dict)) (ne $profile "demo") (ne $profile "production") -}}
{{- fail "the bundled Postgres is demo-only (D9): bring your own and name its DSN Secrets (maku.broker.database, docs/database.md)" -}}
{{- end -}}
{{- if eq $profile "production" -}}
{{- include "afumbrella.production" . -}}
{{- else if ne $profile "demo" -}}
{{- fail (printf "profile is required: demo (-f values-demo.yaml), or production (docs/upgrading.md#from-the-demo-to-production); got %q" $profile) -}}
{{- else if not .Values.demoValues -}}
{{- fail "profile demo needs -f values-demo.yaml (the demo's defaults moved there in v0.4x)" -}}
{{- end -}}
{{- $d := .Values.clusterDomain -}}
{{- if or (ne $d (.Values.maku.clusterDomain | default "cluster.local")) (ne $d (index .Values "maku-factory" "clusterDomain" | default "cluster.local")) -}}
{{- fail (printf "clusterDomain, maku.clusterDomain and maku-factory.clusterDomain must be equal (got %q, %q, %q): the broker certificate's names and the factory's broker URL are built from them" $d .Values.maku.clusterDomain (index .Values "maku-factory" "clusterDomain")) -}}
{{- end -}}
{{- /* v4 D10: per-run tokens are two halves. The factory mints and writes <run>-git, the run-controller mounts it: one without the other leaves every run Pending. */ -}}
{{- $perRun := dig "runTokens" "perRun" false (index .Values "maku-factory") -}}
{{- $pat := dig "runController" "gitSecret" "" .Values.maku -}}
{{- if and $perRun $pat -}}
{{- fail (printf "maku-factory.runTokens.perRun and maku.runController.gitSecret (%q): choose one: the factory writes each run's token Secret, which the run-controller mounts unless a laptop's PAT replaces it" $pat) -}}
{{- end -}}
{{- if and (index .Values "maku-factory").enabled .Values.maku.enabled (dig "runController" "enabled" true .Values.maku) (not $perRun) (not $pat) -}}
{{- fail "maku-factory.runTokens.perRun: the factory mints each run's git token, which the run-controller mounts (v4 step 8 removed the shared agent-git writer); a demo may name a laptop's PAT in maku.runController.gitSecret instead" -}}
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
{{- include "afumbrella.expose" . -}}
{{- include "afumbrella.webProxy" . -}}
{{- include "afumbrella.preflight" . -}}
{{- include "afumbrella.slack" . -}}
{{- include "afumbrella.factoryPolicy" . -}}
{{- end }}

{{/* profile production (v4 EX16, D25): a gate per enabled component. It refuses the demo's own values
and lists every missing seam of every enabled component in one failure, a line each with its key and
its doc. The agreements the umbrella checks for both profiles (identity, expose, webProxy, slack) are
listed with them. maku.enabled false with maku-factory.enabled true is a valid render: an install adopts
the umbrella one component at a time. networkPolicies.mode none and factoryInsecure stay allowed, on the
install banner (PR9); sandbox.unsafeAllowRunc does not (D18: the demo and kind only). The Slack App's
three Secret names are its schema's (minLength 1). */}}
{{- define "afumbrella.production" -}}
{{- $errs := list -}}
{{- $up := "docs/upgrading.md#from-the-demo-to-production" -}}
{{- if .Values.demoValues -}}
{{- $errs = append $errs (printf "demoValues: values-demo.yaml is the demo's; a production install starts from the skeleton (%s)" $up) -}}
{{- end -}}
{{- if dig "enabled" false (.Values.postgres | default dict) -}}
{{- $errs = append $errs "postgres.enabled: the bundled Postgres is demo-only (D9): bring your own and name its DSN Secrets (maku.broker.database, docs/database.md)" -}}
{{- end -}}
{{- if dig "plain" false (.Values.networkPolicies | default dict) -}}
{{- $errs = append $errs (printf "networkPolicies.plain: the demo's ingress-only policy; production uses networkPolicies.factory, in maku.networkPolicies' mode (%s)" $up) -}}
{{- end -}}
{{- $pin := "^[A-Za-z0-9_.-]+@sha256:[0-9a-f]+$" -}}
{{- $maku := .Values.maku | default dict -}}
{{- if $maku.enabled -}}
{{- $b := $maku.broker | default dict -}}
{{- $h := $b.human | default dict -}}
{{- $rc := $maku.runController | default dict -}}
{{- if $maku.demoValues -}}
{{- $errs = append $errs (printf "maku.demoValues: the maku chart's demo marker, which admits runController.model.mode secret (the provider key in the sandbox, D7) (%s)" $up) -}}
{{- end -}}
{{- if not (dig "e2e" "unpinnedImages" false $maku) -}}
{{- $images := list (list "maku.broker.image.tag" (dig "image" "tag" "" $b)) -}}
{{- if ne (toString $rc.enabled) "false" -}}
{{- $images = concat $images (list (list "maku.runController.image.tag" (dig "image" "tag" "" $rc)) (list "maku.runController.images.harness.tag" (dig "images" "harness" "tag" "" $rc)) (list "maku.runController.images.bridge.tag" (dig "images" "bridge" "tag" "" $rc))) -}}
{{- end -}}
{{- range $images -}}
{{- if not (regexMatch $pin (toString (index . 1))) -}}
{{- $errs = append $errs (printf "%s: <version>@sha256:<digest>, never a bare tag (got %q; docs/security.md#supply-chain)" (index . 0) (index . 1)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if eq ($h.mode | default "oidc") "local" -}}
{{- $errs = append $errs "maku.broker.human.mode: oidc (got local, one loopback user behind a port-forward; docs/identity.md)" -}}
{{- else if not $h.issuer -}}
{{- $errs = append $errs "maku.broker.human.issuer: the OIDC issuer humans sign in at (docs/identity.md#the-keys)" -}}
{{- end -}}
{{- if and (eq (dig "runIssuer" "mode" "jwks" $b) "jwks") (not (dig "runIssuer" "issuer" "" $b)) -}}
{{- $errs = append $errs "maku.broker.runIssuer.issuer: the cluster's ServiceAccount issuer the runs' tokens are verified against, or runIssuer.mode tokenreview (charts/maku/README.md)" -}}
{{- end -}}
{{- if not (dig "database" "brokerSecret" "" $b) -}}
{{- $errs = append $errs "maku.broker.database.brokerSecret: the broker's DSN Secret, rooms_broker (docs/database.md)" -}}
{{- end -}}
{{- if not (hasPrefix "https://" ($b.publicURL | default "")) -}}
{{- $errs = append $errs (printf "maku.broker.publicURL: the https URL humans reach the rooms at (got %q; docs/identity.md#the-keys)" ($b.publicURL | default "")) -}}
{{- end -}}
{{- if ne (toString $rc.enabled) "false" -}}
{{- if eq (dig "model" "mode" "gateway" $rc) "secret" -}}
{{- $errs = append $errs "maku.runController.model.mode: secret is demo-only (D7): the provider key would sit in the sandbox; set gateway, with maku-gateway or an external gateway (docs/upgrading.md#the-gateway-is-the-model-path)" -}}
{{- end -}}
{{- $sb := $maku.sandbox | default dict -}}
{{- if $sb.unsafeAllowRunc -}}
{{- $errs = append $errs "maku.sandbox.unsafeAllowRunc: runs on the node's own runtime are for the demo and kind only (D18); name the sandbox in maku.sandbox.runtimeClassName (docs/security.md#the-run-pods-admission)" -}}
{{- else if not $sb.runtimeClassName -}}
{{- $errs = append $errs "maku.sandbox.runtimeClassName: the RuntimeClass every run pod runs under, e.g. gvisor (docs/security.md#the-run-pods-admission)" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $f := index .Values "maku-factory" | default dict -}}
{{- if $f.enabled -}}
{{- if not (dig "e2e" "unpinnedImages" false $f) -}}
{{- $t := dig "image" "tag" "" $f -}}
{{- if not (regexMatch $pin (toString $t)) -}}
{{- $errs = append $errs (printf "maku-factory.image.tag: <version>@sha256:<digest>, never a bare tag (got %q; docs/security.md#supply-chain)" $t) -}}
{{- end -}}
{{- end -}}
{{- if or $f.demo (dig "config" "demo" false $f) -}}
{{- $errs = append $errs (printf "maku-factory.demo, maku-factory.config.demo: the factory's laptop demo mode (%s)" $up) -}}
{{- end -}}
{{- if not (dig "apps" "factory" "existingSecret" "" $f) -}}
{{- $errs = append $errs "maku-factory.apps.factory.existingSecret: the factory App's Secret, keys app_id and private_key (docs/github-apps.md#the-factory-app)" -}}
{{- end -}}
{{- if and $maku.enabled (not (dig "config" "api" "humanIssuer" "" $f)) -}}
{{- $errs = append $errs "maku-factory.config.api.humanIssuer: the factory verifies again each human token the broker forwards, against maku.broker.human's issuer (docs/identity.md#the-keys)" -}}
{{- end -}}
{{- end -}}
{{- range $e := concat (include "afumbrella.identityErrors" . | fromJsonArray) (include "afumbrella.exposeErrors" . | fromJsonArray) (include "afumbrella.webProxyErrors" . | fromJsonArray) (include "afumbrella.slackErrors" . | fromJsonArray) -}}
{{- $errs = append $errs $e -}}
{{- end -}}
{{- if $errs -}}
{{- fail (printf "profile production: %d missing or refused seams (docs/upgrading.md#from-the-demo-to-production):\n  - %s" (len $errs) (join "\n  - " $errs)) -}}
{{- end -}}
{{- end }}

{{/* The preflight (v4 D24) collects the factory's /preflight, whose exposure check reads the
namespace's routes: the factory's Role grants that read only with its own preflight.enabled. */}}
{{- define "afumbrella.preflight" -}}
{{- $mf := index .Values "maku-factory" | default dict -}}
{{- if and .Values.maku.enabled (dig "preflight" "enabled" false .Values.maku) $mf.enabled (not (dig "preflight" "enabled" false $mf)) -}}
{{- fail "maku.preflight.enabled needs maku-factory.preflight.enabled: the factory's exposure check reads the namespace's HTTPRoutes and Ingresses, which only that grants" -}}
{{- end -}}
{{- end }}

{{/* The humans' identity agreement (v4 D8, PR2): the broker forwards each human's token to the
factory, which verifies it again, so both must read the same issuer, preset, token use, roles claim,
group names, audience and sponsored client claim. The client ids and an audienceFile are files,
read at use, so a render cannot compare them. Checked once the factory takes human tokens (config.api.humanIssuer set) and the
broker is in oidc mode; each refusal names both keys. Empty values compare as their defaults. */}}
{{- define "afumbrella.identityErrors" -}}
{{- $errs := list -}}
{{- $h := dig "broker" "human" dict (.Values.maku | default dict) -}}
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
{{- $errs = append $errs (printf "maku.broker.human.%s %q and maku-factory.config.api.%s %q must be equal: the factory verifies the token the broker forwards" (index $pair 0) (index $pair 2) (index $pair 1) (index $pair 3)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $errs | toJson -}}
{{- end }}

{{- define "afumbrella.identity" -}}
{{- with include "afumbrella.identityErrors" . | fromJsonArray }}{{ fail (first .) }}{{ end -}}
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
{{- /* The run-controller is maku's: a release without maku (an install adopting the umbrella factory first) has no runs to route. */ -}}
{{- if (.Values.maku | default dict).enabled -}}
{{- include "afumbrella.runsGateway" . -}}
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

{{/* The run-controller's model.mode gateway (plan v4 step 4, Task 10): runs call either this release's
maku-gateway, which must then serve exactly the aliases, ports and prefix the run-controller's config
names, or an external gateway named by maku.runController.model.gateway.service (ANR's form). */}}
{{- define "afumbrella.runsGateway" -}}
{{- $gw := index .Values "maku-gateway" -}}
{{- $m := dig "runController" "model" dict .Values.maku -}}
{{- $mode := $m.mode | default "gateway" -}}
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

{{/* maku's networkPolicies mode, as its ap.netMode reads it: kubernetes by default. maku's own
helper validates the value. */}}
{{- define "afumbrella.makuNetMode" -}}
{{- $np := (.Values.maku | default dict).networkPolicies | default dict -}}
{{- $np.mode | default "kubernetes" -}}
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

{{/* Exposure (plan v4 step 6, EX2, EX6; D19): the surfaces expose.mode renders, as JSON
{"web": bool, "slack": bool}. Read it with `include "afumbrella.exposed" . | fromJson`. */}}
{{- define "afumbrella.exposed" -}}
{{- $x := .Values.expose | default dict -}}
{{- $on := ne ($x.mode | default "none") "none" -}}
{{- $web := and $on (ne (toString (dig "web" "enabled" true $x)) "false") -}}
{{- $slack := and $on (dig "enabled" false (.Values.slack | default dict)) (ne (toString (dig "slack" "enabled" true $x)) "false") -}}
{{- dict "web" $web "slack" $slack | toJson -}}
{{- end }}

{{/* The exposure rules (EX2, EX6), checked by the profile gate. Each surface needs its hosts and, by
mode, its parentRefs or IngressClass. The web is the broker's :8080 through the web front door, and
never around it: the broker in oidc mode reads oauth2-proxy's identity headers, so it needs the proxy
and a network policy that admits only the proxy. Default-deny would drop every request a policy does not
admit, so the gateway's pods must be named. */}}
{{- define "afumbrella.exposeErrors" -}}
{{- $errs := list -}}
{{- $x := .Values.expose | default dict -}}
{{- $surfaces := include "afumbrella.exposed" . | fromJson -}}
{{- $maku := .Values.maku | default dict -}}
{{- $netMode := include "afumbrella.makuNetMode" . -}}
{{- range $name := list "web" "slack" -}}
{{- if index $surfaces $name -}}
{{- $s := index $x $name | default dict -}}
{{- if eq $name "web" -}}
{{- if not $maku.enabled -}}
{{- $errs = append $errs "expose.web needs maku.enabled: the web surface is the broker" -}}
{{- end -}}
{{- if not $s.hostnames -}}
{{- $errs = append $errs "expose.web.hostnames is empty: name the hosts the web is served at" -}}
{{- end -}}
{{- if eq (dig "broker" "human" "mode" "oidc" $maku) "local" -}}
{{- $errs = append $errs "expose.web: the broker serves humans on a port-forward in local mode (maku.broker.human.mode local); set it to oidc, or expose.web.enabled false" -}}
{{- end -}}
{{- if not (dig "enabled" false ($.Values.webProxy | default dict)) -}}
{{- $errs = append $errs "expose.web needs webProxy.enabled: in oidc mode the broker reads oauth2-proxy's identity headers, which only the web front door may set" -}}
{{- end -}}
{{- if eq $netMode "none" -}}
{{- $errs = append $errs "expose.web needs maku.networkPolicies.mode cilium or kubernetes: in mode none nothing keeps a client from the broker's :8080 around the web front door" -}}
{{- end -}}
{{- else if not $s.hostnames -}}
{{- $errs = append $errs "expose.slack.hostnames is empty: name the host Slack posts its events to" -}}
{{- end -}}
{{- if and (eq $x.mode "httproute") (not $s.parentRefs) (not (dig "httproute" "parentRefs" list $x)) -}}
{{- $errs = append $errs (printf "expose.%s.parentRefs and expose.httproute.parentRefs are empty: name the Gateway (and its listener) the %s route attaches to" $name $name) -}}
{{- end -}}
{{- if and (eq $x.mode "ingress") (not $s.className) (not (dig "ingress" "className" "" $x)) -}}
{{- $errs = append $errs (printf "expose.%s.className and expose.ingress.className are empty: name the IngressClass the %s Ingress uses" $name $name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if or $surfaces.web $surfaces.slack -}}
{{- if and (eq $netMode "kubernetes") (not (dig "networkPolicy" "from" list $x)) -}}
{{- $errs = append $errs "expose.networkPolicy.from is empty: name the pods of your gateway or ingress controller (maku.networkPolicies.mode kubernetes is default-deny)" -}}
{{- end -}}
{{- if and (eq $netMode "cilium") (not (dig "networkPolicy" "cilium" "fromEntities" list $x)) -}}
{{- $errs = append $errs "expose.networkPolicy.cilium.fromEntities is empty: name the Cilium identity of your gateway or ingress controller (maku.networkPolicies.mode cilium is default-deny)" -}}
{{- end -}}
{{- end -}}
{{- $errs | toJson -}}
{{- end }}

{{- define "afumbrella.expose" -}}
{{- with include "afumbrella.exposeErrors" . | fromJsonArray }}{{ fail (first .) }}{{ end -}}
{{- end }}

{{/* The identity headers oauth2-proxy may set (EX2), which the web route removes from every client
request by exact name, and the forwarded headers a proxy trusting its client would read (the security
review of #545: X-Forwarded-Uri once selected a skip-auth route). A YAML list. */}}
{{- define "afumbrella.identityHeaders" -}}
- X-Forwarded-Access-Token
- X-Forwarded-User
- X-Forwarded-Email
- X-Forwarded-Groups
- X-Forwarded-Preferred-Username
- X-Auth-Request-User
- X-Auth-Request-Email
- X-Auth-Request-Preferred-Username
- X-Auth-Request-Groups
- X-Auth-Request-Access-Token
- X-Forwarded-Uri
- X-Forwarded-Host
- X-Forwarded-Proto
{{- end }}

{{/* The web front door's checks (EX4): it derives its arguments from the broker's oidc identity, so it
needs that identity, its Secrets by name (D14), and the broker's agreement (maku.broker.webProxy opens
:8080 to it alone). Without a default-deny policy the broker would be reachable around it. */}}
{{- define "afumbrella.webProxyErrors" -}}
{{- $errs := list -}}
{{- $w := .Values.webProxy | default dict -}}
{{- $maku := .Values.maku | default dict -}}
{{- $b := $maku.broker | default dict -}}
{{- if $w.enabled -}}
{{- if not $maku.enabled -}}
{{- $errs = append $errs "webProxy.enabled needs maku.enabled: the web front door is the broker's" -}}
{{- end -}}
{{- if eq (dig "human" "mode" "oidc" $b) "local" -}}
{{- $errs = append $errs "webProxy.enabled needs maku.broker.human.mode oidc: in local mode the broker serves one loopback user, no identity provider" -}}
{{- end -}}
{{- if not (dig "human" "issuer" "" $b) -}}
{{- $errs = append $errs "webProxy.enabled needs maku.broker.human.issuer: oauth2-proxy signs people in there" -}}
{{- end -}}
{{- if not (hasPrefix "https://" ($b.publicURL | default "")) -}}
{{- $errs = append $errs (printf "webProxy.enabled needs maku.broker.publicURL, https (got %q): the sign-in returns to <publicURL>/oauth2/callback" ($b.publicURL | default "")) -}}
{{- end -}}
{{- if not $w.existingSecret -}}
{{- $errs = append $errs (printf "webProxy.existingSecret is empty: name the Secret holding the web client and the cookie secret (keys %s, %s, %s; D14)" $w.keys.clientID $w.keys.clientSecret $w.keys.cookieSecret) -}}
{{- end -}}
{{- if not (dig "oidc" "existingSecret" "" $b) -}}
{{- $errs = append $errs "webProxy.enabled needs maku.broker.oidc.existingSecret: its cli key is the CLI client oauth2-proxy lets maku's bearers through for" -}}
{{- end -}}
{{- if eq (include "afumbrella.makuNetMode" .) "none" -}}
{{- $errs = append $errs "webProxy.enabled needs maku.networkPolicies.mode cilium or kubernetes: in mode none nothing keeps a client from the broker's :8080 around the web front door" -}}
{{- end -}}
{{- /* The derived arguments read the identity checked above: only once it holds. */ -}}
{{- if not $errs -}}
{{- $derived := include "afumbrella.webProxyArgs" . | fromJson -}}
{{- range $k := keys ($w.extraArgs | default dict) | sortAlpha -}}
{{- /* Derived, or a flag that would widen what passes the front door: a trusted forwarder's headers
(trusted-ip, reverse-proxy), more unauthenticated routes or domains, or a config file that replaces the
flags. */ -}}
{{- $forbidden := list "client-id" "client-secret" "cookie-secret" "extra-jwt-issuers" "scope" "http-address" "metrics-address"
  "trusted-ip" "skip-auth-regex" "skip-auth-preflight" "whitelist-domain" "config" "alpha-config" "reverse-proxy"
  "pass-user-headers" "skip-auth-strip-headers" -}}
{{- if or (hasKey $derived $k) (has $k $forbidden) -}}
{{- $errs = append $errs (printf "webProxy.extraArgs.%s: the chart derives it or forbids it (docs/exposure.md#the-web-front-door); it may not be set" $k) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range ($w.extraEnv | default list) -}}
{{- if hasPrefix "OAUTH2_PROXY_" (.name | default "") -}}
{{- $errs = append $errs (printf "webProxy.extraEnv %s: oauth2-proxy reads it as a flag, which only the chart sets (use webProxy.extraArgs)" .name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if ne (toString (dig "webProxy" false $b)) (toString ($w.enabled | default false)) -}}
{{- $errs = append $errs (printf "maku.broker.webProxy (%v) must equal webProxy.enabled (%v): it opens the broker's :8080 to the web front door, and to it alone" (dig "webProxy" false $b) ($w.enabled | default false)) -}}
{{- end -}}
{{- $errs | toJson -}}
{{- end }}

{{- define "afumbrella.webProxy" -}}
{{- with include "afumbrella.webProxyErrors" . | fromJsonArray }}{{ fail (first .) }}{{ end -}}
{{- end }}

{{/* The GitHub account link is on (step 5 PR C): broker.githubLink's Secret, or human.githubLink set
directly. Its callback is a browser redirect from github.com, which arrives without the SameSite
session cookie, so it is a skip-auth route. */}}
{{- define "afumbrella.githubLink" -}}
{{- $b := (.Values.maku | default dict).broker | default dict -}}
{{- if or (dig "githubLink" "existingSecret" "" $b) (dig "human" "githubLink" dict $b) }}true{{ end -}}
{{- end }}

{{/* oauth2-proxy's derived arguments (EX4), as JSON {name: value | [values]}: each is rendered
--name=value, once per value of a list. The client, the cookie secret, the CLI issuer and (zitadel) the
scope come from the environment, built from Secrets, never from a rendered value. */}}
{{- define "afumbrella.webProxyArgs" -}}
{{- $w := .Values.webProxy -}}
{{- $b := .Values.maku.broker -}}
{{- $h := $b.human | default dict -}}
{{- $preset := $h.preset | default "zitadel" -}}
{{- $skip := list "GET=^/\\.well-known/roomctl$" -}}
{{- if include "afumbrella.githubLink" . -}}
{{- $skip = append $skip "GET=^/link/github/callback$" -}}
{{- end -}}
{{- $a := dict
  "provider" "oidc"
  "oidc-issuer-url" $h.issuer
  "redirect-url" (printf "%s/oauth2/callback" (trimSuffix "/" $b.publicURL))
  "upstream" (printf "http://room-broker.%s.svc.%s:8080" .Release.Namespace .Values.clusterDomain)
  "email-domain" "*"
  "pass-authorization-header" "true"
  "pass-access-token" "true"
  "skip-auth-strip-headers" "true"
  "pass-user-headers" "true"
  "pass-basic-auth" "false"
  "set-xauthrequest" "false"
  "cookie-name" "_rooms"
  "cookie-secure" "true"
  "cookie-httponly" "true"
  "cookie-samesite" $w.cookieSameSite
  "cookie-refresh" $w.cookieRefresh
  "reverse-proxy" "false"
  "skip-provider-button" "true"
  "skip-jwt-bearer-tokens" "true"
  "skip-auth-route" $skip -}}
{{- if eq $preset "generic" -}}
{{- $scopes := list "openid" "profile" "email" "offline_access" -}}
{{- range ($h.scopes | default list) }}{{ if not (has . $scopes) }}{{ $scopes = append $scopes . }}{{ end }}{{ end -}}
{{- $_ := set $a "scope" (join " " $scopes) -}}
{{- end -}}
{{- /* Defense in depth: oauth2-proxy reads a top-level claim name (Task 9, X5); the broker stays the
authority, refusing a token in no agents group. A pointer (/a/b) or a dotted name is left to it. */ -}}
{{- if regexMatch "^[A-Za-z0-9_:-]+$" ($h.rolesClaim | default "") -}}
{{- $_ := set $a "oidc-groups-claim" $h.rolesClaim -}}
{{- $_ := set $a "allowed-group" (list (dig "groups" "admin" "" $h) (dig "groups" "member" "" $h)) -}}
{{- end -}}
{{- toJson $a -}}
{{- end }}

{{/* The web front door's labels: its name is what the broker's policies admit on :8080. */}}
{{- define "afumbrella.webProxyLabels" -}}
{{ include "afumbrella.labels" . }}
app.kubernetes.io/name: rooms-web-proxy
app.kubernetes.io/component: web-proxy
{{- end }}

{{/* afumbrella.at: the value at a dotted path of a values map, as {"v": value}; null when absent.
Call with (dict "v" <map> "path" "a.b.c"), read with fromJson. */}}
{{- define "afumbrella.at" -}}
{{- $cur := .v -}}
{{- range (splitList "." .path) -}}
{{- if kindIs "map" $cur }}{{ $cur = index $cur . }}{{ else }}{{ $cur = nil }}{{ end -}}
{{- end -}}
{{- dict "v" $cur | toJson -}}
{{- end -}}

{{/* The Slack App's agreements (plan v4 step 6, EX7, EX8). Helm cannot set one subchart's values from
another's, so the umbrella checks they agree: the broker's subscriber is the App's :8081 with the App's
signing key, both the broker and the factory sponsor the App's principal, and a set brokerURL is the
broker's human listener. Each side is checked only when its component is in this release. Off, no
subscriber may still point at the App: every event would end in RoomWebhookDisabled. */}}
{{- define "afumbrella.slackErrors" -}}
{{- $errs := list -}}
{{- $s := .Values.slack | default dict -}}
{{- $maku := .Values.maku | default dict -}}
{{- $b := $maku.broker | default dict -}}
{{- $hooks := $b.webhooks | default list -}}
{{- $ns := .Release.Namespace -}}
{{- $want := printf "http://factory-slack-app.%s.svc.%s:8081/webhooks" $ns .Values.clusterDomain -}}
{{- if $s.enabled -}}
{{- $principal := $s.principal | default "slack-app" -}}
{{- $secret := $s.webhookSecret | default "room-broker-webhook-slack-app" -}}
{{- if $maku.enabled -}}
{{- $hook := dict -}}
{{- range $hooks }}{{ if eq .name $principal }}{{ $hook = . }}{{ end }}{{ end -}}
{{- if not $hook -}}
{{- $errs = append $errs (printf "slack.enabled needs the App's subscriber in maku.broker.webhooks: name %s, network cluster, url %s, existingSecret %s" $principal $want $secret) -}}
{{- else -}}
{{- if ne ($hook.network | default "") "cluster" -}}
{{- $errs = append $errs (printf "maku.broker.webhooks[%s].network %q must be cluster: the App's :8081 is in-cluster only" $principal ($hook.network | default "")) -}}
{{- end -}}
{{- if ne $hook.url $want -}}
{{- $errs = append $errs (printf "maku.broker.webhooks[%s].url %q must be %s, the App's webhooks listener" $principal $hook.url $want) -}}
{{- end -}}
{{- if ne ($hook.existingSecret | default "") $secret -}}
{{- $errs = append $errs (printf "maku.broker.webhooks[%s].existingSecret %q must equal slack.webhookSecret %q: the App verifies with the key the broker signs with" $principal ($hook.existingSecret | default "") $secret) -}}
{{- end -}}
{{- /* The App reads signing-key from that Secret: another key, or a secretFile elsewhere, signs with
a key the App never reads, and every delivery is refused. */ -}}
{{- if not (has ($hook.key | default "signing-key") (list "signing-key")) -}}
{{- $errs = append $errs (printf "maku.broker.webhooks[%s].key %q must be empty or signing-key: the App reads signing-key from slack.webhookSecret" $principal $hook.key) -}}
{{- end -}}
{{- if $hook.secretFile -}}
{{- $errs = append $errs (printf "maku.broker.webhooks[%s].secretFile %q must be unset: the chart mounts slack.webhookSecret's signing-key, the key the App verifies with" $principal $hook.secretFile) -}}
{{- end -}}
{{- end -}}
{{- $hi := dig "human" "issuer" "" $b -}}
{{- if and $hi $s.issuerURL (ne (trimSuffix "/" $s.issuerURL) (trimSuffix "/" $hi)) -}}
{{- $errs = append $errs (printf "slack.issuerURL %q must equal maku.broker.human.issuer %q: the broker verifies the App's token against its issuer" $s.issuerURL $hi) -}}
{{- end -}}
{{- $named := false -}}
{{- range (dig "human" "mcp" "sponsored" list $b) }}{{ if eq .name $principal }}{{ $named = true }}{{ end }}{{ end -}}
{{- if not $named -}}
{{- $errs = append $errs (printf "slack.principal %q must be named by maku.broker.human.mcp.sponsored and maku-factory.config.api.sponsored: the broker lacks it" $principal) -}}
{{- end -}}
{{- $broker := printf "http://room-broker.%s.svc.%s:8080" $ns .Values.clusterDomain -}}
{{- if and $s.brokerURL (ne $s.brokerURL $broker) -}}
{{- $errs = append $errs (printf "slack.brokerURL %q must be the broker's human listener, %s" $s.brokerURL $broker) -}}
{{- end -}}
{{- end -}}
{{- if $maku.enabled -}}
{{- $mode := include "afumbrella.makuNetMode" . -}}
{{- $smode := dig "networkPolicy" "mode" "none" $s -}}
{{- if ne $smode $mode -}}
{{- $errs = append $errs (printf "slack.networkPolicy.mode %q must equal maku.networkPolicies.mode %q: the App joins the release's default-deny" $smode $mode) -}}
{{- end -}}
{{- end -}}
{{- if (include "afumbrella.exposed" . | fromJson).slack -}}
{{- $snp := $s.networkPolicy | default dict -}}
{{- if and (eq ($snp.mode | default "none") "kubernetes") (not $snp.from) -}}
{{- $errs = append $errs "slack.networkPolicy.from is empty: expose.slack routes Slack's events to the App's :8080, which default-deny then drops; name your gateway's or ingress controller's pods (as expose.networkPolicy.from)" -}}
{{- end -}}
{{- if and (eq ($snp.mode | default "none") "cilium") (not (dig "cilium" "fromEntities" (list "ingress") $snp)) -}}
{{- $errs = append $errs "slack.networkPolicy.cilium.fromEntities is empty: expose.slack routes Slack's events to the App's :8080, which default-deny then drops" -}}
{{- end -}}
{{- end -}}
{{- $f := index .Values "maku-factory" | default dict -}}
{{- if $f.enabled -}}
{{- $named := false -}}
{{- range (dig "config" "api" "sponsored" list $f) }}{{ if eq .name $principal }}{{ $named = true }}{{ end }}{{ end -}}
{{- if not $named -}}
{{- $errs = append $errs (printf "slack.principal %q must be named by maku.broker.human.mcp.sponsored and maku-factory.config.api.sponsored: the factory lacks it" $principal) -}}
{{- end -}}
{{- end -}}
{{- else if $maku.enabled -}}
{{- range $hooks -}}
{{- if eq (first (splitList "." ((urlParse (.url | default "")).hostname))) "factory-slack-app" -}}
{{- $errs = append $errs (printf "maku.broker.webhooks[%s] targets factory-slack-app, but slack.enabled is false: every event would go to a subscriber that does not exist" .name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $errs | toJson -}}
{{- end }}

{{- define "afumbrella.slack" -}}
{{- with include "afumbrella.slackErrors" . | fromJsonArray }}{{ fail (first .) }}{{ end -}}
{{- end }}

{{/* The factory's default-deny (plan v4 step 6, Task 16): it joins maku's mode, so mode none has no
default-deny to join; the demo's ingress-only plain policy would select the same pods. */}}
{{- define "afumbrella.factoryPolicy" -}}
{{- if dig "factory" "enabled" false (.Values.networkPolicies | default dict) -}}
{{- if eq (include "afumbrella.makuNetMode" .) "none" -}}
{{- fail "networkPolicies.factory.enabled needs maku.networkPolicies.mode cilium or kubernetes: the factory joins the release's default-deny" -}}
{{- end -}}
{{- if .Values.networkPolicies.plain -}}
{{- fail "networkPolicies.factory.enabled and networkPolicies.plain both select the factory: plain is the demo's ingress-only policy; turn it off" -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/* The factory's URL-derived egress (the review of #556), as JSON {"rules": [{id, key, host, port,
to, external}]}: one rule per configured URL of maku-factory.config, plus meter and otlp when only
their peers are named. An external host is opened by name (cilium toFQDNs) or as public addresses
(kubernetes); an in-cluster one needs networkPolicies.factory.<id>.to, or the render fails naming it. */}}
{{- define "afumbrella.factoryEgress" -}}
{{- $f := .Values.networkPolicies.factory | default dict -}}
{{- $c := dig "config" dict (index .Values "maku-factory") -}}
{{- $rules := list -}}
{{- range $pair := list
  (list "idp" "api.humanIssuer" (dig "api" "humanIssuer" "" $c))
  (list "classifier" "triage.classifierURL" (dig "triage" "classifierURL" "" $c))
  (list "meter" "meter.url" (dig "meter" "url" "" $c))
  (list "logs" "meter.logsURL" (dig "meter" "logsURL" "" $c))
  (list "otlp" "tracing.otlpEndpoint" (dig "tracing" "otlpEndpoint" "" $c))
  (list "agentGuard" "hardening.agentGuard.url" (dig "hardening" "agentGuard" "url" "" $c))
  (list "gatewayLog" "hardening.drills.gatewayLog.url" (dig "hardening" "drills" "gatewayLog" "url" "" $c))
  (list "octoSTS" "hardening.publisher.octoSTS.url" (dig "hardening" "publisher" "octoSTS" "url" "" $c)) -}}
{{- $id := index $pair 0 -}}
{{- $key := index $pair 1 -}}
{{- $raw := tpl (toString (index $pair 2)) $ -}}
{{- $e := index $f $id | default dict -}}
{{- if or $raw $e.to -}}
{{- $host := "" -}}
{{- $port := 0 -}}
{{- if $raw -}}
{{- $url := ternary $raw (printf "grpc://%s" $raw) (contains "://" $raw) -}}
{{- $u := urlParse $url -}}
{{- $host = $u.hostname | default (first (splitList ":" $u.host)) -}}
{{- $port = ternary 443 80 (eq $u.scheme "https") -}}
{{- if regexMatch ":[0-9]+$" $u.host }}{{ $port = regexFind "[0-9]+$" $u.host | int }}{{ end -}}
{{- end -}}
{{- with $e.port }}{{ $port = int . }}{{ end -}}
{{- if not $port -}}
{{- fail (printf "networkPolicies.factory.%s.port is required with its to: the port the factory dials" $id) -}}
{{- end -}}
{{- $internal := or (not $host) (not (contains "." $host)) (regexMatch "\\.svc(\\..+)?$" $host) (hasSuffix ".local" $host) -}}
{{- if and $internal (not $e.to) -}}
{{- fail (printf "maku-factory.config.%s %q is in-cluster: name its pods in networkPolicies.factory.%s.to, or the factory's default-deny drops every call" $key $raw $id) -}}
{{- end -}}
{{- $rules = append $rules (dict "id" $id "key" $key "host" $host "port" $port "to" ($e.to | default list) "external" (not $e.to)) -}}
{{- end -}}
{{- end -}}
{{- dict "rules" $rules | toJson -}}
{{- end }}
