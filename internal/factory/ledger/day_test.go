// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDayUsage(t *testing.T) {
	res1, _ := json.Marshal(Reservation{
		Principal:   "system:factory",
		Person:      "system:upkeep",
		MaxMicroUSD: 1_500_000,
	})
	res2, _ := json.Marshal(Reservation{
		Principal:   "system:factory",
		Person:      "github:alice",
		MaxMicroUSD: 2_000_000,
	})
	res3, _ := json.Marshal(Reservation{
		Principal:   "system:upkeep",
		MaxMicroUSD: 500_000,
	})
	spentRaw, _ := json.Marshal(map[string]int64{
		"system:factory": 5_000_000,
		"system:upkeep":  3_000_000,
		"github:alice":   1_000_000,
	})
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: Prefix + "20260927", Namespace: "agent-system"},
		Data: map[string]string{
			Spent:           string(spentRaw),
			Reserved + "r1": string(res1),
			Reserved + "r2": string(res2),
			Reserved + "r3": string(res3),
		},
	}

	cases := []struct {
		name         string
		cm           *corev1.ConfigMap
		key          string
		wantSpent    int64
		wantReserved int64
	}{
		{
			name:         "missing configmap returns zero",
			key:          "system:upkeep",
			wantSpent:    0,
			wantReserved: 0,
		},
		{
			name:         "upkeep principal and person reservations sum together",
			cm:           cm,
			key:          "system:upkeep",
			wantSpent:    3_000_000,
			wantReserved: 2_000_000,
		},
		{
			name:         "person key includes spent and person reservation",
			cm:           cm,
			key:          "github:alice",
			wantSpent:    1_000_000,
			wantReserved: 2_000_000,
		},
		{
			name:         "factory principal sums its reservations",
			cm:           cm,
			key:          "system:factory",
			wantSpent:    5_000_000,
			wantReserved: 3_500_000,
		},
		{
			name:         "unknown key returns zero",
			cm:           cm,
			key:          "github:bob",
			wantSpent:    0,
			wantReserved: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(scheme())
			if tc.cm != nil {
				b = b.WithObjects(tc.cm)
			}
			c := b.Build()
			spent, reserved, err := DayUsage(t.Context(), c, "agent-system", now, tc.key)
			if err != nil {
				t.Fatal(err)
			}
			if spent != tc.wantSpent || reserved != tc.wantReserved {
				t.Fatalf("DayUsage(%q) = (%d, %d), want (%d, %d)", tc.key, spent, reserved, tc.wantSpent, tc.wantReserved)
			}
			used, err := DayUsed(t.Context(), c, "agent-system", tc.key, now)
			if err != nil {
				t.Fatal(err)
			}
			if used != spent+reserved {
				t.Fatalf("DayUsed(%q) = %d, want %d", tc.key, used, spent+reserved)
			}
		})
	}
}
