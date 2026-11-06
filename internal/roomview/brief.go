// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/summary"
)

// PlanVersion is which plan ran: the proposal a task was filed from (the plan tool's version and hash,
// filled by that plan), or the scoper's own scope state. Source is "proposal", "scope", or "" for
// neither.
type PlanVersion struct {
	Source          string
	Rev             int
	Hash            string
	Class           string
	CeilingMicroUSD int64
	CeilingSource   string
}

// BriefIn is everything the brief renders. Every string that an agent, a person or GitHub wrote is
// untrusted.
type BriefIn struct {
	Room    string
	RoomURL string
	Issue   *envelope.IssueFact
	PR      *envelope.PRFact
	Facts   *envelope.TaskFacts
	Plan    PlanVersion
	// Scope is the last handoff's scope (SC6): the state the plan line prints and the acceptance
	// checks the brief quotes. Nil when no handoff carried one.
	Scope   *envelope.Scope
	Since   time.Time // the cursor's time; zero: no "Changed since" header
	Changed ChangeSet
	Blocker *summary.Need
	// Sealed is the room's seal ("" while open; summary.State.Sealed's two values): a sealed log takes
	// no event, so its dollar figure is the last a reader will see and is not settling (CD23).
	Sealed string
	// Cause is summary.Cause's code and is rendered as one "Fix:" line. It is empty here: the table
	// that produces it lands with brief.ciEvidence (PR 7), and until then the line never appears.
	Cause string
	Now   time.Time
}

// Brief writes the resume brief: fixed prose, quoted data (CD11). The plan version is display only;
// nothing here decides anything, and no line carries a command that acts. A section whose data is
// absent is left out, so one renderer serves a room with facts, one without a cursor and one whose
// title is hostile.
func Brief(w io.Writer, in BriefIn, o Options) error {
	c := &card{o: o}
	c.briefTitle(in)
	// Three blocks, blank-line separated: what was asked, the changes (a list, which would swallow the
	// lines after it by lazy continuation), and where the room stands. Each goes in its own card so a
	// block with no data leaves no blank line behind.
	head, changed, tail := &card{o: o}, &card{o: o}, &card{o: o}
	head.briefAsked(in)
	head.briefAcceptance(in)
	head.briefPlan(in)
	changed.briefChanged(in)
	tail.briefBlocked(in)
	tail.briefFix(in)
	tail.briefNext(in)
	tail.briefLinks(in)
	for _, b := range []*card{head, changed, tail} {
		if b.b.Len() == 0 {
			continue
		}
		c.line("")
		c.b.WriteString(b.b.String())
	}
	_, err := io.WriteString(w, c.b.String())
	return err
}

// briefTitle is the first line: the room, then the issue and its title, as the frame's one bold line.
// The title is escaped plain text, as the card escapes it into its heading.
func (c *card) briefTitle(in BriefIn) {
	h := "Resume: " + c.token(in.Room, false)
	if i := in.Issue; i != nil && i.Number > 0 {
		h += " · #" + strconv.Itoa(i.Number)
		if clean(i.Title) != "" {
			h += " " + c.text(i.Title, false)
		}
	}
	c.line(c.bold(h))
}

// briefAsked is what the work was asked for: the issue, then its title in prose. A conventional
// commit's type prefix ("docs: ", "fix(x)!: ") is the headline's, not the ask's, so it is dropped here
// and kept on the title line above — the ask reads as the sentence a person wrote.
func (c *card) briefAsked(in BriefIn) {
	i := in.Issue
	if i == nil || i.Number <= 0 || clean(i.Title) == "" {
		return
	}
	c.line("Asked: #" + strconv.Itoa(i.Number) + ", " + c.text(askOf(i.Title), false))
}

// askType is a conventional commit's type prefix: a short lowercase type, an optional scope, an
// optional "!" and ": ".
var askType = regexp.MustCompile(`^[a-z]{2,12}(\([^)]{1,20}\))?!?: `)

// askOf is a title as the ask reads it: without its conventional-commit prefix, and never emptied by
// the removal (a title that is nothing but a prefix stays as it is).
func askOf(title string) string {
	if rest := askType.ReplaceAllString(clean(title), ""); rest != "" {
		return rest
	}
	return title
}

// briefAcceptance is the scope's checks, each in a code span: an agent wrote them, so a span is the one
// place they sit. Proves is dropped: the check is what a reader re-runs.
func (c *card) briefAcceptance(in BriefIn) {
	if in.Scope == nil {
		return
	}
	var checks []string
	for _, a := range in.Scope.Acceptance {
		if clean(a.Check) != "" {
			checks = append(checks, c.quoted(a.Check, false))
		}
	}
	if len(checks) == 0 {
		return
	}
	c.line("Acceptance: " + strings.Join(checks, ", "))
}

// briefPlan is which plan ran and what it was allowed to spend. The scope half reads the scope it
// names: the state it prints is the scope's, so a caller that hands over a scope needs no second field
// to say which plan ran (SC6).
func (c *card) briefPlan(in BriefIn) {
	var parts []string
	switch {
	case in.Plan.Source == "proposal":
		s := "proposal"
		if in.Plan.Rev > 0 {
			s += " v" + strconv.Itoa(in.Plan.Rev)
		}
		if h := clean(in.Plan.Hash); h != "" {
			s += " " + c.token(h, false)
		}
		parts = append(parts, s)
	case in.Scope != nil:
		s := "scope"
		if st := clean(in.Scope.State); st != "" {
			s += " " + c.token(st, false)
		}
		parts = append(parts, s)
	}
	if in.Plan.CeilingMicroUSD > 0 {
		parts = append(parts, "budget "+dollars(in.Plan.CeilingMicroUSD)+" ceiling")
	}
	// CD23: the dollars' state, as the card says it. The brief has no Summary, so it reads the facts:
	// a finished task names its figure only once the factory has claimed it final, says it is settling
	// until then, and says it is unpriced when the meter priced none of its runs — the case where no
	// final figure is ever posted, so "settling" would promise a change that never comes.
	if w := briefSpend(in.Facts, in.Sealed != ""); w != "" {
		parts = append(parts, w)
	}
	if len(parts) == 0 {
		return
	}
	c.line("Plan: " + strings.Join(parts, " · "))
}

