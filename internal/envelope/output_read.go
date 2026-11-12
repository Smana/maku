// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"encoding/json"
	"strings"
)

// factoryActorPrefix is the id every factory principal's system actor starts with.
const factoryActorPrefix = "system:factory"

// ReadOutput is the one reader of safe-output events for run runID: it answers e's kind and
// record only when the broker's stamp vouches for it, and the record passes Validate. A request
// counts only from the run's own agent actor ("agent:"+runID) by the client origin, as
// integration_result reads; an answer or a shadow only from a factory system actor. Anything
// else in the room, however its payload is shaped, is not an output.
func ReadOutput(e Event, runID string) (MessageKind, OutputRecord, bool) {
	if e.Type != Message || runID == "" || e.RunID != runID {
		return "", OutputRecord{}, false
	}
	var p MessagePayload
	if json.Unmarshal(e.Payload, &p) != nil || p.Output == nil {
		return "", OutputRecord{}, false
	}
	switch p.Kind {
	case KindOutputRequested:
		if e.Origin != OriginClient || e.Actor.Kind != ActorAgent || e.Actor.ID != "agent:"+runID {
			return "", OutputRecord{}, false
		}
	case KindOutputApplied, KindOutputRefused, KindOutputShadowed:
		if e.Actor.Kind != ActorSystem || !strings.HasPrefix(e.Actor.ID, factoryActorPrefix) {
			return "", OutputRecord{}, false
		}
	default:
		return "", OutputRecord{}, false
	}
	if p.Output.Validate(p.Kind) != nil {
		return "", OutputRecord{}, false
	}
	return p.Kind, *p.Output, true
}
