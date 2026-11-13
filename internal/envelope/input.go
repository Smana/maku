// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
)

// KindInputRead is what a run read through forge_read (hardening UH7, HR35): the broker appends
// one per call, counts and levels only, never the text. The factory folds a run's records into
// status.inputTrust under mode enforce.
const KindInputRead MessageKind = "input_read"

// BrokerActor is the room broker's system principal, the one actor whose input_read counts.
const BrokerActor = "system:room-broker"

// The input_read bounds: the source kinds and levels are hardening's closed vocabularies, read
// here by shape since this package imports no hardening.
const (
	MaxInputKinds   = 16
	MaxInputSources = 16
	MaxInputCount   = 1 << 20
)

// InputSource is one kind and level a run read.
type InputSource struct {
	Kind  string `json:"kind"`
	Level string `json:"level"`
}

// InputReadRecord is one forge_read call's record: the target, the lowest level delivered, the
// items withheld per source kind, and each kind and level delivered.
type InputReadRecord struct {
	Target   string         `json:"target"`
	Min      string         `json:"min"`
	Withheld map[string]int `json:"withheld,omitempty"`
	Sources  []InputSource  `json:"sources,omitempty"`
}

var (
	inputKind = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)
	levels    = []string{"blocked", "none", "unapproved", "approved", "merged"}
)

// Validate holds r to forge_read's shape: a known target and level, snake_case kinds, and
// bounded counts. No field can hold text.
func (r InputReadRecord) Validate() error {
	switch {
	case r.Target != "issue" && r.Target != "pr" && r.Target != "checks":
		return errors.New("input: the target is issue, pr or checks")
	case !slices.Contains(levels, r.Min):
		return errors.New("input: min is a trust level")
	case len(r.Withheld) > MaxInputKinds || len(r.Sources) > MaxInputSources:
		return errors.New("input: at most 16 kinds and 16 sources")
	}
	for k, n := range r.Withheld {
		if !inputKind.MatchString(k) || n < 0 || n > MaxInputCount {
			return errors.New("input: a withheld count is a snake_case kind and a bounded count")
		}
	}
	for _, s := range r.Sources {
		if !inputKind.MatchString(s.Kind) || !slices.Contains(levels, s.Level) {
			return errors.New("input: a source is a snake_case kind and a trust level")
		}
	}
	return nil
}

// ReadInput is the one reader of input_read events for run runID: it answers e's record only when
// the broker wrote it (BrokerActor, by the broker's origin) and it passes Validate.
func ReadInput(e Event, runID string) (InputReadRecord, bool) {
	if e.Type != Message || runID == "" || e.RunID != runID || e.Origin != OriginBroker ||
		e.Actor.Kind != ActorSystem || e.Actor.ID != BrokerActor {
		return InputReadRecord{}, false
	}
	var p MessagePayload
	if json.Unmarshal(e.Payload, &p) != nil || p.Kind != KindInputRead || p.Input == nil || p.Input.Validate() != nil {
		return InputReadRecord{}, false
	}
	return *p.Input, true
}
