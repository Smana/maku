// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/runrequest"
)

// tokenAuth is headerAuth with the caller's access token, tok-<user>, as the verifier keeps it.
type tokenAuth struct{ headerAuth }

func (a tokenAuth) Authenticate(r *http.Request) (authn.Principal, error) {
	p, err := a.headerAuth.Authenticate(r)
	if err == nil {
		p.AccessToken = "tok-" + p.Sub
	}
	return p, err
}

// fakeGateway is the factory's coordination routes: what each call carried, and a refusal to give.
type fakeGateway struct {
	calls  []string
	refuse error
}

func (g *fakeGateway) CoordinationDraft(_ context.Context, token, parent string, in coordination.CoordinationConfirmation, onBehalfOf string) (coordination.CoordinationConfirmation, error) {
	g.calls = append(g.calls, "draft "+token+" "+parent+" "+onBehalfOf)
	return in, g.refuse
}

func (g *fakeGateway) CoordinationConfirm(_ context.Context, token, parent string, in coordination.CoordinationConfirmation) (coordination.CoordinationConfirmation, error) {
	kind := "parent"
	if in.Child != nil {
		kind = "child:" + in.Child.ChildID
	}
	g.calls = append(g.calls, "confirm "+token+" "+parent+" "+kind)
	return in, g.refuse
}

func writesOn(g *fakeGateway) option {
	return func(s *Server, h *fanout.Hub, v *hubView) {
		coordOn(&fakeCoordinations{views: map[string]coordination.CoordinationView{roomID: coordFamily()}}, coordWorld())(s, h, v)
		s.Humans = tokenAuth{}
		s.CoordinationWrites = g
	}
}

