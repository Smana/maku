// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// The safe-output kinds (hardening UH4): a run's request, then exactly one of the applier's
// answers. Output carries the record; Text is never part of it. KindOutputShadowed is the audit
// stage's record (HR1): what the factory would have done with a write the run made itself.
// KindOutputWaiting is not an answer (HR27): a push held for a maintainer's approval of its
// digest, answered later, once, applied or refused.
const (
	KindOutputRequested MessageKind = "output_requested"
	KindOutputApplied   MessageKind = "output_applied"
	KindOutputRefused   MessageKind = "output_refused"
	KindOutputShadowed  MessageKind = "output_shadowed"
	KindOutputWaiting   MessageKind = "output_waiting"
)

// WaitingReason is the one reason an output_waiting record carries: its push touches a protected
// path and waits for a maintainer (hardening.RefuseNeedsApproval).
const WaitingReason = "protected_path_needs_approval"

// MaxOutputFindings caps OutputRecord.Audit.
const MaxOutputFindings = 5

// MaxOutputBytes caps OutputRecord.Bytes: the largest output, a push bundle (hardening.MaxBundle).
const MaxOutputBytes = 8 << 20

// OutputFinding is one threat-check finding recorded without deciding the record (HR2): its
// layer, such as t5_classifier, and its bounded reason.
type OutputFinding struct {
	Layer  string `json:"layer"`
	Reason string `json:"reason"`
}

// OutputRecord is one safe-output event's record: the request's id, type and payload digest, and
// the GitHub object an applied output wrote or the one bounded reason a refused one got. The
// payload itself, for every type but push, is Payload; a push bundle is stored beside the log.
type OutputRecord struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Digest  string          `json:"digest"`
	Bytes   int             `json:"bytes,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Object  string          `json:"object,omitempty"`
	Reason  string          `json:"reason,omitempty"`
	// Audit holds the findings that did not decide the record, at most MaxOutputFindings, on an
	// applied, refused or shadowed record only (HR39: T5 runs in audit after a refusal too).
	Audit []OutputFinding `json:"audit,omitempty"`
	// ApprovalID names the approval a waiting push waits for, on output_waiting only: a ULID.
	ApprovalID string `json:"approvalId,omitempty"`
}

var (
	outputID     = regexp.MustCompile(`^[0-9a-f]{16}$`)
	outputType   = regexp.MustCompile(`^[a-z]{2,16}$`)
	outputDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
	outputReason = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	outputLayer  = regexp.MustCompile(`^t[1-5]_[a-z]+$`)
	approvalULID = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
)

// Validate holds r to its kind: an id, type and digest always; an object on github.com only, and
// always, when applied or shadowed; a snake_case reason always when refused, optionally when
// shadowed (a would-refuse or would_wait), never otherwise; audit findings only when applied,
// refused or shadowed; a payload on a request only, so an answer or a shadow never carries the
// written text; a size from 0 to MaxOutputBytes. A waiting record is a push with WaitingReason
// and the ULID of its approval, which no other kind carries.
func (r OutputRecord) Validate(k MessageKind) error {
	if k == KindOutputWaiting {
		return r.validateWaiting()
	}
	written := k == KindOutputApplied || k == KindOutputShadowed
	switch {
	case k != KindOutputRequested && k != KindOutputApplied && k != KindOutputRefused && k != KindOutputShadowed:
		return errors.New("output: not an output kind")
	case r.ApprovalID != "":
		return errors.New("output: an approval id only on output_waiting")
	case !outputID.MatchString(r.ID) || !outputType.MatchString(r.Type) || !outputDigest.MatchString(r.Digest):
		return errors.New("output: id is 16-hex, type lowercase, digest 64-hex")
	case len(r.Payload) > 0 && k != KindOutputRequested:
		return errors.New("output: a payload only on a request")
	case r.Bytes < 0 || r.Bytes > MaxOutputBytes:
		return errors.New("output: bytes is from 0 to 8 MiB")
	case written != (r.Object != ""):
		return errors.New("output: an object only, and always, when applied or shadowed")
	case r.Object != "" && !strings.HasPrefix(r.Object, "https://github.com/"):
		return errors.New("output: the object is a github.com URL")
	case k == KindOutputRefused && r.Reason == "",
		k != KindOutputRefused && k != KindOutputShadowed && r.Reason != "":
		return errors.New("output: a reason always when refused, optionally when shadowed, never otherwise")
	case r.Reason != "" && !outputReason.MatchString(r.Reason):
		return errors.New("output: the reason is at most 64 bytes of snake_case")
	case len(r.Audit) > 0 && k == KindOutputRequested:
		return errors.New("output: audit findings only on an answer or a shadow")
	case len(r.Audit) > MaxOutputFindings:
		return errors.New("output: at most 5 audit findings")
	}
	for _, f := range r.Audit {
		if !outputLayer.MatchString(f.Layer) || !outputReason.MatchString(f.Reason) {
			return errors.New("output: a finding is a t1 to t5 layer and a snake_case reason")
		}
	}
	return nil
}

func (r OutputRecord) validateWaiting() error {
	switch {
	case !outputID.MatchString(r.ID) || r.Type != "push" || !outputDigest.MatchString(r.Digest):
		return errors.New("output: a waiting record is a push with a 16-hex id and a 64-hex digest")
	case r.Bytes < 0 || r.Bytes > MaxOutputBytes:
		return errors.New("output: bytes is from 0 to 8 MiB")
	case len(r.Payload) > 0 || r.Object != "" || len(r.Audit) > 0:
		return errors.New("output: a waiting record carries no payload, object or findings")
	case r.Reason != WaitingReason:
		return errors.New("output: a waiting record waits for a protected path's approval")
	case !approvalULID.MatchString(r.ApprovalID):
		return errors.New("output: a waiting record names its approval's ULID")
	}
	return nil
}
