// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"strings"
	"testing"
)

// ReadOutput trusts the broker's stamp, never the payload: a request counts only from the run's
// own agent by the client origin, and an answer or a shadow only from the factory's system actor.
func TestReadOutput(t *testing.T) {
	const run = "run-1"
	d := strings.Repeat("a", 64)
	req := OutputRecord{ID: "0123456789abcdef", Type: "pr", Digest: d}
	ans := OutputRecord{ID: "0123456789abcdef", Type: "pr", Digest: d, Object: "https://github.com/o/r/pull/7"}
	agent := Actor{Kind: ActorAgent, ID: "agent:" + run, Role: "implementer"}
	factory := Actor{Kind: ActorSystem, ID: "system:factory"}
	ev := func(a Actor, o Origin, runID string, k MessageKind, r OutputRecord) Event {
		return Event{Type: Message, RunID: runID, Actor: a, Origin: o,
			Payload: Must(MessagePayload{Kind: k, Delivery: DeliveryNone, Output: &r})}
	}
	for _, tc := range []struct {
		name string
		e    Event
		want MessageKind
	}{
		{"the run's own request", ev(agent, OriginClient, run, KindOutputRequested, req), KindOutputRequested},
		{"the factory's answer", ev(factory, OriginClient, run, KindOutputApplied, ans), KindOutputApplied},
		{"the factory's shadow", ev(factory, OriginClient, run, KindOutputShadowed, ans), KindOutputShadowed},
		{"the factory's refusal", ev(factory, OriginClient, run,
			KindOutputRefused, OutputRecord{ID: req.ID, Type: "pr", Digest: d, Reason: "output_schema"}), KindOutputRefused},
		{"an answer from a principal that only starts like the factory's", ev(Actor{Kind: ActorSystem, ID: "system:factory-b"}, OriginClient, run,
			KindOutputRefused, OutputRecord{ID: req.ID, Type: "pr", Digest: d, Reason: "output_schema"}), ""},
		{"an agent-stamped answer", ev(agent, OriginClient, run, KindOutputApplied, ans), ""},
		{"another run's request", ev(Actor{Kind: ActorAgent, ID: "agent:run-2"}, OriginClient, run, KindOutputRequested, req), ""},
		{"a request on another run's events", ev(agent, OriginClient, "run-2", KindOutputRequested, req), ""},
		{"a request by the harness origin", ev(agent, OriginHarness, run, KindOutputRequested, req), ""},
		{"a request from the system actor", ev(factory, OriginClient, run, KindOutputRequested, req), ""},
		{"an answer from another system", ev(Actor{Kind: ActorSystem, ID: "system:triage"}, OriginClient, run, KindOutputApplied, ans), ""},
		{"an answer by the broker origin", ev(factory, OriginBroker, run, KindOutputApplied, ans), ""},
		{"an answer from a human", ev(Actor{Kind: ActorHuman, ID: "system:factory"}, OriginClient, run, KindOutputApplied, ans), ""},
		{"a record that fails Validate", ev(factory, OriginClient, run, KindOutputApplied, req), ""},
		{"not an output kind", ev(agent, OriginClient, run, KindChat, req), ""},
		{"not a message", func() Event { e := ev(agent, OriginClient, run, KindOutputRequested, req); e.Type = ToolCall; return e }(), ""},
		{"no record", Event{Type: Message, RunID: run, Actor: agent, Origin: OriginClient,
			Payload: Must(MessagePayload{Kind: KindOutputRequested, Delivery: DeliveryNone})}, ""},
		{"a payload that is not JSON", Event{Type: Message, RunID: run, Actor: agent, Origin: OriginClient, Payload: []byte("{")}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, r, ok := ReadOutput(tc.e, run)
			if ok != (tc.want != "") || k != tc.want {
				t.Fatalf("ReadOutput = %q, %v, want %q", k, ok, tc.want)
			}
			if ok && r.ID != req.ID {
				t.Fatalf("record id %q", r.ID)
			}
		})
	}
}
