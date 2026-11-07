// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
)

const (
	coordPrimary = "Smana/public-contracts"
	coordOther   = "Smana/public-consumer"
)

// fakeCoordination records what the API hands the service and answers err, else the input back.
type fakeCoordination struct {
	actors   []coordination.Actor
	drafts   []factoryv1.CoordinationPlan
	confirms []coordination.CoordinationConfirmation
	err      error

	recoveries []string
	repoErr    error
}

func (f *fakeCoordination) Draft(_ context.Context, a coordination.Actor, p factoryv1.CoordinationPlan, hash string, pl coordination.PlanningReconciliation) (coordination.CoordinationConfirmation, error) {
	f.actors, f.drafts = append(f.actors, a), append(f.drafts, p)
	return coordination.CoordinationConfirmation{Plan: p, PlanHash: hash, Planning: pl}, f.err
}

func (f *fakeCoordination) RecoveryRepository(context.Context, string) (string, error) {
	return coordOther, f.repoErr
}

func (f *fakeCoordination) Recover(_ context.Context, a coordination.Actor, task, code string, version int32) error {
	f.actors, f.recoveries = append(f.actors, a), append(f.recoveries, fmt.Sprintf("%s %s v%d", task, code, version))
	return f.err
}

func (f *fakeCoordination) Confirm(_ context.Context, a coordination.Actor, in coordination.CoordinationConfirmation) (coordination.CoordinationConfirmation, error) {
	f.actors, f.confirms = append(f.actors, a), append(f.confirms, in)
	return in, f.err
}

// coordAccess is the D4 check by principal and by login.
type coordAccess struct{ principals, logins map[string]bool }

func (a coordAccess) CanWrite(_ context.Context, p authn.Principal, repository string) (bool, error) {
	return a.principals[p.ID+"|"+repository], nil
}

func (a coordAccess) CanWriteLogin(_ context.Context, repository, login string) (bool, error) {
	return a.logins[strings.ToLower(login)+"|"+repository], nil
}

// coordLogins links a ZITADEL sub to a GitHub login.
type coordLogins map[string]string

func (l coordLogins) Login(_ context.Context, sub, _ string) (string, error) { return l[sub], nil }

var (
	coordHuman    = authn.Principal{Kind: envelope.ActorHuman, ID: "human:291", Sub: "291"}
	coordUnlinked = authn.Principal{Kind: envelope.ActorHuman, ID: "human:7", Sub: "7"}
	coordReader   = authn.Principal{Kind: envelope.ActorHuman, ID: "human:8", Sub: "8"}
	coordSlack    = authn.Principal{Kind: envelope.ActorSystem, ID: "system:slack-app", Sponsored: true}
)

func coordServer(mode string) (*Server, *fakeCoordination) {
	f := &fakeCoordination{}
	return &Server{
		Auth: byToken{"human": coordHuman, "unlinked": coordUnlinked, "reader": coordReader, "slack": coordSlack},
		Cfg: &config.Config{Coordination: config.Coordination{Mode: mode},
			Repositories: []config.Repository{{Name: coordPrimary, Maintainers: []string{"Smana"}}, {Name: coordOther, Maintainers: []string{"other"}}},
			API: config.API{Sponsored: []config.Sponsored{{Name: "slack-app", Sponsor: "Smana", Repositories: []string{coordPrimary},
				OnBehalfOf: config.OnBehalfOf{Slack: map[string]string{"T01ABC/U02DEF": "smana"}}}}}},
		Stopped: func(context.Context) bool { return false },
		Access: coordAccess{principals: map[string]bool{"human:291|" + coordPrimary: true, "human:8|" + coordOther: true},
			logins: map[string]bool{"smana|" + coordPrimary: true}},
		Logins:       coordLogins{"291": "Smana", "8": "reader"},
		Coordination: f,
	}, f
}

