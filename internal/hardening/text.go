// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Smana/maku/internal/factory/sanitize"
)

// MaxLinks is the live links a text output keeps; the rest are defused (T2).
const MaxLinks = 20

// TextReport counts what T2 rewrote, beside sanitize's own counts: never the text.
type TextReport struct {
	sanitize.Report
	Comments int // HTML comments removed
	Mentions int // @ mentions neutralised
	Links    int // links past MaxLinks defused
	Closes   int // closing keywords naming another issue, made Refs
}

var (
	htmlComment = regexp.MustCompile(`(?s)<!--.*?(-->|$)`)
	mention     = regexp.MustCompile("(^|[^\\w`@/])@([A-Za-z0-9][A-Za-z0-9-]{0,38}(/[A-Za-z0-9._-]+)?)")
	link        = regexp.MustCompile(`(?i)\bhttps?://`)
	closing     = regexp.MustCompile(`(?i)\b(close[sd]?|fix(?:e[sd])?|resolve[sd]?)(:?\s+)((?:[\w.-]+/[\w.-]+)?#(\d+)|https://github\.com/[\w.-]+/[\w.-]+/issues/(\d+))`)
)

// Published is the payload an applied request of type t published, before the factory's footer or
// marker: T1's decode, then T2, as Check runs them. The tracer compares an observed write with it.
// false when the payload does not decode.
func Published(t OutputType, payload json.RawMessage, ownIssue int) (any, bool) {
	p, r := DecodeOutput(t, payload, nil)
	if r != "" || p == nil {
		return nil, false
	}
	p, _ = sanitiseOutput(p, ownIssue)
	return p, true
}

// sanitiseOutput is T2 on every text field of p.
func sanitiseOutput(p any, ownIssue int) (any, TextReport) {
	var rep TextReport
	s := func(t string) string { return sanitiseText(t, ownIssue, &rep) }
	switch o := p.(type) {
	case PRPayload:
		o.Title, o.Body = s(o.Title), s(o.Body)
		return o, rep
	case CommentPayload:
		o.Body = s(o.Body)
		return o, rep
	case IssuePayload:
		o.Title, o.Body = s(o.Title), s(o.Body)
		return o, rep
	}
	return p, rep
}

// sanitiseText removes HTML comments before sanitize.Text (which strips invisible and control
// characters, folds NFKC and defuses images and raw HTML), then neutralises what GitHub acts on:
// mentions notify, a closing keyword closes another issue, a link list exfiltrates.
func sanitiseText(s string, ownIssue int, rep *TextReport) string {
	s = htmlComment.ReplaceAllStringFunc(s, func(string) string { rep.Comments++; return "" })
	s, r := sanitize.Text(s)
	rep.Invisible += r.Invisible
	rep.Control += r.Control
	rep.Images += r.Images
	rep.Markup += r.Markup
	rep.Fences += r.Fences
	if r.Withheld {
		rep.Withheld = true
		return s
	}
	s = mention.ReplaceAllStringFunc(s, func(m string) string {
		rep.Mentions++
		g := mention.FindStringSubmatch(m)
		return g[1] + "`@" + g[2] + "`"
	})
	own := fmt.Sprintf("#%d", ownIssue)
	s = closing.ReplaceAllStringFunc(s, func(m string) string {
		g := closing.FindStringSubmatch(m)
		if ownIssue > 0 && g[3] == own {
			return m
		}
		rep.Closes++
		return "Refs" + g[2] + g[3]
	})
	n := 0
	return link.ReplaceAllStringFunc(s, func(m string) string {
		if n++; n <= MaxLinks {
			return m
		}
		rep.Links++
		return strings.Replace(strings.ToLower(m), "http", "hxxp", 1)
	})
}
