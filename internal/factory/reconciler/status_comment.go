// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/narrate"
)

// statusCommentDue reports whether an issue task has narration to represent, or a recorded status
// to repair, without making a GitHub call on an already-current reconcile.
func (r *Reconciler) statusCommentDue(cfg *config.Config, t *v1alpha1.Task) bool {
	comment := t.Status.StatusComment
	if comment != nil && comment.Inactive {
		return false
	}
	if t.Spec.Issue == 0 {
		return false
	}
	if comment == nil && len(t.Status.Outbox) == 0 && len(t.Status.Narrated) == 0 {
		return false
	}
	return comment == nil || comment.ID == 0 || comment.Body != narrate.Status(t, cfg.RoomsURL, r.repo(cfg, t).Merge.ApproveToMerge)
}

// syncStatusComment records the remote identity and body in t.Status; Reconcile's second status
// update persists them alongside the outbox and facts results.
func (r *Reconciler) syncStatusComment(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	if comment := t.Status.StatusComment; comment != nil && comment.Inactive {
		return nil
	}
	return r.narrator(ctx, cfg).SyncStatus(ctx, t, cfg.RoomsURL, r.repo(cfg, t).Merge.ApproveToMerge)
}
