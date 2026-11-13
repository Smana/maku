// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/runwatch"
)

// servedOutputs are the safe-output types the broker serves: H2's four, and push from stage H3,
// which the harness's shim requests through output_push (HR23).
var servedOutputs = []string{"comment", "issue", "labels", "pr", "push"}

// safeOutputs is the run's served safe-output types, sorted, from the factory's annotation (HR14).
func safeOutputs(r *v1alpha1.AgentRun) []string {
	var out []string
	for _, t := range strings.Split(r.Annotations[runwatch.SafeOutputsAnnotation], ",") {
		if t = strings.TrimSpace(t); slices.Contains(servedOutputs, t) && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out
}

// The composition's constants (crossplane-configuration v0.9.15).
const (
	BridgeHealthPort = 8085
	HarnessPort      = 8000
	roomTokenTTL     = 600
	// roomToolsDir holds the harness's :8090 token (audience room-tools), apart from the bridge's.
	roomToolsDir     = "/var/run/secrets/agents/room-tools"
	roomGraceSeconds = 45
	bridgeFlushGrace = "12s"
	runUID           = 10001
)

var sizes = map[string]struct {
	cpu, memory [2]string
	scratch     string
}{
	"small":  {[2]string{"1", "2"}, [2]string{"2Gi", "4Gi"}, "10Gi"},
	"medium": {[2]string{"2", "4"}, [2]string{"4Gi", "8Gi"}, "20Gi"},
	"large":  {[2]string{"4", "8"}, [2]string{"8Gi", "16Gi"}, "40Gi"},
}

func containerSecurity() *corev1.SecurityContext {
	f, t := false, true
	return &corev1.SecurityContext{AllowPrivilegeEscalation: &f, ReadOnlyRootFilesystem: &t, RunAsNonRoot: &t,
		Capabilities:   &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
}

func res(cpuReq, memReq, cpuLim, memLim string) corev1.ResourceRequirements {
	q := resource.MustParse
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: q(cpuReq), corev1.ResourceMemory: q(memReq)},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: q(cpuLim), corev1.ResourceMemory: q(memLim)}}
}

func harnessResources(cpu, memory [2]string, scratch string) corev1.ResourceRequirements {
	rr := res(cpu[0], memory[0], cpu[1], memory[1])
	rr.Requests[corev1.ResourceEphemeralStorage] = resource.MustParse("1Gi")
	rr.Limits[corev1.ResourceEphemeralStorage] = resource.MustParse(scratch)
	return rr
}

func localGet(path string) []string {
	return []string{"python3", "-c", fmt.Sprintf("import urllib.request; urllib.request.urlopen('http://127.0.0.1:%d%s', timeout=2)", HarnessPort, path)}
}

func emptyDir(name, size string, mem bool) corev1.Volume {
	q := resource.MustParse(size)
	e := &corev1.EmptyDirVolumeSource{SizeLimit: &q}
	if mem {
		e.Medium = corev1.StorageMediumMemory
	}
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: e}}
}

// roomToolsReadOnly is the bridge's MCP_READ_ONLY_TOOLS for a run given the room tools: they only
// append to the room.
const roomToolsReadOnly = "room-broker__room_read,room-broker__room_post,room-broker__room_progress," +
	"room-broker__room_handoff,room-broker__room_verdict"

