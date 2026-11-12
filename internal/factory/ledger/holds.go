// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Holds reports whether today's or yesterday's (UTC, from now) ledger in ns holds a reservation
// under run id: the proof that admission let the run in (R48), which the run-token minter asks of
// a run no Task accounts for (v4 D10, plan PR4). A missing ledger holds nothing.
func Holds(ctx context.Context, c client.Reader, ns string, now time.Time, id string) (bool, error) {
	for _, day := range []time.Time{now, now.Add(-24 * time.Hour)} {
		cm := &corev1.ConfigMap{}
		err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: Prefix + day.UTC().Format(DayLayout)}, cm)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("ledger: read the day ledger: %w", err)
		}
		if _, ok := cm.Data[Reserved+id]; ok {
			return true, nil
		}
	}
	return false, nil
}
