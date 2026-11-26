// SPDX-License-Identifier: Apache-2.0

// Package egressproxy is the egress proxy of the run-controller's kubernetes network mode (v4 D6,
// plan PR5): a CONNECT-only forward proxy that knows a client by its source pod IP and lets it reach
// only the host:port pairs of its pod's maku.ogenki.io/egress annotation, which the run-controller
// alone writes (the run-pod policy holds it). A check-enabled run also reaches its install set
// (maku.ogenki.io/egress-install) until its harness closes it or its deadline passes, whichever
// comes first; the close is one-way.
package egressproxy

import (
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/Smana/maku/internal/runctl"
)

// The run pod's annotations the proxy reads.
const (
	EgressAnnotation        = runctl.EgressAnnotation
	InstallAnnotation       = runctl.EgressInstallAnnotation
	InstallUntilAnnotation  = runctl.EgressInstallUntilAnnotation
	InstallClosedAnnotation = runctl.EgressInstallClosedAnnotation
)

// Entry is one client's identity, allow-list and install set.
type Entry struct {
	RunID string
	Allow []string
	// Install is the install set; InstallUntil its deadline, zero when the pod names none or an
	// unreadable one, which reads as closed; InstallClosed that the set was closed.
	Install       []string
	InstallUntil  time.Time
	InstallClosed bool
	// The pod's identity, which a close names.
	Namespace, Name, UID string
}

// InstallOpen is whether the install set admits a CONNECT at now.
func (e Entry) InstallOpen(now time.Time) bool {
	return len(e.Install) > 0 && !e.InstallClosed && now.Before(e.InstallUntil)
}

// Index maps a pod IP to its run, from the pods a watch feeds it.
type Index struct {
	mu   sync.RWMutex
	pods map[string]*corev1.Pod // by namespace/name
	// closed are the pod UIDs whose install set this replica closed, ahead of the watch delivering the
	// pod's closed annotation; an entry goes with its pod.
	closed map[string]bool
}

// NewIndex is an empty Index.
func NewIndex() *Index { return &Index{pods: map[string]*corev1.Pod{}, closed: map[string]bool{}} }

// Upsert records p's latest state.
func (x *Index) Upsert(p *corev1.Pod) {
	x.mu.Lock()
	defer x.mu.Unlock()
	k := p.Namespace + "/" + p.Name
	if old, ok := x.pods[k]; ok && old.UID != p.UID {
		delete(x.closed, string(old.UID))
	}
	x.pods[k] = p
}

// Delete forgets a pod.
func (x *Index) Delete(namespace, name string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	k := namespace + "/" + name
	if old, ok := x.pods[k]; ok {
		delete(x.closed, string(old.UID))
	}
	delete(x.pods, k)
}

// MarkClosed records that the pod with uid had its install set closed. It only narrows: nothing
// reopens a set.
func (x *Index) MarkClosed(uid string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, p := range x.pods {
		if string(p.UID) == uid {
			x.closed[uid] = true
			return
		}
	}
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
	allow := hostPorts(hit.Annotations[EgressAnnotation])
	if len(allow) == 0 {
		return Entry{}, false
	}
	e := Entry{RunID: hit.Labels[runctl.LabelRunID], Allow: allow, Install: hostPorts(hit.Annotations[InstallAnnotation]),
		InstallClosed: hit.Annotations[InstallClosedAnnotation] == "true" || x.closed[string(hit.UID)],
		Namespace:     hit.Namespace, Name: hit.Name, UID: string(hit.UID)}
	if until, err := time.Parse(time.RFC3339, hit.Annotations[InstallUntilAnnotation]); err == nil {
		e.InstallUntil = until
	}
	return e, true
}

// hostPorts splits an annotation's comma-joined host:port list.
func hostPorts(v string) []string {
	var out []string
	for h := range strings.SplitSeq(v, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
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
