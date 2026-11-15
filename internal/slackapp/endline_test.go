// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/summary"
	"github.com/Smana/maku/internal/webhook"
)

func TestEndLineParts(t *testing.T) {
	t0 := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	runs := func(d time.Duration) []envelope.RunFact {
		return []envelope.RunFact{{ID: "run1", StartedAt: t0, EndedAt: t0.Add(d)}}
	}
	spend := func(used, limit int64) *envelope.SpendFact {
		return &envelope.SpendFact{UsedMicroUSD: used, LimitMicroUSD: limit}
	}

	cases := []struct {
		name string
		ev   webhook.Event
		sum  summary.Summary
		want string
	}{
		{
			name: "merged on maintainer approval",
			ev:   webhook.Event{Type: "room.merged", Room: "aaaaaaaa", PR: 418},
			sum: func() summary.Summary {
				var s summary.Summary
				s.Status.Phase = "Merged"
				s.Status.PR = &envelope.PRFact{Number: 418}
				s.Status.Spend = spend(160_000, 2_000_000)
				s.Status.SpendFinal = true
				s.Status.Policy = &summary.PolicyView{Decision: "merged by the merger App on @alice's approval"}
				s.Runs = runs(11 * time.Minute)
				return s
			}(),
			want: "Merged #418 · $0.16 of $2.00 · 11 min · merged on @alice's approval",
		},
		{
			name: "ended stopped",
			ev:   webhook.Event{Type: "room.ended", Room: "aaaaaaaa", State: "stopped"},
			sum: func() summary.Summary {
				var s summary.Summary
				s.Status.Phase = "Stopped"
				s.Status.Spend = spend(40_000, 2_000_000)
				s.Status.SpendFinal = true
				s.Runs = runs(2 * time.Minute)
				return s
			}(),
			want: "Ended: stopped · $0.04 of $2.00 · 2 min",
		},
		{
			name: "ended escalated out of budget",
			ev:   webhook.Event{Type: "room.ended", Room: "aaaaaaaa", State: "failed", Reason: "budget-task"},
			sum: func() summary.Summary {
				var s summary.Summary
				s.Status.Phase = "Escalated"
				s.Status.ReasonCode = "budget-task"
				s.Status.Spend = spend(2_000_000, 2_000_000)
				s.Status.SpendFinal = true
				s.Runs = runs(31 * time.Minute)
				return s
			}(),
			want: "Ended: escalated (out of budget) · $2.00 of $2.00 · 31 min",
		},
		{
			name: "spend not final says so far",
			ev:   webhook.Event{Type: "room.merged", Room: "aaaaaaaa", PR: 418},
			sum: func() summary.Summary {
				var s summary.Summary
				s.Status.Phase = "Merged"
				s.Status.PR = &envelope.PRFact{Number: 418}
				s.Status.Spend = spend(160_000, 2_000_000)
				s.Status.SpendFinal = false
				s.Status.Policy = &summary.PolicyView{Decision: "merged by the merger App: the class's conditions held"}
				s.Runs = runs(11 * time.Minute)
				return s
			}(),
			want: "Merged #418 · $0.16 of $2.00 so far · 11 min · merged automatically",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := endLine(tc.ev, tc.sum); got != tc.want {
				t.Fatalf("endLine = %q, want %q", got, tc.want)
			}
		})
	}

	for dec, want := range map[string]string{
		"": "",
		"merged by the merger App: the class's conditions held": "merged automatically",
		"merged by the merger App on @alice's approval":         "merged on @alice's approval",
		"touches a protected file":                              "waits for a human: touches a protected file",
	} {
		if got := gatePart(dec); got != want {
			t.Errorf("gatePart(%q) = %q, want %q", dec, got, want)
		}
	}
}

