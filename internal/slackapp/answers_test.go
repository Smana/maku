// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/roomview"
	"github.com/Smana/maku/internal/summary"
)

// answerRoom is the room every thread here filed: the shape roomRE accepts, so an answer that reads it
// reads a room the broker could have minted.
const (
	answerRoom = "26zfnuxm"
	answerPage = "https://rooms.example/r/" + answerRoom
)

// errLogUnreadable is what a broker answers a room whose log it cannot read (the rooms client's own
// spelling, as humanapi sends it): the answer says the room could not be read and never guesses.
var errLogUnreadable = errors.New("room log unreadable")

// answerSummary is the room the answers read: a running task with one issue whose title a test may make
// hostile. The issue's title is a person's words, so it is data the answer quotes, never a mark.
func answerSummary() summary.Summary {
	var s summary.Summary
	s.APIVersion = "summary/v1"
	s.Room = answerRoom
	s.URL = answerPage
	s.Status.Phase = "Running"
	s.Status.Issue = &envelope.IssueFact{Number: 42, Title: "the install link in the README is dead"}
	return s
}

// answerBroker is the three reads behind the answers (CD25): each answers what the test set, fails when
// the test said so, and records every room it was asked about — which is how a test says "no call at
// all" where the design says none is made.
type answerBroker struct {
	*stubBroker
	sum        summary.Summary
	changes    roomview.ChangeSet
	needs      []summary.Need
	sumErr     error
	changesErr error
	needsErr   error
	rooms      []string // every read, as "tool/room", oldest first
}

func (b *answerBroker) RoomSummary(_ context.Context, room string) (summary.Summary, error) {
	b.rooms = append(b.rooms, "room_summary/"+room)
	if b.sumErr != nil {
		return summary.Summary{}, b.sumErr
	}
	return b.sum, nil
}

func (b *answerBroker) RoomChanges(_ context.Context, room string) (roomview.ChangeSet, error) {
	b.rooms = append(b.rooms, "room_changes/"+room)
	if b.changesErr != nil {
		return roomview.ChangeSet{}, b.changesErr
	}
	return b.changes, nil
}

func (b *answerBroker) NeedsList(_ context.Context, room string) ([]summary.Need, error) {
	b.rooms = append(b.rooms, "needs_list/"+room)
	if b.needsErr != nil {
		return nil, b.needsErr
	}
	return b.needs, nil
}

// The three reads on the plain stub, which every other flow uses: an empty room, so a test about
// something else never depends on them. The tests below use answerBroker.
func (b *stubBroker) RoomSummary(context.Context, string) (summary.Summary, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.summary != nil {
		return *b.summary, nil
	}
	return summary.Summary{}, nil
}

func (b *stubBroker) RoomChanges(context.Context, string) (roomview.ChangeSet, error) {
	return roomview.ChangeSet{}, nil
}

func (b *stubBroker) NeedsList(context.Context, string) ([]summary.Need, error) { return nil, nil }

// filedThread is a thread the App filed: the parent mention and the Filed line that closes it, with the
// room the marker carries ("" for a Filed line posted before markers carried one).
func filedThread(rig *appRig, room string) {
	rig.slack.seed(Message{TS: proposalThreadTS, User: "U02DEF", Text: "<@U0BOT> " + mentionWords})
	rig.slack.seed(Message{TS: "1729.0002", ThreadTS: proposalThreadTS, User: "U0BOT", Text: "Filed #42",
		Metadata: metaOf(rig.t, filedEventType, MarkerPayload{Repo: "Smana/ai-native-ref", Room: room})})
}

// asked is one question a thread asks, and what the answer must be: every phrase in the closed
// vocabulary, an unknown one, and a stop that reaches no broker at all (CD25).
type asked struct {
	name   string
	phrase string
	broker func(*answerBroker)
	want   []string // substrings the answer must carry
	nope   []string // substrings it must not
	label  string   // the question's own counter word
	reads  int      // the sponsored calls the answer may make
}

