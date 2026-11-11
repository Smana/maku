// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/narrate"
)

// A model provider's refusal (billing, authentication, quota: envelope.ModelRefusal) is a platform
// condition, not the agent's error (#450). The task holds in Queued and runs the same work again
// after a backoff, up to modelHoldLimit consecutive refusals; the next refusal escalates, so a
// provider that never recovers cannot loop the task forever.
const (
	modelHoldLimit      = 6
	modelHoldBackoff    = time.Minute // doubled per consecutive refusal
	modelHoldBackoffMax = 30 * time.Minute
)

// modelHoldDelay is the backoff after the n-th consecutive refusal: 1, 2, 4, 8, 16, then 30 minutes.
func modelHoldDelay(n int32) time.Duration {
	d := modelHoldBackoff << max(n-1, 0)
	if d <= 0 || d > modelHoldBackoffMax {
		return modelHoldBackoffMax
	}
	return d
}

// holdForModel handles the current run's end by a provider refusal (reason is one of
// envelope.EndModel*): counted and logged once per refusal, the task held with its backoff and the
// room and issue told; past modelHoldLimit it escalates with the refusal as its reason.
func (r *Reconciler) holdForModel(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, reason string) error {
	cause, role := envelope.ModelRefusalCause(reason), current(t).Role
	record(ctx, func(ctx context.Context) { r.Metrics.ModelRefused(ctx, cause, role) })
	r.log().Warn("the model provider refused the run: the task is held, not escalated", "task.id", t.Name,
		"run.id", current(t).ID, "role", role, "cause", cause, "holds", t.Status.ModelHolds+1)
	if t.Status.ModelHolds >= modelHoldLimit {
		return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, reason)
	}
	t.Status.ModelHolds++
	delay := modelHoldDelay(t.Status.ModelHolds)
	until := metav1.NewTime(r.Now().Add(delay))
	t.Status.ModelHoldUntil = &until
	if role == "reviewer" {
		r.requestVerifier(t, role)
	} else {
		t.Status.NextTrigger = "resume"
		r.to(t, v1alpha1.PhaseQueued, reason)
	}
	t.Status.Reason = reason
	narrateLater(t, narrate.ModelHeld(t, current(t).ID, reason, delay, int(t.Status.ModelHolds), modelHoldLimit))
	return nil
}

// modelHeld reports whether the task's backoff after a provider refusal is still running.
func (r *Reconciler) modelHeld(t *v1alpha1.Task) bool {
	return t.Status.ModelHoldUntil != nil && r.Now().Before(t.Status.ModelHoldUntil.Time)
}

// clearModelHold ends the count of consecutive refusals: a run ended another way.
func clearModelHold(t *v1alpha1.Task) {
	t.Status.ModelHolds, t.Status.ModelHoldUntil = 0, nil
}
