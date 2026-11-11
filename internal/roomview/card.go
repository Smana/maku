// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/summary"
)

// maxCardNotes is how many of the agents' notes the card quotes.
const maxCardNotes = 5

// card writes one view in one format: the two differ in how they quote and link, not in what
// they say.
type card struct {
	b strings.Builder
	o Options
	// over is whether the task is done, failed or stopped: a run it left unfinished is not running.
	over bool
}

func (c *card) md() bool { return c.o.Format == Markdown }

// slack is whether the view is the one Slack reads: Slack's mrkdwn, where a < opens a link or a
// mention unless it is escaped (CD7).
func (c *card) slack() bool { return c.o.Format == Slack }

// slackEscaper is slackapp's own escaping of user text (slackText), character for character: a
// message built here is as inert as one built there, and the two cannot disagree about which
// characters are.
var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "```", "'''")

// slackInert is s on one line and escaped: the state a piece of untrusted text is in before it
// reaches a Slack message, when it must not be cut.
func slackInert(s string) string { return slackEscaper.Replace(strip(s)) }

// text is untrusted words as plain text: escaped in Markdown, cleaned on a terminal, and escaped
// with Slack's three in Slack.
func (c *card) text(s string, cell bool) string {
	switch {
	case c.md():
		return escapeTitle(s, cell)
	case c.slack():
		return slackEscaper.Replace(clean(s))
	}
	return clean(s)
}

// quoted is untrusted text, quoted: in a code span in Markdown, in double quotes on a terminal, and
// on one uncut line in Slack, whose text is inert by escaping rather than by a code span.
func (c *card) quoted(s string, cell bool) string {
	switch {
	case c.md():
		return code(s, cell)
	case c.slack():
		return slackInert(s)
	}
	return quote(s)
}

// plainToken is a value from the broker's own vocabulary: as is when it is made of nothing but
// identifier characters (a run id, a version), quoted otherwise.
var plainToken = regexp.MustCompile(`^[A-Za-z0-9.+/-]{1,64}$`)

func (c *card) token(s string, cell bool) string {
	if plainToken.MatchString(s) && !autolinkish(s) {
		if c.md() && strings.HasPrefix(s, "-") {
			return code(s, cell)
		}
		return s
	}
	if c.md() {
		return code(s, cell)
	}
	return clean(s)
}

// words is a phrase of the vocabulary, or the cleaned value escaped when the vocabulary lacks it.
func (c *card) words(raw, got string, cell bool) string {
	if got == clean(raw) {
		return c.text(got, cell)
	}
	return got
}

// bold marks s in Markdown, in a terminal that shows colour, and with Slack's single asterisk.
func (c *card) bold(s string) string {
	switch c.o.Format {
	case Markdown:
		return "**" + s + "**"
	case TextColor:
		return "\x1b[1m" + s + "\x1b[0m"
	case Slack:
		return "*" + s + "*"
	}
	return s
}

// head is a section's title, in bold in Markdown and plain on a terminal.
func (c *card) head(s string) string {
	if c.md() {
		return "**" + s + "**"
	}
	return s
}

// url links text to url, or leaves the text alone when the broker did not build url.
func (c *card) url(text, url string) string {
	if !allowed(url, c.o) {
		return text
	}
	switch {
	case c.md():
		return "[" + text + "](" + url + ")"
	case c.slack():
		return "<" + url + "|" + text + ">"
	}
	return text + " (" + url + ")"
}

func (c *card) line(s string) { c.b.WriteString(s + "\n") }

// Card writes view 2, the room card, with view 3, its runs line. A line whose data is absent is
// left out. It prints no cursor: the caller that needs one has the summary.
func Card(w io.Writer, s summary.Summary, o Options) error {
	c := &card{o: o}
	c.over = over(cardState(s))
	needs := len(s.NeedsYou) > 0
	c.status(s)
	c.title(s)
	c.stateLine(s, needs)
	c.step(s)
	c.facts(s)
	c.coordination(s)
	c.needs(s)
	c.runs(s)
	c.notes(s)
	c.queued(s)
	c.ranOn(s)
	c.actions(s)
	_, err := io.WriteString(w, strings.TrimRight(c.b.String(), "\n")+"\n")
	return err
}

