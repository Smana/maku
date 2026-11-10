// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"reflect"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
)

// MR13, MR15: the broker admits a sponsored principal to a room by membership only, so the Slack
// relay follows a parent only as a watcher of the parent's own Room. Only a sponsored principal
// with onBehalfOf.slackLinks is added, only as a watcher, only when the parent Room is created,
// never to a child Room, and an existing Room is never widened.
func TestParentRoomAdmitsOnlyConfiguredSponsoredWatcher(t *testing.T) {
	p := coordinationPlan(t, false)
	sponsored := []config.Sponsored{
		{Name: "slack", OnBehalfOf: config.OnBehalfOf{SlackLinks: true}},
		{Name: "ci"},
	}
	want := roomv1.Member{Principal: "system:slack", Role: "watcher"}

	t.Run("a new parent Room", func(t *testing.T) {
		parent := filedParent(p, coordination.ReasonAwaitingConfirmation)
		parent.Status.Phase, parent.Status.RoomRef = "", ""
		g, _ := coordinationRig(t, config.CoordinationPlan, parent)
		g.r.Cfg.Current().API.Sponsored = sponsored
		g.reconcile(t, p.ID, 4)
		var room roomv1.Room
		if err := g.c.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: p.ID}, &room); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(room.Spec.Members, []roomv1.Member{want}) {
			t.Fatalf("members = %+v, want only the slackLinks principal as watcher", room.Spec.Members)
		}
	})

	t.Run("an existing parent Room is never widened", func(t *testing.T) {
		parent := filedParent(p, coordination.ReasonAwaitingConfirmation)
		g, _ := coordinationRig(t, config.CoordinationPlan, parent)
		g.reconcile(t, p.ID, 4) // the Room is created before any watcher is configured
		var before roomv1.Room
		if err := g.c.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: p.ID}, &before); err != nil {
			t.Fatal(err)
		}
		g.r.Cfg.Current().API.Sponsored = sponsored
		g.reconcile(t, p.ID, 2)
		var after roomv1.Room
		if err := g.c.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: p.ID}, &after); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after.Spec, before.Spec) {
			t.Fatalf("the room changed: %+v -> %+v", before.Spec, after.Spec)
		}
	})

	t.Run("no slackLinks principal adds nobody", func(t *testing.T) {
		if got := coordinationWatchers(&config.Config{API: config.API{Sponsored: sponsored[1:]}}); len(got) != 0 {
			t.Fatalf("watchers = %+v", got)
		}
	})
}
