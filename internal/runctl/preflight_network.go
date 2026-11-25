// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// probeResult is netprobe's termination message (internal/app's netprobeResult).
type probeResult struct {
	OK      bool   `json:"ok"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// probeSuffix is 8 random characters of [a-z2-7], a run id's alphabet.
func probeSuffix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// network (kubernetes mode, v4 D6): a probe pod, held by a run's policies, must reach the broker's 8443
// and CONNECT github.com:443 through the egress proxy, and must not reach the broker's 9090. Rulings:
//   - the denied connection succeeding means the CNI does not enforce NetworkPolicy: Failed, so new
//     runs are refused (D6), and checked again at the normal interval;
//   - a positive control failing (the proxy or the broker still starting right after install) holds
//     new runs Pending (Waiting) and is retried soon: no run starts unconfined either way;
//   - a probe pod that does not start is Failed with sandbox set, retried soon: within ProbeStart
//     when nothing shows progress (no sandbox runtime on the nodes, an image it cannot pull, a
//     scale-up the autoscaler declined), within ProbeScaleUp while something does (a scale-up
//     triggered for it, from a sandbox pool at zero; a node bound and starting it). A node's startup
//     taints (not-ready, uninitialized) are not tolerated: they clear, and the probe waits for them.
func (p *Preflight) network(ctx context.Context) (verdict, error) {
	if err := p.deleteProbes(ctx); err != nil {
		return verdict{}, err
	}
	suffix := probeSuffix
	if p.ProbeSuffix != nil {
		suffix = p.ProbeSuffix
	}
	pod := ProbePod(p.Cfg, suffix())
	if err := p.Kube.Create(ctx, pod); err != nil {
		if apierrors.IsForbidden(err) || apierrors.IsInvalid(err) {
			return verdict{refusal: "the probe pod was refused: " + err.Error(), soon: true}, nil
		}
		return verdict{}, fmt.Errorf("create the probe pod: %w", err)
	}
	defer func() {
		bg := metav1.DeletePropagationBackground
		_ = p.Kube.Delete(context.WithoutCancel(ctx), pod, &client.DeleteOptions{PropagationPolicy: &bg})
	}()
	if err := p.Kube.Create(ctx, ProbeNetworkPolicy(p.Cfg, pod)); err != nil {
		if apierrors.IsForbidden(err) || apierrors.IsInvalid(err) {
			return verdict{refusal: "the probe pod's NetworkPolicy was refused: " + err.Error(), soon: true}, nil
		}
		return verdict{}, fmt.Errorf("create the probe pod's NetworkPolicy: %w", err)
	}
	start, scaleUp, poll := p.ProbeStart, p.ProbeScaleUp, p.ProbePoll
	if start <= 0 {
		start = 2 * time.Minute
	}
	if scaleUp <= 0 {
		scaleUp = 10 * time.Minute
	}
	scaleUp = max(scaleUp, start)
	if poll <= 0 {
		poll = 2 * time.Second
	}
	began := p.now()
	for {
		var got corev1.Pod
		if err := p.Client.Get(ctx, client.ObjectKeyFromObject(pod), &got); err != nil {
			return verdict{}, fmt.Errorf("read the probe pod: %w", err)
		}
		switch got.Status.Phase {
		case corev1.PodSucceeded:
			return verdict{}, nil
		case corev1.PodFailed:
			return probeFailed(&got), nil
		case corev1.PodPending, "":
			if waited := p.now().Sub(began); waited > start {
				ev := p.probeEvents(ctx, &got)
				if bound := start; !ev.progressing || waited > scaleUp {
					if ev.progressing {
						bound = scaleUp
					}
					return verdict{refusal: fmt.Sprintf("the probe pod did not start under RuntimeClass %q within %s: %s",
						p.Cfg.RuntimeClassName, bound, ev.last), soon: true, sandbox: true}, nil
				}
			}
		}
		// Running: netprobe ends itself; activeDeadlineSeconds ends it at worst, as Failed.
		t := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return verdict{}, ctx.Err()
		case <-t.C:
		}
	}
}

// probeFailed reads netprobe's termination message. A pod that failed with none never ran netprobe
// to its end (activeDeadlineSeconds on a node that could not start it, an eviction): sandbox.
func probeFailed(pod *corev1.Pod) verdict {
	for _, cs := range pod.Status.ContainerStatuses {
		if t := cs.State.Terminated; t != nil && t.Message != "" {
			var r probeResult
			if err := json.Unmarshal([]byte(t.Message), &r); err != nil {
				return verdict{refusal: "the probe pod failed: " + strings.TrimSpace(t.Message), soon: true}
			}
			if r.Kind == "unreachable" {
				return verdict{refusal: r.Message, soon: true, waits: true}
			}
			return verdict{refusal: r.Message}
		}
	}
	return verdict{refusal: "the probe pod failed: " + cmp.Or(pod.Status.Reason, "no termination message"), soon: true, sandbox: true}
}

// deleteProbes deletes every probe pod left over (by an earlier tick or process).
func (p *Preflight) deleteProbes(ctx context.Context) error {
	var pods corev1.PodList
	if err := p.Client.List(ctx, &pods, client.InNamespace(p.Cfg.Namespace),
		client.MatchingLabels{LabelRunID: ProbeRunID, LabelManagedBy: managedBy}); err != nil {
		return fmt.Errorf("list leftover probe pods: %w", err)
	}
	bg := metav1.DeletePropagationBackground
	for i := range pods.Items {
		if err := p.Kube.Delete(ctx, &pods.Items[i], &client.DeleteOptions{PropagationPolicy: &bg}); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete leftover probe pod %s: %w", pods.Items[i].Name, err)
		}
	}
	return nil
}

// probeStart is what the probe pod's events say of its start: last is the latest, "reason:
// message", for the refusal; progressing is that the pod is on its way, so a longer wait is worth it.
type probeStart struct {
	last        string
	progressing bool
}

// The cluster autoscaler's events about a pending pod: it added a node for it, or it will not.
const (
	eventTriggeredScaleUp  = "TriggeredScaleUp"
	eventNotTriggerScaleUp = "NotTriggerScaleUp"
)

// probeEvents reads the probe pod's events. It is progressing when the autoscaler's latest word on it
// is TriggeredScaleUp (the node is coming: the scheduler's FailedScheduling meanwhile, against no
// node or one still tainted not-ready, is expected), or when it is bound to a node whose latest event
// is no warning (pulling the image, creating the sandbox). A later NotTriggerScaleUp (no node group
// fits, or none may grow) or a warning on the bound node (FailedCreatePodSandBox: no runtime for
// the class) is no progress.
func (p *Preflight) probeEvents(ctx context.Context, pod *corev1.Pod) probeStart {
	var evs corev1.EventList
	if err := p.Client.List(ctx, &evs, client.InNamespace(p.Cfg.Namespace)); err != nil {
		return probeStart{last: "its events are unreadable: " + err.Error()}
	}
	at := func(e *corev1.Event) time.Time {
		if !e.EventTime.IsZero() {
			return e.EventTime.Time
		}
		return e.LastTimestamp.Time
	}
	var last, scale *corev1.Event
	for i := range evs.Items {
		e := &evs.Items[i]
		if e.InvolvedObject.Kind != "Pod" || e.InvolvedObject.Name != pod.Name {
			continue
		}
		if last == nil || !at(e).Before(at(last)) {
			last = e
		}
		if (e.Reason == eventTriggeredScaleUp || e.Reason == eventNotTriggerScaleUp) && (scale == nil || !at(e).Before(at(scale))) {
			scale = e
		}
	}
	if last == nil {
		return probeStart{last: "no event"}
	}
	scaling := scale != nil && scale.Reason == eventTriggeredScaleUp
	starting := pod.Spec.NodeName != "" && last.Type != corev1.EventTypeWarning
	return probeStart{last: last.Reason + ": " + last.Message, progressing: scaling || starting}
}
