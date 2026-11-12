// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"net"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

// ProbeRunID is the run-id label of every preflight probe pod: its NetworkPolicy selects it, and the
// preflight finds leftovers by it.
const ProbeRunID = "preflight"

// probeHost is the probe's positive control through the egress proxy, and its pod's allow-list.
const probeHost = "github.com:443"

// probeUID is the probe container's user, the run-controller image's nonroot user.
const probeUID = 65532

// probeRun is the synthetic run whose builders make the probe's objects, so the probe is held by
// exactly what holds a run with a room: its NetworkPolicy opens DNS, the proxy and the broker's 8443.
func probeRun(c Config, name string) *v1alpha1.AgentRun {
	return &v1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.Namespace},
		Spec: v1alpha1.AgentRunSpec{Role: ProbeRunID, RoomRef: ProbeRunID, Principal: "system:run-controller"}}
}

// ProbeName is a probe pod's name: <namePrefix>preflight-<suffix>.
func ProbeName(c Config, suffix string) string { return c.NamePrefix + "preflight-" + suffix }

// ProbePod is the network preflight's pod (plan PR7): it runs `netprobe` from the run-controller's
// image, under the sandbox class, with run labels and the egress annotation github.com:443, and passes
// the run-pod policy. Its positive controls are the broker's 8443 and CONNECT github.com:443 through the
// proxy. Its negative controls, each always listening:
//   - the broker's 9090, which both its own policy and the broker's deny;
//   - the apiserver (kubernetes.default.svc) on 443 and github.com:443 dialled directly, past the
//     proxy, which only the run's egress policy denies: a CNI that enforces ingress alone fails here.
func ProbePod(c Config, suffix string) *corev1.Pod {
	r := probeRun(c, ProbeName(c, suffix))
	broker := brokerHost(c)
	no, yes, uid, deadline := false, true, int64(probeUID), int64(180)
	om := meta(r, ProbeRunID, r.Name)
	om.Annotations[EgressAnnotation] = probeHost
	var rc *string
	if c.RuntimeClassName != "" {
		rc = &c.RuntimeClassName
	}
	return &corev1.Pod{
		ObjectMeta: om,
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &no, RuntimeClassName: rc, RestartPolicy: corev1.RestartPolicyNever,
			ActiveDeadlineSeconds: &deadline, EnableServiceLinks: &no,
			DNSConfig: &corev1.PodDNSConfig{Options: []corev1.PodDNSConfigOption{{Name: "ndots", Value: new("1")}}},
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &yes, RunAsUser: &uid, RunAsGroup: &uid,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{
				Name: "netprobe", Image: c.Images.Probe,
				Args: []string{"netprobe",
					"--allow", net.JoinHostPort(broker, "8443"),
					"--deny", net.JoinHostPort(broker, "9090"),
					"--deny", net.JoinHostPort("kubernetes.default.svc."+c.clusterDomain(), "443"),
					"--deny", probeHost,
					"--connect", probeHost, "--proxy", c.EgressProxy.URL},
				Resources:                res("10m", "16Mi", "100m", "64Mi"),
				SecurityContext:          containerSecurity(),
				TerminationMessagePolicy: corev1.TerminationMessageReadFile,
			}},
		},
	}
}

// ProbeNetworkPolicy is the probe pod's own policy, built as a run's, owned by the pod (not as its
// controller: no blockOwnerDeletion), so it goes when the pod does.
func ProbeNetworkPolicy(c Config, pod *corev1.Pod) *networkingv1.NetworkPolicy {
	np := KubeNetworkPolicy(probeRun(c, pod.Name), c, Model{}, ProbeRunID)
	np.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID}}
	return np
}
