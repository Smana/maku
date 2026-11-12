// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

// EgressAnnotation is a kubernetes-mode run pod's allow-list, host:port joined by commas. The egress
// proxy trusts it: the run-pod policy lets only the run-controller create pods in the runs namespace,
// and refuses any change to it after create.
const EgressAnnotation = "maku.ogenki.io/egress"

// nsSelector selects a namespace by its immutable name label.
func nsSelector(ns string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": ns}}
}

func tcp(ports ...int) []networkingv1.NetworkPolicyPort {
	out := make([]networkingv1.NetworkPolicyPort, 0, len(ports))
	for _, p := range ports {
		proto, port := corev1.ProtocolTCP, intstr.FromInt32(int32(p)) //nolint:gosec // a port, validated or a constant
		out = append(out, networkingv1.NetworkPolicyPort{Protocol: &proto, Port: &port})
	}
	return out
}

// dnsRule opens 53 on the cluster DNS pods.
func dnsRule(c Config) networkingv1.NetworkPolicyEgressRule {
	udp, tcpP, port := corev1.ProtocolUDP, corev1.ProtocolTCP, intstr.FromInt32(53)
	return networkingv1.NetworkPolicyEgressRule{
		To:    []networkingv1.NetworkPolicyPeer{{NamespaceSelector: nsSelector(c.DNS.Namespace), PodSelector: &metav1.LabelSelector{MatchLabels: c.DNS.Labels}}},
		Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &port}, {Protocol: &tcpP, Port: &port}},
	}
}

// KubeNetworkPolicy is a kubernetes-mode run's policy (plan PR6): DNS to the cluster DNS, the egress
// proxy on its port, the broker on 8443 (and roomToolsURL's port) when the run has a room, the model's
// namespace on its port when the model is in-cluster. Ingress: the bridge's health port only, for the
// kubelet's probe, and nothing without a room. The web is reached only through the proxy, which holds
// the run to Egress; DNS stays open, so DNS tunnelling is this mode's residual risk (docs/security.md).
func KubeNetworkPolicy(r *v1alpha1.AgentRun, c Config, m Model, runID string) *networkingv1.NetworkPolicy {
	egress := []networkingv1.NetworkPolicyEgressRule{dnsRule(c), {
		To:    []networkingv1.NetworkPolicyPeer{{NamespaceSelector: nsSelector(c.EgressProxy.Namespace), PodSelector: &metav1.LabelSelector{MatchLabels: c.EgressProxy.Labels}}},
		Ports: tcp(c.EgressProxy.Port()),
	}}
	if ns, inCluster := inClusterNamespace(m.Host); inCluster {
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: nsSelector(ns)}}, Ports: tcp(m.Port)})
	}
	var ingress []networkingv1.NetworkPolicyIngressRule
	if r.Spec.RoomRef != "" {
		ports := []int{8443}
		if c.RoomToolsURL != "" {
			ports = append(ports, c.roomToolsPort())
		}
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{
			To:    []networkingv1.NetworkPolicyPeer{{NamespaceSelector: nsSelector(c.Broker.Namespace), PodSelector: &metav1.LabelSelector{MatchLabels: c.Broker.Labels}}},
			Ports: tcp(ports...)})
		ingress = []networkingv1.NetworkPolicyIngressRule{{Ports: tcp(BridgeHealthPort)}}
	}
	return &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: meta(r, runID, r.Name),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{LabelRunID: runID}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     ingress, Egress: egress,
		},
	}
}

// proxyEnv is a kubernetes-mode harness's proxy variables, both cases: git and curl read the lower,
// Python's libraries either. NO_PROXY keeps loopback (the bridge) and in-cluster Services direct.
func proxyEnv(c Config) []corev1.EnvVar {
	noProxy := strings.Join([]string{"127.0.0.1", "localhost", ".svc." + c.clusterDomain()}, ",")
	return []corev1.EnvVar{
		{Name: "HTTPS_PROXY", Value: c.EgressProxy.URL}, {Name: "https_proxy", Value: c.EgressProxy.URL},
		{Name: "NO_PROXY", Value: noProxy}, {Name: "no_proxy", Value: noProxy},
	}
}

// egressAnnotation is Egress joined, for EgressAnnotation.
func egressAnnotation(r *v1alpha1.AgentRun, c Config, m Model) string {
	return strings.Join(Egress(r, c, m), ",")
}
