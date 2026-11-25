// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

// The gcp-0 incident (2026-11-25): the sandbox pool scales from zero, so the probe pod is what wakes
// it. The autoscaler triggered a scale-up for the probe, the node came up still tainted not-ready,
// and the scheduler's FailedScheduling was the probe's latest event when its start bound ran out.

const probeName = "run-preflight-abcdefgh"

func probeEvent(name, typ, reason, msg string, at time.Time) *corev1.Event {
	return &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "agents"}, Type: typ, Reason: reason, Message: msg,
		LastTimestamp: metav1.NewTime(at), InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: probeName, Namespace: "agents"}}
}

// scaleUpEvents are the incident's: the autoscaler's TriggeredScaleUp, then the scheduler's
// FailedScheduling against the new node's startup taint.
func scaleUpEvents(base time.Time) []client.Object {
	return []client.Object{
		probeEvent("ev-1", corev1.EventTypeWarning, "FailedScheduling", "0/5 nodes are available: 5 node(s) didn't match Pod's node affinity/selector", base),
		probeEvent("ev-2", corev1.EventTypeNormal, eventTriggeredScaleUp, "pod triggered scale-up: [{agents-gvisor 0->1 (max: 3)}]", base.Add(time.Second)),
		probeEvent("ev-3", corev1.EventTypeWarning, "FailedScheduling",
			"0/6 nodes are available: 1 node(s) had untolerated taint(s), 5 node(s) didn't match Pod's node affinity/selector", base.Add(2*time.Second)),
	}
}

// pendingProbe is a fake whose probe pod stays Pending (unbound unless node is set) for `pending`
// reads, then ends Succeeded; pending < 0 keeps it Pending.
func pendingProbe(t *testing.T, pending int, node string, objs ...client.Object) client.WithWatch {
	t.Helper()
	inner := fakeReader(t, append([]client.Object{gvisor()}, objs...)...)
	var mu sync.Mutex
	reads := 0
	return interceptor.NewClient(inner, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			if p, ok := o.(*corev1.Pod); ok {
				p.Status.Phase, p.Spec.NodeName = corev1.PodPending, node
			}
			return c.Create(ctx, o, opts...)
		},
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			if err := c.Get(ctx, key, o, opts...); err != nil {
				return err
			}
			if p, ok := o.(*corev1.Pod); ok && key.Name == probeName {
				mu.Lock()
				reads++
				if pending >= 0 && reads > pending {
					p.Status.Phase = corev1.PodSucceeded
				}
				mu.Unlock()
			}
			return nil
		},
	})
}

// fakeClock advances step on every read: each poll of the probe moves time on by step.
func fakeClock(start time.Time, step time.Duration) func() time.Time {
	var mu sync.Mutex
	now := start
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(step)
		return now
	}
}

// Fails before the fix: a probe pending past ProbeStart failed, whatever the autoscaler was doing.
func TestPreflightNetworkWaitsForATriggeredScaleUp(t *testing.T) {
	p := netPreflight(t, pendingProbe(t, 15, "", scaleUpEvents(time.Now())...))
	p.ProbeStart, p.ProbePoll = 20*time.Millisecond, 10*time.Millisecond
	p.Tick(t.Context())
	if got := p.Result(); got.State != PreflightPassed {
		t.Fatalf("a probe the autoscaler is adding a node for waits for it: %+v", got)
	}
}

