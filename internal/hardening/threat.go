// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/factory/sanitize"
)

// Mode is a layer's or the track's setting: off checks nothing, audit records what it would
// refuse, enforce refuses.
type Mode string

// The modes, in order.
const (
	ModeOff     Mode = "off"
	ModeAudit   Mode = "audit"
	ModeEnforce Mode = "enforce"
)

var modeRank = map[Mode]int{ModeOff: 0, ModeAudit: 1, ModeEnforce: 2}

// ParseMode reads off, audit or enforce.
func ParseMode(s string) (Mode, error) {
	if _, ok := modeRank[Mode(s)]; !ok {
		return "", fmt.Errorf("%q is none of off, audit, enforce", s)
	}
	return Mode(s), nil
}

// AtLeast reports whether m is as strict as o.
func (m Mode) AtLeast(o Mode) bool { return modeRank[m] >= modeRank[o] }

// Layer names a threat-check layer (UH8).
type Layer string

// The layers, in the order they run.
const (
	LayerSchema     Layer = "t1_schema"
	LayerSanitise   Layer = "t2_sanitise"
	LayerSecrets    Layer = "t3_secrets"
	LayerPaths      Layer = "t4_paths"
	LayerClassifier Layer = "t5_classifier"
)

// Layers holds each layer's mode.
type Layers struct {
	Schema, Sanitise, Secrets, Paths, Classifier Mode
}

// LayersFor is the configuration's layers: T1 to T4 run at the track's mode and cannot be set
// below it; T5 runs at its own mode, never above the track's.
func LayersFor(mode, classifier Mode) Layers {
	if !mode.AtLeast(classifier) {
		classifier = mode
	}
	return Layers{Schema: mode, Sanitise: mode, Secrets: mode, Paths: mode, Classifier: classifier}
}

// SecretScanner is the gitleaks redactor's string half; *redact.Redactor implements it.
type SecretScanner interface {
	String(s string) (string, []string)
}

// Classifier is T5: agent-guard's prompt-injection verdict over the texts an output publishes.
type Classifier interface {
	Injection(ctx context.Context, texts []string) (bool, error)
}

// Request is one output request with the facts the applier read for it. For a push, Files and
// Added are the bundle's changed paths and its added lines and commit messages, and Approved is a
// maintainer's approval of the bundle's digest (UH9): the bundle reader is the applier's (H3).
type Request struct {
	Type     OutputType
	Payload  json.RawMessage
	OwnIssue int    // the task's issue: the only one Closes or Fixes may name
	Canary   string // the run's canary, planted in its brief
	Files    []string
	Added    []string
	Approved bool
	// DiffUnavailable: the push's changed paths could not be read whole, so Files is not the
	// bundle's list and T4 refuses it (diff_unavailable).
	DiffUnavailable bool
}

// Finding is a refusal an audit layer recorded without acting on it.
type Finding struct {
	Layer   Layer   `json:"layer"`
	Refusal Refusal `json:"refusal"`
}

// Verdict is the check's answer: an enforced refusal and its layer, or none with the sanitised
// payload to apply; Audit lists what audit layers would have refused, Report what T2 rewrote.
type Verdict struct {
	Refusal Refusal
	Layer   Layer
	Audit   []Finding
	Payload any
	Report  TextReport
	// T5 is the classifier's answer when it ran: hit, pass or unavailable; "" when it did not
	// (HR39's counter).
	T5 string
}

// The T5 answers Verdict.T5 holds.
const (
	T5Hit         = "hit"
	T5Pass        = "pass"
	T5Unavailable = "unavailable"
)

func t5Result(r Refusal) string {
	switch r {
	case "":
		return T5Pass
	case RefuseInjection:
		return T5Hit
	}
	return T5Unavailable
}