func TestEndLineOnePerThread(t *testing.T) {
	t0 := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	makeSum := func(phase, decision string) *summary.Summary {
		var s summary.Summary
		s.Room = "aaaaaaaa"
		s.Status.Phase = phase
		s.Status.PR = &envelope.PRFact{Number: 418}
		s.Status.Spend = &envelope.SpendFact{UsedMicroUSD: 160_000, LimitMicroUSD: 2_000_000}
		s.Status.SpendFinal = true
		if decision != "" {
			s.Status.Policy = &summary.PolicyView{Decision: decision}
		}
		s.Runs = []envelope.RunFact{{ID: "r1", StartedAt: t0, EndedAt: t0.Add(11 * time.Minute)}}
		return &s
	}

	t.Run("ended posts once and later merges update in place", func(t *testing.T) {
		rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
		rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})
		rig.broker.summary = makeSum("Done", "")

		rig.webhook(`{"type":"room.ended","room":"aaaaaaaa","state":"done"}`)
		posts := rig.posts()
		if len(posts) != 1 {
			t.Fatalf("posts = %d, want 1", len(posts))
		}
		th, ok := rig.threads.get("aaaaaaaa")
		if !ok || th.EndTS == "" || th.EndTS != posts[0].ts {
			t.Fatalf("EndTS = %q (ok=%v), want %q", th.EndTS, ok, posts[0].ts)
		}

		rig.broker.summary = makeSum("Merged", "merged by the merger App: the class's conditions held")
		rig.webhook(`{"type":"room.merged","room":"aaaaaaaa","pr":418}`)
		if len(rig.posts()) != 1 {
			t.Fatalf("room.merged posted a second message: %+v", rig.posts())
		}
		u := rig.slack.updatesMade()
		if len(u) != 1 || u[0].ts != th.EndTS || !strings.HasPrefix(u[0].text, "Merged #418 · $0.16 of $2.00 · 11 min · merged automatically") {
			t.Fatalf("updates = %+v, want 1 update on %s", u, th.EndTS)
		}

		rig.broker.summary = makeSum("Merged", "merged by the merger App on @alice's approval")
		rig.webhook(`{"type":"room.merged","room":"aaaaaaaa","pr":418}`)
		u = rig.slack.updatesMade()
		if len(rig.posts()) != 1 || len(u) != 2 || u[1].ts != th.EndTS || !strings.Contains(u[1].text, "merged on @alice's approval") {
			t.Fatalf("second merge: posts=%d updates=%+v", len(rig.posts()), u)
		}
	})

	t.Run("merged arriving first posts end line and later ended updates it", func(t *testing.T) {
		rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
		rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})
		rig.broker.summary = makeSum("Merged", "merged by the merger App: the class's conditions held")

		rig.webhook(`{"type":"room.merged","room":"aaaaaaaa","pr":418}`)
		posts := rig.posts()
		if len(posts) != 1 || !strings.HasPrefix(posts[0].text, "Merged #418") {
			t.Fatalf("posts = %+v, want 1 Merged #418 post", posts)
		}
		th, _ := rig.threads.get("aaaaaaaa")
		if th.EndTS != posts[0].ts {
			t.Fatalf("EndTS = %q, want %q", th.EndTS, posts[0].ts)
		}

		rig.webhook(`{"type":"room.ended","room":"aaaaaaaa","state":"done"}`)
		u := rig.slack.updatesMade()
		if len(rig.posts()) != 1 || len(u) != 1 || u[0].ts != th.EndTS || !strings.HasPrefix(u[0].text, "Merged #418") {
			t.Fatalf("later room.ended: posts=%d updates=%+v", len(rig.posts()), u)
		}
	})
}

func TestRevertEditsEndLine(t *testing.T) {
	t0 := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	makeSum := func(phase string) *summary.Summary {
		var s summary.Summary
		s.Room = "aaaaaaaa"
		s.Status.Phase = phase
		s.Status.PR = &envelope.PRFact{Number: 418}
		s.Status.Spend = &envelope.SpendFact{UsedMicroUSD: 160_000, LimitMicroUSD: 2_000_000}
		s.Status.SpendFinal = true
		s.Runs = []envelope.RunFact{{ID: "r1", StartedAt: t0, EndedAt: t0.Add(11 * time.Minute)}}
		return &s
	}

	t.Run("default channel rewrites end line in place and posts nothing", func(t *testing.T) {
		rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
		rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})
		rig.broker.summary = makeSum("Merged")
		rig.webhook(`{"type":"room.ended","room":"aaaaaaaa","state":"done","pr":418}`)
		th, _ := rig.threads.get("aaaaaaaa")

		rig.broker.summary = makeSum("Reverted")
		rig.webhook(`{"type":"room.reverted","room":"aaaaaaaa","pr":418}`)
		u := rig.slack.updatesMade()
		if len(rig.posts()) != 1 || len(u) != 1 || u[0].ts != th.EndTS || !strings.HasPrefix(u[0].text, "Reverted #418") {
			t.Fatalf("posts=%d updates=%+v", len(rig.posts()), u)
		}
	})

	t.Run("channel listing room.reverted rewrites end line and also posts a line", func(t *testing.T) {
		ch := Channel{
			Repositories: []string{"Smana/ai-native-ref"},
			MaxCostUSD:   2,
			Notify:       []string{"room.ended", "room.reverted"},
		}
		rig := newAppRig(t, ch)
		rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})
		rig.broker.summary = makeSum("Merged")
		rig.webhook(`{"type":"room.ended","room":"aaaaaaaa","state":"done","pr":418}`)
		th, _ := rig.threads.get("aaaaaaaa")

		rig.broker.summary = makeSum("Reverted")
		rig.webhook(`{"type":"room.reverted","room":"aaaaaaaa","pr":418}`)
		u := rig.slack.updatesMade()
		posts := rig.posts()
		if len(u) != 1 || u[0].ts != th.EndTS || !strings.HasPrefix(u[0].text, "Reverted #418") {
			t.Fatalf("updates=%+v", u)
		}
		if len(posts) != 2 || !strings.Contains(posts[1].text, "#418 reverted") {
			t.Fatalf("posts=%+v, want extra revert line", posts)
		}
	})
}

