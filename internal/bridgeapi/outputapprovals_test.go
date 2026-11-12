// SPDX-License-Identifier: Apache-2.0

package bridgeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/wire"
)

// memOutputApprovals is the store's digest-bound approvals: one row per output.
type memOutputApprovals struct {
	mu     sync.Mutex
	rows   map[string]store.Approval
	drafts []envelope.Draft
}

func (m *memOutputApprovals) RequestOutputApproval(_ context.Context, a store.Approval, d envelope.Draft) (store.Approval, envelope.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.RunID == a.RunID && r.BindingOutput == a.BindingOutput {
			if r.BindingDigest != a.BindingDigest || r.BindingBranch != a.BindingBranch {
				return store.Approval{}, envelope.Event{}, store.ErrBindingConflict
			}
			return r, envelope.Event{}, nil
		}
	}
	a.State = store.ApprovalPending
	m.rows[a.ID] = a
	m.drafts = append(m.drafts, d)
	return a, envelope.Event{Seq: 1}, nil
}

func (m *memOutputApprovals) CloseOutputApproval(_ context.Context, id, state string) (store.Approval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.rows[id]
	if !ok || !a.DigestBound() {
		return store.Approval{}, store.ErrNoApproval
	}
	if a.State == store.ApprovalPending {
		a.State, a.DecidedBy = state, "system:room-broker"
		m.rows[id] = a
	}
	return a, nil
}

func (m *memOutputApprovals) Approval(_ context.Context, id string) (store.Approval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.rows[id]
	if !ok {
		return store.Approval{}, store.ErrNoApproval
	}
	return a, nil
}

func (m *memOutputApprovals) Prompters(context.Context, string, string) ([]string, error) {
	return []string{"human:own"}, nil
}

const (
	approvalDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	approvalOutput = "0123456789abcdef"
)

func outputApprovalBody(mut func(map[string]any)) map[string]any {
	b := map[string]any{"runId": runA, "output": approvalOutput, "digest": approvalDigest, "branch": "agent/t-1",
		"ttlSeconds": 86400, "action": map[string]any{"output": approvalOutput, "digest": approvalDigest, "protected": []string{"go.mod"}}}
	if mut != nil {
		mut(b)
	}
	return b
}