// Checker is the layered threat check (UH8), the port other writers reuse (RunLore RK16,
// coordination's plans).
type Checker struct {
	Layers         Layers
	Paths          ProtectedPaths
	Secrets        SecretScanner
	Classifier     Classifier
	LabelAllowlist []string
	// ClassifierEnforce are the output types T5 is promoted to enforce for (HR40): a hit refuses
	// injection_suspected and an unreachable classifier classifier_unavailable. Every other type
	// runs T5 at Layers.Classifier. With T5 off, nothing is promoted.
	ClassifierEnforce []OutputType
}

// classifierMode is T5's mode for an output of type t.
func (c Checker) classifierMode(t OutputType) Mode {
	if c.Layers.Classifier != ModeOff && slices.Contains(c.ClassifierEnforce, t) {
		return ModeEnforce
	}
	return c.Layers.Classifier
}

// Check runs the layers in order on req, counting it against n once T1's schema passes. The first
// enforced refusal stops it; an audit layer records its finding and the request goes on.
func (c Checker) Check(ctx context.Context, n *Counts, req Request) Verdict {
	var v Verdict
	refuse := v.refuse
	if c.Layers.Schema == ModeOff {
		return v
	}
	p, r := DecodeOutput(req.Type, req.Payload, c.LabelAllowlist)
	if r == "" && !n.Take(req.Type) {
		r = RefuseCap
	}
	if refuse(LayerSchema, c.Layers.Schema, r) || p == nil {
		return v
	}
	if c.Layers.Sanitise != ModeOff {
		p, v.Report = sanitiseOutput(p, req.OwnIssue)
		if refuse(LayerSanitise, c.Layers.Sanitise, refuseIf(v.Report.Withheld, RefuseText)) {
			return v
		}
	}
	v.Payload = p
	texts := published(p, req)
	if refuse(LayerSecrets, c.Layers.Secrets, c.secrets(texts, req.Canary)) {
		return v
	}
	if req.Type == Push && refuse(LayerPaths, c.Layers.Paths, c.paths(req)) {
		return v
	}
	if mode := c.classifierMode(req.Type); mode != ModeOff {
		r := c.classify(ctx, texts)
		v.T5 = t5Result(r)
		refuse(LayerClassifier, mode, r)
	}
	return v
}

// measured are the refusals whose output T5 may still read (HR39, #520's review): a T1 target
// refusal, whose text is otherwise well formed, and a push's T4 refusals, whose added lines already
// passed T3. A T3 refusal is never measured (the text holds a secret or the canary, and T5 would send
// it out of the factory), nor a cap refusal (a run could drive the classifier's load with it), nor a
// T2 withholding (only its placeholder is left).
var measured = []Refusal{RefuseTarget, RefuseBlockedPath, RefuseDiffUnavailable}

// Measure runs T5 in audit on an output another layer refused (HR39), so T5's detection on the
// drills can be set against the pattern layers'. Only a refusal in measured is read, and only once
// T3's scan of the very texts T5 would read finds neither a secret nor the canary: with no scanner,
// nothing is sent. The finding joins v.Audit and its answer v.T5; v's refusal is unchanged.
func (c Checker) Measure(ctx context.Context, req Request, v Verdict) Verdict {
	if v.T5 != "" || c.Layers.Classifier == ModeOff || !slices.Contains(measured, v.Refusal) {
		return v
	}
	p, _ := DecodeOutput(req.Type, req.Payload, c.LabelAllowlist)
	if p == nil {
		return v
	}
	if c.Layers.Sanitise != ModeOff {
		var rep TextReport
		if p, rep = sanitiseOutput(p, req.OwnIssue); rep.Withheld {
			return v
		}
	}
	texts := slices.DeleteFunc(published(p, req), func(s string) bool { return s == "" })
	if len(texts) == 0 || c.secrets(texts, req.Canary) != "" {
		return v
	}
	r := c.classify(ctx, texts)
	v.T5 = t5Result(r)
	if r != "" {
		v.Audit = append(v.Audit, Finding{LayerClassifier, r})
	}
	return v
}

