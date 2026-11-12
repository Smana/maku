// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/intent"
	"github.com/Smana/maku/internal/summary"
)

// shadowRig is a filed thread in a channel whose intents switch is set, with the room the answers read
// and a broker that classifies as the test says.
func shadowRig(t *testing.T, ch Channel) (*appRig, *answerBroker, *bytes.Buffer) {
	t.Helper()
	rig, _ := withProposals(t)
	c := rig.app.Channels["C03"]
	c.Intents, c.CollectReplies = ch.Intents, ch.CollectReplies
	rig.app.Channels["C03"] = c
	filedThread(rig, answerRoom)
	// The broker serves the summary with its coarse state set, as the room page reads it.
	sum := answerSummary()
	sum.Status.State = summary.StateRunning
	ab := &answerBroker{stubBroker: rig.broker, sum: sum}
	rig.app.Broker = ab
	rig.broker.intentResult = IntentResult{Intent: "stop", Confidence: 0.9, TextHash: intent.TextHash("")}
	logs := &bytes.Buffer{}
	rig.app.Logger = slog.New(slog.NewJSONHandler(logs, nil))
	return rig, ab, logs
}

func intentCounts(rig *appRig) map[string]int { return rig.metrics.counted("intent") }

func TestTierZeroNeverReachesTheClassifier(t *testing.T) {
	for phrase := range questionPhrases {
		t.Run(phrase, func(t *testing.T) {
			rig, _, _ := shadowRig(t, Channel{Intents: IntentsShadow})
			rig.replySaid("U02DEF", "1729.9001", phrase)
			if n := len(rig.broker.intents); n != 0 {
				t.Fatalf("%q reached the classifier %d times", phrase, n)
			}
			if got := intentCounts(rig); len(got) != 0 {
				t.Fatalf("tier 1 counted %v for a tier 0 phrase", got)
			}
		})
	}
}