// Pod is the run's pod: the composition's Sandbox pod template without identity-proxy,
// octo-sts, traces, Kueue and suspend. The model key and the PAT are files the harness reads
// (3a R4). It is named like the run, as the Sandbox's pod is.
func Pod(r *v1alpha1.AgentRun, c Config, m Model, runID string) *corev1.Pod {
	s := r.Spec
	size, ok := sizes[s.Size]
	if !ok {
		size = sizes["small"]
	}
	branch := Branch(r, runID)
	deadline := s.Budget.MaxMinutes * 60
	if deadline == 0 {
		deadline = 120 * 60
	}
	grace := int64(30)
	if s.RoomRef != "" {
		grace = roomGraceSeconds
	}
	no, yes, uid := false, true, int64(runUID)
	mode := int32(0o440) // fsGroup-readable: a nonroot container reads a 0400 Secret only as its owner
	env := []corev1.EnvVar{
		{Name: "RUN_ID", Value: runID}, {Name: "ROLE", Value: s.Role}, {Name: "REPOSITORY", Value: s.Repository},
		{Name: "BASE_REF", Value: s.BaseRef}, {Name: "BRANCH", Value: branch}, {Name: "MODEL", Value: m.Name},
		{Name: "DATA_CLASS", Value: s.DataClass}, {Name: "CONVERSATION_ID", Value: string(r.UID)},
		{Name: "LLM_BASE_URL", Value: m.BaseURL}, {Name: "LLM_API_KEY_FILE", Value: modelDir + "/" + modelKey(c)},
		{Name: "GIT_TOKEN_FILE", Value: "/run/agent/github/token"},
		{Name: "TASK_FILE", Value: "/run/agent/task/task.md"}, {Name: "RULES_FILE", Value: "/run/agent/task/rules.md"},
		{Name: "HOME", Value: "/home/openhands"},
	}
	if c.PerRun() { // the harness revokes the run's minted token on exit (plan PR5)
		env = append(env, corev1.EnvVar{Name: "GIT_TOKEN_REVOCABLE", Value: "1"})
	}
	if s.RoomRef != "" {
		env = append(env, corev1.EnvVar{Name: "ROOM_ID", Value: s.RoomRef},
			corev1.EnvVar{Name: "BRIDGE_URL", Value: fmt.Sprintf("http://127.0.0.1:%d", BridgeHealthPort)})
	}
	if v := r.Labels[LabelTask]; v != "" {
		env = append(env, corev1.EnvVar{Name: "TASK_ID", Value: v})
	}
	if s.Task.URL != "" {
		env = append(env, corev1.EnvVar{Name: "TASK_URL", Value: s.Task.URL})
	}
	if forgeAPIDenied(r) { // HR37: the gh wrapper refuses forge reads; the run uses forge_read
		env = append(env, corev1.EnvVar{Name: "FORGE_READ", Value: "1"})
	}
	outputs := safeOutputs(r)
	if len(outputs) > 0 { // the gh wrapper turns these writes away to the output tools (HR14)
		env = append(env, corev1.EnvVar{Name: "SAFE_OUTPUTS", Value: strings.Join(outputs, ",")})
	}
	env = append(env, manifestEnv(r)...)
	// v3 feature 3: the repository's checks, and the pod's deadline for the final run's margin (RC9).
	if s.Loop.Checks {
		env = append(env, corev1.EnvVar{Name: "AGENT_CHECKS", Value: "1"},
			corev1.EnvVar{Name: "AGENT_RUN_SECONDS", Value: strconv.FormatInt(deadline, 10)})
	}
	// v3 feature 5: the search tool, which the harness offers an implementer only; the bridge reads
	// the same switch to class a bare "search" as that tool (and not as an MCP write).
	searchOn := s.Loop.Search && s.Role == "implementer"
	if searchOn {
		env = append(env, corev1.EnvVar{Name: "AGENT_SEARCH", Value: "1"})
	}
	// The loop policy: AGENT_LOOP holds the fields the run sets (the harness fills the rest), and an
	// unset judgeModel is "none": the harness's default would switch a judge on. An implementer only
	// reads it. The portable runtime has no identity proxy and resolves no judge alias, so a judge
	// calls the pod's own model endpoint directly (JUDGE_BASE_URL) with the model Secret's judge_model,
	// or the run's model without it (JUDGE_MODEL): the demo has one model for every role.
	if l := s.Loop; l.StallSteps != nil || l.Continuations != nil || l.JudgeModel != "" {
		policy := map[string]any{"judgeModel": "none"}
		if l.StallSteps != nil {
			policy["stallSteps"] = *l.StallSteps
		}
		if l.Continuations != nil {
			policy["continuations"] = *l.Continuations
		}
		if l.JudgeModel != "" {
			policy["judgeModel"] = l.JudgeModel
			env = append(env, corev1.EnvVar{Name: "JUDGE_BASE_URL", Value: cmp.Or(m.JudgeBaseURL, m.BaseURL)}, corev1.EnvVar{Name: "JUDGE_MODEL", Value: cmp.Or(m.Judge, m.Name)})
			if c.Gateway() { // the judge's alias has its own audience, so its own token (GW5)
				env = append(env, corev1.EnvVar{Name: "JUDGE_API_KEY_FILE", Value: modelDir + "/judge-token"})
			}
		}
		raw, _ := json.Marshal(policy) // a map of numbers and strings
		env = append(env, corev1.EnvVar{Name: "AGENT_LOOP", Value: string(raw)})
	}
	harness := corev1.Container{
		Name: "harness", Image: c.Images.Harness, Env: env,
		StartupProbe:    &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: localGet("/ready")}}, PeriodSeconds: 5, FailureThreshold: 60, TimeoutSeconds: 5},
		ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: localGet("/ready")}}, PeriodSeconds: 10, TimeoutSeconds: 5},
		LivenessProbe:   &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: localGet("/health")}}, PeriodSeconds: 20, FailureThreshold: 3, TimeoutSeconds: 5},
		Resources:       harnessResources(size.cpu, size.memory, size.scratch),
		SecurityContext: containerSecurity(),
		VolumeMounts: []corev1.VolumeMount{
			{Name: "workspace", MountPath: "/workspace"}, {Name: "home", MountPath: "/home/openhands"},
			{Name: "tmp", MountPath: "/tmp"}, {Name: "git-cache", MountPath: "/run/agent/git"},
			{Name: "task", MountPath: "/run/agent/task", ReadOnly: true},
			{Name: modelVolume(c), MountPath: modelDir, ReadOnly: true},
			{Name: "github", MountPath: "/run/agent/github", ReadOnly: true},
		},
	}
	vols := []corev1.Volume{
		{Name: "task", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: r.Name + "-task"}}}},
		modelVolumeSource(r, c, deadline, &mode),
		{Name: "github", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: c.GitSecretFor(r), DefaultMode: &mode,
			Items: []corev1.KeyToPath{{Key: "token", Path: "token"}}}}},
		emptyDir("workspace", size.scratch, false), emptyDir("home", "2Gi", false), emptyDir("tmp", "2Gi", false),
		emptyDir("git-cache", "1Mi", true),
	}
	var inits []corev1.Container
	if s.RoomRef != "" {
		always := corev1.ContainerRestartPolicyAlways
		ttl := int64(roomTokenTTL)
		bridgeEnv := []corev1.EnvVar{
			{Name: "RUN_ID", Value: runID}, {Name: "ROOM_ID", Value: s.RoomRef}, {Name: "BRANCH", Value: branch},
			{Name: "CONVERSATION_ID", Value: string(r.UID)}, {Name: "BROKER_URL", Value: c.Broker.URL},
			{Name: "BROKER_CA_FILE", Value: "/etc/room-broker-ca/ca.crt"},
			{Name: "HARNESS_URL", Value: fmt.Sprintf("http://127.0.0.1:%d", HarnessPort)},
			{Name: "ROOM_TOKEN_FILE", Value: "/var/run/secrets/agents/room/token"},
			{Name: "EGRESS_PROFILES", Value: egressProfiles(r)}, {Name: "FLUSH_GRACE", Value: bridgeFlushGrace},
		}
		if searchOn {
			bridgeEnv = append(bridgeEnv, corev1.EnvVar{Name: "AGENT_SEARCH", Value: "1"})
		}
		if c.RoomToolsURL != "" {
			// The harness calls the broker directly, so its room tools reach the bridge bare; the
			// broker still enforces each tool's roles.
			// An output tool only appends a request to the room: the factory applies it (HR14).
			readOnly := roomToolsReadOnly
			for _, t := range outputs {
				readOnly += ",room-broker__output_" + t
			}
			if slices.Contains(outputs, "push") { // the rules send a waiting push to output_status
				readOnly += ",room-broker__output_status"
			}
			bridgeEnv = append(bridgeEnv, corev1.EnvVar{Name: "MCP_BARE_SERVER", Value: "room-broker"},
				corev1.EnvVar{Name: "MCP_READ_ONLY_TOOLS", Value: readOnly})
		}
		health := corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(BridgeHealthPort)}}
		inits = []corev1.Container{{
			Name: "room-bridge", Image: c.Images.Bridge, RestartPolicy: &always, Env: bridgeEnv,
			Ports:           []corev1.ContainerPort{{Name: "bridge-health", ContainerPort: BridgeHealthPort, Protocol: corev1.ProtocolTCP}},
			StartupProbe:    &corev1.Probe{ProbeHandler: health, PeriodSeconds: 1, FailureThreshold: 30},
			ReadinessProbe:  &corev1.Probe{ProbeHandler: health, PeriodSeconds: 10},
			LivenessProbe:   &corev1.Probe{ProbeHandler: health, PeriodSeconds: 20, FailureThreshold: 3},
			Resources:       res("20m", "32Mi", "100m", "64Mi"),
			SecurityContext: containerSecurity(),
			VolumeMounts: []corev1.VolumeMount{
				{Name: "room-token", MountPath: "/var/run/secrets/agents/room", ReadOnly: true},
				{Name: "room-broker-ca", MountPath: "/etc/room-broker-ca", ReadOnly: true},
			},
		}, {
			Name: "room-gate", Image: c.Images.Bridge, Args: []string{"gate"},
			Resources: res("10m", "16Mi", "50m", "32Mi"), SecurityContext: containerSecurity(),
			TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
		}}
		vols = append(vols,
			corev1.Volume{Name: "room-token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{DefaultMode: &mode,
				Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: "room-broker", ExpirationSeconds: &ttl, Path: "token"}}}}}},
			corev1.Volume{Name: "room-broker-ca", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: r.Name + "-ca"}}}})
	}
	// Room tools act on the run's room: a run with none gets no MCP server. The token is the harness's
	// own, audience room-tools, which the broker's :8090 alone accepts: the bridge's room-broker token,
	// which opens :8443, never enters the container where the model's commands run.
	if c.RoomToolsURL != "" && s.RoomRef != "" {
		ttl := int64(roomTokenTTL)
		harness.Env = append(harness.Env, corev1.EnvVar{Name: "MCP_URL", Value: c.RoomToolsURL},
			corev1.EnvVar{Name: "MCP_TOKEN_FILE", Value: roomToolsDir + "/token"})
		harness.VolumeMounts = append(harness.VolumeMounts, corev1.VolumeMount{Name: "room-tools-token", MountPath: roomToolsDir, ReadOnly: true})
		vols = append(vols, corev1.Volume{Name: "room-tools-token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{DefaultMode: &mode,
			Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: "room-tools", ExpirationSeconds: &ttl, Path: "token"}}}}}})
	}
	om := meta(r, runID, r.Name)
	// kubernetes mode (plan PR6): the harness alone goes through the egress proxy, which reads the
	// pod's allow-list from its annotation; the bridge talks in-cluster only.
	if c.NetworkPolicy == NetworkPolicyKubernetes {
		harness.Env = append(harness.Env, proxyEnv(c)...)
		om.Annotations[EgressAnnotation] = egressAnnotation(r, c, m)
	}
	var rc *string
	if c.RuntimeClassName != "" {
		rc = &c.RuntimeClassName
	}
	return &corev1.Pod{
		ObjectMeta: om,
		Spec: corev1.PodSpec{
			ServiceAccountName: r.Name, AutomountServiceAccountToken: &no, RuntimeClassName: rc,
			RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: &deadline,
			EnableServiceLinks: &no, ShareProcessNamespace: &no, TerminationGracePeriodSeconds: &grace,
			DNSConfig: &corev1.PodDNSConfig{Options: []corev1.PodDNSConfigOption{{Name: "ndots", Value: new("1")}}},
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &yes, RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			InitContainers: inits, Containers: []corev1.Container{harness}, Volumes: vols,
		},
	}
}

