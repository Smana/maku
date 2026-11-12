// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	toolscache "k8s.io/client-go/tools/cache"

	"github.com/Smana/maku/internal/egressproxy"
	"github.com/Smana/maku/internal/runctl"
)

func TestTunnelCaps(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	all, per, err := tunnelCaps(env(nil))
	if err != nil || all != 1024 || per != 64 {
		t.Fatalf("defaults: %d %d %v", all, per, err)
	}
	all, per, err = tunnelCaps(env(map[string]string{"MAX_TUNNELS": "10", "MAX_TUNNELS_PER_CLIENT": "2"}))
	if err != nil || all != 10 || per != 2 {
		t.Fatalf("set: %d %d %v", all, per, err)
	}
	for _, bad := range []string{"0", "-1", "x"} {
		if _, _, err := tunnelCaps(env(map[string]string{"MAX_TUNNELS_PER_CLIENT": bad})); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestPodIndexHandlerForgetsATombstonedPod(t *testing.T) {
	x := egressproxy.NewIndex()
	h := podIndexHandler(x)
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "run-abcdefgh", Namespace: "agents",
		Labels:      map[string]string{runctl.LabelManagedBy: "run-controller", runctl.LabelRunID: "abcdefgh"},
		Annotations: map[string]string{runctl.EgressAnnotation: "github.com:443"}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.5"}}
	h.OnAdd(p, false)
	if _, ok := x.Lookup("10.0.0.5"); !ok {
		t.Fatal("an added pod is indexed")
	}
	h.OnDelete(toolscache.DeletedFinalStateUnknown{Key: "agents/run-abcdefgh", Obj: p})
	if _, ok := x.Lookup("10.0.0.5"); ok {
		t.Fatal("a tombstone deletes the pod")
	}
}