// briefSpend is a task's dollars for the brief's plan line: the figure once it is final (or once the
// room is sealed and can take no other), else the two words the card uses (CD23), "" while the task runs
// or has nothing to report.
func briefSpend(f *envelope.TaskFacts, sealedRoom bool) string {
	if f == nil || !over(summary.Coarse(f.Phase, f.Phase, false)) {
		return ""
	}
	used := int64(0)
	if f.Spend != nil {
		used = f.Spend.UsedMicroUSD
	}
	switch {
	case f.SpendFinal || sealedRoom:
		if used > 0 {
			return "spend " + dollars(used)
		}
		return ""
	case used > 0:
		return spendSettling
	case unpriced(f.Spend, f.Budget):
		return spendUnpriced
	}
	return ""
}

// briefChanged is what happened since the cursor, one line per milestone, through the card's own
// milestone lines: the brief quotes changes exactly as view 4 does, in one vocabulary. With no cursor
// there is no "Changed since" header to print — the lines stand on their clock alone.
func (c *card) briefChanged(in BriefIn) {
	if len(in.Changed.Items) == 0 {
		return
	}
	if !in.Since.IsZero() {
		c.line("Changed since " + stamp(in.Since, c.o.Now) + " (" + span(c.o.Now.Sub(in.Since)) + "):")
	}
	for _, m := range in.Changed.Items {
		c.line("- " + clock(m.At, in.Since) + rangeEnd(m, in.Since) + " " + c.milestone(m))
	}
	if in.Changed.More > 0 {
		c.line("- and " + strconv.Itoa(in.Changed.More) + " more")
	}
}

// briefBlocked is the one need that stands in the way: its kind, the broker's own word in a code span,
// then what it is in the broker's prose.
func (c *card) briefBlocked(in BriefIn) {
	n := in.Blocker
	if n == nil {
		return
	}
	kind, what := clean(n.Kind), c.text(n.What, false)
	switch {
	case kind == "" && what == "":
		return
	case kind == "":
		c.line("Blocked on: " + what)
	case what == "":
		c.line("Blocked on: " + c.quoted(kind, false))
	default:
		c.line("Blocked on: " + c.quoted(kind, false) + " — " + what)
	}
}

// briefFix is the one line a cause earns: summary.Cause's code, shown as prose. It is the hook PR 7
// fills with summary.Fix's sentence; no golden carries it until then.
func (c *card) briefFix(in BriefIn) {
	if in.Cause == "" {
		return
	}
	c.line("Fix: " + c.text(in.Cause, false))
}

// briefNext is the one thing to do and where: the action's own words, whose code spans are the
// broker's, then the URL it happens at. The URL is printed bare — Markdown links it — and only when the
// broker built it: a URL off the allowlist is dropped rather than printed (it would autolink).
func (c *card) briefNext(in BriefIn) {
	n := in.Blocker
	if n == nil {
		return
	}
	label, url := c.broker(n.Action.Label), n.Action.URL
	if url == "" {
		url = n.URL
	}
	if !allowed(url, c.o) {
		url = ""
	}
	switch {
	case label != "" && url != "":
		c.line("Next: " + label + " → " + url)
	case url != "":
		c.line("Next: " + url)
	case label != "":
		c.line("Next: " + label)
	}
}

// briefLinks is where this room's work lives: the room page, the issue and the pull request, each only
// when the broker built its URL. The URLs are printed bare — Markdown links them, and assertInert holds
// every autolink to the allowlist — never taken from an agent's text.
func (c *card) briefLinks(in BriefIn) {
	var parts []string
	if allowed(in.RoomURL, c.o) {
		parts = append(parts, "room "+in.RoomURL)
	}
	if i := in.Issue; i != nil && allowed(i.URL, c.o) {
		parts = append(parts, "issue "+i.URL)
	}
	if p := in.PR; p != nil && allowed(p.URL, c.o) {
		parts = append(parts, "PR "+p.URL)
	}
	if len(parts) == 0 {
		return
	}
	c.line("Links: " + strings.Join(parts, " · "))
}

// Untrusted is the strings of in that an agent wrote: the blocker's words, and the changed items whose
// text is an agent's — a note, a handoff, an approval's question, a judge's reason (ChangeSet's own
// rule). Each belongs in a code span or in escaped prose, never in a structural node, and the goldens
// hold it there (offline proof 8). The issue's title is not listed: the brief escapes it into its bold
// title line, as the card escapes it into a heading.
func (in BriefIn) Untrusted() []string {
	var out []string
	if in.Blocker != nil {
		out = append(out, in.Blocker.What)
	}
	for _, m := range in.Changed.Items {
		switch m.Kind {
		case kindNote, kindHandoff, kindJudge, kindApprovalRequested:
			out = append(out, m.Text)
		}
	}
	return out
}
