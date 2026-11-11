// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"io"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/intake"
)

// Plan writes plan/v1 as the view a person reads before anything is filed: the repository the plan
// files into, the ceiling its work is charged against and where that number came from, one line per
// issue, each issue's draft in a block of its own, and the question the broker must ask first.
//
// This is the one escaper applied to a model's text (CD18): a title is a code span, so it can never
// become a heading, a list item of its own or a link; a body is a block whose fence outruns every
// backtick in it, so the validator's own headings stay text; a question, an alternative and the
// evidence are escaped or quoted like any other untrusted line. label, when it is not empty, is the
// broker's sentence about the plan itself — `intake.TemplateLabel` for a degraded one — and is
// written first, so a reader cannot mistake a template draft for the planner's.
func Plan(w io.Writer, p intake.Plan, label string, o Options) error {
	if len(p.Gaps) > 0 {
		// A ranked plan still has one compatibility question. A legacy plan has no Gaps, so its own
		// Question is left untouched; neither mapping participates in the material hash.
		p = intake.WithGaps(p, p.Gaps)
	}
	md := o.Format == Markdown
	now := o.Now
	if now.IsZero() {
		now = p.Planner.At
	}
	var b strings.Builder

	if label != "" {
		b.WriteString(brokerLine(label, md, false) + "\n\n")
	}
	head := "Plan · " + clean(p.Repository) + " · " + clean(p.Hash)
	if md {
		head = "**Plan** · " + escapeTitle(p.Repository, false) + " · " + code(p.Hash, false)
	}
	b.WriteString(head + "\n")

	b.WriteString("Ceiling " + dollars(int64(math.Round(p.Budget.CeilingUSD*1e6))))
	if p.Budget.Source != "" {
		b.WriteString(" · " + brokerLine(p.Budget.Source, md, true))
	}
	b.WriteString("\n")
	if p.Planner.Model != "" {
		line := "Planner " + brokerLine(p.Planner.Model, md, true)
		if p.Planner.ServedModel != "" && p.Planner.ServedModel != p.Planner.Model {
			line += " (served by " + brokerLine(p.Planner.ServedModel, md, true) + ")"
		}
		if p.Planner.Surface != "" {
			line += " on " + brokerLine(p.Planner.Surface, md, true)
		}
		if !p.Planner.At.IsZero() {
			line += " · " + stamp(p.Planner.At, now)
		}
		b.WriteString(line + "\n")
	}

	if len(p.Issues) > 0 {
		b.WriteString("\n")
	}
	for _, is := range p.Issues {
		// The title is the model's: a code span, so that a "# " or a "](…)" in it is the text it
		// wrote and not the structure this view is made of.
		line := strconv.Itoa(is.Index) + ". " + untrusted(is.Title, md)
		if is.Class != "" {
			line += " · class " + brokerLine(is.Class, md, true)
		}
		if len(is.Roles) > 0 {
			line += " · roles " + brokerLine(strings.Join(is.Roles, ", "), md, true)
		}
		if len(is.DependsOn) > 0 {
			deps := make([]string, 0, len(is.DependsOn))
			for _, d := range is.DependsOn {
				deps = append(deps, "#"+strconv.Itoa(d))
			}
			line += " · waits for " + strings.Join(deps, ", ")
		}
		b.WriteString(line + "\n")
	}

	for _, is := range p.Issues {
		if strings.TrimSpace(is.Body) == "" {
			continue
		}
		b.WriteString("\nIssue " + strconv.Itoa(is.Index) + "\n")
		if md {
			b.WriteString(block(is.Body) + "\n")
		} else {
			b.WriteString(is.Body + "\n")
		}
	}

	if q := p.Question; q != nil {
		b.WriteString("\nQuestion " + untrusted(q.Text, md) + "\n")
		b.WriteString("Recommended " + untrusted(q.Recommended.Answer, md))
		if q.Recommended.Why != "" {
			b.WriteString(" · " + untrusted(q.Recommended.Why, md))
		}
		b.WriteString("\n")
	}
	for _, a := range p.Alternatives {
		b.WriteString("\nAlso fits " + untrusted(a.Repository, md))
		if a.Why != "" {
			b.WriteString(" · " + untrusted(a.Why, md))
		}
		b.WriteString("\n")
	}
	if len(p.RepositoryEvidence) > 0 {
		b.WriteString("\nWhy this repository")
		if slices.ContainsFunc(p.RepositoryEvidence, func(e intake.Evidence) bool { return e.Kind == intake.ModelClaim }) {
			// The broker reads no repository on this path: what the model says it read is its claim.
			b.WriteString(" (unverified: the planner's claims, nothing here was checked)")
		}
		b.WriteString("\n")
		for _, e := range p.RepositoryEvidence {
			// No URL: Evidence.URL is the broker's alone and this view builds none, so an evidence
			// item reads as the text it is (CD18, §security).
			b.WriteString("- " + untrusted(e.Text, md) + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// untrusted is a model's text as one inline value: a code span in Markdown, cleaned text otherwise.
// One line, never a paragraph of its own: a title, a question or an alternative is read inside the
// broker's sentence, not instead of it.
func untrusted(s string, md bool) string {
	if md {
		return code(s, false)
	}
	return quote(s)
}

// brokerLine is a value this package's own config or vocabulary wrote: as it is when it is one line
// of plain text, escaped when the caller is asking for a fixture's or an operator's arbitrary string.
// It never makes a link.
func brokerLine(s string, md, escape bool) string {
	s = clean(s)
	if !md || !escape {
		return s
	}
	return escapeTitle(s, false)
}

// block is s in a fenced block, the one place a multi-line untrusted body can sit without its own
// headings, lists or links taking effect: the fence is longer than any run of backticks in s.
func block(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
			continue
		}
		run = 0
	}
	fence := strings.Repeat("`", longest+1)
	if longest+1 < 3 {
		fence = "```"
	}
	return fence + "\n" + s + "\n" + fence
}