func TestShadowAnswersAsToday(t *testing.T) {
	rig, ab, logs := shadowRig(t, Channel{Intents: IntentsShadow})
	reply := "could you please stop the task, it is going the wrong way &amp; I am out of patience"
	rig.replySaid("U02DEF", "1729.9001", reply)

	posts := rig.posts()
	if len(posts) != 1 || posts[0].text != strings.ReplaceAll(unknownQuestionLine, "%s", roomPageLink()) || posts[0].meta != nil {
		t.Fatalf("shadow must post exactly today's unknown line: %+v", posts)
	}
	if n := len(rig.broker.intents); n != 1 {
		t.Fatalf("%d classifications, want one", n)
	}
	got := rig.broker.intents[0]
	if got.Team != "T01ABC" || got.User != "U02DEF" || got.Channel != "C03" || got.Phase != "running" || got.Collect ||
		got.Reply != "could you please stop the task, it is going the wrong way & I am out of patience" {
		t.Fatalf("the request = %+v", got)
	}
	if reads := strings.Join(ab.rooms, ","); reads != "room_summary/"+answerRoom {
		t.Fatalf("the reads = %s, want the one room_summary", reads)
	}
	if c := intentCounts(rig); len(c) != 1 || c["1/stop/shadowed"] != 1 {
		t.Fatalf("counted %v, want one 1/stop/shadowed", c)
	}
	if a := rig.metrics.counted("answer"); a["unknown"] != 1 {
		t.Fatalf("tier 0's own counter: %v", a)
	}
	for _, leak := range []string{"patience", "wrong way"} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("the log carries the reply (%q): %s", leak, logs)
		}
	}
	for _, want := range []string{`"intent":"stop"`, `"sha256"`, `"length"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the classification line lacks %s: %s", want, logs)
		}
	}
}

// roomPageLink is the room page the unknown line names.
func roomPageLink() string { return "<" + answerPage + "|the room page>" }

func TestPhaseWordIsTheStatusKind(t *testing.T) {
	rig, ab, _ := shadowRig(t, Channel{Intents: IntentsShadow})
	var s summary.Summary
	s.Status.Phase, s.Status.State = "AwaitingCI", "running"
	ab.sum = s
	rig.replySaid("U02DEF", "1729.9001", "carry on then")
	if len(rig.broker.intents) != 1 || rig.broker.intents[0].Phase != "waiting" {
		t.Fatalf("%+v, want the status line's kind", rig.broker.intents)
	}
	rig2, ab2, _ := shadowRig(t, Channel{Intents: IntentsShadow})
	ab2.sumErr = errLogUnreadable
	rig2.replySaid("U02DEF", "1729.9001", "carry on then")
	if len(rig2.broker.intents) != 1 || rig2.broker.intents[0].Phase != "unknown" {
		t.Fatalf("%+v, want the phase unknown for an unreadable room", rig2.broker.intents)
	}
}

func TestOffNeverClassifies(t *testing.T) {
	for _, on := range []string{"", IntentsOff} {
		rig, ab, _ := shadowRig(t, Channel{Intents: on})
		rig.replySaid("U02DEF", "1729.9001", "please stop the task")
		if len(rig.broker.intents) != 0 || len(ab.rooms) != 0 {
			t.Fatalf("intents %q: %d classifications, reads %v", on, len(rig.broker.intents), ab.rooms)
		}
		if len(rig.posts()) != 1 || len(intentCounts(rig)) != 0 {
			t.Fatalf("intents %q: posts %d, counts %v", on, len(rig.posts()), intentCounts(rig))
		}
	}
}

func TestClassifierDownIsUnknown(t *testing.T) {
	for name, err := range map[string]error{
		"capped":     Refused("capped"),
		"not linked": Refused("not_linked"),
		"an outage":  errors.New("slackapp: broker answered 503"),
		"a timeout":  Refused("unavailable"),
	} {
		t.Run(name, func(t *testing.T) {
			rig, _, _ := shadowRig(t, Channel{Intents: IntentsShadow})
			rig.broker.intentErr = err
			rig.replySaid("U02DEF", "1729.9001", "please stop the task")
			posts := rig.posts()
			if len(posts) != 1 || !strings.Contains(posts[0].text, "I only understand these here") {
				t.Fatalf("today's line is still posted: %+v", posts)
			}
			if c := intentCounts(rig); len(c) != 1 || c["1/unknown/refused"] != 1 {
				t.Fatalf("counted %v, want one 1/unknown/refused", c)
			}
		})
	}
	t.Run("a broker without the tool", func(t *testing.T) {
		rig, _, _ := shadowRig(t, Channel{Intents: IntentsShadow})
		rig.broker.intentErr = ErrUnknownTool
		rig.replySaid("U02DEF", "1729.9001", "please stop the task")
		if c := intentCounts(rig); len(c) != 1 || c["1/unknown/not_enabled"] != 1 {
			t.Fatalf("counted %v", c)
		}
	})
	t.Run("a label outside the nine is unknown", func(t *testing.T) {
		rig, _, _ := shadowRig(t, Channel{Intents: IntentsShadow})
		rig.broker.intentResult = IntentResult{Intent: "merge-everything", Confidence: 1}
		rig.replySaid("U02DEF", "please stop the task", "x")
		if c := intentCounts(rig); len(c) > 1 {
			t.Fatalf("a series carries a broker's invention: %v", c)
		}
	})
}

func TestCollectFollowsTheChannel(t *testing.T) {
	for _, tc := range []struct {
		name string
		ch   Channel
		want bool
	}{
		{"opted in", Channel{Intents: IntentsShadow, CollectReplies: true}, true},
		{"not opted in", Channel{Intents: IntentsShadow}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, _, _ := shadowRig(t, tc.ch)
			rig.replySaid("U02DEF", "1729.9001", "please stop the task")
			if len(rig.broker.intents) != 1 || rig.broker.intents[0].Collect != tc.want {
				t.Fatalf("%+v, want collect=%v", rig.broker.intents, tc.want)
			}
		})
	}
}

// A reply on a thread that filed no room (a Filed line from before markers carried one) is today's
// unknown-room line and is not classified: tier 1 never guesses a room.
func TestNoRoomIsNeverClassified(t *testing.T) {
	rig, _ := withProposals(t)
	c := rig.app.Channels["C03"]
	c.Intents = IntentsShadow
	rig.app.Channels["C03"] = c
	filedThread(rig, "")
	rig.replySaid("U02DEF", "1729.9001", "please stop the task")
	if len(rig.broker.intents) != 0 {
		t.Fatalf("classified a reply of a thread with no room: %+v", rig.broker.intents)
	}
}

// The phase words the App can send are the ones the broker accepts: every status kind is one.
func TestStatusKindsArePhases(t *testing.T) {
	for _, k := range []string{summary.KindRunning, summary.KindQueued, summary.KindWaitingYou, summary.KindWaitingOther,
		summary.KindWaitingPR, summary.KindWaiting, summary.KindDone, summary.KindFailed, summary.KindStopped} {
		if !intent.ValidPhase(k) {
			t.Errorf("status kind %q is not a phase the broker accepts", k)
		}
	}
}
