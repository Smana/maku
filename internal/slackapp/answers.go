// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/Smana/maku/internal/roomview"
	"github.com/Smana/maku/internal/summary"
)

// Question is one of the five things a filed proposal's thread answers, or the one word for everything
// else (CD25). The values are the counter's closed label set: one word per question, never a person's
// phrase, so slack_thread_answers_total{question} stays six series.
type Question string

const (
	// QuestionStatus is where the task is: its phase and the room it is in.
	QuestionStatus Question = "status"
	// QuestionNeeds is what waits on a person in that room, the needs/v1 items.
	QuestionNeeds Question = "needs"
	// QuestionChanges is what happened recently, the room's milestones.
	QuestionChanges Question = "changes"
	// QuestionPR is the pull request the task opened, if it has one.
	QuestionPR Question = "pr"
	// QuestionStop is how to stop a run, which the App can only say, never do.
	QuestionStop Question = "stop"
	// QuestionUnknown is every phrase outside the vocabulary: answered with where things happen.
	QuestionUnknown Question = "unknown"
)

// The lines an answer says as it is. Each one names the surface that can act, because the thread is
// never a command surface: no reply here approves, steers or stops anything (invariant 8, CD25).
const (
	unknownRoomLine = "I can't tell which room this thread filed; open the room page from the Filed line above."
	unreadRoomLine  = "The room could not be read just now; try again in a minute."
	nothingNeeds    = "Nothing waits on this task right now."
	noPRLine        = "No pull request yet in this room."
	// stopLine takes the room page: the label is on GitHub, the button is on the page, and neither is
	// here.
	stopLine = "I can't stop a run from here. Apply the `factory/stop` label on the issue, or use " +
		"Stop on the room page: "
	// unknownQuestionLine takes the room page: a phrase outside the vocabulary is not guessed at, it is
	// answered with where the work is and what can be asked.
	unknownQuestionLine = "I only understand these here: `status`, `needs me`, `what changed`, `the pull request` " +
		"(and `stop`, which says how to stop). Progress is in this thread; approvals and steering are on %s."
)

const (
	// maxAnswerLines is what a message may be: a longer answer is cut at maxAnswerCut with …, so a
	// room's whole history never becomes a thread message.
	maxAnswerLines = 12
	maxAnswerCut   = 10
	// maxAnswerValue clips one quoted value: a title, a note or a milestone text is data, and one of
	// them may not fill the answer.
	maxAnswerValue = 300
	// maxChangesLines is how many milestones an answer shows; More says the room page has the rest.
	maxChangesLines = 10
)

// questionPhrases is the closed vocabulary a thread reply is read against (CD25): the words a person
// might reasonably type for each question, and nothing else. A phrase outside it is not a command and
// not a guess; it is QuestionUnknown, which says where things happen.
var questionPhrases = map[string]Question{
	"status":            QuestionStatus,
	"how is it going":   QuestionStatus,
	"how's it going":    QuestionStatus,
	"where is it":       QuestionStatus,
	"what's the status": QuestionStatus,
	"needs me":          QuestionNeeds,
	"what needs me":     QuestionNeeds,
	"what changed":      QuestionChanges,
	"what has changed":  QuestionChanges,
	"the pull request":  QuestionPR,
	"pr":                QuestionPR,
	"the pr":            QuestionPR,
	"stop":              QuestionStop,
	"stop it":           QuestionStop,
	"how do i stop it":  QuestionStop,
}

// questionOf reads a reply as one of the five questions (CD25). It lowercases, strips the punctuation a
// person ends a question with and collapses the whitespace between words, then looks the phrase up in
// the closed map above. Every other reply is QuestionUnknown: the App reads no reply as a command, and
// nothing in a reply reaches the plan, an approval or a run.
func questionOf(text string) Question {
	s := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(text))), " ")
	s = strings.TrimRight(s, "?.!")
	if q, ok := questionPhrases[s]; ok {
		return q
	}
	return QuestionUnknown
}

