// SPDX-License-Identifier: Apache-2.0

// Package egressproxy is the egress proxy of the run-controller's kubernetes network mode (v4 D6,
// plan PR5): a CONNECT-only forward proxy that knows a client by its source pod IP and lets it reach
// only the host:port pairs of its pod's maku.ogenki.io/egress annotation, which the run-controller
// alone writes (the run-pod policy holds it).
package egressproxy

import (
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"

	"github.com/Smana/maku/internal/runctl"
)

// EgressAnnotation is the run pod's allow-list.
const EgressAnnotation = runctl.EgressAnnotation

// Entry is one client's identity and allow-list.
type Entry struct {
	RunID string
	Allow []string
}

// Index maps a pod IP to its run, from the pods a watch feeds it.
type Index struct {
	mu   sync.RWMutex
	pods map[string]*corev1.Pod // by namespace/name
}

// NewIndex is an empty Index.
func NewIndex() *Index { return &Index{pods: map[string]*corev1.Pod{}} }

// Upsert records p's latest state.
func (x *Index) Upsert(p *corev1.Pod) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.pods[p.Namespace+"/"+p.Name] = p
}

// Delete forgets a pod.
func (x *Index) Delete(namespace, name string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.pods, namespace+"/"+name)
}

// finished pods keep their IP in status, but no longer hold it: the IP may already be another pod's.
func finished(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}

// Lookup is ip's entry. It has one only when exactly one unfinished pod holds ip, and that pod is the
// run-controller's, not terminating, and annotated. Pending counts with Running: a pod with an IP is
// already running containers, and the watch can trail its phase change. A terminating pod still holds
// its IP, so it makes the IP ambiguous instead of freeing it.
func (x *Index) Lookup(ip string) (Entry, bool) {
	if ip == "" {
		return Entry{}, false
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	var hit *corev1.Pod
	for _, p := range x.pods {
		if finished(p) || !hasIP(p, ip) {
			continue
		}
		if hit != nil {
			return Entry{}, false
		}
		hit = p
	}
	if hit == nil || hit.DeletionTimestamp != nil || hit.Labels[runctl.LabelManagedBy] != "run-controller" {
		return Entry{}, false
	}
	var allow []string
	for h := range strings.SplitSeq(hit.Annotations[EgressAnnotation], ",") {
		if h = strings.TrimSpace(h); h != "" {
			allow = append(allow, h)
		}
	}
	if len(allow) == 0 {
		return Entry{}, false
	}
	return Entry{RunID: hit.Labels[runctl.LabelRunID], Allow: allow}, true
}

func hasIP(p *corev1.Pod, ip string) bool {
	if p.Status.PodIP == ip {
		return true
	}
	for _, a := range p.Status.PodIPs {
		if a.IP == ip {
			return true
		}
	}
	return false
}