// coordPlan is the coordination package's example plan, as the broker's planner sends it.
func coordPlan(t *testing.T) factoryv1.CoordinationPlan {
	t.Helper()
	f, err := os.Open("../coordination/testdata/plan/valid.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	p, err := coordination.DecodePlan(f)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func coordDo(s *Server, method, path, tok string, body any) *httptest.ResponseRecorder {
	var b []byte
	if raw, ok := body.(string); ok {
		b = []byte(raw)
	} else {
		b, _ = json.Marshal(body)
	}
	r := httptest.NewRequestWithContext(context.Background(), method, path, bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func draftBody(p factoryv1.CoordinationPlan, extra map[string]any) map[string]any {
	b := map[string]any{"plan": p, "planHash": coordination.PlanHash(p), "planning": map[string]any{"attemptID": "a1", "effectiveRequester": "github:smana"}}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

// MR16: off, or unset, serves no coordination route at all.
func TestCoordinationRoutesAreOffByDefault(t *testing.T) {
	p := coordPlan(t)
	for _, mode := range []string{"", config.CoordinationOff, config.CoordinationPlan} {
		s, f := coordServer(mode)
		draft := coordDo(s, http.MethodPut, "/v1/coordinations/"+p.ID+"/draft", "human", draftBody(p, nil))
		confirm := coordDo(s, http.MethodPost, "/v1/coordinations/"+p.ID+"/confirmations", "human", map[string]any{"plan": p})
		off := mode != config.CoordinationPlan
		if (draft.Code == http.StatusNotFound) != off || (confirm.Code == http.StatusNotFound) != off {
			t.Fatalf("mode %q: draft %d, confirm %d", mode, draft.Code, confirm.Code)
		}
		if off && len(f.drafts)+len(f.confirms) != 0 {
			t.Fatalf("mode %q reached the service", mode)
		}
	}
}

// The draft's actor is the token's: a linked human writing the primary, or a sponsored
// principal's onBehalfOf maintainer writing it. The body never names one.
func TestCoordinationDraftResolvesTheActorFromTheToken(t *testing.T) {
	p := coordPlan(t)
	path := "/v1/coordinations/" + p.ID + "/draft"
	for _, tc := range []struct {
		name, tok string
		body      any
		code      int
		reason    string
		actor     coordination.Actor
	}{
		{name: "a linked human with write", tok: "human", body: draftBody(p, nil), code: http.StatusOK,
			actor: coordination.Actor{Principal: "human:291", LinkedGitHubLogin: "Smana"}},
		{name: "a sponsored principal for its linked maintainer", tok: "slack", body: draftBody(p, map[string]any{"onBehalfOf": "slack:T01ABC/U02DEF"}),
			code: http.StatusOK, actor: coordination.Actor{Principal: "system:slack-app", LinkedGitHubLogin: "smana"}},
		{name: "an unlinked human", tok: "unlinked", body: draftBody(p, nil), code: http.StatusForbidden, reason: "not_linked"},
		{name: "a human without write on the primary", tok: "reader", body: draftBody(p, nil), code: http.StatusForbidden, reason: "repository_permission"},
		{name: "a human naming onBehalfOf", tok: "human", body: draftBody(p, map[string]any{"onBehalfOf": "slack:T01ABC/U02DEF"}),
			code: http.StatusBadRequest, reason: "on_behalf_of_is_sponsored_only"},
		{name: "a sponsored principal for an unlinked user", tok: "slack", body: draftBody(p, map[string]any{"onBehalfOf": "slack:T09/U09"}),
			code: http.StatusForbidden, reason: "not_linked"},
		{name: "another task in the path", tok: "human", body: draftBody(func() factoryv1.CoordinationPlan { q := p; q.ID = "aaaaaaaa"; return q }(), nil),
			code: http.StatusBadRequest, reason: "task_mismatch"},
		{name: "an AgentRun id", tok: "human", body: draftBody(p, map[string]any{"planning": map[string]any{"attemptID": "a1", "agentRunID": "r1"}}),
			code: http.StatusBadRequest, reason: "bad_json"},
		{name: "a confirmation smuggled into a draft", tok: "human", body: draftBody(p, map[string]any{"parent": map[string]any{"planHash": "x"}}),
			code: http.StatusBadRequest, reason: "bad_json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := coordServer(config.CoordinationPlan)
			w := coordDo(s, http.MethodPut, path, tc.tok, tc.body)
			if w.Code != tc.code || (tc.reason != "" && !strings.Contains(w.Body.String(), `"error":"`+tc.reason+`"`)) {
				t.Fatalf("%d %s, want %d %s", w.Code, w.Body, tc.code, tc.reason)
			}
			if tc.code != http.StatusOK {
				if len(f.drafts) != 0 {
					t.Fatal("a refused draft reached the service")
				}
				return
			}
			if len(f.actors) != 1 || f.actors[0] != tc.actor {
				t.Fatalf("actor %+v, want %+v", f.actors, tc.actor)
			}
			var out coordination.CoordinationConfirmation
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.PlanHash != coordination.PlanHash(p) || out.Planning.AttemptID != "a1" {
				t.Fatalf("answer %s (%v)", w.Body, err)
			}
		})
	}
}

// Confirmations are a human's, as their token links them: a body's actor fields are never read.
func TestCoordinationConfirmIsHumanOnlyAndTokenBound(t *testing.T) {
	p := coordPlan(t)
	path := "/v1/coordinations/" + p.ID + "/confirmations"
	h := coordination.PlanHash(p)
	parent := map[string]any{"plan": p, "planHash": h, "parent": map[string]any{"planHash": h, "actor": "mallory", "linkedGitHubLogin": "mallory"},
		"planning": map[string]any{"attemptID": "a1", "effectiveRequester": "github:smana"}}

	s, f := coordServer(config.CoordinationPlan)
	if w := coordDo(s, http.MethodPost, path, "slack", parent); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "human_only") {
		t.Fatalf("sponsored: %d %s", w.Code, w.Body)
	}
	if w := coordDo(s, http.MethodPost, path, "unlinked", parent); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "not_linked") {
		t.Fatalf("unlinked: %d %s", w.Code, w.Body)
	}
	if len(f.confirms) != 0 {
		t.Fatal("a refused confirmation reached the service")
	}
	if w := coordDo(s, http.MethodPost, path, "human", parent); w.Code != http.StatusOK {
		t.Fatalf("human: %d %s", w.Code, w.Body)
	}
	if f.actors[0] != (coordination.Actor{Principal: "human:291", LinkedGitHubLogin: "Smana"}) {
		t.Fatalf("actor %+v", f.actors[0])
	}
}

// The service's bounded reasons map to statuses in one place and name only the reason.
func TestCoordinationRefusalsNameTheirReason(t *testing.T) {
	p := coordPlan(t)
	h := coordination.PlanHash(p)
	body := map[string]any{"plan": p, "planHash": h, "parent": map[string]any{"planHash": h}, "planning": map[string]any{"attemptID": "a1", "effectiveRequester": "x"}}
	for _, tc := range []struct {
		err    error
		code   int
		reason string
	}{
		{fmt.Errorf("%w: no prerequisite port", coordination.ErrPrerequisites), http.StatusConflict, "coordination_prerequisites"},
		{coordination.ErrRepositoryPermission, http.StatusForbidden, "repository_permission"},
		{coordination.ErrParentConflict, http.StatusConflict, "coordination_parent_conflict"},
		{coordination.ErrPlanningBudgetExhausted, http.StatusConflict, "planning_budget_exhausted"},
		{coordination.ErrAccessUnverified, http.StatusServiceUnavailable, "access_unverified"},
		{coordination.ErrParentNotFound, http.StatusNotFound, "coordination_not_found"},
		{fmt.Errorf("etcd said: secret detail"), http.StatusServiceUnavailable, "coordination_unavailable"},
	} {
		s, f := coordServer(config.CoordinationExecute)
		f.err = tc.err
		w := coordDo(s, http.MethodPost, "/v1/coordinations/"+p.ID+"/confirmations", "human", body)
		if w.Code != tc.code || strings.TrimSpace(w.Body.String()) != `{"error":"`+tc.reason+`"}` {
			t.Errorf("%v: %d %s, want %d %s", tc.err, w.Code, w.Body, tc.code, tc.reason)
		}
	}
}

// MR2: the bare-run endpoint refuses a coordination parent's Room before any claim, reservation,
// branch or token, whatever the caller's rights in the Room.
func TestParentRunRequestIsReadOnly(t *testing.T) {
	parent := &factoryv1.Task{ObjectMeta: metav1.ObjectMeta{Name: "3kq7x2ma", Namespace: "agent-system"},
		Spec: factoryv1.TaskSpec{Kind: factoryv1.TaskKindCoordination, Source: factoryv1.Source{Kind: factoryv1.SourceKindCoordination},
			Repository: "Smana/cloud-native-ref", DataClass: "public"}}
	st := &store{}
	c := fake.NewClientBuilder().WithScheme(scheme()).WithObjects(parent, aliceRoom()).Build()
	s := mkServer(t, st, c, false, false, "7f3cq2xz", 5_000_000)
	body := base()
	body["roomRef"] = "3kq7x2ma"
	w := post(s.Handler(), "alice", body)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"error":"coordination_read_only"`) || len(st.created) != 0 {
		t.Fatalf("%d %s, %d runs", w.Code, w.Body, len(st.created))
	}
	var cms corev1.ConfigMapList
	if err := c.List(t.Context(), &cms); err != nil || len(cms.Items) != 0 {
		t.Fatalf("a ledger was written: %d %v", len(cms.Items), err)
	}
}

// MR11: POST /v1/coordinations/{task}/recovery takes {code, planVersion} from a linked human only,
// resolves their login on the repository the proposed action's authority is on, and answers 204;
// refusals are {"error": reason}. Off, it does not exist.
func TestCoordinationRecoveryRoute(t *testing.T) {
	p := coordPlan(t)
	path := "/v1/coordinations/" + p.ID + "/recovery"
	body := map[string]any{"code": "retry_child", "planVersion": 3}
	for _, mode := range []string{"", config.CoordinationOff} {
		s, f := coordServer(mode)
		if w := coordDo(s, http.MethodPost, path, "human", body); w.Code != http.StatusNotFound || len(f.recoveries) != 0 {
			t.Fatalf("mode %q: %d", mode, w.Code)
		}
	}
	for _, tc := range []struct {
		name, tok string
		body      any
		err       error
		repoErr   error
		code      int
		reason    string
		login     string
	}{
		{name: "the child's linked maintainer", tok: "reader", body: body, code: http.StatusNoContent, login: "reader"},
		{name: "a sponsored principal", tok: "slack", body: body, code: http.StatusForbidden, reason: "human_only"},
		{name: "an unlinked human", tok: "unlinked", body: body, code: http.StatusForbidden, reason: "not_linked"},
		{name: "an unknown field", tok: "human", body: map[string]any{"code": "retry_child", "planVersion": 3, "childID": "consumer"},
			code: http.StatusBadRequest, reason: "bad_json"},
		{name: "a stale version", tok: "human", body: body, err: &coordination.PlanError{Code: "stale_plan"}, code: http.StatusConflict, reason: "stale_plan"},
		{name: "nothing proposed", tok: "human", body: body, repoErr: coordination.ErrRecoveryNotProposed, code: http.StatusConflict, reason: "recovery_not_proposed"},
		{name: "no authority", tok: "human", body: body, err: coordination.ErrRepositoryPermission, code: http.StatusForbidden, reason: "repository_permission"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := coordServer(config.CoordinationExecute)
			f.err, f.repoErr = tc.err, tc.repoErr
			w := coordDo(s, http.MethodPost, path, tc.tok, tc.body)
			if w.Code != tc.code {
				t.Fatalf("%d %s, want %d", w.Code, w.Body, tc.code)
			}
			if tc.reason != "" {
				var got map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got["error"] != tc.reason || len(got) != 1 {
					t.Fatalf("body %s, want {\"error\":%q}", w.Body, tc.reason)
				}
			}
			if tc.code == http.StatusNoContent {
				if w.Body.Len() != 0 || len(f.recoveries) != 1 || f.recoveries[0] != p.ID+" retry_child v3" ||
					f.actors[0] != (coordination.Actor{Principal: coordReader.ID, LinkedGitHubLogin: tc.login}) {
					t.Fatalf("recoveries %v actors %+v body %q", f.recoveries, f.actors, w.Body)
				}
			}
		})
	}
}