// answer reads ev as one of the five questions and answers it from the room the thread filed (CD25),
// or says where the question is answered instead. It reads the broker at most once — the stop and the
// unknown answers not at all, since the App can no more stop a run than it can decide an approval —
// and every answer goes through capLines, so twelve lines is the most a thread ever gets.
//
// Nothing here is an instruction: every value the room supplies is answerText'd, and the only links the
// answer builds are the App's own room page and the URLs the broker built (summary.Summary,
// summary.Need.Action and roomview.Milestone carry the ones its own views link).
func (a *App) answer(ctx context.Context, ev slackEvent, room string) string {
	q := questionOf(ev.Text)
	a.answerCount(ctx, q)
	if !roomRE.MatchString(room) { // no marker names a room, or one this App could not have minted
		return unknownRoomLine
	}
	page := a.roomLink(room)
	switch q {
	case QuestionStop:
		return capLines([]string{stopLine + page})
	case QuestionUnknown:
		return capLines([]string{fmt.Sprintf(unknownQuestionLine, page)})
	case QuestionStatus, QuestionPR:
		sum, err := a.Broker.RoomSummary(ctx, room)
		if err != nil {
			a.answerFailed(ctx, ev, q, err)
			return unreadRoomLine
		}
		if q == QuestionPR {
			return prAnswer(sum)
		}
		return statusAnswer(sum, page)
	case QuestionNeeds:
		needs, err := a.Broker.NeedsList(ctx, room)
		if err != nil {
			a.answerFailed(ctx, ev, q, err)
			return unreadRoomLine
		}
		return needsAnswer(needs)
	case QuestionChanges:
		set, err := a.Broker.RoomChanges(ctx, room)
		if err != nil {
			a.answerFailed(ctx, ev, q, err)
			return unreadRoomLine
		}
		return changesAnswer(set)
	}
	// A Question is a closed set: a value outside it never reaches here, and the honest answer if one
	// ever did is the one an unknown phrase gets.
	return capLines([]string{fmt.Sprintf(unknownQuestionLine, page)})
}

// answerCount counts one answer by the question it read (CD25). It is the question asked, not what the
// App could answer: a room that could not be read still answered the status someone asked for.
func (a *App) answerCount(ctx context.Context, q Question) {
	if a.Metrics != nil {
		a.Metrics.ThreadAnswer(ctx, string(q))
	}
}

// answerFailed logs a read the broker could not serve. Ids and the question only: the error may carry a
// room's payload, and the thread gets unreadRoomLine rather than any of it.
func (a *App) answerFailed(ctx context.Context, ev slackEvent, q Question, err error) {
	a.log().WarnContext(ctx, "slack room read failed", "channel", ev.Channel, "thread", ev.ThreadTS,
		"question", string(q), "err", err)
}

// roomLink is the App's own room page, built from RoomsURL and the room id alone: a room the broker
// cannot read still has a link a person can open.
func (a *App) roomLink(room string) string {
	return "<" + strings.TrimSuffix(a.RoomsURL, "/") + "/r/" + room + "|the room page>"
}

// statusAnswer is where the task is: its phase, the issue it works on, and the room page. The issue's
// title is a person's words, so it is quoted inert rather than shown as a mark of its own.
func statusAnswer(sum summary.Summary, page string) string {
	phase := strings.TrimSpace(sum.Status.Phase)
	if phase == "" {
		phase = "no phase yet"
	}
	head := "Phase: " + answerText(phase)
	if issue := sum.Status.Issue; issue != nil && issue.Number > 0 {
		head += fmt.Sprintf(". Issue #%d", issue.Number)
		if title := strings.TrimSpace(issue.Title); title != "" {
			head += " " + answerText(title)
		}
	}
	return capLines([]string{head, "Room: " + page})
}

