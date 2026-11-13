// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Held is run id's reservation in today's or yesterday's (UTC, from now) ledger in ns: the proof
// that admission let the run in (R48), with the repository and role admission granted it, which
// the run-token minter trusts over the run's claim for a run no Task accounts for (v4 D10, plan
// PR4). ok is false when neither ledger holds one; a missing ledger holds nothing.
func Held(ctx context.Context, c client.Reader, ns string, now time.Time, id string) (r Reservation, ok bool, err error) {
	for _, day := range []time.Time{now, now.Add(-24 * time.Hour)} {
		cm := &corev1.ConfigMap{}
		err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: Prefix + day.UTC().Format(DayLayout)}, cm)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return Reservation{}, false, fmt.Errorf("ledger: read the day ledger: %w", err)
		}
		raw, found := cm.Data[Reserved+id]
		if !found {
			continue
		}
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return Reservation{}, false, fmt.Errorf("ledger %s reservation %s: %w", cm.Name, id, err)
		}
		return r, true, nil
	}
	return Reservation{}, false, nil
}
