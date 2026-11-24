// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/intake"
)

// The blocks carry the workflow select only when the payload lists pins, and only on a plan with
// nothing left to ask; each option's value binds the hash and revision shown.
func TestProposalSelectBlocks(t *testing.T) {
	p := proposalFixture(t)
	_, raw, err := proposalSections(p, nil, nil, "C03")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), actionProposalProfile) {
		t.Fatalf("a payload without pins has a select: %s", raw)
	}
	p.Pins = []pinOption{{Name: "links-solo"}, {Name: "links-pair"}}
	_, raw, err = proposalSections(p, nil, nil, "C03")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"static_select"`, actionProposalProfile, "Factory's choice",
		`"value":"` + profileValue(p.Hash, p.Rev, "links-pair") + `"`, `"value":"` + profileValue(p.Hash, p.Rev, "") + `"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("blocks lack %q: %s", want, raw)
		}
	}
	if strings.Contains(string(raw), `"Profile`) {
		t.Error("the word is workflow (WF1)")
	}
}

func TestThePinSelectDescribesEachWorkflow(t *testing.T) {
	b := pinBlock("sha256:abc", 1, pinSelect{
		Workflow:           "standard",
		DefaultDescription: "An implementer and a reviewer. The default.",
		DefaultCeilingUSD:  5.10,
		Options: []pinOption{
			{Name: "quick", Description: "One implementer. For changes a check can verify.", CeilingUSD: 5.10},
			{Name: "careful", Description: "Scoped first, judged before review. For wide or unclear tasks.", CeilingUSD: 13.60},
		},
	})
	sel := b.Elements.([]any)[0].(slackSelect)
	if got := sel.Options[0].Text.Text; got != "Factory's choice: standard" {
		t.Fatalf("first option %q", got)
	}
	if sel.Options[0].Description == nil || !strings.Contains(sel.Options[0].Description.Text, "Up to $5.10") || len([]rune(sel.Options[0].Description.Text)) > 75 {
		t.Errorf("first option description %+v", sel.Options[0].Description)
	}
	for _, o := range sel.Options[1:] {
		if o.Description == nil || !strings.Contains(o.Description.Text, "Up to $") || len([]rune(o.Description.Text)) > 75 {
			t.Errorf("option %q: description %+v", o.Text.Text, o.Description)
		}
		if strings.HasPrefix(o.Text.Text, "Workflow: ") {
			t.Errorf("a bare name: %q", o.Text.Text)
		}
	}
}

func TestAnOldOptionValueStillPins(t *testing.T) {
	if _, p, ok := cutProfileValue(profileValue("sha256:abc", 1, "careful")); !ok || p != "careful" {
		t.Fatal("the option's value changed shape")
	}
}

// pinRig is a live proposal whose payload offers pins, and the broker's stored handoff ready for
// handoff_check to pin a workflow on.
func pinRig(t *testing.T) (*appRig, ProposalPayload, string) {
	t.Helper()
	rig, plan := withProposals(t)
	plan.Issues[0].Suggestion = &intake.Suggestion{Merge: intake.MergeShown{Outcome: "maintainer"}}
	rig.broker.setPlan(plan, false, []string{"links-solo", "links-pair"})
	rig.slack.seed(Message{TS: proposalThreadTS, User: "U02DEF", Text: "<@U0BOT> " + mentionWords})
	p, err := newProposal(plan, intake.Hash(plan), "U02DEF", 1)
	if err != nil {
		t.Fatal(err)
	}
	p.Pins = []pinOption{{Name: "links-solo"}, {Name: "links-pair"}}
	ts := "1729.0002"
	rig.slack.seed(Message{TS: ts, ThreadTS: proposalThreadTS, User: "U0BOT", Text: "Proposal v1", Metadata: metaOf(t, proposalEventType, p)})
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

// Choosing a workflow calls handoff_check with {id, on_behalf_of, workflow}, and the message becomes
// the new revision with its new hash; a Confirm on the old hash then files nothing.
func TestProposalProfileChoice(t *testing.T) {
	rig, p, ts := pinRig(t)
	if st := rig.interact(selectPressOf(t, ts, proposalPresser, profileValue(p.Hash, 1, "links-pair"))); st != http.StatusOK {
		t.Fatalf("status %d", st)
	}
	checks := rig.broker.handoffChecks()
	if len(checks) != 1 {
		t.Fatalf("handoff_check calls %+v", checks)
	}
	in := checks[0]
	if in.id != p.ID || in.workflow == nil || *in.workflow != "links-pair" || in.onBehalfOf != "slack:T01ABC/"+proposalPresser {
		t.Fatalf("handoff_check %+v", in)
	}
	if len(rig.broker.handoffCalls()) != 0 {
		t.Fatalf("workflow select called handoff: %+v", rig.broker.handoffCalls())
	}
	edits := rig.slack.proposalEdits()
	if len(edits) != 1 {
		t.Fatalf("edits %+v", edits)
	}
	next, ok := edits[0].meta.proposalPayload()
	var pinNames []string
	for _, o := range next.Pins {
		pinNames = append(pinNames, o.Name)
	}
	if !ok || next.Rev != 2 || next.Pinned != "links-pair" || next.Hash == p.Hash ||
		strings.Join(pinNames, ",") != "links-solo,links-pair" {
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

// A select press carrying a hash the message no longer shows pins nothing.
func TestProposalStaleSelect(t *testing.T) {
	rig, _, ts := pinRig(t)
	old := planFixture(t, "slack-one-issue")
	old.Issues[0].Title += " (an older revision)"
	rig.interact(selectPressOf(t, ts, proposalPresser, profileValue(intake.Hash(old), 1, "links-pair")))
	for _, c := range rig.broker.handoffChecks() {
		if c.workflow != nil {
			t.Fatalf("a stale select pinned a workflow: %+v", c)
		}
	}
	if e := rig.slack.whispers(); len(e) != 1 || e[0].text != planChangedLine {
		t.Fatalf("whispers %+v", e)
	}
}

// The broker's refusal of a pin is the presser's alone, and the message stays as it was.
func TestProposalPinRefused(t *testing.T) {
	rig, p, ts := pinRig(t)
	rig.broker.checkErr = Refused("not_maintainer: Only a maintainer of this repository can choose the workflow; the factory will choose it.")
	rig.interact(selectPressOf(t, ts, proposalPresser, profileValue(p.Hash, 1, "links-pair")))
	if e := rig.slack.whispers(); len(e) != 1 || !strings.Contains(e[0].text, "Only a maintainer") {
		t.Fatalf("whispers %+v", e)
	}
	if n := len(rig.slack.proposalEdits()); n != 0 {
		t.Fatalf("%d edits", n)
	}
}
