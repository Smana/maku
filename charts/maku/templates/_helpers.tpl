{{/* Labels of one component: `ap.labelsFor` takes (dict "root" . "name" "run-controller"); `ap.labels` is the broker's. */}}
{{- define "ap.labelsFor" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/part-of: maku
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
{{- end -}}

{{- define "ap.labels" -}}
{{- include "ap.labelsFor" (dict "root" . "name" "room-broker") -}}
{{- end -}}

{{/* The image by digest: a bare tag can move under a running broker. e2e.unpinnedImages lifts it for kind-loaded images. */}}
{{- define "ap.image" -}}
{{- $tag := required "image.tag is <version>@sha256:<digest>" .image.tag -}}
{{- if and (not .root.Values.e2e.unpinnedImages) (not (regexMatch "^[A-Za-z0-9_.-]+@sha256:[0-9a-f]+$" $tag)) -}}
{{- fail "image.tag is <version>@sha256:<digest>, never a bare tag" -}}
{{- end -}}
{{ .image.repository }}:{{ $tag }}
{{- end -}}

{{/* A kind-loaded image is not in any registry: never pull it. */}}
{{- define "ap.pullPolicy" -}}
{{- if .Values.e2e.unpinnedImages }}imagePullPolicy: Never{{ end -}}
{{- end -}}

{{/* The run issuer's subject pattern, from `runs` alone (3a I2). */}}
{{- define "ap.runSubPattern" -}}
{{- printf "^system:serviceaccount:%s:%s([a-z2-7]{8})$" .Values.runs.namespace .Values.runs.namePrefix -}}
{{- end -}}

{{- define "ap.local" -}}
{{- if eq (.Values.broker.human.mode | default "oidc") "local" }}true{{ end -}}
{{- end -}}

{{- define "ap.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities: {drop: [ALL]}
{{- end -}}

{{/* The run pod's sandbox, resolved from sandbox.*, as JSON: {"class": <RuntimeClass name, "" for
none>, "allowUnsandboxed": <bool>} (the run-controller's config keys). Read it with
`include "ap.sandbox" . | fromJson`. The run-controller's older keys are removed (v4 step 8): the
schema refuses them, and so does this helper under --skip-schema-validation. */}}
{{- define "ap.sandbox" -}}
{{- $s := .Values.sandbox | default dict -}}
{{- $c := .Values.runController -}}
{{- if hasKey $c "runtimeClassName" -}}
{{- fail "runController.runtimeClassName was removed in v4 step 8: set sandbox.runtimeClassName (docs/upgrading.md#the-run-paths-old-keys)" -}}
{{- end -}}
{{- if hasKey $c "allowUnsandboxed" -}}
{{- fail "runController.allowUnsandboxed was removed in v4 step 8: set sandbox.unsafeAllowRunc (docs/upgrading.md#the-run-paths-old-keys)" -}}
{{- end -}}
{{- if and $s.unsafeAllowRunc $s.runtimeClassName -}}
{{- fail "sandbox.unsafeAllowRunc and sandbox.runtimeClassName: choose one (unsafeAllowRunc needs sandbox.runtimeClassName \"\", whose default is gvisor)" -}}
{{- end -}}
{{- $class := "" -}}
{{- if not $s.unsafeAllowRunc -}}
{{- $class = $s.runtimeClassName | default "" -}}
{{- end -}}
{{- dict "class" $class "allowUnsandboxed" ($s.unsafeAllowRunc | default false) | toJson -}}
{{- end -}}

{{/* The install's unsafe settings (v4 D18, plan PR9), as a JSON list of the broker's bounded codes
(config.Unsafe*): read it with `include "ap.unsafe" . | fromJsonArray`. Only the run-controller's
settings count, so an install without it (the XR) lists none and renders as before. */}}
{{- define "ap.unsafe" -}}
{{- $codes := list -}}
{{- if .Values.runController.enabled -}}
{{- $sb := include "ap.sandbox" . | fromJson -}}
{{- if and (not $sb.class) $sb.allowUnsandboxed -}}{{- $codes = append $codes "unsafe_allow_runc" -}}{{- end -}}
{{- if eq (include "ap.netMode" .) "none" -}}{{- $codes = append $codes "network_policy_none" -}}{{- end -}}
{{- if .Values.runController.unsafeSharedGitToken -}}{{- $codes = append $codes "shared_git_token" -}}{{- end -}}
{{- if not (include "ap.runModelGateway" . | fromJson) -}}{{- $codes = append $codes "model_key_in_sandbox" -}}{{- end -}}
{{- end -}}
{{- $codes | toJson -}}
{{- end -}}

{{/* The network mode (v4 D6): networkPolicies.mode, kubernetes by default. The old
networkPolicies.enabled is removed (v4 step 8) and refused; kubernetes needs the run-controller,
whose proxy and per-run policies it is. */}}
{{- define "ap.netMode" -}}
{{- $np := .Values.networkPolicies | default dict -}}
{{- if hasKey $np "enabled" -}}
{{- fail "networkPolicies.enabled was removed in v4 step 8: set networkPolicies.mode (cilium, kubernetes or none; docs/upgrading.md#the-run-paths-old-keys)" -}}
{{- end -}}
{{- $mode := $np.mode | default "kubernetes" -}}
{{- if not (has $mode (list "cilium" "kubernetes" "none")) -}}
{{- fail (printf "networkPolicies.mode %q is not cilium, kubernetes or none" $mode) -}}
{{- else if and (eq $mode "kubernetes") (not .Values.runController.enabled) -}}
{{- fail "networkPolicies.mode kubernetes needs runController.enabled: the egress proxy and the per-run NetworkPolicies are the run-controller's" -}}
{{- else if and (eq $mode "kubernetes") (not .Values.runPodPolicy.enabled) -}}
{{- fail "networkPolicies.mode kubernetes needs runPodPolicy.enabled: the egress proxy trusts a run pod's maku.ogenki.io/egress annotation because the run-pod policy alone lets the run-controller write it" -}}
{{- end -}}
{{- $mode -}}
{{- end -}}

{{/* The egress rules every kubernetes-mode component shares: DNS on 53, and the apiserver on
apiServerPorts, at apiServerCIDRs or any address. */}}
{{- define "ap.npDNSAndAPIServer" -}}
{{- $k := .Values.networkPolicies.kubernetes -}}
- to:
    - namespaceSelector:
        matchLabels: {kubernetes.io/metadata.name: {{ $k.dns.namespace }}}
      podSelector:
        matchLabels: {{- toYaml $k.dns.labels | nindent 10 }}
  ports: [{port: 53, protocol: UDP}, {port: 53, protocol: TCP}]
# The apiserver, after service translation (see apiServerPorts).
- {{- with $k.apiServerCIDRs }}
  to:
    {{- range . }}
    - ipBlock: {cidr: {{ . }}}
    {{- end }}
  {{- end }}
  ports:
    {{- range .Values.apiServerPorts }}
    - {port: {{ . }}, protocol: TCP}
    {{- end }}
{{- end -}}

{{/* The database egress of the broker and the retention job, in kubernetes mode: the PostgreSQL pods
by database.podLabels or podLabel, or database.egress.cidrs for a database outside the cluster. */}}
{{- define "ap.npDatabase" -}}
{{- $db := .Values.broker.database -}}
- to:
    {{- with dig "egress" "cidrs" (list) $db }}
    {{- range . }}
    - ipBlock: {cidr: {{ . | quote }}}
    {{- end }}
    {{- else }}
    - podSelector:
        {{- with $db.podLabels }}
        matchLabels: {{- toYaml . | nindent 10 }}
        {{- else }}
        matchExpressions:
          - {key: {{ $db.podLabel }}, operator: Exists}
        {{- end }}
    {{- end }}
  ports: [{port: 5432, protocol: TCP}]
{{- end -}}

{{/* The same, in cilium mode. */}}
{{- define "ap.cnpDatabase" -}}
{{- $db := .Values.broker.database -}}
{{- with dig "egress" "cidrs" (list) $db -}}
- toCIDR:
    {{- range . }}
    - {{ . | quote }}
    {{- end }}
{{- else -}}
- toEndpoints:
    - matchLabels:
        io.kubernetes.pod.namespace: {{ $.Release.Namespace }}
      matchExpressions:
        - {key: {{ $db.podLabel }}, operator: Exists}
{{- end }}
  toPorts:
    - ports: [{port: "5432", protocol: TCP}]
{{- end -}}

{{/* The egress proxy's in-cluster URL, in kubernetes mode. */}}
{{- define "ap.egressProxyURL" -}}
{{- printf "http://egress-proxy.%s.svc.%s:3128" .Release.Namespace .Values.clusterDomain -}}
{{- end -}}

{{/* A misconfiguration is a helm error, not a crashloop. */}}
{{- define "ap.validate" -}}
{{- $b := .Values.broker -}}
{{- $_ := include "ap.sandbox" . -}}
{{- $_ = include "ap.netMode" . -}}
{{- if hasKey .Values "factory" -}}
{{- fail "factory: this chart has no factory: install charts/maku-factory on its own" -}}
{{- end -}}
{{- if hasKey $b "runtimeClassName" -}}
{{- fail "broker.runtimeClassName: the broker runs on the default runtime: kubectl port-forward cannot reach a runsc pod (install-anywhere spike, outcome 3)" -}}
{{- end -}}
{{- if and (eq ($b.human.mode | default "oidc") "local") $b.publicURL (not (regexMatch "^http://(localhost|127\\.0\\.0\\.1|\\[::1\\]):[0-9]{1,5}$" $b.publicURL)) -}}
{{- fail (printf "broker.publicURL %q in local mode: it is http://localhost:<the port you forward>" $b.publicURL) -}}
{{- end -}}
{{- if and (eq ($b.human.mode | default "oidc") "local") (dig "oidc" "existingSecret" "" $b) -}}
{{- fail "broker.oidc.existingSecret: mode local has no IdP, so no client id to mount" -}}
{{- end -}}
{{- if not (has ($b.human.mode | default "oidc") (list "oidc" "local")) -}}
{{- fail (printf "broker.human.mode %q is not oidc or local" $b.human.mode) -}}
{{- end -}}
{{- if not (has $b.runIssuer.mode (list "jwks" "tokenreview")) -}}
{{- fail (printf "broker.runIssuer.mode %q is not jwks or tokenreview" $b.runIssuer.mode) -}}
{{- end -}}
{{- $rtAuth := ($b.roomTools | default dict).auth | default "key" -}}
{{- if not (has $rtAuth (list "key" "tokenreview")) -}}
{{- fail (printf "broker.roomTools.auth %q is not key or tokenreview" $rtAuth) -}}
{{- end -}}
{{- if and (eq $rtAuth "tokenreview") (ne $b.runIssuer.mode "tokenreview") -}}
{{- fail "broker.roomTools.auth tokenreview needs broker.runIssuer.mode tokenreview: :8090 asks the apiserver through the run issuer, and maps the subject with its pattern" -}}
{{- end -}}
{{- if not .Values.apiServerPorts -}}
{{- fail "apiServerPorts is empty: every CiliumNetworkPolicy that reaches the apiserver would render no port" -}}
{{- end -}}
{{- if and .Values.runController.enabled (ne .Values.runs.group "maku.ogenki.io") -}}
{{- fail (printf "runs.group %q with runController.enabled: the run controller reconciles maku.ogenki.io AgentRuns only; set runController.enabled=false where another controller runs %s AgentRuns (cloud-native-ref's Crossplane composition)" .Values.runs.group .Values.runs.group) -}}
{{- end -}}
{{- if and .Values.runController.enabled (eq ($b.tls.caSecretName | default "") $b.tls.secretName) -}}
{{- fail "broker.tls.caSecretName must not be broker.tls.secretName: the run controller needs ca.crt only, never the broker's tls.key" -}}
{{- end -}}
{{- if and .Values.runController.enabled (eq .Release.Namespace .Values.runs.namespace) -}}
{{- fail (printf "runs.namespace %q is the release namespace: with runController.enabled the controller could create a pod there that mounts the broker's tls.key; install into another namespace or set runs.namespace elsewhere" .Values.runs.namespace) -}}
{{- end -}}
{{- if not $b.tls.secretName -}}
{{- fail "broker.tls.secretName is required: the broker mounts its key pair from it" -}}
{{- end -}}
{{- $dsn := ne ($b.database.brokerSecret | default "") "" -}}
{{- range $b.extraEnv -}}{{- if eq .name "ROOMS_DATABASE_URL" -}}{{- $dsn = true -}}{{- end -}}{{- end -}}
{{- if not $dsn -}}
{{- fail "no database: set broker.database.brokerSecret, or ROOMS_DATABASE_URL in broker.extraEnv" -}}
{{- end -}}
{{- if and $b.factoryURL (hasPrefix "http://" $b.factoryURL) (not $b.factoryInsecure) -}}
{{- fail (printf "broker.factoryURL %q is http: the broker forwards human tokens to it; serve the factory chart's api.tls and use https, or set broker.factoryInsecure: true" $b.factoryURL) -}}
{{- end -}}
{{- end -}}

{{/* "true" when the broker serves :8090 and verifies the runs' own tokens there (roomTools.auth
tokenreview): the Service, the broker CNP and the run controller then open it to the runs. */}}
{{- define "ap.roomToolsTokenReview" -}}
{{- $rt := .Values.broker.roomTools | default dict -}}
{{- if and (ne (toString $rt.enabled) "false") (eq ($rt.auth | default "key") "tokenreview") -}}true{{- end -}}
{{- end -}}

{{/*
broker.roomTools.knowledge (EX11), as JSON: {} while url is unset, else {config, port, to}. config is
the broker's roomTools.knowledge block (every key but networkPolicy, with the defaults filled), port
networkPolicy.port, else the URL's (443 for https, 80 for http, unless it names one), and to the
peers the broker's egress opens on it. A policy matches the destination pod's port, after service
translation: a URL through a Service whose targetPort differs needs networkPolicy.port. In cilium
mode an entry of to is an endpoint selector, grouped under one toEndpoints rule, or a whole peer
(toFQDNs, toCIDR, toCIDRSet, toEntities, toEndpoints), rendered as its own rule: an external https
endpoint is a toFQDNs or toCIDR peer. A default-deny mode with no peer would refuse every call, so it
fails the render.
*/}}
{{- define "ap.knowledge" -}}
{{- $k := (.Values.broker.roomTools | default dict).knowledge | default dict -}}
{{- if $k.url -}}
{{- $u := urlParse $k.url -}}
{{- $port := ternary "443" "80" (eq $u.scheme "https") -}}
{{- if regexMatch ":[0-9]+$" $u.host }}{{ $port = regexFind "[0-9]+$" $u.host }}{{ end -}}
{{- with ($k.networkPolicy | default dict).port }}{{ $port = toString . }}{{ end -}}
{{- $to := ($k.networkPolicy | default dict).to | default list -}}
{{- range $i, $e := $to -}}
{{- if hasKey $e "toPorts" }}{{ fail (printf "broker.roomTools.knowledge.networkPolicy.to[%d] sets toPorts: the port is the URL's, or networkPolicy.port" $i) }}{{ end -}}
{{- end -}}
{{- if and (ne (include "ap.netMode" .) "none") (not $to) -}}
{{- fail (printf "broker.roomTools.knowledge.networkPolicy.to is empty: networkPolicies.mode %s denies the broker's egress to broker.roomTools.knowledge.url but to these peers" (include "ap.netMode" .)) -}}
{{- end -}}
{{- $cfg := dict "url" $k.url "tools" ($k.tools | default (list "kb_search" "kb_get")) "roles" ($k.roles | default (list "triager"))
      "timeout" ($k.timeout | default "10s") "maxBytes" (int ($k.maxBytes | default 65536)) -}}
{{- dict "config" $cfg "port" $port "to" $to | toJson -}}
{{- else -}}
{}
{{- end -}}
{{- end -}}

{{/*
broker.modelGateway (plan v4 step 4, GW8), as JSON: {} when service is unset, else {service,
namespace, port, audiencePrefix, base, tokens}. base is the clients listener's URL, to which
/clients/<client>/v1 is appended; tokens the directory of the projected tokens, one file per client.
*/}}
{{- define "ap.modelGateway" -}}
{{- $g := .Values.broker.modelGateway | default dict -}}
{{- if $g.service -}}
{{- $ns := $g.namespace | default .Release.Namespace -}}
{{- dict "service" $g.service "namespace" $ns "port" (int $g.port) "audiencePrefix" $g.audiencePrefix
      "base" (printf "http://%s.%s.svc.%s:%d" $g.service $ns .Values.clusterDomain (int $g.port))
      "tokens" "/var/run/secrets/maku/model-gateway" | toJson -}}
{{- else -}}
{}
{{- end -}}
{{- end -}}

{{/*
The human block as the broker reads it (oidc mode): the values' block, with the model gateway's
routes filled in where the block leaves both url and apiKeyFile unset (D25: a pair you set wins).
The planner's only when it is enabled; the classifier's only when human.slackIntents exists. One of
the two set alone fails the render: the gateway's token would go to another endpoint, or another
key to the gateway.
*/}}
{{- define "ap.human" -}}
{{- $h := omit (deepCopy .Values.broker.human) "mode" -}}
{{- with include "ap.githubLink" . | fromJson -}}
{{- $l := deepCopy ($h.githubLink | default dict) -}}
{{- range $k, $v := . }}{{ if not (hasKey $l $k) }}{{ $_ := set $l $k $v }}{{ end }}{{ end -}}
{{- $_ := set $h "githubLink" $l -}}
{{- end -}}
{{- with .Values.broker.oidc -}}
{{- if .existingSecret -}}
{{- if not $h.clientIDFile }}{{ $_ := set $h "clientIDFile" (printf "/etc/room-broker/oidc/%s" .keys.web) }}{{ end -}}
{{- if not $h.roomctlClientIDFile }}{{ $_ := set $h "roomctlClientIDFile" (printf "/etc/room-broker/oidc/%s" .keys.cli) }}{{ end -}}
{{- if and (has ($h.preset | default "zitadel") (list "zitadel")) (not $h.projectIDFile) }}{{ $_ := set $h "projectIDFile" (printf "/etc/room-broker/oidc/%s" .keys.project) }}{{ end -}}
{{- end -}}
{{- end -}}
{{- $g := include "ap.modelGateway" . | fromJson -}}
{{- if $g.service -}}
{{- $plan := dig "intake" "plan" dict $h -}}
{{- if $plan.enabled -}}
{{- include "ap.gatewayPair" (dict "block" $plan "key" "broker.human.intake.plan" "url" (printf "%s/clients/planner/v1" $g.base) "token" (printf "%s/planner" $g.tokens)) -}}
{{- end -}}
{{- with $h.slackIntents -}}
{{- include "ap.gatewayPair" (dict "block" . "key" "broker.human.slackIntents" "url" (printf "%s/clients/classifier/v1" $g.base) "token" (printf "%s/classifier" $g.tokens)) -}}
{{- end -}}
{{- end -}}
{{- toYaml $h -}}
{{- end -}}

{{/* One client's url and apiKeyFile: both filled from the gateway when both are unset, kept when both
are set, and a render error naming both keys when only one is. Mutates block. */}}
{{- define "ap.gatewayPair" -}}
{{- $b := .block -}}
{{- if and (not $b.url) (not $b.apiKeyFile) -}}
{{- $_ := set $b "url" .url -}}
{{- $_ := set $b "apiKeyFile" .token -}}
{{- else if not (and $b.url $b.apiKeyFile) -}}
{{- fail (printf "%s.url and %s.apiKeyFile: set both or neither (broker.modelGateway fills both when neither is set): one alone would pair the gateway's token with another endpoint, or another key with the gateway" .key .key) -}}
{{- end -}}
{{- end -}}

{{/* The run-controller's model gateway (v4 step 4, GW3; ap.modelGateway is the broker's), as JSON: {} in model.mode secret (demo-only since v4 step 8, R4),
else the resolved gateway: url, ports, audiencePrefix, aliases. Read it with
`include "ap.runModelGateway" . | fromJson`. An empty service is maku-gateway's default name, model-gateway,
and an empty namespace the release's, which is where the umbrella runs it. */}}
{{- define "ap.runModelGateway" -}}
{{- $m := .Values.runController.model | default dict -}}
{{- $mode := $m.mode | default "gateway" -}}
{{- if eq $mode "gateway" -}}
{{- $g := $m.gateway | default dict -}}
{{- $svc := $g.service | default "model-gateway" -}}
{{- $ns := $g.namespace | default .Release.Namespace -}}
{{- $prefix := $g.audiencePrefix | default "maku-gateway" -}}
{{- $aliases := $g.aliases | default dict -}}
{{- if not $aliases.public -}}
{{- fail "runController.model.gateway.aliases.public is required with model.mode gateway: the aliases the gateway's public listener serves (maku-gateway aliases.public's keys)" -}}
{{- end -}}
{{- /* The prefix is pasted into the run-pod policy's CEL: only lowercase labels and dots. */ -}}
{{- if not (regexMatch "^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$" $prefix) -}}
{{- fail (printf "runController.model.gateway.audiencePrefix %q must be dot-separated lowercase labels" $prefix) -}}
{{- end -}}
{{- $ports := $g.ports | default dict -}}
{{- dict "url" (printf "http://%s.%s.svc.%s" $svc $ns .Values.clusterDomain)
  "ports" (dict "public" ($ports.public | default 8080 | int) "internal" ($ports.internal | default 8081 | int))
  "audiencePrefix" $prefix
  "aliases" (dict "public" $aliases.public "internal" ($aliases.internal | default list)) | toJson -}}
{{- else if eq $mode "secret" -}}
{{- if not .Values.demoValues -}}
{{- fail "runController.model.mode secret is demo-only (v4 D7): the provider key would sit in the sandbox. Set model.mode gateway with model.gateway.aliases, or demoValues: true for a demo (values-quickstart.yaml, the umbrella's values-demo.yaml); docs/upgrading.md#the-gateway-is-the-model-path" -}}
{{- end -}}
{}
{{- else -}}
{{- fail (printf "runController.model.mode %q must be secret or gateway" $mode) -}}
{{- end -}}
{{- end -}}

{{/* The GitHub account link from broker.githubLink (v4 D8), as JSON: {} when existingSecret is unset,
else the human.githubLink keys the Secret's mount fills. Read it with
`include "ap.githubLink" . | fromJson`. */}}
{{- define "ap.githubLink" -}}
{{- $g := .Values.broker.githubLink | default dict -}}
{{- if $g.existingSecret -}}
{{- if eq (.Values.broker.human.mode | default "oidc") "local" -}}
{{- fail "broker.githubLink.existingSecret: mode local has no GitHub account to link" -}}
{{- end -}}
{{- $keys := $g.keys | default dict -}}
{{- $out := dict "clientIDFile" (printf "/etc/room-broker/github-link/%s" ($keys.clientID | default "client_id"))
  "clientSecretFile" (printf "/etc/room-broker/github-link/%s" ($keys.clientSecret | default "client_secret"))
  "activeWindow" ($g.activeWindow | default "168h") -}}
{{- with $g.webURL }}{{ $_ := set $out "webURL" (trimSuffix "/" .) }}{{ end -}}
{{- toJson $out -}}
{{- else -}}
{}
{{- end -}}
{{- end }}

{{/* The hosts the GitHub account link calls, as a JSON list: GitHub's site and API (github.com and
api.github.com), or a GHES's one host. Empty without the link, whether broker.githubLink's Secret or
the human block turns it on. */}}
{{- define "ap.githubLinkHosts" -}}
{{- $l := include "ap.githubLink" . | fromJson -}}
{{- $h := (.Values.broker.human | default dict).githubLink | default dict -}}
{{- if or $l $h -}}
{{- $web := $h.webURL | default ($l.webURL | default "https://github.com") -}}
{{- $host := (urlParse $web).hostname -}}
{{- if eq $host "github.com" }}{{ toJson (list "github.com" "api.github.com") }}{{ else }}{{ toJson (list $host) }}{{ end -}}
{{- else -}}
[]
{{- end -}}
{{- end }}

{{/* ap.at: the value at a dotted path of a values map, as {"v": value}; null when absent. */}}
{{- define "ap.at" -}}
{{- $cur := .v -}}
{{- range (splitList "." .path) -}}
{{- if kindIs "map" $cur }}{{ $cur = index $cur . }}{{ else }}{{ $cur = nil }}{{ end -}}
{{- end -}}
{{- dict "v" $cur | toJson -}}
{{- end -}}

{{/* The preflight's Secrets (v4 D24): each seam of the registry (_seams.tpl, task chart:seams) in the
release namespace whose value names a Secret, with its required keys as the values rename them, as
{"secrets": [{seam, namespace, name, keys}]}. The run-controller checks the runs namespace's own. */}}
{{- define "ap.preflightSecrets" -}}
{{- $out := list -}}
{{- range (include "ap.seams" . | fromJson).seams -}}
{{- if and (eq .namespace "release") .value (not .mountOptional) -}}
{{- $name := (include "ap.at" (dict "v" $.Values "path" .value) | fromJson).v | default "" -}}
{{- if $name -}}
{{- $keys := list -}}
{{- if not .dynamicKeys -}}
{{- range .keys -}}
{{- if .required -}}
{{- $k := .name -}}
{{- if .from }}{{ $k = (include "ap.at" (dict "v" $.Values "path" .from) | fromJson).v | default .name }}{{ end -}}
{{- $keys = append $keys $k -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $out = append $out (dict "seam" .id "namespace" $.Release.Namespace "name" $name "keys" $keys) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- dict "secrets" $out | toJson -}}
{{- end -}}

{{/* preflight.timeout in seconds: "5m" is 300. */}}
{{- define "ap.preflightSeconds" -}}
{{- $t := .Values.preflight.timeout | default "5m" -}}
{{- $n := regexFind "^[0-9]+" $t | int -}}
{{- if hasSuffix "h" $t }}{{ mul $n 3600 }}{{ else if hasSuffix "m" $t }}{{ mul $n 60 }}{{ else }}{{ $n }}{{ end -}}
{{- end -}}

{{/* The preflight's pod template, shared by the test hook and the CronJob so the two cannot drift. */}}
{{- define "ap.preflightPod" -}}
{{- $p := .Values.preflight -}}
{{- $img := .Values.runController.image -}}
{{- if $p.image.repository }}{{ $img = $p.image }}{{ end -}}
metadata:
  labels: {{- include "ap.labelsFor" (dict "root" . "name" "preflight") | nindent 4 }}
spec:
  serviceAccountName: {{ .Release.Name }}-preflight
  automountServiceAccountToken: true # the API server is its first peer
  enableServiceLinks: false
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    seccompProfile: {type: RuntimeDefault}
  containers:
    - name: preflight
      image: {{ include "ap.image" (dict "root" . "image" $img) }}
      {{- include "ap.pullPolicy" . | nindent 6 }}
      args: [preflight, --config, /etc/maku-preflight/config.json{{ if $p.strict }}, --strict{{ end }}]
      terminationMessagePolicy: File
      securityContext: {{- include "ap.containerSecurityContext" . | nindent 8 }}
      resources: {{- toYaml $p.resources | nindent 8 }}
      volumeMounts:
        - {name: config, mountPath: /etc/maku-preflight, readOnly: true}
  volumes:
    - name: config
      configMap: {name: {{ .Release.Name }}-preflight}
{{- end -}}

{{/*
broker.webhooks (plan v4 step 6, EX9), as JSON {"config": [...], "mounts": [...], "egress": [...]}:
- config: each entry as the broker reads it, without the chart's keys (existingSecret, key,
  networkPolicy), its secretFile filled from existingSecret's mount when unset (a set one wins);
- mounts: {name, secret} per entry with existingSecret, at /etc/room-broker/webhooks/<name>;
- egress: {name, port, to, host, internet} per entry, for the policies. A cluster entry's peers are
  networkPolicy.to, or the Slack App's pods when its URL is factory-slack-app's; none fails the render
  in a policy mode. An internet entry opens 443 (or its URL's port) to its host (cilium) or to public
  addresses (kubernetes).
*/}}
{{- define "ap.webhooks" -}}
{{- $config := list -}}
{{- $mounts := list -}}
{{- $egress := list -}}
{{- $mode := include "ap.netMode" . -}}
{{- range $w := .Values.broker.webhooks | default list -}}
{{- $np := $w.networkPolicy | default dict -}}
{{- $entry := omit $w "existingSecret" "key" "networkPolicy" -}}
{{- if and $w.existingSecret (not $w.secretFile) -}}
{{- $_ := set $entry "secretFile" (printf "/etc/room-broker/webhooks/%s/%s" $w.name ($w.key | default "signing-key")) -}}
{{- end -}}
{{- $config = append $config $entry -}}
{{- with $w.existingSecret -}}
{{- /* A volume name is a DNS label: webhook-<name>, cut with a hash suffix past 63. */ -}}
{{- $vol := printf "webhook-%s" $w.name -}}
{{- if gt (len $vol) 63 }}{{ $vol = printf "%s-%s" (trunc 54 $vol | trimSuffix "-") (trunc 8 (sha256sum $w.name)) }}{{ end -}}
{{- $mounts = append $mounts (dict "name" $w.name "volume" $vol "secret" .) -}}
{{- end -}}
{{- $u := urlParse ($w.url | default "") -}}
{{- $port := ternary "443" "80" (eq $u.scheme "https") -}}
{{- if regexMatch ":[0-9]+$" $u.host }}{{ $port = regexFind "[0-9]+$" $u.host }}{{ end -}}
{{- with $np.port }}{{ $port = toString . }}{{ end -}}
{{- $to := $np.to | default list -}}
{{- if eq $w.network "cluster" -}}
{{- $parts := splitList "." $u.hostname -}}
{{- if and (not $to) (eq (first $parts) "factory-slack-app") -}}
{{- $ns := $.Release.Namespace -}}
{{- if gt (len $parts) 1 }}{{ $ns = index $parts 1 }}{{ end -}}
{{- if eq $mode "kubernetes" -}}
{{- $to = list (dict "namespaceSelector" (dict "matchLabels" (dict "kubernetes.io/metadata.name" $ns)) "podSelector" (dict "matchLabels" (dict "app.kubernetes.io/name" "factory-slack-app"))) -}}
{{- else -}}
{{- $to = list (dict "matchLabels" (dict "io.kubernetes.pod.namespace" $ns "app.kubernetes.io/name" "factory-slack-app")) -}}
{{- end -}}
{{- end -}}
{{- if eq $mode "cilium" -}}
{{- range $to -}}
{{- if or (hasKey . "podSelector") (hasKey . "namespaceSelector") (hasKey . "ipBlock") -}}
{{- fail (printf "broker.webhooks[%s].networkPolicy.to holds a NetworkPolicy peer (podSelector, namespaceSelector, ipBlock): networkPolicies.mode cilium takes endpoint selectors (matchLabels with io.kubernetes.pod.namespace) or whole Cilium peers" $w.name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if and (ne $mode "none") (not $to) -}}
{{- fail (printf "broker.webhooks[%s].networkPolicy.to is empty: name the subscriber's pods; networkPolicies.mode %s denies the broker's egress to %s but to these peers" $w.name $mode $w.url) -}}
{{- end -}}
{{- end -}}
{{- $egress = append $egress (dict "name" $w.name "port" $port "to" $to "host" $u.hostname "internet" (eq $w.network "internet")) -}}
{{- end -}}
{{- dict "config" $config "mounts" $mounts "egress" $egress | toJson -}}
{{- end -}}