func TestPreflightNetworkScaleUpBounds(t *testing.T) {
	base := time.Date(2026, 11, 25, 22, 53, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		pending int
		node    string
		events  []client.Object
		pass    bool
		want    string // the refusal's prefix and bound
	}{
		// 1 minute a poll: the node joins in 6 minutes, inside the 10-minute bound.
		"a scale-up that lands": {pending: 6, events: scaleUpEvents(base), pass: true},
		"a scale-up that never lands": {pending: -1, events: scaleUpEvents(base),
			want: `the probe pod did not start under RuntimeClass "gvisor" within 10m0s: FailedScheduling: 0/6 nodes are available: 1 node(s) had untolerated taint(s)`},
		// The autoscaler's later word wins: no node group may grow, so the short bound applies.
		"a scale-up declined after one triggered": {pending: 6, events: append(scaleUpEvents(base),
			probeEvent("ev-4", corev1.EventTypeNormal, eventNotTriggerScaleUp, "pod didn't trigger scale-up: 1 max node group size reached", base.Add(3*time.Second))),
			want: `the probe pod did not start under RuntimeClass "gvisor" within 2m0s: NotTriggerScaleUp: pod didn't trigger scale-up: 1 max node group size reached`},
		"no autoscaler and no node": {pending: 6, events: []client.Object{probeEvent("ev-1", corev1.EventTypeWarning, "FailedScheduling", "0/5 nodes are available", base)},
			want: `within 2m0s: FailedScheduling: 0/5 nodes are available`},
		"bound to a node that is pulling the image": {pending: 6, node: "gke-agents-gvisor-1",
			events: []client.Object{probeEvent("ev-1", corev1.EventTypeNormal, "Pulling", "Pulling image", base)}, pass: true},
		"bound to a node with no runtime for the class": {pending: 6, node: "gke-agents-gvisor-1",
			events: []client.Object{probeEvent("ev-1", corev1.EventTypeWarning, "FailedCreatePodSandBox", `no runtime for "runsc" is configured`, base)},
			want:   `within 2m0s: FailedCreatePodSandBox: no runtime for "runsc" is configured`},
	} {
		t.Run(name, func(t *testing.T) {
			p := netPreflight(t, pendingProbe(t, c.pending, c.node, c.events...))
			p.ProbeStart, p.ProbePoll, p.Now = 0, time.Millisecond, fakeClock(base, time.Minute) // default bounds: 2 and 10 minutes
			soon := p.Tick(t.Context())
			got := p.Result()
			if c.pass {
				if got.State != PreflightPassed {
					t.Fatalf("%+v", got)
				}
				return
			}
			if got.State != PreflightFailed || !got.Sandbox || !soon || !strings.Contains(got.Message, c.want) {
				t.Fatalf("soon=%v %+v\nwant Failed, Sandbox, containing %s", soon, got, c.want)
			}
		})
	}
}

// What netprobe itself finds is the cluster's setup, never the sandbox's nodes; a probe pod ended
// with no termination message never ran it to its end.
func TestPreflightNetworkSandboxOnlyWhenTheProbeNeverRan(t *testing.T) {
	for name, c := range map[string]struct {
		msg     string
		sandbox bool
	}{
		"unenforced":             {msg: termMsg(t, "unenforced", "deny connected"), sandbox: false},
		"not JSON":               {msg: "panic: boom", sandbox: false},
		"no termination message": {sandbox: true},
	} {
		t.Run(name, func(t *testing.T) {
			p := netPreflight(t, probeClient(t, corev1.PodFailed, c.msg))
			p.Tick(t.Context())
			if got := p.Result(); got.State != PreflightFailed || got.Sandbox != c.sandbox {
				t.Fatalf("%+v, want Sandbox %v", got, c.sandbox)
			}
		})
	}
}

// The probe is scheduled exactly as a run pod is: neither sets a toleration, node selector or
// affinity of its own, so the RuntimeClass's scheduling (GKE Sandbox's sandbox.gke.io/runtime
// selector and toleration) is all either gets, and a probe that schedules proves a run will. A
// node's startup taints are tolerated by neither: they clear.
func TestProbePodSchedulesExactlyLikeARunPod(t *testing.T) {
	c := kubeConfig(t)
	probe := ProbePod(c, "abcdefgh").Spec
	run := Pod(&v1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run-aaaaaaaa", Namespace: c.Namespace},
		Spec: v1alpha1.AgentRunSpec{Role: "implementer", RoomRef: "r", Principal: "p"}}, c, Model{}, "aaaaaaaa").Spec
	type sched struct {
		rc, priority, scheduler string
		selector                map[string]string
		affinity                *corev1.Affinity
		tolerations             []corev1.Toleration
		spread                  []corev1.TopologySpreadConstraint
	}
	of := func(s corev1.PodSpec) sched {
		return sched{*s.RuntimeClassName, s.PriorityClassName, s.SchedulerName, s.NodeSelector, s.Affinity, s.Tolerations, s.TopologySpreadConstraints}
	}
	if p, r := of(probe), of(run); p.rc != r.rc || p.priority != r.priority || p.scheduler != r.scheduler ||
		len(p.selector)+len(r.selector)+len(p.tolerations)+len(r.tolerations)+len(p.spread)+len(r.spread) != 0 || p.affinity != nil || r.affinity != nil {
		t.Fatalf("probe %+v\nrun   %+v", p, r)
	}
}
