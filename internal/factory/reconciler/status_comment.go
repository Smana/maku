// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/narrate"
)

// statusCommentDue reports whether an issue task has narration to represent, or a recorded status
// to repair, without making a GitHub call on an already-current reconcile.
func (r *Reconciler) statusCommentDue(t *v1alpha1.Task) bool {
	if t.Spec.Issue == 0 {
		return false
	}
	comment := t.Status.StatusComment
	if comment == nil && len(t.Status.Outbox) == 0 && len(t.Status.Narrated) == 0 {
		return false
	}
	return comment == nil || comment.ID == 0 || comment.Body != narrate.Status(t, r.Cfg.RoomsURL, r.repo(t).Merge.ApproveToMerge)
}

// syncStatusComment records the remote identity and body in t.Status; Reconcile's second status
// update persists them alongside the outbox and facts results.
func (r *Reconciler) syncStatusComment(ctx context.Context, t *v1alpha1.Task) error {
	return r.narrator(ctx).SyncStatus(ctx, t, r.Cfg.RoomsURL, r.repo(t).Merge.ApproveToMerge)
}