// refuse records layer l's finding r under mode m and reports whether it stops the request: off
// ignores it, audit records it in Audit, enforce makes it the refusal.
func (v *Verdict) refuse(l Layer, m Mode, r Refusal) bool {
	switch {
	case r == "" || m == ModeOff:
		return false
	case m == ModeAudit:
		v.Audit = append(v.Audit, Finding{l, r})
		return false
	}
	v.Refusal, v.Layer = r, l
	return true
}

// CheckText is the check on free text with no output type (HR21, RunLore RK16): T2, T3 against
// canary, and T5, each at its layer's mode. T1 and T4 do not apply: there is no schema, cap or
// path. The verdict's Payload is the sanitised texts, a []string in order.
func (c Checker) CheckText(ctx context.Context, texts []string, canary string) Verdict {
	var v Verdict
	out := slices.Clone(texts)
	if c.Layers.Sanitise != ModeOff {
		for i, t := range out {
			out[i] = sanitiseText(t, 0, &v.Report)
		}
		if v.refuse(LayerSanitise, c.Layers.Sanitise, refuseIf(v.Report.Withheld, RefuseText)) {
			return v
		}
	}
	v.Payload = out
	if c.Layers.Secrets != ModeOff && v.refuse(LayerSecrets, c.Layers.Secrets, c.secrets(out, canary)) {
		return v
	}
	if c.Layers.Classifier != ModeOff && len(out) > 0 {
		r := c.classify(ctx, out)
		v.T5 = t5Result(r)
		v.refuse(LayerClassifier, c.Layers.Classifier, r)
	}
	return v
}

func refuseIf(b bool, r Refusal) Refusal {
	if b {
		return r
	}
	return ""
}

// published is the text an output puts on GitHub: its sanitised fields, or a push's added lines
// and commit messages.
func published(p any, req Request) []string {
	switch o := p.(type) {
	case PRPayload:
		return []string{o.Title, o.Body}
	case CommentPayload:
		return []string{o.Body}
	case LabelsPayload:
		return o.Add
	case IssuePayload:
		return []string{o.Title, o.Body}
	}
	return req.Added
}

// secrets is T3: the canary first, since it is an alert, then the gitleaks rules. The canary is
// sought in the text as a reader sees it, so a zero-width character cannot split it.
func (c Checker) secrets(texts []string, canary string) Refusal {
	for _, t := range texts {
		if seen, _ := sanitize.Text(t); canary != "" && strings.Contains(seen, canary) {
			return RefuseCanary
		}
	}
	if c.Secrets == nil {
		return RefuseSecret // no scanner is no proof: fail closed
	}
	for _, t := range texts {
		if _, rules := c.Secrets.String(t); len(rules) > 0 {
			return RefuseSecret
		}
	}
	return ""
}

// paths is T4: a blocked path refuses, an approval path waits for the digest's approval.
func (c Checker) paths(req Request) Refusal {
	if req.DiffUnavailable {
		return RefuseDiffUnavailable
	}
	switch c.Paths.Worst(req.Files) {
	case OnBlocked:
		return RefuseBlockedPath
	case OnApproval:
		if !req.Approved {
			return RefuseNeedsApproval
		}
	}
	return ""
}

func (c Checker) classify(ctx context.Context, texts []string) Refusal {
	if c.Classifier == nil {
		return RefuseClassifierDown
	}
	hit, err := c.Classifier.Injection(ctx, texts)
	switch {
	case err != nil:
		return RefuseClassifierDown
	case hit:
		return RefuseInjection
	}
	return ""
}

// NewCanary is a run's canary: a random token planted in its brief as an instruction the agent
// must never repeat (T3).
func NewCanary() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (Go 1.24+)
	return "mkc-" + hex.EncodeToString(b)
}

// Digest is a payload's sha256, as output.requested records it and an approval binds it.
func Digest(payload []byte) string {
	s := sha256.Sum256(payload)
	return hex.EncodeToString(s[:])
}