// status is the room's status at first sight: the line the page's strip leads with, from the same
// function (summary.StatusLine). Fixed words and numbers only, so it needs no escaping.
func (c *card) status(s summary.Summary) {
	st := summary.StatusOf(s, c.o.Now)
	if st.Kind == "" {
		return
	}
	line := st.String()
	if c.md() {
		line = c.bold(line)
		c.line(line)
		c.line("")
		return
	}
	c.line(c.bold(line))
}

// title leads with the task, `#42 title`, as a developer names a room, and ends with the room id.
func (c *card) title(s summary.Summary) {
	room := c.text(s.Room, false)
	h := room
	if i := s.Status.Issue; i != nil && i.Number > 0 {
		h = "#" + strconv.Itoa(i.Number)
		if clean(i.Title) != "" {
			h += " " + c.text(i.Title, false)
		}
		h += " · " + room
	}
	if c.md() {
		h = "### " + h
	}
	c.line(h)
}

func (c *card) stateLine(s summary.Summary, needs bool) {
	word := stateWord(cardState(s), needs)
	if needs {
		word = c.bold(word)
	}
	parts := []string{word}
	if p := clean(s.Status.Phase); p != "" {
		parts = append(parts, c.text(phaseWords(p), false))
	}
	if r := repoOf(urlOf(s.Status.Issue), prURL(s.Status.PR)); r != "" {
		parts = append(parts, c.token(r, false))
	}
	if allowed(s.URL, c.o) {
		parts = append(parts, c.url("open the room", s.URL))
	}
	c.line(strings.Join(parts, " · "))
}

// step is the one thing the caller does for a waiting room, and the link to do it.
func (c *card) step(s summary.Summary) {
	st := s.Step
	if st == nil {
		return
	}
	label := c.url(st.Label, st.URL)
	if c.md() {
		c.line("") // its own paragraph: a Markdown line break would join it to the state line
		c.line(c.bold("Your move:") + " " + label)
		return
	}
	c.line("Your move: " + label)
}

// cardState is the coarse state the summary carries, else the one its phase gives.
func cardState(s summary.Summary) string {
	if s.Status.State != "" {
		return s.Status.State
	}
	return summary.Coarse(s.Status.Phase, s.Status.Phase, false)
}

// over is whether a coarse state is a task that ended: the states whose dollars CD23's final spend is
// the only thing left to settle.
func over(state string) bool {
	switch state {
	case summary.StateDone, summary.StateFailed, summary.StateStopped:
		return true
	}
	return false
}

// sealed is a room whose log is closed or sealed, the two values summary.State.Sealed carries: it
// refuses every event, so the dollar figure in it is the last its reader will see, final claim or not.
func sealed(phase string) bool { return phase == "Closed" || phase == "Sealed" }

func urlOf(i *envelope.IssueFact) string {
	if i == nil {
		return ""
	}
	return i.URL
}

func prURL(p *envelope.PRFact) string {
	if p == nil {
		return ""
	}
	return p.URL
}

