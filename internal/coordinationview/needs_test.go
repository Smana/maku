// SPDX-License-Identifier: Apache-2.0

package coordinationview

import (
	"reflect"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/summary"
)

func coordIn(v *View, primaryWrite bool, childWrite map[string]bool, roomScoped bool) summary.NeedsIn {
	st := map[string]summary.GitHubStanding{}
	for repo, w := range childWrite {
		st[repo] = summary.GitHubStanding{Login: "alice", MayWrite: w}
	}
	return summary.NeedsIn{Room: "parent-1", URL: "https://rooms.example/r/parent-1", FactsRead: true,
		GitHub: summary.GitHubStanding{Login: "alice", MayWrite: primaryWrite}, Caller: summary.Caller{WebUI: true, RoomScoped: roomScoped},
		Coordination: NeedsOf(*v), CoordinationStanding: st}
}

func ids(items []summary.Need) []string {
	out := []string{}
	for _, n := range items {
		out = append(out, n.Kind+":"+n.ID+":"+n.Action.Kind)
	}
	return out
}

func TestCoordinationNeeds(t *testing.T) {
	writeBoth := map[string]bool{"acme/api": true, "acme/web": true}
	t.Run("an unconfirmed parent is one plan confirmation for a primary writer", func(t *testing.T) {
		v := family(false)
		got := ids(summary.Items(coordIn(&v, true, writeBoth, false)))
		if !reflect.DeepEqual(got, []string{"plan_confirmation:parent-1/parent/v2:decide"}) {
			t.Fatalf("%v", got)
		}
		n := summary.Items(coordIn(&v, true, writeBoth, false))[0]
		if n.URL != "https://rooms.example/r/parent-1#coordination" || n.Room != "parent-1" {
			t.Fatalf("%+v", n)
		}
	})
	t.Run("a reader without the primary's write gets no item", func(t *testing.T) {
		v := family(false)
		if got := summary.Items(coordIn(&v, false, nil, false)); len(got) != 0 {
			t.Fatalf("%v", ids(got))
		}
	})
	t.Run("a sponsored room read gets the link, never the decision", func(t *testing.T) {
		v := family(false)
		got := summary.Items(coordIn(&v, false, nil, true))
		if len(got) != 1 || got[0].Action.Label != "open the parent room page to confirm" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a missing child authorization is that child's writer's", func(t *testing.T) {
		v := family(true)
		v.ChildAuthorizations = v.ChildAuthorizations[:1]
		got := ids(summary.Items(coordIn(&v, true, map[string]bool{"acme/api": true, "acme/web": true}, false)))
		if !reflect.DeepEqual(got, []string{"plan_confirmation:parent-1/consumer/v2:decide"}) {
			t.Fatalf("%v", got)
		}
		if got := summary.Items(coordIn(&v, true, map[string]bool{"acme/api": true}, false)); len(got) != 0 {
			t.Fatalf("a writer of the primary only got %v", ids(got))
		}
	})
	t.Run("one recovery, and nothing while waiting on an artifact or integration", func(t *testing.T) {
		v := family(true)
		if got := summary.Items(coordIn(&v, true, writeBoth, false)); len(got) != 0 {
			t.Fatalf("a confirmed, waiting family needs nothing: %v", ids(got))
		}
		v.Recovery = &v1alpha1.RecoveryProposal{Code: "retry_integration", PlanVersion: 2}
		got := ids(summary.Items(coordIn(&v, true, writeBoth, false)))
		if !reflect.DeepEqual(got, []string{"recovery:parent-1/retry_integration/v2:retry"}) {
			t.Fatalf("%v", got)
		}
	})
	t.Run("a restricted child is never an item", func(t *testing.T) {
		v := family(true)
		v.Plan.Children, v.Children = v.Plan.Children[:1], v.Children[:1]
		v.ChildAuthorizations = nil
		v.RestrictedChildren = 1
		got := ids(summary.Items(coordIn(&v, true, writeBoth, false)))
		if !reflect.DeepEqual(got, []string{"plan_confirmation:parent-1/contract/v2:decide"}) {
			t.Fatalf("%v", got)
		}
	})
	t.Run("ordered with the room's other needs by the one order", func(t *testing.T) {
		v := family(false)
		in := coordIn(&v, true, writeBoth, false)
		in.Approvals = []summary.Approval{{ID: "a1", What: "push"}}
		in.Caller.CanDecide = true
		got := ids(summary.Ordered(summary.Items(in)))
		if !reflect.DeepEqual(got, []string{"plan_confirmation:parent-1/parent/v2:decide", "approval:a1:decide"}) {
			t.Fatalf("%v", got)
		}
	})
}