// HR28: the factory, and only the factory, requests a digest-bound approval, supersedes it, and
// reads it back: state, binding and decider.
func TestOutputApprovalRoutes(t *testing.T) {
	s, _, w := outputServer(t)
	branched := agentRun(runA, room, "Succeeded")
	branched.Object["spec"].(map[string]any)["branch"] = "agent/t-1"
	w.Upsert(t.Context(), branched)
	m := &memOutputApprovals{rows: map[string]store.Approval{}}
	s.OutputApprovals = m
	h := s.Routes()
	rec := call(t, h, http.MethodPost, "/v1/rooms/"+room+"/approvals", "sys:"+factory, outputApprovalBody(nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var got struct {
		ApprovalID string    `json:"approvalId"`
		State      string    `json:"state"`
		ExpiresAt  time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.State != store.ApprovalPending ||
		!approvalULIDRE.MatchString(got.ApprovalID) || time.Until(got.ExpiresAt) < 23*time.Hour {
		t.Fatalf("%s", rec.Body)
	}
	a := m.rows[got.ApprovalID]
	if a.Class != "forge.push" || a.BindingDigest != approvalDigest || a.BindingOutput != approvalOutput || a.BindingBranch != "agent/t-1" ||
		!slices.Contains(a.Prompters, "human:own") || !strings.Contains(string(a.Action), `"protected":["go.mod"]`) {
		t.Fatalf("%+v", a)
	}
	if d := m.drafts[0]; d.Actor != (envelope.Actor{Kind: envelope.ActorSystem, ID: factory}) || d.OriginClient != factory+":approvals" ||
		d.OriginSeq != outputSeq(approvalOutput) {
		t.Fatalf("draft %+v", d)
	}
	// A retry answers the same approval.
	if rec := call(t, h, http.MethodPost, "/v1/rooms/"+room+"/approvals", "sys:"+factory, outputApprovalBody(nil)); !strings.Contains(rec.Body.String(), got.ApprovalID) {
		t.Fatalf("a retry: %s", rec.Body)
	}

	view := call(t, h, http.MethodGet, "/v1/approvals/"+got.ApprovalID, "sys:"+factory, nil)
	var v OutputApprovalView
	if view.Code != http.StatusOK || json.Unmarshal(view.Body.Bytes(), &v) != nil || v.State != store.ApprovalPending ||
		v.Digest != approvalDigest || v.Output != approvalOutput || v.Branch != "agent/t-1" || v.RunID != runA || v.RoomID != room {
		t.Fatalf("%d %s", view.Code, view.Body)
	}
	sup := call(t, h, http.MethodPost, "/v1/approvals/"+got.ApprovalID+"/supersede", "sys:"+factory, nil)
	if sup.Code != http.StatusOK || !strings.Contains(sup.Body.String(), `"state":"superseded"`) {
		t.Fatalf("%d %s", sup.Code, sup.Body)
	}
	view = call(t, h, http.MethodGet, "/v1/approvals/"+got.ApprovalID, "sys:"+factory, nil)
	if !strings.Contains(view.Body.String(), `"decidedBy":"system:room-broker"`) {
		t.Fatalf("%s", view.Body)
	}

	// A plain approval is not the factory's to read.
	m.rows["01JC0000000000000000000009"] = store.Approval{ID: "01JC0000000000000000000009", Class: "forge.push", State: store.ApprovalPending}
	if rec := call(t, h, http.MethodGet, "/v1/approvals/01JC0000000000000000000009", "sys:"+factory, nil); rec.Code != http.StatusNotFound ||
		reason(t, rec) != wire.ReasonNoApproval {
		t.Fatalf("a plain approval: %d %s", rec.Code, rec.Body)
	}
	for name, c := range map[string]struct {
		method, path, token string
		body                any
		code                int
		reason              string
	}{
		"another system requests": {http.MethodPost, "/v1/rooms/" + room + "/approvals", "sys:system:triage", outputApprovalBody(nil),
			http.StatusForbidden, wire.ReasonNotPermitted},
		"a run requests": {http.MethodPost, "/v1/rooms/" + room + "/approvals", "run:" + runA, outputApprovalBody(nil),
			http.StatusUnauthorized, wire.ReasonUnauthenticated},
		"another system supersedes": {http.MethodPost, "/v1/approvals/" + got.ApprovalID + "/supersede", "sys:system:triage", nil,
			http.StatusForbidden, wire.ReasonNotPermitted},
		"another system reads": {http.MethodGet, "/v1/approvals/" + got.ApprovalID, "sys:system:triage", nil,
			http.StatusForbidden, wire.ReasonNotPermitted},
		"a bad digest": {http.MethodPost, "/v1/rooms/" + room + "/approvals", "sys:" + factory,
			outputApprovalBody(func(b map[string]any) { b["digest"] = "abc" }), http.StatusBadRequest, wire.ReasonBadApproval},
		"a bad output": {http.MethodPost, "/v1/rooms/" + room + "/approvals", "sys:" + factory,
			outputApprovalBody(func(b map[string]any) { b["output"] = "x" }), http.StatusBadRequest, wire.ReasonBadApproval},
		"a branch off agent/": {http.MethodPost, "/v1/rooms/" + room + "/approvals", "sys:" + factory,
			outputApprovalBody(func(b map[string]any) { b["branch"] = "main" }), http.StatusBadRequest, wire.ReasonBadApproval},
		"a ttl past 7 days": {http.MethodPost, "/v1/rooms/" + room + "/approvals", "sys:" + factory,
			outputApprovalBody(func(b map[string]any) { b["ttlSeconds"] = 8 * 86400 }), http.StatusBadRequest, wire.ReasonBadApproval},
		"an action that is not an object": {http.MethodPost, "/v1/rooms/" + room + "/approvals", "sys:" + factory,
			outputApprovalBody(func(b map[string]any) { b["action"] = []string{"x"} }), http.StatusBadRequest, wire.ReasonBadApproval},
		"an unknown field": {http.MethodPost, "/v1/rooms/" + room + "/approvals", "sys:" + factory,
			outputApprovalBody(func(b map[string]any) { b["class"] = "forge.pr" }), http.StatusBadRequest, wire.ReasonBadApproval},
		"a retry with another digest": {http.MethodPost, "/v1/rooms/" + room + "/approvals", "sys:" + factory,
			outputApprovalBody(func(b map[string]any) { b["digest"] = strings.Repeat("f", 64) }), http.StatusConflict, wire.ReasonBindingConflict},
		"a run of another room": {http.MethodPost, "/v1/rooms/" + room + "/approvals", "sys:" + factory,
			outputApprovalBody(func(b map[string]any) { b["runId"] = "cccccccc" }), http.StatusBadRequest, wire.ReasonBadApproval},
		"a branch that is not the run's": {http.MethodPost, "/v1/rooms/" + room + "/approvals", "sys:" + factory,
			outputApprovalBody(func(b map[string]any) { b["branch"] = "agent/other" }), http.StatusBadRequest, wire.ReasonBadApproval},
		"a bad room": {http.MethodPost, "/v1/rooms/NOPE/approvals", "sys:" + factory, outputApprovalBody(nil),
			http.StatusBadRequest, wire.ReasonBadRoom},
		"a bad approval id": {http.MethodGet, "/v1/approvals/nope", "sys:" + factory, nil, http.StatusBadRequest, wire.ReasonBadApproval},
		"no such approval": {http.MethodPost, "/v1/approvals/01JC000000000000000000000Z/supersede", "sys:" + factory, nil,
			http.StatusNotFound, wire.ReasonNoApproval},
	} {
		t.Run(name, func(t *testing.T) {
			rec := call(t, h, c.method, c.path, c.token, c.body)
			if rec.Code != c.code || reason(t, rec) != c.reason {
				t.Fatalf("%d %s, want %d %s", rec.Code, rec.Body, c.code, c.reason)
			}
		})
	}
	t.Run("a broker with no output approvals", func(t *testing.T) {
		s.OutputApprovals = nil
		if rec := call(t, s.Routes(), http.MethodGet, "/v1/approvals/"+got.ApprovalID, "sys:"+factory, nil); rec.Code != http.StatusForbidden {
			t.Fatalf("%d", rec.Code)
		}
	})
}