// facts writes the label/value rows: a table in Markdown, aligned lines on a terminal.
func (c *card) facts(s summary.Summary) {
	var rows [][2]string
	if r := s.Status.Run; r != nil {
		rows = append(rows, [2]string{"Run", c.runFact(*r, runModel(s, r.ID))})
	}
	if v := c.spend(s); v != "" {
		rows = append(rows, [2]string{"Spend", v})
	}
	if v := c.prFact(s); v != "" {
		rows = append(rows, [2]string{"PR", v})
	}
	if j := s.Status.LastJudge; j != nil {
		rows = append(rows, [2]string{"Judge", c.words(j.Verdict, judgeWords(j.Verdict), c.md()) + " " + c.quoted(j.Reason, c.md()) +
			" (reported by the run: the completion judge's reading, at " + stamp(j.At, c.o.Now) + ")"})
	}
	if s.Status.Next != "" {
		rows = append(rows, [2]string{"Next", c.broker(s.Status.Next)})
	}
	if s.Status.Reason != "" {
		rows = append(rows, [2]string{"Why", c.text(s.Status.Reason, c.md())})
	}
	if len(rows) == 0 {
		return
	}
	c.line("")
	if c.md() {
		c.line("| | |\n|---|---|")
		for _, r := range rows {
			c.line("| " + r[0] + " | " + r[1] + " |")
		}
		return
	}
	for _, r := range rows {
		c.line(r[0] + strings.Repeat(" ", 6-utf8.RuneCountInString(r[0])) + r[1])
	}
}

// broker is a sentence the broker wrote (what happens next, how to merge): on one line but never
// cut, as it may end in the command, and without a pipe in a table cell. It may hold code spans of
// its own, so it is never put inside another.
func (c *card) broker(s string) string {
	s = strings.ReplaceAll(strip(s), "|", cellPipe)
	if !c.md() {
		s = strings.ReplaceAll(s, "`", "") // a terminal shows no code spans
	}
	return s
}

// command is a command the broker composed, in a code span in Markdown and plain on a terminal,
// never cut: a cut command does not run.
func (c *card) command(s string) string {
	if c.md() {
		return fence(strip(s), false)
	}
	return strip(s)
}

// span is a value in a code span in Markdown, plain on a terminal.
func (c *card) span(s string) string {
	if c.md() {
		return code(s, false)
	}
	return clean(s)
}

// mergeLine is the merge need as a sentence built here, from its parts: the repository is read from
// the pull request's URL (a GitHub one), the number from the need's id. Summary's own text
// interpolates both unescaped, and a repository may be named "_www.x" or "_x_", so it is not used.
// The one answer it takes from the need is Need.Approve: an approve-to-merge hand-off's line says
// approve and never the bypass, so no path through here prints --admin for one (AM3, fast-follow 1).
// ok is false for a need whose parts are not what they should be.
func (c *card) mergeLine(n summary.Need) (string, bool) {
	if !digits(n.ID) {
		return "", false
	}
	repo := repoOf(n.URL)
	if n.Approve { // an approval returns the hand-off (AM3): approve the head read, by its commit (#301)
		s := "a maintainer approves PR #" + n.ID + ": approve the head you read on the PR page"
		if repo != "" {
			s += ", or run " + c.command("gh api repos/"+repo+"/pulls/"+n.ID+"/reviews -f commit_id=<sha> -f event=APPROVE")
		}
		return s + "; the merger merges that head", true
	}
	s := "a maintainer with admin on "
	if repo != "" {
		s += c.span(repo) + " "
	} else {
		s = "a maintainer with admin on the repository "
	}
	s += "merges PR #" + n.ID + `: the ruleset blocks a plain merge, so tick "bypass rules" on the PR page`
	if repo != "" {
		s += ", or run " + c.command("gh pr merge "+n.ID+" --squash --admin --repo "+repo)
	}
	return s, true
}

// answerLine is the answer need as a sentence built here: the author is named only when it is a
// GitHub login, in a code span.
func (c *card) answerLine(n summary.Need) (string, bool) {
	if !digits(n.ID) {
		return "", false
	}
	who := "the issue's author"
	if n.Author != "" && envelope.ValidLogin(n.Author) {
		who = c.span("@" + n.Author)
	}
	return who + " answers the scoper's question by editing issue #" + n.ID + ", then a maintainer re-applies " +
		c.span("factory/ready") + "; or a maintainer comments " + c.span("/factory retry") + " to proceed on its assumption", true
}

func digits(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }

// runModel is the model and effort the run with this id told its room: the present run's or the
// last's, nil when neither is that run's.
func runModel(s summary.Summary, id string) *summary.RunModel {
	for _, m := range []*summary.RunModel{s.Status.RunModel, s.Status.LastRunModel} {
		if m != nil && m.Run == id {
			return m
		}
	}
	return nil
}

func (c *card) runFact(r envelope.RunFact, m *summary.RunModel) string {
	s := c.text(r.Role, c.md()) + " " + c.token(r.ID, c.md())
	if t := clean(r.Trigger); t != "" {
		s += " (" + c.words(r.Trigger, triggerWords(r.Trigger), c.md()) + ")"
	}
	if m != nil {
		for _, v := range []string{m.Model, m.Effort} {
			if v != "" {
				s += " · " + c.token(v, c.md())
			}
		}
	}
	switch {
	case r.StartedAt.IsZero():
	case r.Outcome != "" || !r.EndedAt.IsZero() || c.over:
		// The factory's facts say it ended (or the task is over): when, never "N min ago".
		s += " · started " + stamp(r.StartedAt, c.o.Now) + ", ended"
		if !r.EndedAt.IsZero() {
			s += " " + stamp(r.EndedAt, c.o.Now)
		}
		if r.Outcome != "" && r.Outcome != "ended" {
			s += " (" + c.words(r.Outcome, outcomeWords(r.Outcome), c.md()) + ")"
		}
	default:
		s += " · started " + stamp(r.StartedAt, c.o.Now) + ", " + ago(r.StartedAt, c.o.Now)
	}
	return s
}

// spend is the task's dollars and tokens. A spend of 0 says nothing (an unpriced run costs 0), so
// the tokens read on their own, against their limit.
//
// CD23: a task that ended names its dollars only once the factory says they are final. Until then the
// figure may still move, so the row says it is settling rather than show a number it would have to
// change — and a task whose runs the meter could not price has no figure and no final fact coming, so
// it says they are unpriced instead of settling for ever. A sealed room is the exception: its log takes
// no event, so the figure in it is the last its reader will see, final claim or not.
func (c *card) spend(s summary.Summary) string {
	sp, b := s.Status.Spend, s.Status.Budget
	if c.over && !s.Status.SpendFinal && !sealed(s.Status.Phase) {
		switch {
		case sp != nil && sp.UsedMicroUSD > 0:
			return joined(spendSettling, countOf(b))
		case unpriced(sp, b):
			return joined(spendUnpriced, tokensWords(b))
		}
		return ""
	}
	if sp != nil && sp.UsedMicroUSD > 0 {
		out := dollars(sp.UsedMicroUSD)
		if sp.LimitMicroUSD > 0 {
			out += fmt.Sprintf(" of %s (%d %%)", dollars(sp.LimitMicroUSD), percent(sp.UsedMicroUSD, sp.LimitMicroUSD))
		}
		return joined(out, countOf(b))
	}
	return tokensWords(b)
}

// The two things a task's dollars say before the factory claims a figure final (CD23).
const (
	spendSettling = "spend settling"
	spendUnpriced = "spend unpriced"
)

// unpriced is a terminal task's dollars with no figure to report: it spent tokens, and the meter priced
// none of them, so the factory withholds its final fact (allPriced) and no figure is coming. A run the
// meter did price carries its dollars, so tokens with no sum to their name is no figure at all — the
// card says so rather than promise dollars that are still settling.
func unpriced(sp *envelope.SpendFact, b *envelope.BudgetFact) bool {
	return (sp == nil || sp.UsedMicroUSD <= 0) && b != nil && b.UsedTokens > 0
}

// joined is the parts that are not empty, " · " between them.
func joined(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " · ")
}

// countOf is the task's tokens as a bare count, beside its dollars: "" when it spent none.
func countOf(b *envelope.BudgetFact) string {
	if b == nil || b.UsedTokens <= 0 {
		return ""
	}
	return tokens(b.UsedTokens) + " tokens"
}

