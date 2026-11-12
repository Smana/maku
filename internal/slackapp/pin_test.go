// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/intake"
)

// catalogueOf is a repository's catalogue with docs-links (mode given) and review (off, floor
// review-floor without a template, review-frontier allowed and offered).
func catalogueOf(maintainer bool, docsMode string) catalogue.Catalogue {
	return catalogue.Catalogue{Repository: "Smana/ai-native-ref", Maintainer: maintainer,
		Classes: []catalogue.Class{
			{Name: "docs-links", Mode: docsMode, Floor: "links-solo", Allowed: []string{"links-pair"}},
			{Name: "review", Mode: "off", Floor: "review-floor", Allowed: []string{"review-frontier"}},
		},
		Profiles: map[string]catalogue.Profile{
			"links-solo":      {Template: "solo", Roles: []string{"implementer"}, Offered: true},
			"links-pair":      {Template: "pair", Roles: []string{"implementer", "reviewer"}, Offered: true},
			"review-floor":    {NotOffered: catalogue.NoTemplate},
			"review-frontier": {Template: "trio", Roles: []string{"implementer", "tester", "reviewer"}, Offered: true},
		}}
}

// suggested is the slack fixture's plan in class, with a suggestion: a plan made from the catalogue.
func suggested(t *testing.T, class string) intake.Plan {
	t.Helper()
	p := planFixture(t, "slack-one-issue")
	p.Issues[0].Class = class
	p.Issues[0].Suggestion = &intake.Suggestion{Merge: intake.MergeShown{Outcome: "maintainer"}}
	return p
}

// P5, spec "Slack proposal": the select is offered for a maintainer and an off class only, and lists
// the factory's choice then the class's offered profiles.
func TestProposalPinOptions(t *testing.T) {
	for name, c := range map[string]struct {
		cat   catalogue.Catalogue
		err   error
		class string
		plain bool // a plan made without the catalogue
		want  []string
	}{
		"a maintainer, an off class":  {cat: catalogueOf(true, "off"), class: "docs-links", want: []string{"links-solo", "links-pair"}},
		"only offered profiles":       {cat: catalogueOf(true, "off"), class: "review", want: []string{"review-frontier"}},
		"a non-maintainer":            {cat: catalogueOf(false, "off"), class: "docs-links"},
		"a live class":                {cat: catalogueOf(true, "live"), class: "docs-links"},
		"a shadow class":              {cat: catalogueOf(true, "shadow"), class: "docs-links"},
		"the catalogue is down":       {err: errors.New("down"), class: "docs-links"},
		"a plan without a suggestion": {cat: catalogueOf(true, "off"), class: "docs-links", plain: true},
	} {
		t.Run(name, func(t *testing.T) {
			rig, _ := withProposals(t)
			rig.broker.classes, rig.broker.classesErr = &c.cat, c.err
			p := suggested(t, c.class)
			if c.plain {
				p.Issues[0].Suggestion = nil
			}
			got := rig.app.pinOptions(t.Context(), p, "U02DEF")
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("pins %v, want %v", got, c.want)
			}
			if c.plain && len(rig.broker.classesAsked) != 0 {
				t.Fatal("a plan without a suggestion read the catalogue")
			}
			if !c.plain && (len(rig.broker.classesAsked) != 1 || rig.broker.classesAsked[0] != "Smana/ai-native-ref|docs-links|slack:T01ABC/U02DEF" && rig.broker.classesAsked[0] != "Smana/ai-native-ref|review|slack:T01ABC/U02DEF") {
				t.Fatalf("asked %v", rig.broker.classesAsked)
			}
		})
	}
}

// The blocks carry the select (and the Options button) only when the payload lists pins, and only on
// a plan with nothing left to ask; each option's value binds the hash and revision shown.
func TestProposalSelectBlocks(t *testing.T) {
	p := proposalFixture(t)
	_, raw, err := proposalSections(p, nil, nil, "C03")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), actionProposalProfile) || strings.Contains(string(raw), actionProposalOptions) {
		t.Fatalf("a payload without pins has a select: %s", raw)
	}
	p.Pins = []string{"links-solo", "links-pair"}
	_, raw, err = proposalSections(p, nil, nil, "C03")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"static_select"`, actionProposalProfile, actionProposalOptions, "Profile: factory's choice",
		`"value":"` + profileValue(p.Hash, p.Rev, "links-pair") + `"`, `"value":"` + profileValue(p.Hash, p.Rev, "") + `"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("blocks lack %q: %s", want, raw)
		}
	}
	if strings.Contains(strings.ToLower(string(raw)), "workflow") {
		t.Error("the word is profile (P1)")
	}
}

// pinRig is a live proposal whose payload offers pins, and the broker's answer to a pin: the same
// plan with the profile pinned and its new hash.
func pinRig(t *testing.T) (*appRig, ProposalPayload, string) {
	t.Helper()
	rig, plan := withProposals(t)
	plan.Issues[0].Suggestion = &intake.Suggestion{Merge: intake.MergeShown{Outcome: "maintainer"}}
	rig.broker.intakeResult = IntakeResult{Plan: plan, Hash: intake.Hash(plan)}
	rig.slack.seed(Message{TS: proposalThreadTS, User: "U02DEF", Text: "<@U0BOT> " + mentionWords})
	p, err := newProposal(plan, intake.Hash(plan), "U02DEF", 1)
	if err != nil {
		t.Fatal(err)
	}
	p.Pins = []string{"links-solo", "links-pair"}
	ts := "1729.0002"
	rig.slack.seed(Message{TS: ts, ThreadTS: proposalThreadTS, User: "U0BOT", Text: "Proposal v1", Metadata: metaOf(t, proposalEventType, p)})
	rig.broker.intakeAnswer = func(in IntakeRequest) IntakeResult {
		next := *in.Candidate
		next.Issues = append([]intake.Issue(nil), next.Issues...)
		next.Issues[0].Body += "\n\n## Decisions\nD1: profile: " + next.Issues[0].Profile + ", pinned by the developer"
		return IntakeResult{Plan: next, Hash: intake.Hash(next)}
	}
	return rig, p, ts
}

