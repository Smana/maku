// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"fmt"
	"time"

	"github.com/Smana/maku/internal/factory/config"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/runs"
)

// roomState says whether an ended task's room is to be closed now (#56), and when to look again
// if it is only waiting. The room's log takes the closing comment's facts and a revert's end, so
// it closes only once the outbox is drained, the usage settled, the facts posted and the revert
// window passed. It is a decision on live state, not a marker: a restart or a replay reaches the
// same answer, and a room already closed (or never made) is "not due". capped names the wait
// roomCloseCap cut short, empty for an ordinary close.
func (r *Reconciler) roomState(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (due bool, capped string, wait time.Duration, err error) {
	if !v1alpha1.TerminalPhase(t.Status.Phase) || t.Status.RoomRef == "" || len(t.Status.Outbox) > 0 ||
		r.settling(t) || r.revertable(cfg, t) || r.revertPending(t) {
		return false, "", 0, nil
	}
	// CD23: the final spend is a fact the room still has to take, and the close seals the log, so the
	// room stays writable until the instant it is due — a task with no merged pull request has no
	// revert window to keep it open otherwise. Past the instant the pass either posts the fact (and
	// this decision is read again after the post) or withholds it, for runs the meter could not price:
	// then the room closes as it always did, so no room is kept for a figure that cannot come.
	if at, ok := r.spendFinalAt(cfg, t); ok && r.Now().Before(at) {
		return false, "", max(at.Sub(r.Now()), time.Second), nil
	}
	var capLeft time.Duration // no end time, no cap: the room waits as before
	if s := t.Status.PhaseSince; s != nil {
		capLeft = max(s.Add(roomCloseCap).Sub(r.Now()), 0)
	}
	atCap := t.Status.PhaseSince != nil && capLeft == 0
	if r.factsDue(cfg, t) {
		if !atCap {
			return false, "", capLeft, nil // a post the broker waits on schedules no retry: the cap must wake the task
		}
		capped = capFactsDue
	}
	if ref := t.Status.PullRequest; t.Status.Phase == v1alpha1.PhaseDone && ref != nil && mergerMerged(ref) &&
		ref.RevertNumber == 0 && ref.MergedAt != nil {
		if left := ref.MergedAt.Add(r.repo(cfg, t).Merge.RevertWindow.Duration).Sub(r.Now()); left > 0 {
			return false, "", left, nil // a maintainer may still ask for the revert, which posts facts again
		}
	}
	if drillHolds(cfg, t) {
		// HR38: the drill runner reads the runs' claims (their canaries) and the room; it scores
		// the task, then annotates it. Past the cap the room closes unscored. With no end time there
		// is no cap: the hold is looked at every drillWait, never in a tight loop (#520's review).
		if !atCap {
			wait := drillWait
			if t.Status.PhaseSince != nil {
				wait = min(drillWait, max(capLeft, time.Second))
			}
			return false, "", wait, nil
		}
		capped = capDrillUnscored
	}
	if r.verdictHolds(cfg, t) {
		// room.monitor_verdict rides the facts (K2): a sealed log takes none
		return false, "", monitorEvery, nil
	}
	var room roomv1.Room
	err = r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: t.Status.RoomRef}, &room)
	switch {
	case apierrors.IsNotFound(err):
		return false, "", 0, nil
	case err != nil:
		return false, "", 0, fmt.Errorf("room %s: %w", t.Status.RoomRef, err)
	}
	// The factory closes only what it made: a room of the task's name that another owner made
	// is that owner's (rooms.Ensure refuses it the same way).
	if room.DeletionTimestamp != nil || room.Spec.Owner != runs.PrincipalFactory {
		return false, "", 0, nil
	}
	if rooms.HumanDriver(&room) && capped == "" { // closing would delete the person's runs mid-session; leases lapse in 2 or 15 min
		if !atCap {
			return false, "", humanDriverWait, nil
		}
		capped = capHumanDriver
	}
	return true, capped, 0, nil
}

// verdictHolds: an origin task's room stays open while its monitor is due and has no verdict,
// since the verdict reaches RunLore through the room's facts. A task without an origin closes as before.
func (r *Reconciler) verdictHolds(cfg *config.Config, t *v1alpha1.Task) bool {
	return t.Spec.Origin != "" && r.monitorDue(cfg, t) && (t.Status.Monitor == nil || t.Status.Monitor.Verdict == "")
}

// drillHolds: a drill task's room stays open until the drill runner has scored it (HR38), only
// while the drills run and on the drill repository: a label left behind holds nothing.
func drillHolds(cfg *config.Config, t *v1alpha1.Task) bool {
	return t.Labels[v1alpha1.LabelDrillInjection] == "true" && t.Annotations[v1alpha1.AnnotationDrillScored] == "" &&
		cfg.DrillTask(t.Spec.Repository)
}

// drillWait is how often an unscored drill task's room is looked at again.
const drillWait = 5 * time.Minute

// humanDriverWait is how long a room a person drives is left before it is looked at again.
const humanDriverWait = 2 * time.Minute

// roomCloseCap bounds the two waits nothing else ends (#92): a driver whose lease never lapses,
// and facts the broker never takes. Past it, counted from the task's end, the room closes anyway.
const roomCloseCap = 24 * time.Hour

// The waits roomCloseCap ends, as agent_factory_rooms_closed_at_cap_total's reason.
const (
	capHumanDriver = "human_driver"
	capFactsDue    = "facts_due"
	// capDrillUnscored: a drill task the drill runner never scored (HR38).
	capDrillUnscored = "drill_unscored"
)

// closeRoom deletes the room, which the room controller finalizes: it ends the room's runs and
// seals the log, and the seal starts the retention clock. NotFound is a room already closed. The
// UID precondition keeps a same-named room another owner recreated since the read from being
// deleted, and the read is repeated here so a person who took the room is not cut off, unless the
// close is capped. It reports whether this call deleted the room.
func (r *Reconciler) closeRoom(ctx context.Context, t *v1alpha1.Task, capped bool) (bool, error) {
	var room roomv1.Room
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: t.Status.RoomRef}, &room)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("close room %s: %w", t.Status.RoomRef, err)
	}
	if room.DeletionTimestamp != nil || room.Spec.Owner != runs.PrincipalFactory || (rooms.HumanDriver(&room) && !capped) {
		return false, nil
	}
	uid := room.UID
	err = r.Client.Delete(ctx, &room, client.Preconditions{UID: &uid})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("close room %s: %w", room.Name, err)
	}
	return true, nil
}
