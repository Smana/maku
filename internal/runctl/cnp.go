// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"net"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

// NetworkPolicy is the run's default-deny CNP: DNS for its names only, the broker on 8443 (and
// roomToolsURL's port when set),
// github and its profiles on 443, the model host on its port, the OTLP collector's pods on its port
// when tracing is configured. Nothing dials in except the
// kubelet's probe of the bridge (C4).
func NetworkPolicy(r *v1alpha1.AgentRun, c Config, m Model, runID string) *unstructured.Unstructured {
	web := webHosts(r, c, m) // reached on 443
	modelNS, inCluster := inClusterNamespace(m.Host)
	names := addUnique(slices.Clone(web), m.Host)
	matchNames := func(hs []string) []any {
		out := make([]any, 0, len(hs))
		for _, h := range hs {
			out = append(out, map[string]any{"matchName": h})
		}
		return out
	}
	port := func(p int) []any {
		return []any{map[string]any{"ports": []any{map[string]any{"port": strconv.Itoa(p), "protocol": "TCP"}}}}
	}
	// A bare ingress section is what puts the endpoint in ingress default-deny; Cilium documents
	// the empty rule {} for it, and a Go-empty list is omitted by omitempty, so it would not.
	ingress := []any{map[string]any{}}
	var roomEgress []any
	if c.Tracing.enabled() {
		names = addUnique(names, c.Tracing.host())
	}
	if r.Spec.RoomRef != "" {
		names = append(names, brokerHost(c))
		sel := map[string]any{"io.kubernetes.pod.namespace": c.Broker.Namespace}
		for k, v := range c.Broker.Labels {
			sel[k] = v
		}
		toPorts := port(8443)
		if c.RoomToolsURL != "" { // the harness's room tools, on the same broker pods
			toPorts = []any{map[string]any{"ports": []any{map[string]any{"port": "8443", "protocol": "TCP"},
				map[string]any{"port": strconv.Itoa(c.roomToolsPort()), "protocol": "TCP"}}}}
		}
		roomEgress = []any{map[string]any{"toEndpoints": []any{map[string]any{"matchLabels": sel}}, "toPorts": toPorts}}
		ingress = []any{map[string]any{"fromEntities": []any{"host"}, "toPorts": port(BridgeHealthPort)}}
	}
	egress := []any{
		map[string]any{
			"toEndpoints": []any{map[string]any{"matchLabels": map[string]any{"io.kubernetes.pod.namespace": "kube-system", "k8s-app": "kube-dns"}}},
			"toPorts": []any{map[string]any{
				"ports": []any{map[string]any{"port": "53", "protocol": "UDP"}, map[string]any{"port": "53", "protocol": "TCP"}},
				"rules": map[string]any{"dns": matchNames(names)},
			}},
		},
		map[string]any{"toFQDNs": matchNames(web), "toPorts": port(443)},
	}
	switch {
	case inCluster:
		// Not toFQDNs: that matches the DNS-resolved ClusterIP, but Cilium translates the Service to a
		// backend pod before policy, so the rule would never match. Policy sees the backend port: the model
		// URL's port must equal the Service's targetPort.
		sel := map[string]any{"io.kubernetes.pod.namespace": modelNS}
		egress = append(egress, map[string]any{"toEndpoints": []any{map[string]any{"matchLabels": sel}}, "toPorts": port(m.Port)})
	case m.Port != 443:
		egress = append(egress, map[string]any{"toFQDNs": matchNames([]string{m.Host}), "toPorts": port(m.Port)})
	}
	egress = append(egress, roomEgress...)
	if c.Tracing.enabled() { // the collector's OTLP port, and only when tracing is configured (step 6 R1)
		sel := map[string]any{"io.kubernetes.pod.namespace": c.Tracing.Collector.Namespace}
		for k, v := range c.Tracing.Collector.Labels {
			sel[k] = v
		}
		egress = append(egress, map[string]any{"toEndpoints": []any{map[string]any{"matchLabels": sel}}, "toPorts": port(c.Tracing.port())})
	}
	spec := map[string]any{
		"endpointSelector": map[string]any{"matchLabels": map[string]any{LabelRunID: runID}},
		"egress":           egress,
		"ingress":          ingress,
	}
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cilium.io/v2", "kind": "CiliumNetworkPolicy", "spec": spec}}
	om := meta(r, runID, r.Name)
	u.SetName(om.Name)
	u.SetNamespace(om.Namespace)
	u.SetLabels(om.Labels)
	return u
}

// webHosts are the hosts a run reaches on 443: github and its profiles, then a model outside the
// cluster served on 443.
func webHosts(r *v1alpha1.AgentRun, c Config, m Model) []string {
	web := c.FQDNs(r)
	if _, inCluster := inClusterNamespace(m.Host); m.Port == 443 && !inCluster {
		web = addUnique(web, m.Host)
	}
	return web
}

// Egress is a run's allow-list as host:port, the same set its CNP opens on the web: webHosts on 443,
// then a model outside the cluster on another port. An in-cluster model is not in it: it is reached
// directly, by NetworkPolicy, never through the egress proxy. It is the pod's EgressAnnotation.
func Egress(r *v1alpha1.AgentRun, c Config, m Model) []string {
	web := webHosts(r, c, m)
	out := make([]string, 0, len(web)+1)
	for _, h := range web {
		out = append(out, net.JoinHostPort(h, "443"))
	}
	if _, inCluster := inClusterNamespace(m.Host); !inCluster && m.Port != 443 {
		out = addUnique(out, net.JoinHostPort(m.Host, strconv.Itoa(m.Port)))
	}
	return out
}

// inClusterNamespace is the namespace of a Service host (<svc>.<ns>.svc or <svc>.<ns>.svc.<domain>),
// the only hosts ModelFrom lets use plain http.
func inClusterNamespace(host string) (string, bool) {
	p := strings.Split(host, ".")
	if len(p) >= 3 && p[2] == "svc" && p[0] != "" && p[1] != "" {
		return p[1], true
	}
	return "", false
}
