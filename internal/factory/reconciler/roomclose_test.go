// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"slices"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/rooms"
)

func endedTask(phase string) *v1alpha1.Task {
	tk := issueTask("3buqdlot", 7, "x")
	since := metav1.NewTime(now.Add(-time.Hour))
	tk.Status = v1alpha1.TaskStatus{Phase: phase, PhaseSince: &since, RoomRef: "3buqdlot"}
	return tk
}

func roomGone(t *testing.T, g *rig) bool {
	t.Helper()
	var room roomv1.Room
	err := g.c.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: "3buqdlot"}, &room)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return apierrors.IsNotFound(err)
}

func withRoom(t *testing.T, g *rig) {
	t.Helper()
	if err := rooms.Ensure(t.Context(), g.c, "agent-system", "3buqdlot", "public", "Smana/cloud-native-ref"); err != nil {
		t.Fatal(err)
	}
}

// #56: a finished task's room is closed (the Room's deletion seals the log, which starts its
// retention clock), so retention can purge it. Closing again is a no-op.
func TestATerminalTaskClosesItsRoom(t *testing.T) {
	for _, phase := range []string{v1alpha1.PhaseDone, v1alpha1.PhaseStopped, v1alpha1.PhaseClosed, v1alpha1.PhaseRejected} {
		g := newRig(t, endedTask(phase))
		withRoom(t, g)
		g.reconcile(t, "3buqdlot", 1)
		if !roomGone(t, g) {
			t.Fatalf("%s: the room stays open, so retention never purges it", phase)
		}
		g.reconcile(t, "3buqdlot", 2) // restart-safe and idempotent: nothing left to close
	}
}

// The closing comment waits for the usage to settle (#84), and the room outlives it.
func TestTheRoomClosesAfterTheSettleWindow(t *testing.T) {
	tk := endedTask(v1alpha1.PhaseDone)
	since := metav1.NewTime(now)
	tk.Status.PhaseSince = &since
	tk.Status.Runs = []v1alpha1.RunRecord{{ID: rid(0), Role: "implementer", Trigger: "initial"}}
	tk.Status.Outbox = []v1alpha1.Narration{{Key: "end-done", Number: 7, Body: "x"}}
	g := newRig(t, tk)
	withRoom(t, g)
	g.reconcile(t, "3buqdlot", 1)
	if roomGone(t, g) {
		t.Fatal("the room closed while the usage was still settling")
	}
	g.r.Now = func() time.Time { return now.Add(g.r.settleWindow()) }
	g.reconcile(t, "3buqdlot", 3)
	if !roomGone(t, g) {
		t.Fatal("the room stays open after the usage settled and the outbox drained")
	}
}

// An auto-merged task can still be reverted inside the window; its facts follow into the room.
func TestTheRoomOutlivesTheRevertWindow(t *testing.T) {
	tk := endedTask(v1alpha1.PhaseDone)
	merged := metav1.NewTime(now.Add(-time.Hour))
	tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", AutoMerged: true, MergedAt: &merged}
	g := newRig(t, tk)
	withRoom(t, g)
	g.reconcile(t, "3buqdlot", 1)
	if roomGone(t, g) {
		t.Fatal("the room closed inside the revert window")
	}
	g.r.Now = func() time.Time { return merged.Add(g.r.Cfg.Merge.RevertWindow.Duration + time.Minute) }
	g.reconcile(t, "3buqdlot", 1)
	if !roomGone(t, g) {
		t.Fatal("the room stays open past the revert window")
	}
}