func TestEscalatedQuestionIsNotAnEnd(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})
	var s summary.Summary
	s.Room = "aaaaaaaa"
	s.Status.Phase = "Escalated"
	s.Status.ReasonCode = "needs_author"
	rig.broker.summary = &s

	rig.webhook(`{"type":"room.waiting_for_human","room":"aaaaaaaa","reason":"needs_author"}`)
	posts := rig.posts()
	if len(posts) != 1 || !strings.Contains(strings.ToLower(posts[0].text), "needs you") {
		t.Fatalf("posts = %+v, want needs you line", posts)
	}
	th, _ := rig.threads.get("aaaaaaaa")
	if th.EndTS != "" {
		t.Fatalf("EndTS = %q, want empty", th.EndTS)
	}
}

func TestDefaultNotifyIsFour(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})

	for _, body := range []string{
		`{"type":"room.verdict_posted","room":"aaaaaaaa","pr":7}`,
		`{"type":"room.budget_reached","room":"aaaaaaaa","limit":"task"}`,
		`{"type":"room.approval_closed","room":"aaaaaaaa","decision":"approved"}`,
	} {
		rig.webhook(body)
	}
	if len(rig.posts()) != 0 {
		t.Fatalf("default channel posted for non-default events: %+v", rig.posts())
	}

	for _, body := range []string{
		`{"type":"room.task_triaged","room":"aaaaaaaa"}`,
		`{"type":"room.waiting_for_human","room":"aaaaaaaa","reason":"approval"}`,
		`{"type":"room.pr_opened","room":"aaaaaaaa","pr":7}`,
		`{"type":"room.ended","room":"aaaaaaaa","state":"done"}`,
	} {
		rig.webhook(body)
	}
	if len(rig.posts()) != 4 {
		t.Fatalf("default channel posted %d lines for the four defaults, want 4: %+v", len(rig.posts()), rig.posts())
	}

	custom := newAppRig(t, Channel{
		Repositories: []string{"Smana/ai-native-ref"},
		MaxCostUSD:   2,
		Notify:       []string{"room.verdict_posted"},
	})
	custom.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})
	custom.webhook(`{"type":"room.verdict_posted","room":"aaaaaaaa","pr":7}`)
	custom.webhook(`{"type":"room.pr_opened","room":"aaaaaaaa","pr":7}`)
	if posts := custom.posts(); len(posts) != 1 || !strings.Contains(posts[0].text, "#7") {
		t.Fatalf("custom channel posted %+v, want only verdict_posted", posts)
	}
}

func TestStartedLine(t *testing.T) {
	rig := newAppRig(t, Channel{Repositories: []string{"Smana/ai-native-ref"}, MaxCostUSD: 2})
	rig.threads.put("aaaaaaaa", Thread{Channel: "C03", TS: "1729.0001"})
	var s summary.Summary
	s.Room = "aaaaaaaa"
	s.Status.Issue = &envelope.IssueFact{Number: 412, Title: "docs: fix runbook"}
	rig.broker.summary = &s

	rig.webhook(`{"type":"room.task_triaged","room":"aaaaaaaa"}`)
	posts := rig.posts()
	want := "Started #412. Room: https://rooms.example/r/aaaaaaaa"
	if len(posts) != 1 || posts[0].text != want {
		t.Fatalf("posts = %+v, want %q", posts, want)
	}
}

