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
