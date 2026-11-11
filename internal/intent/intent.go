// SPDX-License-Identifier: Apache-2.0

// Package intent is the vocabulary of a Slack reply's second reading (spec SI3, SI4): the closed nine
// intents, the schema check that turns a model's answer into one of them or into unknown, and the
// span rule that lets the model choose what to quote but never write it. It does no I/O: the one
// model call is model.go's, and who may be classified is the broker's.
package intent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"unicode"
)

// Intent is one of the nine labels a reply can carry.
type Intent string

// The closed vocabulary. A model answer outside it is Unknown with OutcomeInvalidOutput.
const (
	Status  Intent = "status"
	Needs   Intent = "needs"
	Changes Intent = "changes"
	PR      Intent = "pr"
	Steer   Intent = "steer"
	Amend   Intent = "amend"
	Stop    Intent = "stop"
	Retry   Intent = "retry"
	Unknown Intent = "unknown"
)

// All is the nine, in the order the schema lists them.
var All = []Intent{Status, Needs, Changes, PR, Steer, Amend, Stop, Retry, Unknown}

// Valid reports whether i is one of the nine.
func (i Intent) Valid() bool {
	for _, k := range All {
		if i == k {
			return true
		}
	}
	return false
}

// Write reports whether the intent acts on a task once confirmed: stop, retry, steer and amend. The
// rest are reads or nothing.
func (i Intent) Write() bool {
	switch i {
	case Steer, Amend, Stop, Retry:
		return true
	}
	return false
}

// carriesText is the two intents whose extracted text survives parsing: it is what the human is
// asked to confirm. Every other intent drops any text the model returned.
func (i Intent) carriesText() bool { return i == Steer || i == Amend }

// Outcome is how a classification ended, as maku_intent_classify_total{outcome} counts it.
type Outcome string

// The outcomes Parse decides; the caller adds timeout, capped and error.
const (
	OutcomeOK            Outcome = "ok"
	OutcomeLowConfidence Outcome = "low_confidence"
	OutcomeInvalidOutput Outcome = "invalid_output"
	OutcomeTimeout       Outcome = "timeout"
	OutcomeCapped        Outcome = "capped"
	OutcomeError         Outcome = "error"
)

// Result is a classification: the label, the span of the reply it acts on (steer and amend only) and
// the model's confidence.
type Result struct {
	Intent     Intent
	Text       string
	Confidence float64
}

// wire is the model's answer: exactly these three fields, none missing.
type wire struct {
	Intent     *string  `json:"intent"`
	Text       *string  `json:"text"`
	Confidence *float64 `json:"confidence"`
}

// Parse reads the model's raw answer against reply. Anything that is not the closed shape, names an
// intent outside the nine or carries a confidence outside 0 to 1 is Unknown with OutcomeInvalidOutput;
// a confidence under minConfidence is Unknown with OutcomeLowConfidence. Steer and amend keep the
// span of reply the model quoted (Span), every other intent drops its text.
func Parse(raw []byte, reply string, minConfidence float64) (Result, Outcome) {
	invalid := Result{Intent: Unknown}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var w wire
	if err := dec.Decode(&w); err != nil {
		return invalid, OutcomeInvalidOutput
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return invalid, OutcomeInvalidOutput
	}
	if w.Intent == nil || w.Text == nil || w.Confidence == nil {
		return invalid, OutcomeInvalidOutput
	}
	c := *w.Confidence
	if math.IsNaN(c) || c < 0 || c > 1 {
		return invalid, OutcomeInvalidOutput
	}
	i := Intent(*w.Intent)
	if !i.Valid() {
		return invalid, OutcomeInvalidOutput
	}
	if c < minConfidence {
		return Result{Intent: Unknown, Confidence: c}, OutcomeLowConfidence
	}
	res := Result{Intent: i, Confidence: c}
	if i.carriesText() {
		res.Text = Span(reply, *w.Text)
	}
	return res, OutcomeOK
}

// fold lowercases s and collapses every run of whitespace to one space, remembering for each byte of
// the folded string where its rune starts in s, so a folded match maps back to the reply's spelling.
func fold(s string) (string, []int) {
	var b strings.Builder
	var at []int
	space := true // leading whitespace is dropped
	for i, r := range s {
		if unicode.IsSpace(r) {
			if !space {
				b.WriteByte(' ')
				at = append(at, i)
			}
			space = true
			continue
		}
		space = false
		for _, lr := range strings.ToLower(string(r)) {
			n := len(string(lr))
			b.WriteRune(lr)
			for range n {
				at = append(at, i)
			}
		}
	}
	return strings.TrimRight(b.String(), " "), at
}

// Span is what a steer or an amend acts on: text if it is a contiguous span of reply once case and
// whitespace are folded, returned as the reply spelled it; otherwise, an empty text included, the
// whole reply. The model chooses what to quote and never writes it (SI4).
func Span(reply, text string) string {
	ft, _ := fold(text)
	if ft == "" {
		return reply
	}
	fr, at := fold(reply)
	i := strings.Index(fr, ft)
	if i < 0 {
		return reply
	}
	start := at[i]
	end := len(reply)
	if j := i + len(ft); j < len(at) {
		end = at[j]
	}
	return reply[start:end]
}

// TextHash is the value the confirm binds to: "sha256:" and the hex of the span's SHA-256.
func TextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}