func TestStatusAnswerHasBudgetAndGate(t *testing.T) {
	s := answerSummary()
	s.Status.Spend = &envelope.SpendFact{UsedMicroUSD: 310_000, LimitMicroUSD: 1_500_000}
	s.Status.SpendFinal = true
	s.Status.Policy = &summary.PolicyView{Decision: "touches a protected file"}

	got := statusAnswer(s, "<https://rooms.example/r/26zfnuxm|the room page>")
	for _, want := range []string{
		"State: Running",
		"Budget: $0.31 of $1.50 (21 %)",
		"waits for a human: touches a protected file",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("statusAnswer missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Phase") {
		t.Fatalf("statusAnswer still contains Phase:\n%s", got)
	}
}

func TestProposalSaysWorkflowAndBudget(t *testing.T) {
	p := proposalFixture(t)
	p.Plan.Budget.CeilingUSD = 2
	p.Plan.Issues[0].Suggestion = &intake.Suggestion{
		Profile: "standard",
		Roles:   []string{"implementer", "reviewer"},
		Merge:   intake.MergeShown{Outcome: "auto"},
		Why:     "docs change",
	}
	p.Workflow = "standard"
	p.Pins = []string{"standard", "links-solo"}

	text, raw, err := proposalSections(p, nil, nil, "C03")
	if err != nil {
		t.Fatal(err)
	}
	blocksStr := string(raw)
	for _, want := range []string{"Workflow", "Budget    up to $2.00"} {
		if !strings.Contains(text, want) && !strings.Contains(blocksStr, want) {
			t.Errorf("proposal missing %q:\ntext:\n%s\nblocks:\n%s", want, text, blocksStr)
		}
	}
	for _, nope := range []string{"Merge", "merges on its own", "merges on a maintainer", "merges after review", "Ceiling"} {
		if strings.Contains(text, nope) || strings.Contains(blocksStr, nope) {
			t.Errorf("proposal still contains %q:\ntext:\n%s\nblocks:\n%s", nope, text, blocksStr)
		}
	}

	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatal(err)
	}
	foundPlaceholder := ""
	for _, b := range blocks {
		if b["type"] != "actions" {
			continue
		}
		elems, _ := b["elements"].([]any)
		for _, el := range elems {
			m, _ := el.(map[string]any)
			if m["type"] == "static_select" {
				ph, _ := m["placeholder"].(map[string]any)
				foundPlaceholder, _ = ph["text"].(string)
				if _, hasInit := m["initial_option"]; hasInit {
					t.Errorf("unpinned select should show placeholder, got initial_option: %+v", m["initial_option"])
				}
			}
		}
	}
	if !strings.Contains(foundPlaceholder, "standard") {
		t.Fatalf("select placeholder = %q, want workflow name %q", foundPlaceholder, "standard")
	}
}

func TestPushCardSaysAllowThePush(t *testing.T) {
	e := webhook.Event{
		Room:       "aaaaaaaa",
		ApprovalID: "01M4A1B2C3D4E5F6G7H8J9K0MN",
		Class:      "forge.push",
		Branch:     "agent/26zfnuxm",
		ExpiresAt:  "2026-11-16T12:00:00Z",
	}
	_, raw := approvalCard(e, "https://rooms.example", time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC))
	if !strings.Contains(string(raw), `"text":"Allow the push"`) {
		t.Fatalf("card lacks Allow the push button:\n%s", raw)
	}
}

func TestNoPlannerFixedLine(t *testing.T) {
	for name, err := range map[string]error{
		"unknown tool": ErrUnknownTool,
		"refused off":  Refused("Planning is not available here; file on GitHub."),
	} {
		t.Run(name, func(t *testing.T) {
			rig, _ := withProposals(t)
			rig.broker.handoffErr = err
			rig.mention("C03", "1729.0001", "<@U0BOT> Smana/ai-native-ref docs: fix a dead link")
			posts := rig.posts()
			want := "Planning is not available here; file on GitHub."
			if len(posts) != 1 || posts[0].text != want || posts[0].meta != nil {
				t.Fatalf("posts = %+v, want single fixed line %q", posts, want)
			}
		})
	}
}
