// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// The safe-output kinds (hardening UH4): a run's request, then exactly one of the applier's
// answers. Output carries the record; Text is never part of it.
const (
	KindOutputRequested MessageKind = "output_requested"
	KindOutputApplied   MessageKind = "output_applied"
	KindOutputRefused   MessageKind = "output_refused"
)

// OutputRecord is one safe-output event's record: the request's id, type and payload digest, and
// the GitHub object an applied output wrote or the one bounded reason a refused one got. The
// payload itself, for every type but push, is Payload; a push bundle is stored beside the log.
type OutputRecord struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Digest  string         `json:"digest"`
	Bytes   int            `json:"bytes,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Object  string         `json:"object,omitempty"`
	Reason  string         `json:"reason,omitempty"`
}

var (
	outputID     = regexp.MustCompile(`^[0-9a-f]{16}$`)
	outputType   = regexp.MustCompile(`^[a-z]{2,16}$`)
	outputDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
	outputReason = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// Validate holds r to its kind: an id, type and digest always; an object on github.com only when
// applied; a snake_case reason only when refused.
func (r OutputRecord) Validate(k MessageKind) error {
	switch {
	case k != KindOutputRequested && k != KindOutputApplied && k != KindOutputRefused:
		return errors.New("output: not an output kind")
	case !outputID.MatchString(r.ID) || !outputType.MatchString(r.Type) || !outputDigest.MatchString(r.Digest):
		return errors.New("output: id is 16-hex, type lowercase, digest 64-hex")
	case (k == KindOutputApplied) != (r.Object != ""):
		return errors.New("output: an object only, and always, when applied")
	case r.Object != "" && !strings.HasPrefix(r.Object, "https://github.com/"):
		return errors.New("output: the object is a github.com URL")
	case (k == KindOutputRefused) != (r.Reason != ""):
		return errors.New("output: a reason only, and always, when refused")
	case r.Reason != "" && !outputReason.MatchString(r.Reason):
		return errors.New("output: the reason is at most 64 bytes of snake_case")
	}
	return nil
}
