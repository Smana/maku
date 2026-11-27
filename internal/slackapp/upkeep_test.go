// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/summary"
)

func upkeepRig(t *testing.T, up *UpkeepChannel) *appRig {
	t.Helper()
	rig := newAppRig(t, Channel{
		Upkeep: up,
	})
	rig.app.Channels = map[string]Channel{
		"C03UP": {
			Upkeep:        up,
			IntentActions: []string{"stop", "retry"},
		},
	}
	return rig
}

func TestUpkeepSilentEventsPostNothing(t *testing.T) {
	rig := upkeepRig(t, &UpkeepChannel{
		Repositories: []string{"Smana/ai-native-ref"},
		Routines:     []string{"dead-links"},
	})
	for _, body := range []string{
		`{"type":"room.task_received","room":"aaaaaaaa","repo":"Smana/ai-native-ref","source":"routine","routine":"dead-links"}`,
		`{"type":"room.task_triaged","room":"aaaaaaaa","repo":"Smana/ai-native-ref","source":"routine","routine":"dead-links"}`,
		`{"type":"room.ended","room":"aaaaaaaa","repo":"Smana/ai-native-ref","source":"routine","routine":"dead-links","state":"done","reason":"no_op"}`,
		// Unsubscribed repo or routine.
		`{"type":"room.pr_opened","room":"bbbbbbbb","repo":"Smana/other","source":"routine","routine":"dead-links","pr":7}`,
		`{"type":"room.pr_opened","room":"cccccccc","repo":"Smana/ai-native-ref","source":"routine","routine":"other-job","pr":7}`,
		// Non-routine unmapped room.
		`{"type":"room.pr_opened","room":"dddddddd","repo":"Smana/ai-native-ref","source":"issue","pr":7}`,
	} {
		rig.webhook(body)
	}
	if n := len(rig.posts()); n != 0 {
		t.Fatalf("expected 0 posts for silent/unsubscribed events, got %d: %+v", n, rig.posts())
	}
}

func TestUpkeepPROpensThreadAndSubsequentEventsUpdateInPlace(t *testing.T) {
	rig := upkeepRig(t, &UpkeepChannel{
		Repositories: []string{"smana/ai-native-ref"}, // case-insensitive repo match
		Routines:     []string{"dead-links"},
	})
	rig.broker.summary = &summary.Summary{
		APIVersion: summary.APIVersion,
		Room:       "aaaaaaaa",
	}
	rig.broker.summary.Status.Phase = "Running"
	rig.broker.summary.Status.PR = &envelope.PRFact{Number: 42, URL: "https://github.com/Smana/ai-native-ref/pull/42"}

	// room.pr_opened creates top-level thread and persists it in Threads.
	rig.webhook(`{"type":"room.pr_opened","room":"aaaaaaaa","repo":"Smana/ai-native-ref","source":"routine","routine":"dead-links","pr":42}`)
	posts := rig.posts()
	if len(posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(posts))
	}
	root := posts[0]
	if root.thread != "" || root.channel != "C03UP" {
		t.Fatalf("expected top-level post in C03UP, got %+v", root)
	}
	if !strings.Contains(root.text, "[dead-links]") || !strings.Contains(root.text, "Smana/ai-native-ref") || !strings.Contains(root.text, "#42") || !strings.Contains(root.text, "https://rooms.example/r/aaaaaaaa") {
		t.Fatalf("unexpected upkeep PR text: %q", root.text)
	}
	if root.meta == nil || root.meta.EventType != filedEventType {
		t.Fatalf("missing filedEventType metadata on upkeep root: %+v", root.meta)
	}
	if mark, ok := root.meta.markerPayload(); !ok || mark.Room != "aaaaaaaa" || mark.Repo != "Smana/ai-native-ref" {
		t.Fatalf("unexpected marker payload: %+v %v", mark, ok)
	}
	th, found, err := rig.threads.Get(t.Context(), "aaaaaaaa")
	if err != nil || !found || th.Channel != "C03UP" || th.TS != root.ts {
		t.Fatalf("thread not persisted: found=%v err=%v th=%+v", found, err, th)
	}

	// room.task_triaged after thread exists is still ignored by upkeep channel.
	rig.webhook(`{"type":"room.task_triaged","room":"aaaaaaaa","repo":"Smana/ai-native-ref","source":"routine","routine":"dead-links"}`)
	if len(rig.posts()) != 1 {
		t.Fatalf("room.task_triaged leaked into upkeep thread: %+v", rig.posts())
	}

	// room.ended (done) posts end line in thread; room.merged updates that end line in place.
	rig.webhook(`{"type":"room.ended","room":"aaaaaaaa","repo":"Smana/ai-native-ref","source":"routine","routine":"dead-links","state":"done"}`)
	if len(rig.posts()) != 2 || rig.posts()[1].thread != root.ts {
		t.Fatalf("expected end line reply in thread: %+v", rig.posts())
	}
	rig.webhook(`{"type":"room.merged","room":"aaaaaaaa","repo":"Smana/ai-native-ref","source":"routine","routine":"dead-links","pr":42}`)
	updates := rig.slack.updatesMade()
	if len(updates) != 1 || !strings.Contains(updates[0].text, "Merged #42") {
		t.Fatalf("expected room.merged in-place update, got %+v", updates)
	}

	// Thread reply "status" works on the upkeep thread because of filedEventType MarkerPayload.
	rig.slack.seed(Message{TS: root.ts, User: "U0BOT", Text: root.text, Metadata: root.meta})
	rig.event(`{"type":"message","channel":"C03UP","user":"U02DEF","ts":"1729.0099","thread_ts":"` + root.ts + `","text":"status"}`)
	if len(rig.posts()) != 3 || !strings.Contains(rig.posts()[2].text, "Room:") {
		t.Fatalf("status reply failed: %+v", rig.posts())
	}
}

