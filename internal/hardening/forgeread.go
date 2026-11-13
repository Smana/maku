// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"context"
	"unicode/utf8"

	"github.com/Smana/maku/internal/factory/sanitize"
)

// TargetChecks is forge_read's third target beside TargetIssue and TargetPR (UH11): the run's own
// branch's checks, never another's.
const TargetChecks = "checks"

// ValidTarget reports whether s is one of forge_read's targets.
func ValidTarget(s string) bool { return s == TargetIssue || s == TargetPR || s == TargetChecks }

// ForgeText is one human-written object on the run's issue or pull request.
type ForgeText struct {
	ID      int64
	Kind    string // issue | pull_request | comment | review
	Author  Author
	Body    string
	Vouched bool // factory/vouch on the object, or the issue the trigger label vouched for
	// Source is the kind the item is recorded as: SourceIssue for the task's issue, SourceComment
	// when unset.
	Source SourceKind
}

// CheckText is one check run of the run's branch with its log excerpt. Foreign is a branch another
// author pushed, whose CI log is that author's text.
type CheckText struct {
	Name, Conclusion, Log string
	Foreign               bool
}

// ForgeItem is one item a run reads, with its author's level and never more than MaxForgeBody.
type ForgeItem struct {
	ID         int64  `json:"id,omitempty"`
	Kind       string `json:"kind"`
	Name       string `json:"name,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
	Author     string `json:"author,omitempty"`
	Trust      Level  `json:"trust"`
	Body       string `json:"body"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// ForgeReadResult is forge_read's answer: the items read, a count of those withheld, and the
// provenance the factory records (UH7).
type ForgeReadResult struct {
	Items      []ForgeItem `json:"items"`
	Withheld   string      `json:"withheld,omitempty"`
	InputTrust InputTrust  `json:"inputTrust"`
}

// ForgeReads is forge_read's port (HR35): the factory reads the run's own issue, pull request or
// checks with its App and filters them, so withheld text never leaves the factory. The broker
// passes the verified caller's run, never an argument.
type ForgeReads interface {
	ForgeRead(ctx context.Context, run, target string) (ForgeReadResult, error)
}

// MaxForgeBody bounds one item's body as the room's reads are bounded. TruncatedMark ends a thread
// item cut at its head; TruncatedHead starts a check log cut to its tail, where the failure is.
const (
	MaxForgeBody  = 16 << 10
	TruncatedMark = "\n[truncated]"
	TruncatedHead = "[truncated]\n"
)

// FilterThread is the read filter (UH6, UH11), one pure function: text below p.Min, and a blocked
// author's always, is withheld and counted, never returned; the rest is sanitised, cut at
// MaxForgeBody and levelled.
func FilterThread(p Policy, ts []ForgeText) ForgeReadResult {
	res := ForgeReadResult{Items: []ForgeItem{}, InputTrust: NewInputTrust()}
	withheld := 0
	for _, t := range ts {
		kind := t.Source
		if kind == "" {
			kind = SourceComment
		}
		l := p.LevelOf(t.Author, t.Vouched)
		if p.Withholds(l) {
			withheld++
			res.InputTrust.Withhold(kind)
			continue
		}
		res.add(ForgeItem{ID: t.ID, Kind: t.Kind, Author: t.Author.Login}, kind, l, t.Body, false)
	}
	res.Withheld = Placeholder(withheld, "item")
	return res
}

// FilterChecks levels check logs (UH7): the run's own branch's CI is approved, a branch another
// author pushed is none, read but lowering the run's minimum.
func FilterChecks(cs []CheckText) ForgeReadResult {
	res := ForgeReadResult{Items: []ForgeItem{}, InputTrust: NewInputTrust()}
	for _, ch := range cs {
		kind := SourceOwnCI
		if ch.Foreign {
			kind = SourceForeignCI
		}
		res.add(ForgeItem{Kind: "check", Name: ch.Name, Conclusion: ch.Conclusion}, kind, kind.Level(), ch.Log, true)
	}
	return res
}

// add sanitises and bounds body, keeping its head, or its tail for a log, then records the item and
// its provenance.
func (r *ForgeReadResult) add(it ForgeItem, kind SourceKind, l Level, body string, tail bool) {
	body, _ = sanitize.Text(body)
	if len(body) > MaxForgeBody && tail {
		cut := len(body) - MaxForgeBody
		for cut < len(body) && !utf8.RuneStart(body[cut]) {
			cut++
		}
		body, it.Truncated = TruncatedHead+body[cut:], true
	} else if len(body) > MaxForgeBody {
		cut := MaxForgeBody
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body, it.Truncated = body[:cut]+TruncatedMark, true
	}
	it.Trust, it.Body = l, body
	r.Items = append(r.Items, it)
	r.InputTrust.Record(kind, l)
}

// WithheldCount is the result's total of withheld items, across kinds.
func (r ForgeReadResult) WithheldCount() int {
	n := 0
	for _, c := range r.InputTrust.Withheld {
		n += c
	}
	return n
}
