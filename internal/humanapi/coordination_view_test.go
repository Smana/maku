// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/coordinationview"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/summary"
)

const coordRepoB = "acme/consumer"

// fakeCoordinations is the core's unfiltered views by parent task.
type fakeCoordinations struct {
	views map[string]coordination.CoordinationView
	err   error
	reads int
}

func (f *fakeCoordinations) View(_ context.Context, task string) (coordination.CoordinationView, error) {
	f.reads++
	if f.err != nil {
		return coordination.CoordinationView{}, f.err
	}
	v, ok := f.views[task]
	if !ok {
		return coordination.CoordinationView{}, fmt.Errorf("task %s: %w", task, coordinationview.ErrNoCoordination)
	}
	return v, nil
}

// coordFamily is the test room's family: contract in the room's own repository, consumer in
// coordRepoB, every consumer field marked CMARK.
func coordFamily() coordination.CoordinationView {
	return coordination.CoordinationView{
		ParentTask: roomID, Phase: "Coordinating", Reason: "children_running", PlanHash: "0123456789abcdef",
		Plan: v1alpha1.CoordinationPlan{ID: roomID, Version: 1, Primary: v1alpha1.CoordinationPrimary{Repository: "Smana/maku"},
			Children: []v1alpha1.CoordinationChild{
				{ID: "contract", Repository: "Smana/maku", Roles: []string{"implementer", "reviewer"}},
				{ID: "consumer", Repository: coordRepoB, Roles: []string{"implementer", "reviewer"}, DependsOn: []string{"contract"}, Rationale: "CMARK"},
			},
			Integration: v1alpha1.IntegrationPlan{Role: "triager"},
			Budget:      v1alpha1.CoordinationPlanBudget{TotalMicroUSD: 9_000_000}},
		Children: []coordination.CoordinationChildView{
			{ID: "contract", Repository: "Smana/maku", Task: "aaaabbbb", Room: "aaaabbbb", Phase: "Merged"},
			{ID: "consumer", Repository: coordRepoB, Task: "CMARK-task", Room: "ccccdddd", Phase: "Running", Reason: "CMARK"},
		},
		Budget: coordination.CoordinationBudgetView{TotalMicroUSD: 9_000_000, ChildrenSettledMicroUSD: 1_000_000},
	}
}

func coordOn(f *fakeCoordinations, w *gitHubWorld) option {
	return func(s *Server, _ *fanout.Hub, _ *hubView) {
		w.gate(s)
		s.CoordinationViews = f
	}
}

func coordWorld() *gitHubWorld {
	return world(map[string][]string{"Smana/maku": {"alice", "admin", "bob"}, coordRepoB: {"alice"}}, "carol")
}

func as(user string, admin bool) http.Header {
	h := http.Header{"X-Test-User": {user}}
	if admin {
		h.Set("X-Test-Admin", "1")
	}
	return h
}

func TestCoordinationRouteUsesPrimaryGateThenFilters(t *testing.T) {
	f := &fakeCoordinations{views: map[string]coordination.CoordinationView{roomID: coordFamily()}}
	e := setup(t, coordOn(f, coordWorld()))
	path := "/api/coordinations/" + roomID

	t.Run("a reader of both repositories sees the family", func(t *testing.T) {
		code, body := get(t, e, path, as("alice", false))
		var v coordinationview.View
		if code != http.StatusOK || json.Unmarshal([]byte(body), &v) != nil {
			t.Fatalf("%d %s", code, body)
		}
		if v.RestrictedChildren != 0 || len(v.Children) != 2 {
			t.Fatalf("%+v", v)
		}
	})
	t.Run("an admin without the child's read gets no widening", func(t *testing.T) {
		code, body := get(t, e, path, as("admin", true))
		if code != http.StatusOK || !strings.Contains(body, `"restrictedChildren":1`) || strings.Contains(body, "CMARK") || strings.Contains(body, coordRepoB) {
			t.Fatalf("%d %s", code, body)
		}
	})
	t.Run("an unlinked non-admin is refused as the room is", func(t *testing.T) {
		if code, body := get(t, e, path, as("carol", false)); code != http.StatusNotFound || strings.Contains(body, "CMARK") {
			t.Fatalf("%d %s", code, body)
		}
	})
	t.Run("a room that is no coordination parent is 404", func(t *testing.T) {
		delete(f.views, roomID)
		defer func() { f.views[roomID] = coordFamily() }()
		if code, _ := get(t, e, path, as("alice", false)); code != http.StatusNotFound {
			t.Fatalf("code %d", code)
		}
	})
	t.Run("an unreadable core view is 503", func(t *testing.T) {
		f.err = coordinationview.ErrCoordinationUnavailable
		defer func() { f.err = nil }()
		if code, _ := get(t, e, path, as("alice", false)); code != http.StatusServiceUnavailable {
			t.Fatalf("code %d", code)
		}
	})
}

func TestCoordinationRouteAbsentWhileOff(t *testing.T) {
	e := setup(t, coordWorld().option())
	if code, _ := get(t, e, "/api/coordinations/"+roomID, as("alice", false)); code != http.StatusNotFound {
		t.Fatalf("code %d", code)
	}
}

