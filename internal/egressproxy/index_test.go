// SPDX-License-Identifier: Apache-2.0

package egressproxy

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func runPod(name, ip string, mut ...func(*corev1.Pod)) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "agents",
			Labels:      map[string]string{"app.kubernetes.io/managed-by": "run-controller", "agents.ogenki.io/run-id": name[len(name)-8:]},
			Annotations: map[string]string{EgressAnnotation: "github.com:443,api.github.com:443"}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip},
	}
	for _, m := range mut {
		m(p)
	}
	return p
}

func TestIndexMapsARunningPodToItsAnnotation(t *testing.T) {
	x := NewIndex()
	x.Upsert(runPod("run-abcdefgh", "10.0.0.5"))
	e, ok := x.Lookup("10.0.0.5")
	if !ok || e.RunID != "abcdefgh" || !slices.Equal(e.Allow, []string{"github.com:443", "api.github.com:443"}) {
		t.Fatalf("Lookup = %+v %v", e, ok)
	}
	if _, ok := x.Lookup("10.0.0.6"); ok {
		t.Fatal("an unknown IP maps to nothing")
	}
}

func TestIndexRefuses(t *testing.T) {
	now := metav1.Now()
	for name, pods := range map[string][]*corev1.Pod{
		"no annotation": {runPod("run-abcdefgh", "10.0.0.5", func(p *corev1.Pod) { p.Annotations = nil })},
		"terminating":   {runPod("run-abcdefgh", "10.0.0.5", func(p *corev1.Pod) { p.DeletionTimestamp = &now })},
		"succeeded":     {runPod("run-abcdefgh", "10.0.0.5", func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded })},
		"failed":        {runPod("run-abcdefgh", "10.0.0.5", func(p *corev1.Pod) { p.Status.Phase = corev1.PodFailed })},
		"not ours":      {runPod("run-abcdefgh", "10.0.0.5", func(p *corev1.Pod) { p.Labels["app.kubernetes.io/managed-by"] = "someone" })},
		"duplicate IP":  {runPod("run-abcdefgh", "10.0.0.5"), runPod("run-bbbbbbbb", "10.0.0.5")},
		"duplicate with a terminating pod": {runPod("run-abcdefgh", "10.0.0.5"),
			runPod("run-bbbbbbbb", "10.0.0.5", func(p *corev1.Pod) { p.DeletionTimestamp = &now })},
	} {
		x := NewIndex()
		for _, p := range pods {
			x.Upsert(p)
		}
		if e, ok := x.Lookup("10.0.0.5"); ok {
			t.Errorf("%s: want no entry, got %+v", name, e)
		}
	}
}

func TestIndexFinishedPodDoesNotShadowANewOne(t *testing.T) {
	x := NewIndex()
	x.Upsert(runPod("run-bbbbbbbb", "10.0.0.5", func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }))
	x.Upsert(runPod("run-abcdefgh", "10.0.0.5"))
	if e, ok := x.Lookup("10.0.0.5"); !ok || e.RunID != "abcdefgh" {
		t.Fatalf("a finished pod keeps its IP in status, but no longer holds it: %+v %v", e, ok)
	}
}

func TestIndexDeleteForgetsTheIP(t *testing.T) {
	x := NewIndex()
	p := runPod("run-abcdefgh", "10.0.0.5")
	x.Upsert(p)
	x.Delete(p.Namespace, p.Name)
	if _, ok := x.Lookup("10.0.0.5"); ok {
		t.Fatal("a deleted pod's IP is gone")
	}
}

func TestIndexUpdateReplacesThePod(t *testing.T) {
	x := NewIndex()
	x.Upsert(runPod("run-abcdefgh", "10.0.0.5"))
	x.Upsert(runPod("run-abcdefgh", "10.0.0.5", func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded }))
	if _, ok := x.Lookup("10.0.0.5"); ok {
		t.Fatal("an update to Succeeded removes the entry")
	}
}