// prAnswer names the pull request the task opened, or says there is none yet. The number is the
// task's own fact; the URL is the one the broker built, and only it becomes a link.
func prAnswer(sum summary.Summary) string {
	if pr := sum.Status.PR; pr != nil && pr.Number > 0 {
		head := fmt.Sprintf("Pull request #%d", pr.Number)
		if link := brokerLink(pr.URL, "open it"); link != "" {
			return capLines([]string{head + ": " + link})
		}
		return capLines([]string{head})
	}
	return capLines([]string{noPRLine})
}

// needsAnswer is one line per need with its one action's label and where it happens, or the one line a
// room with nothing waiting gives. A need beyond the line cap is cut with …, as every answer is.
func needsAnswer(needs []summary.Need) string {
	if len(needs) == 0 {
		return capLines([]string{nothingNeeds})
	}
	lines := make([]string, 0, len(needs))
	for _, n := range needs {
		label := strings.TrimSpace(n.Action.Label)
		if label == "" {
			label = strings.TrimSpace(n.What)
		}
		if label == "" {
			label = n.Kind
		}
		line := "- " + answerText(label)
		if link := brokerLink(n.Action.URL, "open it"); link != "" {
			line += ": " + link
		}
		lines = append(lines, line)
	}
	return capLines(lines)
}

// changesAnswer is what happened in the room: the newest maxChangesLines milestones of the ones the
// broker returned, oldest-first as it sends them, and the room page when there are more. The broker's
// page is itself the oldest of the window (More counts later ones), so with More set the newest are on
// the room page, which the last line names. An empty room says so rather than answering nothing.
func changesAnswer(set roomview.ChangeSet) string {
	items := set.Items
	if len(items) > maxChangesLines {
		items = items[len(items)-maxChangesLines:]
	}
	lines := make([]string, 0, maxChangesLines+1)
	for _, m := range items {
		lines = append(lines, "- "+milestoneLine(m))
	}
	if len(lines) == 0 {
		return capLines([]string{"Nothing has happened in this room yet."})
	}
	if set.More > 0 || len(set.Items) > len(items) {
		lines = append(lines, "More: the room page")
	}
	return capLines(lines)
}

// milestoneLine is one milestone as a thread line: the text the broker wrote or the agent's own, quoted
// inert, with the milestone's URL as a link when it has one.
func milestoneLine(m roomview.Milestone) string {
	text := strings.TrimSpace(m.Text)
	if text == "" {
		text = strings.ReplaceAll(m.Kind, "_", " ")
	}
	line := answerText(text)
	if link := brokerLink(m.URL, "open it"); link != "" {
		line += " " + link
	}
	return line
}

// answerText is a value an answer quotes: at most one line, no control byte, and Slack's three escapes,
// so a title, a note or a milestone text is data — a <!channel>, a <@user> or a link in it fires
// nothing, and an escape sequence from a captured terminal carries no colour into the thread.
func answerText(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r): // bidi overrides and zero-widths reorder or hide what is read
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxAnswerValue {
		s = string(r[:maxAnswerValue]) + "…"
	}
	return slackText(s)
}

// brokerLink is a URL the broker built, as a Slack link: only an https URL with a host and no byte
// Slack's own markup reads is a destination. Anything else, plain http included, is dropped rather
// than shown, so no quoted value ever becomes a link the App did not build.
func brokerLink(raw, label string) string {
	u, err := url.Parse(raw)
	if err != nil || raw == "" || u.Scheme != "https" || u.Host == "" ||
		u.User != nil || strings.ContainsAny(raw, "<>| \t\n") {
		return ""
	}
	return "<" + u.String() + "|" + slackText(label) + ">"
}

// capLines is an answer's shape: at most maxAnswerLines lines, a longer one cut at maxAnswerCut with …
func capLines(lines []string) string {
	if len(lines) > maxAnswerLines {
		out := make([]string, 0, maxAnswerCut+1)
		out = append(out, lines[:maxAnswerCut]...)
		out = append(out, "…") // the cut slice is this one, never the caller's
		lines = out
	}
	return strings.Join(lines, "\n")
}
