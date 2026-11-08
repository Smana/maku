// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
}

// Checker is the layered threat check (UH8), the port other writers reuse (RunLore RK16,
// coordination's plans).
type Checker struct {
	Layers         Layers
	Paths          ProtectedPaths
	Secrets        SecretScanner
	Classifier     Classifier
	LabelAllowlist []string
}

// Check runs the layers in order on req, counting it against n once T1's schema passes. The first
// enforced refusal stops it; an audit layer records its finding and the request goes on.
func (c Checker) Check(ctx context.Context, n *Counts, req Request) Verdict {
	var v Verdict
	// refuse reports whether the layer's finding stops the request.
	refuse := func(l Layer, m Mode, r Refusal) bool {
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
	if c.Layers.Classifier != ModeOff {
		refuse(LayerClassifier, c.Layers.Classifier, c.classify(ctx, texts))
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
