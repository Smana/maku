// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"encoding/json"
	"testing"
)

// HR35: input_read holds counts and levels only, and counts only from the broker's own stamp.
func TestInputRead(t *testing.T) {
	good := InputReadRecord{Target: "issue", Min: "approved", Withheld: map[string]int{"comment": 2},
		Sources: []InputSource{{Kind: "comment", Level: "approved"}}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]InputReadRecord{
		"target":       {Target: "o/r#3", Min: "none"},
		"min":          {Target: "pr", Min: "trusted"},
		"withheld":     {Target: "pr", Min: "none", Withheld: map[string]int{"Some text here": 1}},
		"count":        {Target: "pr", Min: "none", Withheld: map[string]int{"comment": -1}},
		"source level": {Target: "pr", Min: "none", Sources: []InputSource{{Kind: "comment", Level: "x"}}},
	} {
		if r.Validate() == nil {
			t.Errorf("%s: accepted %+v", name, r)
		}
	}
	payload, _ := json.Marshal(MessagePayload{Kind: KindInputRead, Delivery: DeliveryNone, Input: &good})
	ev := Event{Type: Message, RunID: "r1", Origin: OriginBroker, Actor: Actor{Kind: ActorSystem, ID: BrokerActor}, Payload: payload}
	if got, ok := ReadInput(ev, "r1"); !ok || got.Withheld["comment"] != 2 {
		t.Fatalf("%+v %v", got, ok)
	}
	for name, e := range map[string]Event{
		"another run":    func() Event { e := ev; e.RunID = "r2"; return e }(),
		"client origin":  func() Event { e := ev; e.Origin = OriginClient; return e }(),
		"agent actor":    func() Event { e := ev; e.Actor = Actor{Kind: ActorAgent, ID: "agent:r1"}; return e }(),
		"factory actor":  func() Event { e := ev; e.Actor = Actor{Kind: ActorSystem, ID: FactoryActor}; return e }(),
		"another kind":   func() Event { e := ev; e.Payload, _ = json.Marshal(MessagePayload{Kind: KindChat, Input: &good}); return e }(),
		"invalid record": func() Event { e := ev; e.Payload, _ = json.Marshal(MessagePayload{Kind: KindInputRead, Input: &InputReadRecord{}}); return e }(),
	} {
		if _, ok := ReadInput(e, "r1"); ok {
			t.Errorf("%s counted", name)
		}
	}
}
