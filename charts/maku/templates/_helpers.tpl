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

{{/* The run pod's sandbox, resolved from sandbox.* and the run-controller's older keys (plan PR8), as
JSON: {"class": <RuntimeClass name, "" for none>, "allowUnsandboxed": <bool>}. Read it with
`include "ap.sandbox" . | fromJson`. */}}
{{- define "ap.sandbox" -}}
{{- $s := .Values.sandbox | default dict -}}
{{- $c := .Values.runController -}}
{{- if and $s.unsafeAllowRunc $s.runtimeClassName -}}
{{- fail "sandbox.unsafeAllowRunc and sandbox.runtimeClassName: choose one" -}}
{{- end -}}
{{- $class := "" -}}
{{- if not $s.unsafeAllowRunc -}}
{{- $class = $s.runtimeClassName | default $c.runtimeClassName | default "" -}}
{{- end -}}
{{- dict "class" $class "allowUnsandboxed" (or ($s.unsafeAllowRunc | default false) ($c.allowUnsandboxed | default false)) | toJson -}}
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
{{- end -}}
{{- $codes | toJson -}}
{{- end -}}

{{/* The network mode (v4 D6): networkPolicies.mode when set, else the old key's meaning
(networkPolicies.enabled: cilium when true, none when false). Mode cilium with enabled false names both
keys and fails; kubernetes needs the run-controller, whose proxy and per-run policies it is. */}}
{{- define "ap.netMode" -}}
{{- $np := .Values.networkPolicies | default dict -}}
{{- $enabled := ne (toString $np.enabled) "false" -}}
{{- $mode := $np.mode | default "" -}}
{{- if not $mode -}}
{{- $mode = ternary "cilium" "none" $enabled -}}
{{- else if not (has $mode (list "cilium" "kubernetes" "none")) -}}
{{- fail (printf "networkPolicies.mode %q is not cilium, kubernetes or none" $mode) -}}
{{- else if and (eq $mode "cilium") (not $enabled) -}}
{{- fail "networkPolicies.mode cilium and networkPolicies.enabled false: choose one (unset networkPolicies.enabled, the old key)" -}}
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
the URL's (443 for https, 80 for http, unless it names one), and to the peers the broker's egress
opens on it. A default-deny mode with no peer would refuse every call, so it fails the render.
*/}}
{{- define "ap.knowledge" -}}
{{- $k := (.Values.broker.roomTools | default dict).knowledge | default dict -}}
{{- if $k.url -}}
{{- $u := urlParse $k.url -}}
{{- $port := ternary "443" "80" (eq $u.scheme "https") -}}
{{- if regexMatch ":[0-9]+$" $u.host }}{{ $port = regexFind "[0-9]+$" $u.host }}{{ end -}}
{{- $to := ($k.networkPolicy | default dict).to | default list -}}
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

{{/* The run-controller's model gateway (v4 step 4, GW3; ap.modelGateway is the broker's), as JSON: {} in model.mode secret (the default),
else the resolved gateway: url, ports, audiencePrefix, aliases. Read it with
`include "ap.runModelGateway" . | fromJson`. An empty service is maku-gateway's default name, model-gateway,
and an empty namespace the release's, which is where the umbrella runs it. */}}
{{- define "ap.runModelGateway" -}}
{{- $m := .Values.runController.model | default dict -}}
{{- $mode := $m.mode | default "secret" -}}
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
{}
{{- else -}}
{{- fail (printf "runController.model.mode %q must be secret or gateway" $mode) -}}
{{- end -}}
{{- end -}}
