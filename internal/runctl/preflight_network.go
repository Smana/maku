// SPDX-License-Identifier: Apache-2.0

package runctl

import (
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
//   - a probe pod that does not start in ProbeStart (no sandbox runtime on the nodes, an image it cannot
//     pull) is Failed, retried soon.
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
	start, poll := p.ProbeStart, p.ProbePoll
	if start <= 0 {
		start = 2 * time.Minute
	}
	if poll <= 0 {
		poll = 2 * time.Second
	}
	began := time.Now()
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
			if time.Since(began) > start {
				return verdict{refusal: fmt.Sprintf("the probe pod did not start under RuntimeClass %q: %s",
					p.Cfg.RuntimeClassName, p.lastEvent(ctx, got.Name)), soon: true}, nil
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

// probeFailed reads netprobe's termination message.
func probeFailed(pod *corev1.Pod) verdict {
	msg := pod.Status.Reason
	for _, cs := range pod.Status.ContainerStatuses {
		if t := cs.State.Terminated; t != nil && t.Message != "" {
			var r probeResult
			if err := json.Unmarshal([]byte(t.Message), &r); err != nil {
				msg = strings.TrimSpace(t.Message)
				break
			}
			if r.Kind == "unreachable" {
				return verdict{refusal: r.Message, soon: true, waits: true}
			}
			return verdict{refusal: r.Message}
		}
	}
	if msg == "" {
		msg = "no termination message"
	}
	return verdict{refusal: "the probe pod failed: " + msg, soon: true}
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

// lastEvent is the latest event about the pod, "reason: message", for the refusal.
func (p *Preflight) lastEvent(ctx context.Context, name string) string {
	var evs corev1.EventList
	if err := p.Client.List(ctx, &evs, client.InNamespace(p.Cfg.Namespace)); err != nil {
		return "its events are unreadable: " + err.Error()
	}
	var last *corev1.Event
	at := func(e *corev1.Event) time.Time {
		if !e.EventTime.IsZero() {
			return e.EventTime.Time
		}
		return e.LastTimestamp.Time
	}
	for i := range evs.Items {
		e := &evs.Items[i]
		if e.InvolvedObject.Kind == "Pod" && e.InvolvedObject.Name == name && (last == nil || !at(e).Before(at(last))) {
			last = e
		}
	}
	if last == nil {
		return "no event"
	}
	return last.Reason + ": " + last.Message
}
