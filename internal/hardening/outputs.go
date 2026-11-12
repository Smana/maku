// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Smana/maku/internal/factory/sanitize"
)

// OutputType is one entry of the safe-output vocabulary (UH3). Anything else is refused.
type OutputType string

// The vocabulary.
const (
	Push    OutputType = "push"
	PR      OutputType = "pr"
	Comment OutputType = "comment"
	Labels  OutputType = "labels"
	Issue   OutputType = "issue"
)

// Types is the vocabulary, in the order the design lists it.
func Types() []OutputType { return []OutputType{Push, PR, Comment, Labels, Issue} }

// Roles are the run roles that may request t: the reviewer and the triager have no output but the
// room tools, and a coordination integration run's only output is its verdict.
func Roles(OutputType) []string { return []string{"implementer", "tester"} }

// Refusal is an output's one bounded reason (threat check table). Empty is no refusal.
type Refusal string

// The refusals, by layer.
const (
	RefuseType           Refusal = "output_type"
	RefuseSchema         Refusal = "output_schema"
	RefuseCap            Refusal = "output_cap"
	RefuseText           Refusal = "output_text"
	RefuseSecret         Refusal = "secret_found"
	RefuseCanary         Refusal = "canary_leaked"
	RefuseNeedsApproval  Refusal = "protected_path_needs_approval"
	RefuseBlockedPath    Refusal = "protected_path_blocked"
	RefuseInjection      Refusal = "injection_suspected"
	RefuseClassifierDown Refusal = "classifier_unavailable"
	// RefuseDiffUnavailable is T4 on a push whose changed paths cannot be read whole (a compare
	// past GitHub's file cap, or no merge base): with no file list, no path can be cleared.
	RefuseDiffUnavailable Refusal = "diff_unavailable"
	// RefuseTarget is T1 on a comment or labels request whose target the task does not have yet: no
	// open pull request, or no issue for a text task (HR17).
	RefuseTarget Refusal = "output_target"
)

// Payload bounds (safe outputs table).
const (
	MaxBundle     = 8 << 20
	MaxPRTitle    = 256 // characters
	MinPRBody     = 20  // characters
	MaxPRBody     = 65000
	MaxComment    = 16 << 10 // bytes
	MaxLabelsCall = 3
)

// caps is each type's per-run cap: a pr is one open and five edits, labels one call.
var caps = map[OutputType]int{Push: 20, PR: 6, Comment: 3, Labels: 1, Issue: 1}

// PushPayload is a git bundle with one ref, agent/<task>. Its ref and tree are the applier's
// checks (H3): here it is bytes within MaxBundle.
type PushPayload struct {
	Bundle []byte `json:"bundle"`
}

// PRPayload opens or edits the task's one pull request; its base and head are the factory's.
type PRPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Draft bool   `json:"draft,omitempty"`
}

// Comment targets: the task's own pull request or issue, never another.
const (
	TargetPR    = "pr"
	TargetIssue = "issue"
)

// CommentPayload comments on the task's pull request or issue.
type CommentPayload struct {
	Target string `json:"target"`
	Body   string `json:"body"`
}

// LabelsPayload adds labels from hardening.labelAllowlist.
type LabelsPayload struct {
	Add []string `json:"add"`
}

// IssuePayload files a follow-up through the issue draft template, always factory/proposed.
type IssuePayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// DecodeOutput is T1's schema half: payload decoded strictly into t's type and held to its
// bounds. allow is hardening.labelAllowlist. It returns the typed payload or one refusal.
func DecodeOutput(t OutputType, payload json.RawMessage, allow []string) (any, Refusal) {
	var v any
	switch t {
	case Push:
		v = &PushPayload{}
	case PR:
		v = &PRPayload{}
	case Comment:
		v = &CommentPayload{}
	case Labels:
		v = &LabelsPayload{}
	case Issue:
		v = &IssuePayload{}
	default:
		return nil, RefuseType
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil || dec.Decode(&struct{}{}) != io.EOF {
		return nil, RefuseSchema
	}
	var ok bool
	switch p := v.(type) {
	case *PushPayload:
		ok = len(p.Bundle) > 0 && len(p.Bundle) <= MaxBundle
		return *p, refuseUnless(ok)
	case *PRPayload:
		n := utf8.RuneCountInString(p.Body)
		ok = line(p.Title, MaxPRTitle) && n >= MinPRBody && n <= MaxPRBody
		return *p, refuseUnless(ok)
	case *CommentPayload:
		ok = (p.Target == TargetPR || p.Target == TargetIssue) && strings.TrimSpace(p.Body) != "" && len(p.Body) <= MaxComment
		return *p, refuseUnless(ok)
	case *LabelsPayload:
		ok = len(p.Add) >= 1 && len(p.Add) <= MaxLabelsCall
		for i, l := range p.Add {
			ok = ok && slices.Contains(allow, l) && !slices.Contains(p.Add[:i], l)
		}
		return *p, refuseUnless(ok)
	case *IssuePayload:
		d, err := sanitize.CheckFinalDraft(p.Title, p.Body)
		if err != nil {
			return nil, RefuseSchema
		}
		return IssuePayload{Title: d.Title, Body: d.Body}, ""
	}
	return nil, RefuseType
}

func refuseUnless(ok bool) Refusal {
	if ok {
		return ""
	}
	return RefuseSchema
}

func line(s string, maxRunes int) bool {
	return strings.TrimSpace(s) != "" && !strings.ContainsAny(s, "\r\n") && utf8.RuneCountInString(s) <= maxRunes
}

// Counts is what one run has requested, per type: T1's cap half.
type Counts map[OutputType]int

// Take counts one request of t, or reports false at its cap without counting it.
func (c *Counts) Take(t OutputType) bool {
	limit, ok := caps[t]
	if !ok {
		return false
	}
	if *c == nil {
		*c = Counts{}
	}
	if (*c)[t] >= limit {
		return false
	}
	(*c)[t]++
	return true
}