// ask sends one reply in a filed thread and returns the answer the App posted and the broker it read.
func ask(t *testing.T, tc asked) (string, *answerBroker) {
	t.Helper()
	rig, _ := withProposals(t)
	filedThread(rig, answerRoom)
	ab := &answerBroker{stubBroker: rig.broker, sum: answerSummary()}
	if tc.broker != nil {
		tc.broker(ab)
	}
	rig.app.Broker = ab
	rig.replySaid("U02DEF", "1729.9001", tc.phrase)
	posts := rig.posts()
	if len(posts) != 1 || posts[0].channel != "C03" || posts[0].thread != proposalThreadTS {
		t.Fatalf("%q got %d posts, want the one answer in the thread: %+v", tc.phrase, len(posts), posts)
	}
	if posts[0].meta != nil {
		t.Fatalf("%q answered with metadata: %+v", tc.phrase, posts[0].meta)
	}
	got := rig.metrics.counted("answer")
	if len(got) != 1 || got[tc.label] != 1 {
		t.Fatalf("%q was counted as %v, want one %q", tc.phrase, got, tc.label)
	}
	return posts[0].text, ab
}

// CD25: five questions and nothing else. Each known phrase answers from the room and never from the
// thread's text; each unknown phrase gets one line saying where it acts; a stop request only prints
// the label and the link.
func TestThreadQuestionVocabulary(t *testing.T) {
	need := summary.Need{Kind: "review", ID: "7", What: "review the pull request",
		URL: "https://github.com/Smana/ai-native-ref/pull/7",
		Action: summary.NeedAction{Kind: "review", Label: "review PR #7",
			URL: "https://github.com/Smana/ai-native-ref/pull/7"}}

	withNeed := func(b *answerBroker) { b.needs = []summary.Need{need} }
	withPR := func(b *answerBroker) {
		s := answerSummary()
		s.Status.PR = &envelope.PRFact{Number: 12, URL: "https://github.com/Smana/ai-native-ref/pull/12"}
		b.sum = s
	}
	withChanges := func(b *answerBroker) {
		set := roomview.ChangeSet{Room: answerRoom, URL: answerPage, More: 4}
		for i := range 12 {
			set.Items = append(set.Items, roomview.Milestone{Seq: int64(i + 1), Kind: "run_started",
				Text: "milestone " + strconv.Itoa(i+1)})
		}
		b.changes = set
	}

	status := []string{"Phase: Running", "Issue #42", "the install link in the README is dead", answerPage}
	cases := []asked{
		{name: "status", phrase: "status", want: status, label: "status", reads: 1},
		{name: "Status?", phrase: "Status?", want: status, label: "status", reads: 1},
		{name: "how is it going", phrase: "how is it going", want: status, label: "status", reads: 1},
		{name: "needs me", phrase: "needs me", broker: withNeed, label: "needs", reads: 1,
			want: []string{"review PR #7", "https://github.com/Smana/ai-native-ref/pull/7"}},
		{name: "what needs me", phrase: "what needs me", label: "needs", reads: 1,
			want: []string{"Nothing waits on this task right now."}},
		{name: "what changed", phrase: "what changed", broker: withChanges, label: "changes", reads: 1,
			want: []string{"milestone 3", "milestone 12", "More: the room page"}, nope: []string{"milestone 2\n"}},
		{name: "the pull request", phrase: "the pull request", broker: withPR, label: "pr", reads: 1,
			want: []string{"Pull request #12", "https://github.com/Smana/ai-native-ref/pull/12"}},
		{name: "pr", phrase: "pr", broker: withPR, label: "pr", reads: 1,
			want: []string{"Pull request #12", "https://github.com/Smana/ai-native-ref/pull/12"}},
		{name: "no pull request yet", phrase: "pr", want: []string{"No pull request yet in this room."},
			label: "pr", reads: 1},
		{name: "stop", phrase: "stop", label: "stop", reads: 0,
			want: []string{"I can't stop a run from here. Apply the `factory/stop` label on the issue, or use Stop on the room page: <" + answerPage + "|"}},
		{name: "stop it", phrase: "stop it", label: "stop", reads: 0,
			want: []string{"I can't stop a run from here. Apply the `factory/stop` label on the issue, or use Stop on the room page: <" + answerPage + "|"}},
		{name: "an unknown phrase", phrase: "please fix the tests", label: "unknown", reads: 0,
			want: []string{"Progress is in this thread; approvals and steering are on the room page: <" + answerPage + "|",
				"Ask `status`, `needs me`, `what changed` or `the pull request`."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answer, ab := ask(t, tc)
			for _, w := range tc.want {
				if !strings.Contains(answer, w) {
					t.Fatalf("%q answered %q, want it to carry %q", tc.phrase, answer, w)
				}
			}
			for _, n := range tc.nope {
				if strings.Contains(answer, n) {
					t.Fatalf("%q answered %q, want no %q", tc.phrase, answer, n)
				}
			}
			if len(ab.rooms) != tc.reads {
				t.Fatalf("%q made %d broker reads (%v), want %d", tc.phrase, len(ab.rooms), ab.rooms, tc.reads)
			}
			if n := len(strings.Split(answer, "\n")); n > maxAnswerLines {
				t.Fatalf("%q answered %d lines, want at most %d: %q", tc.phrase, n, maxAnswerLines, answer)
			}
		})
	}
}

