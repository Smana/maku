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
