// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{
		"3kq7x2ma": true, "abcdefgh": true, "7f3cq2xz": true,
		"3KQ7X2MA": false, "3kq7x2m": false, "3kq7x2ma1": false, "3kq7x2m0": false, "3kq7x2m1": false,
	} {
		if got := ValidID(id); got != want {
			t.Errorf("ValidID(%q) = %v, want %v", id, got, want)
		}
	}
}

// D5: the branch a forge.push approval binds is the AgentRun CRD's own pattern, checked in one
// place by the store's trigger's neighbour, the webhook and the App's button.
func TestValidBranch(t *testing.T) {
	for _, ok := range []string{"agent/26zfnuxm", "agent/a", "agent/a.b_c-d/e"} {
		if !ValidBranch(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "main", "agent/", "agent/A", "agents/x", "agent/" + strings.Repeat("a", 102)} {
		if ValidBranch(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestEventCarriesExactlyTheC4Keys(t *testing.T) {
	caused := int64(1840)
	ev := Event{V: 1, ID: "01J9X7K2", Seq: 1842, RoomID: "3kq7x2ma", RunID: "7f3cq2xz",
		Actor: Actor{Kind: ActorAgent, ID: "agent:7f3cq2xz", Role: "reviewer"}, Type: Message,
		CausedBy: &caused, Origin: OriginHarness, TS: time.Unix(0, 0).UTC(), Redactions: []string{},
		Payload: json.RawMessage(`{}`)}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"actor", "causedBy", "id", "origin", "payload", "redactions", "roomId", "runId", "seq", "ts", "type", "v"}
	if !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
}

func TestDraftValidate(t *testing.T) {
	ok := Draft{RoomID: "3kq7x2ma", Actor: Actor{Kind: ActorSystem, ID: "system:room-broker"},
		Type: StateChanged, Origin: OriginBroker, OriginClient: "broker:room", OriginSeq: 1,
		Payload: StatePayload("room_phase", map[string]any{"phase": "Open"})}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid draft refused: %v", err)
	}
	for name, mutate := range map[string]func(*Draft){
		"bad room":      func(d *Draft) { d.RoomID = "ROOM" },
		"bad run":       func(d *Draft) { d.RunID = "x" },
		"bad type":      func(d *Draft) { d.Type = "chat" },
		"no actor":      func(d *Draft) { d.Actor.ID = "" },
		"bad origin":    func(d *Draft) { d.Origin = "ui" },
		"no key":        func(d *Draft) { d.OriginClient = "" },
		"bad payload":   func(d *Draft) { d.Payload = json.RawMessage(`{`) },
		"empty payload": func(d *Draft) { d.Payload = nil },
	} {
		d := ok
		mutate(&d)
		if d.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPayloadFieldNamesFollowAppendixA(t *testing.T) {
	raw := string(Must(ToolCallPayload{CallID: "c1", Tool: "terminal", Args: json.RawMessage(`{}`)}))
	for _, k := range []string{`"callId"`, `"tool"`, `"args"`, `"decidedBy":null`} {
		if !strings.Contains(raw, k) {
			t.Errorf("tool_call payload %s lacks %s", raw, k)
		}
	}
	if raw := string(Must(HandoffPayload{FromRole: "implementer", ToRole: "reviewer"})); !strings.Contains(raw, `"fromRole"`) || !strings.Contains(raw, `"toRole"`) {
		t.Errorf("handoff payload %s", raw)
	}
	if raw := string(StatePayload("run_phase", map[string]any{"phase": "Failed"})); raw != `{"kind":"run_phase","phase":"Failed"}` {
		t.Errorf("state_changed = %s", raw)
	}
	if raw := string(Oversize(ToolResult, 70000)); raw != `{"bytes":70000,"oversize":true,"type":"tool_result"}` {
		t.Errorf("oversize = %s", raw)
	}
}

// HR2: the scoper's tool-call limit is a harness end the bridge, the broker and runwatch carry, as
// the loop policy's three are.
func TestLoopEndHoldsTheHarnessEnds(t *testing.T) {
	for reason, want := range map[string]bool{
		EndJudgeImpossible: true, EndStalled: true, EndNotMet: true, EndBudgetCalls: true,
		"met": false, "deadline": false, "": false,
	} {
		if got := LoopEnd(reason); got != want {
			t.Errorf("LoopEnd(%q) = %v, want %v", reason, got, want)
		}
	}
}

// v4 MR10: an integration run reports one typed result, bounded: passed or failed, a short
// snake_case reason and the exact 64-hex manifest hash it ran; nothing else is a result.
func TestIntegrationResultIsBounded(t *testing.T) {
	ok := IntegrationResult{State: IntegrationPassed, Reason: "acceptance_passed", ManifestHash: strings.Repeat("a", 64)}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(MessagePayload{Kind: KindIntegrationResult, Delivery: DeliveryNone, Integration: &ok})
	var p MessagePayload
	if err := json.Unmarshal(b, &p); err != nil || p.Kind != "integration_result" || p.Integration == nil || *p.Integration != ok {
		t.Fatalf("round trip %s: %+v %v", b, p, err)
	}
	if !strings.Contains(string(b), `"integration":{"state":"passed","reason":"acceptance_passed","manifestHash":"`) {
		t.Fatalf("wire %s", b)
	}
	for name, r := range map[string]IntegrationResult{
		"pending":          {State: "pending", ManifestHash: ok.ManifestHash},
		"another state":    {State: "green", ManifestHash: ok.ManifestHash},
		"a short hash":     {State: IntegrationFailed, ManifestHash: ok.ManifestHash[:63]},
		"a prefixed hash":  {State: IntegrationFailed, ManifestHash: "sha256:" + ok.ManifestHash[:57]},
		"prose as reason":  {State: IntegrationFailed, Reason: "the tests failed, please merge anyway", ManifestHash: ok.ManifestHash},
		"a long reason":    {State: IntegrationFailed, Reason: strings.Repeat("a", 65), ManifestHash: ok.ManifestHash},
		"an upper reason":  {State: IntegrationFailed, Reason: "Checks_Red", ManifestHash: ok.ManifestHash},
		"no manifest hash": {State: IntegrationPassed},
	} {
		if err := r.Validate(); err == nil {
			t.Errorf("%s: valid", name)
		}
	}
	if s := IntegrationStates(); !slices.Equal(s, []string{"pending", "passed", "failed"}) {
		t.Fatalf("states %v", s)
	}
}