func TestRoomSummaryCoordinationAttachesFilteredView(t *testing.T) {
	f := &fakeCoordinations{views: map[string]coordination.CoordinationView{roomID: coordFamily()}}
	e := setup(t, coordOn(f, coordWorld()))
	code, body := get(t, e, "/api/rooms/"+roomID+"/summary", as("bob", false))
	var s summary.Summary
	if code != http.StatusOK || json.Unmarshal([]byte(body), &s) != nil {
		t.Fatalf("%d %s", code, body)
	}
	if v, ok := coordinationview.Of(s); !ok || v.RestrictedChildren != 1 || s.Flow == nil || s.Flow.Coordination == nil {
		t.Fatalf("coordination not attached: %s", body)
	}
	if strings.Contains(body, "CMARK") || strings.Contains(body, coordRepoB) {
		t.Fatalf("the withheld child reached the summary: %s", body)
	}

	// A failing core read leaves the summary served, without a family.
	f.err = errors.Join(coordinationview.ErrCoordinationUnavailable, errors.New("etcd"))
	code, body = get(t, e, "/api/rooms/"+roomID+"/summary", as("bob", false))
	if code != http.StatusOK || strings.Contains(body, `"coordination"`) {
		t.Fatalf("%d %s", code, body)
	}
}

func TestSponsoredRepositoriesAreOnlyAnUpperBound(t *testing.T) {
	// A sponsored viewer is bounded by its configured repositories, and nothing is resolved on
	// GitHub for it.
	f := &fakeCoordinations{views: map[string]coordination.CoordinationView{roomID: coordFamily()}}
	w := coordWorld()
	e := setup(t, coordOn(f, w))
	e.srv.SponsoredRepositories = map[string]map[string]bool{"system:slack-app": {"Smana/maku": true, coordRepoB: true}}
	links := w.links
	v := e.srv.filterCoordination(t.Context(), coordFamily(), sponsoredPrincipal("system:slack-app"))
	if v.RestrictedChildren != 0 || w.links != links {
		t.Fatalf("restricted %d, links %d -> %d", v.RestrictedChildren, links, w.links)
	}
	if v := e.srv.filterCoordination(t.Context(), coordFamily(), sponsoredPrincipal("system:other")); v.RestrictedChildren != 2 {
		t.Fatalf("an unconfigured sponsored principal read %d children", 2-v.RestrictedChildren)
	}
}

func sponsoredPrincipal(id string) authn.Principal {
	return authn.Principal{Kind: "system", ID: id, Sponsored: true}
}

func checkoutFamily() coordination.CoordinationView {
	v := coordFamily()
	v.Children[0].IssueURL, v.Children[0].PRURL = "https://github.com/Smana/maku/issues/1", "https://github.com/Smana/maku/pull/2"
	v.Children[0].MergeCommitSHA = strings.Repeat("a", 40)
	v.Children[1].Task, v.Children[1].Reason = "ccccdddd", ""
	v.Children[1].IssueURL, v.Children[1].PRURL = "https://github.com/acme/consumer/issues/3", "https://github.com/acme/consumer/pull/4"
	v.Children[1].HeadSHA = strings.Repeat("b", 40)
	v.Plan.Children[1].Rationale = ""
	return v
}

func TestCoordinationCheckoutRoute(t *testing.T) {
	f := &fakeCoordinations{views: map[string]coordination.CoordinationView{roomID: checkoutFamily()}}
	e := setup(t, coordOn(f, coordWorld()))
	path := "/api/coordinations/" + roomID + "/checkout"

	code, body := get(t, e, path, as("alice", false))
	var d coordinationview.CheckoutDocument
	if code != http.StatusOK || json.Unmarshal([]byte(body), &d) != nil || d.APIVersion != coordinationview.CheckoutAPIVersion || len(d.Children) != 2 {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := get(t, e, path, as("bob", false)); code != http.StatusNotFound || strings.Contains(body, coordRepoB) || strings.Contains(body, "contract") {
		t.Fatalf("a reader of one child: %d %s", code, body)
	}
	v := checkoutFamily()
	v.Children[1].HeadSHA = ""
	f.views[roomID] = v
	if code, _ := get(t, e, path, as("alice", false)); code != http.StatusConflict {
		t.Fatalf("an unready family: %d", code)
	}
	off := setup(t, coordWorld().option())
	if code, _ := get(t, off, path, as("alice", false)); code != http.StatusNotFound {
		t.Fatalf("off: %d", code)
	}
}

// The route counts what it served by a closed outcome, never the repository or the viewer.
func TestCoordinationRouteCountsOutcomes(t *testing.T) {
	f := &fakeCoordinations{views: map[string]coordination.CoordinationView{roomID: checkoutFamily()}}
	e := setup(t, coordOn(f, coordWorld()))
	get(t, e, "/api/coordinations/"+roomID, as("alice", false))
	get(t, e, "/api/coordinations/"+roomID, as("bob", false))
	get(t, e, "/api/coordinations/"+roomID, as("carol", false))
	get(t, e, "/api/coordinations/"+roomID+"/checkout", as("bob", false))
	body := metricsBody(t, e)
	for _, want := range []string{
		`maku_rooms_coordination_views_total{outcome="ok"} 1`,
		`maku_rooms_coordination_views_total{outcome="restricted"} 1`,
		`maku_rooms_coordination_views_total{outcome="not_found"} 1`,
		`maku_rooms_coordination_checkout_manifests_total{outcome="restricted"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(body, coordRepoB) || strings.Contains(body, "alice") {
		t.Fatal("a repository or a viewer reached a label")
	}
}