// Deleting a room deletes its runs: a person mid-session is not cut off, and the task looks again.
func TestAHumanDrivenRoomIsLeftOpen(t *testing.T) {
	g := newRig(t, endedTask(v1alpha1.PhaseDone))
	withRoom(t, g)
	var room roomv1.Room
	key := types.NamespacedName{Namespace: "agent-system", Name: "3buqdlot"}
	if err := g.c.Get(t.Context(), key, &room); err != nil {
		t.Fatal(err)
	}
	room.Status.Driver = "human:291"
	if err := g.c.Status().Update(t.Context(), &room); err != nil {
		t.Fatal(err)
	}
	res, err := g.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	if err != nil {
		t.Fatal(err)
	}
	if roomGone(t, g) || res.RequeueAfter <= 0 {
		t.Fatalf("a human-driven room is closed or never looked at again: requeue %s", res.RequeueAfter)
	}
	room.Status.Driver = "system:factory" // the lease lapsed
	if err := g.c.Status().Update(t.Context(), &room); err != nil {
		t.Fatal(err)
	}
	g.reconcile(t, "3buqdlot", 1)
	if !roomGone(t, g) {
		t.Fatal("the room stays open after the person let go")
	}
}

// The delete carries the UID it read, so a room recreated under the name is never the one deleted.
func TestClosingARoomIsPreconditionedOnItsUID(t *testing.T) {
	g := newRig(t, endedTask(v1alpha1.PhaseDone))
	withRoom(t, g)
	var seen client.DeleteOptions
	g.r.Client = interceptor.NewClient(g.c.(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, o ...client.DeleteOption) error {
			if _, ok := obj.(*roomv1.Room); ok {
				seen.ApplyOptions(o)
			}
			return cl.Delete(ctx, obj, o...)
		}})
	g.reconcile(t, "3buqdlot", 1)
	if seen.Preconditions == nil || seen.Preconditions.UID == nil {
		t.Fatalf("no UID precondition: %+v", seen.Preconditions)
	}
}

// A room of the task's name the factory did not make is not the factory's to close.
func TestAForeignRoomIsLeftOpen(t *testing.T) {
	g := newRig(t, endedTask(v1alpha1.PhaseDone), &roomv1.Room{ObjectMeta: metav1.ObjectMeta{Name: "3buqdlot", Namespace: "agent-system"},
		Spec: roomv1.RoomSpec{Owner: "human:someone", Driver: "human:someone"}})
	g.reconcile(t, "3buqdlot", 1)
	if roomGone(t, g) {
		t.Fatal("closed a room another owner made")
	}
}

// #92: a driver whose lease never lapses would hold a finished task's room open forever. Past
// the cap the room closes anyway, and the close is counted.
func TestAHumanDrivenRoomClosesAtTheCap(t *testing.T) {
	g := newRig(t, endedTask(v1alpha1.PhaseDone))
	withRoom(t, g)
	var room roomv1.Room
	key := types.NamespacedName{Namespace: "agent-system", Name: "3buqdlot"}
	if err := g.c.Get(t.Context(), key, &room); err != nil {
		t.Fatal(err)
	}
	room.Status.Driver = "human:291"
	if err := g.c.Status().Update(t.Context(), &room); err != nil {
		t.Fatal(err)
	}
	g.reconcile(t, "3buqdlot", 1)
	if roomGone(t, g) {
		t.Fatal("a human-driven room closed before the cap")
	}
	g.r.Now = func() time.Time { return now.Add(-time.Hour + roomCloseCap + time.Minute) } // PhaseSince + cap
	g.reconcile(t, "3buqdlot", 1)
	if !roomGone(t, g) {
		t.Fatal("a human-driven room stays open past the cap")
	}
	if !slices.Contains(g.metrics.recorded, "room_closed_at_cap human_driver") {
		t.Fatalf("the close at the cap is not counted: %v", g.metrics.recorded)
	}
}