// selectPressOf is a static select's press: Slack carries the chosen option's value, not a value.
func selectPressOf(t *testing.T, ts, user, value string) string {
	t.Helper()
	return pressPayload(t, func(m map[string]any) {
		m["user"] = map[string]any{"id": user}
		m["container"] = map[string]any{"type": "message", "message_ts": ts, "channel_id": "C03"}
		m["message"] = map[string]any{"ts": ts}
		m["actions"] = []any{map[string]any{"action_id": actionProposalProfile, "type": "static_select",
			"selected_option": map[string]any{"value": value}, "action_ts": "1729.0600"}}
	})
}

// Choosing a profile re-plans as a profile-only revision of the plan shown, and the message becomes
// the new revision with its new hash; a Confirm on the old hash then files nothing.
func TestProposalProfileChoice(t *testing.T) {
	rig, p, ts := pinRig(t)
	if st := rig.interact(selectPressOf(t, ts, proposalPresser, profileValue(p.Hash, 1, "links-pair"))); st != http.StatusOK {
		t.Fatalf("status %d", st)
	}
	calls := rig.broker.intakeCalls()
	if len(calls) != 1 {
		t.Fatalf("intake calls %+v", calls)
	}
	in := calls[0]
	if in.Plan == nil || in.Candidate == nil || in.Hash != p.Hash || in.Candidate.Issues[0].Profile != "links-pair" ||
		in.Plan.Issues[0].Profile != "" || in.OnBehalfOf != "slack:T01ABC/"+proposalPresser {
		t.Fatalf("revision %+v", in)
	}
	edits := rig.slack.proposalEdits()
	if len(edits) != 1 {
		t.Fatalf("edits %+v", edits)
	}
	next, ok := edits[0].meta.proposalPayload()
	if !ok || next.Rev != 2 || next.Plan.Issues[0].Profile != "links-pair" || next.Hash == p.Hash ||
		strings.Join(next.Pins, ",") != "links-solo,links-pair" {
		t.Fatalf("next %+v", next)
	}
	if e := rig.slack.whispers(); len(e) != 1 || !strings.Contains(e[0].text, "links-pair") {
		t.Fatalf("whispers %+v", e)
	}

	// A Confirm on the hash the select replaced binds a plan nobody shows any more.
	rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm, proposalValue(p.Hash, 1)))
	if e := rig.slack.whispers(); len(e) != 2 || e[1].text != planChangedLine || len(rig.broker.accepted) != 0 {
		t.Fatalf("stale confirm: %+v %+v", e, rig.broker.accepted)
	}
}

// A select press carrying a hash the message no longer shows re-plans nothing.
func TestProposalStaleSelect(t *testing.T) {
	rig, _, ts := pinRig(t)
	old := planFixture(t, "slack-one-issue")
	old.Issues[0].Title += " (an older revision)"
	rig.interact(selectPressOf(t, ts, proposalPresser, profileValue(intake.Hash(old), 1, "links-pair")))
	if len(rig.broker.intakeCalls()) != 0 {
		t.Fatal("a stale select re-planned")
	}
	if e := rig.slack.whispers(); len(e) != 1 || e[0].text != planChangedLine {
		t.Fatalf("whispers %+v", e)
	}
}

// The broker's refusal of a pin is the presser's alone, and the message stays as it was.
func TestProposalPinRefused(t *testing.T) {
	rig, p, ts := pinRig(t)
	rig.broker.intakeAnswer = nil
	rig.broker.intakeErr = Refused("not_maintainer: Only a maintainer of this repository can choose the profile; the factory will choose it.")
	rig.interact(selectPressOf(t, ts, proposalPresser, profileValue(p.Hash, 1, "links-pair")))
	if e := rig.slack.whispers(); len(e) != 1 || !strings.Contains(e[0].text, "Only a maintainer") {
		t.Fatalf("whispers %+v", e)
	}
	if n := len(rig.slack.proposalEdits()); n != 0 {
		t.Fatalf("%d edits", n)
	}
}

// Options posts classes_list's answer for the class, to the presser alone.
func TestProposalOptions(t *testing.T) {
	rig, p, ts := pinRig(t)
	cat := catalogueOf(true, "off")
	rig.broker.classes = &cat
	rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalOptions, proposalValue(p.Hash, 1)))
	e := rig.slack.whispers()
	if len(e) != 1 || !strings.Contains(e[0].text, "links-pair") || e[0].user != proposalPresser {
		t.Fatalf("whispers %+v", e)
	}
	if got := rig.broker.classesAsked; len(got) != 1 || got[0] != "Smana/ai-native-ref|docs-links|slack:T01ABC/"+proposalPresser {
		t.Fatalf("asked %v", got)
	}
	if len(rig.posts()) != 0 {
		t.Fatal("Options posted in the thread")
	}
}

// ClassesList is the broker's classes_list for one class, read for the Slack user named.
func (b *stubBroker) ClassesList(_ context.Context, repository, class, onBehalfOf string) (catalogue.Catalogue, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.classesAsked = append(b.classesAsked, repository+"|"+class+"|"+onBehalfOf)
	if b.classesErr != nil {
		return catalogue.Catalogue{}, b.classesErr
	}
	if b.classes == nil {
		return catalogue.Catalogue{}, errors.New("no catalogue")
	}
	return *b.classes, nil
}