func TestUpkeepWaitingAndEscalatedOpenThreads(t *testing.T) {
	rig := upkeepRig(t, &UpkeepChannel{
		Repositories: []string{"Smana/ai-native-ref"},
	})

	rig.webhook(`{"type":"room.waiting_for_human","room":"aaaaaaaa","repo":"Smana/ai-native-ref","source":"routine","routine":"dead-links","reason":"approval"}`)
	rig.webhook(`{"type":"room.ended","room":"bbbbbbbb","repo":"Smana/ai-native-ref","source":"routine","routine":"doc-drift","state":"failed","reason":"gate_failed"}`)
	posts := rig.posts()
	if len(posts) != 2 {
		t.Fatalf("posts = %d, want 2", len(posts))
	}
	if !strings.Contains(posts[0].text, "[dead-links]") || !strings.Contains(posts[0].text, "Needs you (approval)") || posts[0].blocks == nil {
		t.Fatalf("waiting post = %+v", posts[0])
	}
	if !strings.Contains(posts[1].text, "[doc-drift]") || !strings.Contains(posts[1].text, "Ended: escalated") || posts[1].blocks == nil {
		t.Fatalf("escalated post = %+v", posts[1])
	}
	if th, found, _ := rig.threads.Get(t.Context(), "bbbbbbbb"); !found || th.EndTS != posts[1].ts {
		t.Fatalf("escalated root should record EndTS: found=%v th=%+v", found, th)
	}
}

func TestRoutinePausedPostsAlertBeforeRoomRECheck(t *testing.T) {
	rig := upkeepRig(t, &UpkeepChannel{
		Repositories: []string{"Smana/ai-native-ref"},
		Routines:     []string{"dead-links"},
	})
	// routine.paused has empty Room and arrives before roomRE check.
	rig.webhook(`{"type":"routine.paused","repo":"Smana/ai-native-ref","source":"routine","routine":"dead-links","reason":"low_merge_rate"}`)
	posts := rig.posts()
	if len(posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(posts))
	}
	got := posts[0].text
	if !strings.Contains(got, "[dead-links]") || !strings.Contains(got, "Smana/ai-native-ref") || !strings.Contains(got, "low_merge_rate") {
		t.Fatalf("paused alert = %q", got)
	}
	if !strings.Contains(got, "https://github.com/Smana/ai-native-ref/edit/main/.github/agent-factory/upkeep/dead-links.md") {
		t.Fatalf("missing prompt edit link in %q", got)
	}
	// Unsubscribed routine is ignored.
	rig.webhook(`{"type":"routine.paused","repo":"Smana/ai-native-ref","source":"routine","routine":"other-job","reason":"low_merge_rate"}`)
	if len(rig.posts()) != 1 {
		t.Fatalf("unsubscribed routine.paused posted: %+v", rig.posts())
	}
}

func TestUpkeepOnlyChannelIgnoresInteractiveMention(t *testing.T) {
	rig := upkeepRig(t, &UpkeepChannel{
		Repositories: []string{"Smana/ai-native-ref"},
	})
	rig.mention("C03UP", "1729.0100", "<@U0BOT> Smana/ai-native-ref fix something")
	if len(rig.posts()) != 0 || len(rig.broker.handoffs) != 0 || len(rig.broker.accepted) != 0 {
		t.Fatalf("upkeep-only channel should ignore interactive proposal mention: posts=%d handoffs=%d", len(rig.posts()), len(rig.broker.handoffs))
	}
}
