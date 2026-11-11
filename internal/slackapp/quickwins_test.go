// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The Slack quick wins: a refused mention says its cause and fix to the person alone, a slow plan
// shows it is working, a filed thread lists what it understands, and each refusal reason is logged and
// counted apart. None of it reads free text as a command (CD25).

func TestMentionRefusalOrients(t *testing.T) {
	for _, tc := range []struct {
		name, refusal, outcome string
		want                   []string
	}{
		{"not_linked", "not_linked: your account is not linked to GitHub; link it first", "not_linked",
			[]string{"not linked to a GitHub account", "`@factory link`", "Smana/ai-native-ref, Smana/cloud-native-ref",
				"<https://rooms.example|the rooms page>"}},
		{"repository_not_allowed", "repository_not_allowed: none of the candidates", "repo_not_allowed",
			[]string{"this channel serves Smana/ai-native-ref, Smana/cloud-native-ref", "<https://rooms.example|the rooms page>"}},
		{"repository_not_served_here", "repository_not_served_here: runlore isn't served here (served: Smana/ai-native-ref); an operator adds it to the repositories served here",
			"repo_not_served",
			[]string{"Not proposed: runlore isn't served here (served: Smana/ai-native-ref); an operator adds it", "<https://rooms.example|the rooms page>"}},
		{"no_write", "no_write: no push access", "no_write",
			[]string{"cannot write to Smana/ai-native-ref, Smana/cloud-native-ref", "<https://rooms.example|the rooms page>"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, _ := withProposals(t)
			rig.broker.intakeErr = Refused(tc.refusal)
			rig.mention("C03", "1729.0001", "<@U0BOT> "+mentionWords)

			w := rig.slack.whispers()
			if len(w) != 1 || w[0].channel != "C03" || w[0].user != "U02DEF" || w[0].thread != "1729.0001" {
				t.Fatalf("want one ephemeral to the mentioner in the thread, got %+v", w)
			}
			for _, s := range tc.want {
				if !strings.Contains(w[0].text, s) {
					t.Fatalf("the orientation %q lacks %q", w[0].text, s)
				}
			}
			if posts := rig.posts(); len(posts) != 0 {
				t.Fatalf("the thread must stay clean, got %+v", posts)
			}
			if got := rig.metrics.count("events", tc.outcome); got != 1 {
				t.Fatalf("outcome %q counted %d times: %v", tc.outcome, got, rig.metrics.counted("events"))
			}
		})
	}

	t.Run("a failed ephemeral falls back to the thread", func(t *testing.T) {
		rig, _ := withProposals(t)
		rig.broker.intakeErr = Refused("not_linked: link it first")
		rig.slack.failEphemerals = 1
		rig.mention("C03", "1729.0001", "<@U0BOT> "+mentionWords)
		posts := rig.posts()
		if len(posts) != 1 || posts[0].thread != "1729.0001" || !strings.Contains(posts[0].text, "`@factory link`") {
			t.Fatalf("the fallback is one thread line with the fix: %+v", posts)
		}
		if got := rig.metrics.count("events", "not_linked"); got != 1 {
			t.Fatalf("counted %d", got)
		}
	})

	t.Run("the reason is logged, never the request", func(t *testing.T) {
		rig, _ := withProposals(t)
		var buf bytes.Buffer
		rig.app.Logger = slog.New(slog.NewTextHandler(&buf, nil))
		rig.broker.intakeErr = Refused("not_linked: link it first")
		rig.mention("C03", "1729.0001", "<@U0BOT> "+mentionWords)
		log := buf.String()
		for _, s := range []string{"reason=not_linked", "channel=C03", "user=U02DEF"} {
			if !strings.Contains(log, s) {
				t.Fatalf("log lacks %q: %s", s, log)
			}
		}
		if strings.Contains(log, mentionWords) {
			t.Fatalf("the request leaked into the log: %s", log)
		}
	})
}

func TestPlannerProgress(t *testing.T) {
	slow := func(t *testing.T) *appRig {
		rig, _ := withProposals(t)
		rig.app.PlanningAfter = 5 * time.Millisecond
		rig.broker.intakeGate = func() { // hold the plan until the status has been posted
			for i := 0; i < 400 && len(rig.posts()) == 0; i++ {
				time.Sleep(5 * time.Millisecond)
			}
		}
		return rig
	}

	t.Run("a slow plan shows one status, then the proposal replaces it in place", func(t *testing.T) {
		rig := slow(t)
		rig.mention("C03", "1729.0001", "<@U0BOT> "+mentionWords)
		posts := rig.posts()
		if len(posts) != 1 || posts[0].thread != "1729.0001" || !strings.Contains(posts[0].text, "Planning") {
			t.Fatalf("want one status message, got %+v", posts)
		}
		edits := rig.slack.proposalEdits()
		if len(edits) != 1 || edits[0].ts != posts[0].ts || edits[0].meta == nil || edits[0].meta.EventType != proposalEventType ||
			len(edits[0].blocks) == 0 {
			t.Fatalf("the proposal must replace the status in place: %+v", edits)
		}
		if got := rig.metrics.count("events", "proposed"); got != 1 {
			t.Fatalf("counted %v", rig.metrics.counted("events"))
		}
	})

	t.Run("a fast plan posts no status", func(t *testing.T) {
		rig, _ := withProposals(t)
		rig.app.PlanningAfter = time.Hour
		rig.mention("C03", "1729.0001", "<@U0BOT> "+mentionWords)
		posts := rig.posts()
		if len(posts) != 1 || strings.Contains(posts[0].text, "Planning") || len(rig.slack.proposalEdits()) != 0 {
			t.Fatalf("%+v", posts)
		}
	})

	t.Run("a slow refusal closes the status and says the refusal", func(t *testing.T) {
		rig := slow(t)
		rig.broker.intakeErr = Refused("planner_unavailable: down")
		rig.mention("C03", "1729.0001", "<@U0BOT> "+mentionWords)
		posts, ups := rig.posts(), rig.slack.updatesMade()
		if len(posts) != 2 || !strings.HasPrefix(posts[1].text, "Not proposed:") {
			t.Fatalf("%+v", posts)
		}
		if len(ups) != 1 || ups[0].ts != posts[0].ts || strings.Contains(ups[0].text, "Planning…") {
			t.Fatalf("the status must stop saying it is planning: %+v", ups)
		}
	})
}

func TestUnknownPhraseListsWhatWorks(t *testing.T) {
	rig, _, _ := proposalAt(t, 1)
	rig.slack.seed(Message{TS: "1729.0003", ThreadTS: proposalThreadTS, User: "U0BOT",
		Text:     "Filed #42 for <@U02DEF>; the factory starts it. Room: https://rooms.example/r/26zfnuxm",
		Metadata: metaOf(t, filedEventType, MarkerPayload{Repo: "Smana/ai-native-ref", Room: "26zfnuxm"})})
	rig.replySaid("U02DEF", "1729.9001", "please fix the tests")
	got := rig.lastPost().text
	for _, s := range []string{"`status`", "`needs me`", "`what changed`", "`the pull request`", "`stop`",
		"<https://rooms.example/r/26zfnuxm|the room page>"} {
		if !strings.Contains(got, s) {
			t.Fatalf("the unknown-phrase answer %q lacks %q", got, s)
		}
	}
	if n := len(rig.broker.runCreates()); n != 0 {
		t.Fatalf("free text filed %d tasks", n)
	}
}
