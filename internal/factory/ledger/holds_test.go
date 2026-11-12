// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestHolds(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 30, 0, 0, time.UTC)
	c := fake.NewClientBuilder().WithObjects(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: Prefix + "20260928", Namespace: "ns"}, Data: map[string]string{Reserved + "today000": "{}"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: Prefix + "20260927", Namespace: "ns"}, Data: map[string]string{Reserved + "yesterda": "{}"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: Prefix + "20260926", Namespace: "ns"}, Data: map[string]string{Reserved + "twodays2": "{}"}},
	).Build()
	for id, want := range map[string]bool{"today000": true, "yesterda": true, "twodays2": false, "nobody22": false} {
		got, err := Holds(t.Context(), c, "ns", now, id)
		if err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", id, got, err, want)
		}
	}
}