// modelDir is where the harness reads its model credential: the model Secret's api_key, or in gateway
// mode the run's own gateway tokens.
const modelDir = "/run/agent/model"

// gatewayTokenSlack is how long a gateway token outlives the pod's deadline (GW9, the XR's R2): the
// harness reads it once, and gVisor raises no inotify event for a rotated token.
const gatewayTokenSlack = 600

// modelKey is the file under modelDir that the run's own calls send as their bearer.
func modelKey(c Config) string {
	if c.Gateway() {
		return "token"
	}
	return "api_key"
}

// modelVolume names the model credential's volume.
func modelVolume(c Config) string {
	if c.Gateway() {
		return "model-token"
	}
	return "model"
}

// modelVolumeSource is the model Secret's api_key, or in gateway mode a projected volume of the run's
// ServiceAccount tokens (GW3): token, audience <prefix>.<class>.<spec.model>, and with a judge
// judge-token, audience <prefix>.<class>.<judgeModel>, each living the deadline plus gatewayTokenSlack.
func modelVolumeSource(r *v1alpha1.AgentRun, c Config, deadline int64, mode *int32) corev1.Volume {
	if !c.Gateway() {
		return corev1.Volume{Name: "model", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: c.ModelSecret, DefaultMode: mode,
			Items: []corev1.KeyToPath{{Key: "api_key", Path: "api_key"}}}}}
	}
	ttl := deadline + gatewayTokenSlack
	token := func(alias, path string) corev1.VolumeProjection {
		return corev1.VolumeProjection{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: c.audience(r, alias), ExpirationSeconds: &ttl, Path: path}}
	}
	srcs := []corev1.VolumeProjection{token(alias(r), "token")}
	if j := r.Spec.Loop.JudgeModel; j != "" {
		srcs = append(srcs, token(j, "judge-token"))
	}
	return corev1.Volume{Name: "model-token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{DefaultMode: mode, Sources: srcs}}}
}

// egressProfiles is the bridge's EGRESS_PROFILES: github first, as the composition joins them.
func egressProfiles(r *v1alpha1.AgentRun) string {
	out := "github"
	for _, p := range r.Spec.Egress.Profiles {
		out += "," + string(p)
	}
	return out
}

// brokerHost is the broker URL's host, for the CNP's DNS rule.
func brokerHost(c Config) string {
	u, _ := url.Parse(c.Broker.URL) // validated at load
	return u.Hostname()
}