// tokensWords is the tokens alone, against their limit: what a task with no dollar figure reports.
func tokensWords(b *envelope.BudgetFact) string {
	if b == nil || b.UsedTokens <= 0 && b.LimitTokens <= 0 {
		return ""
	}
	if b.LimitTokens > 0 {
		return fmt.Sprintf("%s of %s tokens (%d %%)", tokens(b.UsedTokens), tokens(b.LimitTokens), percent(b.UsedTokens, b.LimitTokens))
	}
	return tokens(b.UsedTokens) + " tokens"
}

func percent(used, limit int64) int64 { return (100*used + limit/2) / limit }

func (c *card) prFact(s summary.Summary) string {
	var parts []string
	if p := s.Status.PR; p != nil && p.Number > 0 {
		label := "#" + strconv.Itoa(p.Number)
		if c.md() && allowed(p.URL, c.o) {
			label = "[" + label + "](" + p.URL + ")"
		} else if allowed(p.URL, c.o) {
			label += " (" + p.URL + ")"
		}
		parts = append(parts, label)
	}
	if v := s.Status.LastVerdict; v != nil {
		parts = append(parts, c.text(v.By, c.md())+" "+c.words(v.Verdict, verdictWords(v.Verdict), c.md())+" at "+stamp(v.At, c.o.Now))
	}
	return strings.Join(parts, " · ")
}

// needLine is the sentence build makes for n, else the need's untrusted-looking text in a span: a
// need whose parts are off is not rendered as the broker's prose.
func (c *card) needLine(n summary.Need, build func(summary.Need) (string, bool)) string {
	if s, ok := build(n); ok {
		return s
	}
	return c.quoted(n.What, false)
}

func (c *card) needs(s summary.Summary) {
	if len(s.NeedsYou) == 0 {
		return
	}
	c.line("")
	c.line(c.head("Needs you"))
	for i, n := range s.NeedsYou {
		var item, label string
		switch n.Kind {
		case "approval":
			item, label = "approve "+c.quoted(n.What, false), "decide in the room"
			if !n.Deadline.IsZero() {
				item += " by " + stamp(n.Deadline, c.o.Now)
			}
		case "merge":
			item = c.needLine(n, c.mergeLine)
			label = "open the pull request"
			if n.Approve { // an approval of the head is the action, and the merger does the merge (AM3)
				label = "approve the head on the pull request"
			}
		case summary.NeedQuestion:
			item, label = c.needLine(n, c.answerLine), "answer on the issue"
		case "hold":
			item, label = c.broker(n.What), "open the room"
		default:
			item, label = c.quoted(n.What, false), "open the room"
		}
		if allowed(n.URL, c.o) {
			if c.md() {
				item += " → " + c.url(label, n.URL)
			} else {
				item += " → " + n.URL
			}
		}
		c.line(strconv.Itoa(i+1) + ". " + item)
	}
}

// runs is view 3, the runs line: every run, oldest first, one phrase each.
func (c *card) runs(s summary.Summary) {
	if len(s.Runs) == 0 {
		return
	}
	nodes := map[string]summary.FlowNode{}
	if s.Flow != nil {
		for _, n := range s.Flow.Nodes {
			nodes[n.Run] = n
		}
	}
	parts := make([]string, len(s.Runs))
	for i, r := range s.Runs {
		n := nodes[r.ID]
		parts[i] = c.runPhrase(r, n.Steered, n.Chat)
	}
	c.line("")
	c.line(c.head("Runs") + " " + strings.Join(parts, " → "))
}