// CD8 and CD25's security row: an answer carries no agent text as an instruction, and every value it
// quotes is escaped. A hostile issue title and a hostile milestone text change no link and no mark.
func TestThreadAnswersAreInert(t *testing.T) {
	rig, _ := withProposals(t)
	filedThread(rig, answerRoom)
	sum := answerSummary()
	sum.Status.Issue.Title = "docs: [x](https://evil.example) <!channel>"
	ab := &answerBroker{stubBroker: rig.broker, sum: sum,
		changes: roomview.ChangeSet{Room: answerRoom, URL: answerPage, Items: []roomview.Milestone{
			{Seq: 1, Kind: "note", Text: "\x1b[31m| <https://evil.example|click> see @here"},
		}}}
	rig.app.Broker = ab

	for i, tc := range []struct{ phrase, escaped string }{
		{"status", "&lt;!channel&gt;"},
		{"what changed", "&lt;https://evil.example|click&gt;"},
	} {
		rig.replySaid("U02DEF", "1729.90"+strconv.Itoa(i), tc.phrase)
		answer := rig.lastPost().text
		for _, n := range []string{"<https://evil.example|", "<!channel>", "\x1b"} {
			if strings.Contains(answer, n) {
				t.Fatalf("%q answered %q, which carries %q", tc.phrase, answer, n)
			}
		}
		if !strings.Contains(answer, tc.escaped) {
			t.Fatalf("%q answered %q, want the quote made inert as %q", tc.phrase, answer, tc.escaped)
		}
		// Every link the answer does carry is the App's own room page: no quoted URL is a destination.
		for at := 0; ; {
			i := strings.IndexByte(answer[at:], '<')
			if i < 0 {
				break
			}
			at += i
			if !strings.HasPrefix(answer[at:], "<"+answerPage+"|") {
				t.Fatalf("%q answered %q, whose < at %d is no room page", tc.phrase, answer, at)
			}
			end := strings.IndexByte(answer[at:], '>')
			if end < 0 {
				t.Fatalf("%q answered %q, whose link never closes", tc.phrase, answer)
			}
			at += end
		}
	}
}

// Review Focus 4: a marker with no room, or a room the broker cannot read, answers that the room is
// unknown — never a guess from the issue number.
func TestThreadAnswerUnknownRoom(t *testing.T) {
	t.Run("a Filed line whose marker names no room", func(t *testing.T) {
		rig, _ := withProposals(t)
		filedThread(rig, "")
		ab := &answerBroker{stubBroker: rig.broker, sum: answerSummary()}
		rig.app.Broker = ab
		rig.replySaid("U02DEF", "1729.9001", "status")
		want := "I can't tell which room this thread filed; open the room page from the Filed line above."
		if p := rig.lastPost(); p.text != want {
			t.Fatalf("%q, want %q", p.text, want)
		}
		if len(ab.rooms) != 0 {
			t.Fatalf("read %v: a marker with no room names no room to read", ab.rooms)
		}
	})

	t.Run("a room the broker cannot read", func(t *testing.T) {
		rig, _ := withProposals(t)
		filedThread(rig, answerRoom)
		ab := &answerBroker{stubBroker: rig.broker, sumErr: errLogUnreadable,
			changesErr: errLogUnreadable, needsErr: errLogUnreadable}
		rig.app.Broker = ab
		want := "The room could not be read just now; try again in a minute."
		for i, phrase := range []string{"status", "needs me", "what changed", "pr"} {
			before := len(ab.rooms)
			rig.replySaid("U02DEF", "1729.91"+strconv.Itoa(i), phrase)
			if p := rig.lastPost(); p.text != want {
				t.Fatalf("%q answered %q, want %q", phrase, p.text, want)
			}
			if len(ab.rooms)-before != 1 {
				t.Fatalf("%q made %d reads, want the one that failed", phrase, len(ab.rooms)-before)
			}
		}
	})
}