func sendJSON(t *testing.T, e env, method, path string, h http.Header, body any) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	if s, ok := body.(string); ok {
		raw = []byte(s)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, e.ts.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = h.Clone()
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func confirmation(child string) coordination.CoordinationConfirmation {
	c := coordination.CoordinationConfirmation{Plan: v1alpha1.CoordinationPlan{ID: roomID}, PlanHash: "0123456789abcdef"}
	if child == "" {
		c.Parent = &coordination.ParentConfirmation{PlanHash: c.PlanHash}
	} else {
		c.Child = &coordination.ChildAuthorization{ChildID: child}
	}
	return c
}

func withClient(h http.Header, client string) http.Header {
	h = h.Clone()
	h.Set("X-Test-Client", client)
	return h
}

func TestCoordinationDraftForwardsTheCallersToken(t *testing.T) {
	g := &fakeGateway{}
	e := setup(t, writesOn(g))
	path := "/api/coordinations/" + roomID + "/draft"
	draft := confirmation("")
	draft.Parent = nil
	// A draft names no confirmation: one that does is refused before the factory is asked.
	if code, _ := sendJSON(t, e, http.MethodPut, path, as("alice", false), confirmation("")); code != http.StatusBadRequest || len(g.calls) != 0 {
		t.Fatalf("a draft carried a confirmation: %d %v", code, g.calls)
	}
	if code, body := sendJSON(t, e, http.MethodPut, path, withClient(as("alice", false), "maku-cli"), draft); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if len(g.calls) != 1 || g.calls[0] != "draft tok-alice "+roomID+" " {
		t.Fatalf("%v", g.calls)
	}
	// A field beyond plan, planHash and planning is refused before the factory is asked.
	if code, _ := sendJSON(t, e, http.MethodPut, path, as("alice", false), `{"plan":{},"planHash":"h","planning":{},"onBehalfOf":"U1"}`); code != http.StatusBadRequest || len(g.calls) != 1 {
		t.Fatalf("an onBehalfOf from a human was forwarded: %d %v", code, g.calls)
	}
	// A reader the room's gate refuses never reaches the factory.
	if code, _ := sendJSON(t, e, http.MethodPut, path, as("carol", false), draft); code != http.StatusNotFound || len(g.calls) != 1 {
		t.Fatalf("%d %v", code, g.calls)
	}
}

func TestCoordinationConfirmIsTheWebUIsAlone(t *testing.T) {
	g := &fakeGateway{}
	e := setup(t, writesOn(g))
	path := "/api/coordinations/" + roomID + "/confirmations"
	for _, client := range []string{"maku-cli", "slack-app", "mcp"} {
		if code, body := sendJSON(t, e, http.MethodPost, path, withClient(as("alice", false), client), confirmation("")); code != http.StatusForbidden || !strings.Contains(body, "web_only") {
			t.Fatalf("%s: %d %s", client, code, body)
		}
	}
	if len(g.calls) != 0 {
		t.Fatalf("a non-web client reached the factory: %v", g.calls)
	}
	if code, body := sendJSON(t, e, http.MethodPost, path, as("alice", false), confirmation("consumer")); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if g.calls[0] != "confirm tok-alice "+roomID+" child:consumer" {
		t.Fatalf("%v", g.calls)
	}
	// Another task's plan is refused here, whatever the path says.
	other := confirmation("")
	other.Plan.ID = "zzzzzzzz"
	if code, _ := sendJSON(t, e, http.MethodPost, path, as("alice", false), other); code != http.StatusBadRequest || len(g.calls) != 1 {
		t.Fatalf("%d %v", code, g.calls)
	}
}

func TestCoordinationConfirmationStalePlanRefreshesWithoutWrite(t *testing.T) {
	g := &fakeGateway{refuse: runrequest.ErrRefused{Status: http.StatusConflict, Reason: "stale_plan"}}
	e := setup(t, writesOn(g))
	code, body := sendJSON(t, e, http.MethodPost, "/api/coordinations/"+roomID+"/confirmations", as("alice", false), confirmation(""))
	if code != http.StatusConflict || strings.TrimSpace(body) != `{"error":"stale_plan"}` {
		t.Fatalf("%d %s", code, body)
	}
}

func TestCoordinationWritesAbsentWhileOff(t *testing.T) {
	e := setup(t, coordWorld().option())
	if code, _ := sendJSON(t, e, http.MethodPost, "/api/coordinations/"+roomID+"/confirmations", as("alice", false), confirmation("")); code == http.StatusOK {
		t.Fatal("a confirmation route while off")
	}
}

// coordination_draft is the Slack App's door to a parent's draft: listed only beside coordination_plan
// and the factory's routes, forwarded with the App's token and its user, never a confirmation.
func TestCoordinationDraftToolIsSponsoredAndDraftOnly(t *testing.T) {
	trips := &tripwires{}
	g := &fakeGateway{}
	e := setup(t, mcpOn, withSponsored(sponsoredP), withCoordination(t, "plan", trips), func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.CoordinationWrites = g
	})
	args := map[string]any{"plan": map[string]any{"id": "p4zt6wce"}, "planHash": strings.Repeat("a", 64), "planning": map[string]any{},
		"on_behalf_of": "slack:T01ABC/U04DEV"}
	got := call(t, e, bearer("slack-app"), mcpwire.CoordinationDraft, args)
	if got.isError || len(g.calls) != 1 || !strings.HasPrefix(g.calls[0], "draft ") || !strings.HasSuffix(g.calls[0], " p4zt6wce slack:T01ABC/U04DEV") {
		t.Fatalf("%+v %v", got, g.calls)
	}
	args["parent"] = map[string]any{"planHash": "x"}
	if got := call(t, e, bearer("slack-app"), mcpwire.CoordinationDraft, args); !got.isError || len(g.calls) != 1 {
		t.Fatalf("a confirmation rode a draft: %+v", got)
	}
	if _, out := rpc(t, e, viaRoomctl("dev"), "tools/call", map[string]any{"name": mcpwire.CoordinationDraft, "arguments": args}); rpcCode(out) != -32602 {
		t.Fatalf("a human was served the sponsored draft tool: %v", out)
	}
	if trips.touched() != 0 {
		t.Fatal("a draft touched a task, run or room")
	}
}