// runPhrase is a run as the line reads it: its role, its trigger, how it ended, how long it took
// and what it cost. A run with no end and no outcome is running, and its time counts to Now. An
// outcome the factory set without an end (a stopped task's last run) is that outcome, never
// running, and has no time to count. Steered and chat are the humans' messages counted on the
// run's flow node, shown after the time and left out at zero.
func (c *card) runPhrase(r envelope.RunFact, steered, chat int) string {
	reviews := r.Role == "reviewer" // a tester writes tests: shown as a writer (TO1)
	s := c.text(r.Role, false)
	if !reviews && clean(r.Trigger) != "" {
		s += " (" + c.words(r.Trigger, triggerWords(r.Trigger), false) + ")"
	}
	var how string
	switch {
	case r.Outcome == "" && r.EndedAt.IsZero() && !c.over:
		how = c.bold("running")
	case r.Outcome == "":
		how = "ended"
	case reviews && r.Outcome == "succeeded" && r.Verdict != "":
		how = c.words(r.Verdict, verdictWords(r.Verdict), false)
	default:
		how = c.words(r.Outcome, outcomeWords(r.Outcome), false)
	}
	s += " " + how
	if d, ok := duration(r, c.o.Now, c.over); ok {
		s += " · " + d
	}
	if steered > 0 {
		s += " · ↪ steered ×" + strconv.Itoa(steered)
	}
	if chat > 0 {
		s += " · " + strconv.Itoa(chat) + " chat"
	}
	if r.MicroUSD > 0 {
		s += " · " + dollars(r.MicroUSD)
	}
	return s
}

// duration is how long a run took, or has taken so far; an unfinished run of a task that is over has
// no time to count.
func duration(r envelope.RunFact, now time.Time, over bool) (string, bool) {
	end := r.EndedAt
	switch {
	case r.StartedAt.IsZero():
		return "", false
	case end.IsZero() && r.Outcome == "" && !over:
		end = now
	case end.IsZero():
		return "", false
	}
	return spanText(end.Sub(r.StartedAt)), true
}

func (c *card) notes(s summary.Summary) {
	items := s.Notes.Items
	if len(items) == 0 {
		return
	}
	if len(items) > maxCardNotes {
		items = items[len(items)-maxCardNotes:]
	}
	c.line("")
	c.line(c.head("Agents' notes") + ", their claims, not checked")
	for _, n := range items {
		c.line("- " + stamp(n.At, c.o.Now) + " " + c.token(n.Run, false) + " " + c.quoted(n.Text, false))
	}
}

// queued lists the notes waiting for the next run: the developer's own words, quoted as inert as
// the agents' notes.
func (c *card) queued(s summary.Summary) {
	items := s.Queued.Items
	if len(items) == 0 {
		return
	}
	more := 0
	if len(items) > maxCardNotes {
		more, items = len(items)-maxCardNotes, items[:maxCardNotes]
	}
	c.line("")
	c.line(c.head("Queued for the next run") + ", read by that run as data")
	for _, n := range items {
		c.line("- " + c.quoted(n.Text, false))
	}
	if more > 0 {
		c.line("- and " + strconv.Itoa(more) + " more")
	}
}

func (c *card) ranOn(s summary.Summary) {
	r := s.Status.Run
	if r == nil || r.Provenance == nil {
		return
	}
	var parts []string
	if v := r.Provenance.FactoryVersion; v != "" {
		parts = append(parts, "factory "+c.token(v, false))
	}
	for _, m := range r.Provenance.ServedModels {
		parts = append(parts, c.token(m, false))
	}
	if len(parts) == 0 {
		return
	}
	c.line("")
	c.line("Ran on " + strings.Join(parts, " · "))
}

func (c *card) actions(s summary.Summary) {
	var can []string
	for _, a := range s.Actions {
		switch a.Kind {
		case "stop":
			can = append(can, "stop it with the label "+c.lit("factory/stop"))
		default:
			can = append(can, c.text(a.What, false))
		}
	}
	if len(can) == 0 {
		return
	}
	c.line("")
	c.line(c.head("You can") + " " + strings.Join(can, " · "))
}

// lit is a fixed literal of this package, in a code span in Markdown.
func (c *card) lit(s string) string {
	if c.md() {
		return "`" + s + "`"
	}
	return s
}