// A cancelled proposal filed nothing: its thread has no room to ask about and no Filed line to point
// at, so a reply there is ignored, as it was before the thread answered questions.
func TestThreadAnswerCancelledIsIgnored(t *testing.T) {
	rig, _ := withProposals(t)
	rig.slack.seed(Message{TS: proposalThreadTS, User: "U02DEF", Text: "<@U0BOT> " + mentionWords})
	rig.slack.seed(Message{TS: "1729.0002", ThreadTS: proposalThreadTS, User: "U0BOT",
		Text:     "Proposal cancelled by <@U02DEF>; mention me again to plan another.",
		Metadata: metaOf(rig.t, filedEventType, MarkerPayload{Repo: "Smana/ai-native-ref", Cancelled: true})})
	ab := &answerBroker{stubBroker: rig.broker, sum: answerSummary()}
	rig.app.Broker = ab
	rig.replySaid("U02DEF", "1729.9001", "status")
	if posts := rig.posts(); len(posts) != 0 {
		t.Fatalf("a cancelled thread answered %+v", posts)
	}
	if len(ab.rooms) != 0 {
		t.Fatalf("a cancelled thread read %v", ab.rooms)
	}
	if got := rig.metrics.count("events", "ignored"); got != 1 {
		t.Fatalf("the reply was counted %d times as ignored", got)
	}
}

// The changes answer is the newest milestones the broker returned, oldest-first as it sends them,
// and a room with more than the answer holds is cut at ten lines with the room page named.
func TestThreadChangesAreTheNewest(t *testing.T) {
	set := roomview.ChangeSet{Room: answerRoom, URL: answerPage}
	for i := range 15 {
		set.Items = append(set.Items, roomview.Milestone{Seq: int64(i + 1), Kind: "note",
			Text: "milestone " + strconv.Itoa(i+1) + "."})
	}
	got := changesAnswer(set)
	lines := strings.Split(got, "\n")
	if len(lines) != maxChangesLines+1 || lines[0] != "- milestone 6." || lines[9] != "- milestone 15." ||
		lines[10] != "More: the room page" {
		t.Fatalf("changes answered %q, want milestones 6..15 then More", got)
	}
}

// capLines keeps an answer to twelve lines: a longer one is its first ten and an ellipsis.
func TestAnswerLineCap(t *testing.T) {
	needs := make([]summary.Need, 13)
	for i := range needs {
		needs[i] = summary.Need{Kind: "review", Action: summary.NeedAction{Label: "need " + strconv.Itoa(i+1)}}
	}
	lines := strings.Split(needsAnswer(needs), "\n")
	if len(lines) != maxAnswerCut+1 || lines[maxAnswerCut] != "…" || lines[9] != "- need 10" {
		t.Fatalf("thirteen needs answered %q, want ten and an ellipsis", lines)
	}
}

// A URL the broker sent is a link only when nothing in it is Slack markup, and a quoted value loses
// the invisible characters that reorder or hide what a person reads.
func TestAnswerLinksAndInvisibles(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/x|evil>", "https://github.com/x>y", "javascript:alert(1)",
		"https://user:pw@github.com/x", "https://github.com/a b", "",
		"http://github.com/Smana/maku/pull/7", // plain http: a link the App shows is TLS or none
	} {
		if got := brokerLink(raw, "open it"); got != "" {
			t.Fatalf("brokerLink(%q) = %q, want no link", raw, got)
		}
	}
	if got := brokerLink("https://github.com/Smana/maku/pull/7", "open it"); got != "<https://github.com/Smana/maku/pull/7|open it>" {
		t.Fatalf("a plain broker URL linked as %q", got)
	}
	// U+202E (right-to-left override), U+200B (zero width space) and U+2066 (left-to-right isolate):
	// the invisibles that reorder or hide what a person reads, written as escapes so the source holds
	// no such character itself.
	if got := answerText("fix\u202ett\u200bxe\u2066"); got != "fixttxe" {
		t.Fatalf("answerText kept invisibles: %q", got)
	}
}