// #92: facts the broker never takes keep factsDue true; the task wakes at the cap and closes the room.
func TestARoomWhoseFactsNeverPostClosesAtTheCap(t *testing.T) {
	g := newRig(t, endedTask(v1alpha1.PhaseDone))
	withRoom(t, g)
	g.log.noRoom = true
	res, err := g.r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "agent-system", Name: "3buqdlot"}})
	if err != nil {
		t.Fatal(err)
	}
	if roomGone(t, g) {
		t.Fatal("the room closed with its facts due, before the cap")
	}
	if left := roomCloseCap - time.Hour; res.RequeueAfter != left {
		t.Fatalf("requeue %s, want the cap's %s: nothing else wakes the task", res.RequeueAfter, left)
	}
	g.r.Now = func() time.Time { return now.Add(res.RequeueAfter) }
	g.reconcile(t, "3buqdlot", 1)
	if !roomGone(t, g) {
		t.Fatal("a room whose facts never post stays open past the cap")
	}
	if !slices.Contains(g.metrics.recorded, "room_closed_at_cap facts_due") {
		t.Fatalf("the close at the cap is not counted: %v", g.metrics.recorded)
	}
}

// originVerdictRig: an ended task merged by a person an hour ago, of a class with a 2 h monitor,
// its room open and main's CI pending. origin is spec.origin.
func originVerdictRig(t *testing.T, origin string) (*rig, metav1.Time) {
	t.Helper()
	tk := endedTask(v1alpha1.PhaseDone)
	merged := metav1.NewTime(now.Add(-time.Hour))
	tk.Spec.Origin, tk.Spec.PredictedClass = origin, "alert-rules"
	tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", MergedAt: &merged, MergeCommitSHA: "m1"}
	g := newRig(t, tk)
	g.r.Merger = g.f
	g.r.Cfg.Classes = map[string]config.Class{"alert-rules": {Monitor: &config.Monitor{FollowUpWindow: config.Duration{Duration: 2 * time.Hour}}}}
	g.f.SetPR(forge.PR{Number: 12, State: "MERGED", MergedAt: merged.Time, MergeCommitSHA: "m1"})
	g.f.SetCommitChecks("m1", forge.Check{Name: "Pre-commit checks", State: "PENDING"})
	withRoom(t, g)
	return g, merged
}

// lastFacts is the facts the fake log holds at the highest clientSeq.
func lastFacts(g *rig) envelope.TaskFacts {
	var top int64 = -1
	for seq := range g.r.Rooms.(*fakeLog).facts {
		top = max(top, seq)
	}
	return g.r.Rooms.(*fakeLog).facts[top]
}

// room.monitor_verdict rides the facts (K2): a sealed log takes none, so an origin task's room
// waits for its verdict; a task without an origin closes as before.
func TestAnOriginRoomWaitsForItsVerdict(t *testing.T) {
	for _, origin := range []string{"abcd2345", ""} {
		t.Run("origin "+origin, func(t *testing.T) {
			g, merged := originVerdictRig(t, origin)
			g.reconcile(t, "3buqdlot", 1)
			if gone := roomGone(t, g); gone != (origin == "") {
				t.Fatalf("origin %q: room gone %v before the verdict", origin, gone)
			}
			if origin != "" { // the comment alone guards nothing: the second block runs only for an origin task
				// past the window the monitor decides; the facts carry it before the room closes
				g.r.Now = func() time.Time { return merged.Add(2*time.Hour + searchLag + time.Minute) }
				got := g.reconcile(t, "3buqdlot", 2)
				if got.Status.Monitor.Verdict != "inconclusive" || lastFacts(g).Monitor != "inconclusive" || !roomGone(t, g) {
					t.Fatalf("the verdict must reach the log before the close: %+v, facts %+v", got.Status, lastFacts(g))
				}
			}
		})
	}
}

// K1: a regression decided before the window ends is final, so the room is released at once.
func TestARegressionReleasesAnOriginRoomAtOnce(t *testing.T) {
	g, _ := originVerdictRig(t, "abcd2345")
	g.f.SetCommitChecks("m1", forge.Check{Name: "Pre-commit checks", State: "FAILURE"})
	got := g.reconcile(t, "3buqdlot", 2)
	if got.Status.Monitor == nil || got.Status.Monitor.Verdict != "regression" || got.Status.Monitor.Posted ||
		lastFacts(g).Monitor != "regression" || !roomGone(t, g) {
		t.Fatalf("a regression inside the window must reach the log and release the room: %+v, facts %+v", got.Status.Monitor, lastFacts(g))
	}
}
