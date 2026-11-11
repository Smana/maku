// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/intake"
)

func TestCanonicalRepo(t *testing.T) {
	list := []string{"Smana/ai-native-ref", "Smana/cloud-native-ref"}
	for name, tc := range map[string]struct {
		repo, want string
		ok         bool
	}{
		"exact":              {"Smana/ai-native-ref", "Smana/ai-native-ref", true},
		"the planner's form": {"smana/ai-native-ref", "Smana/ai-native-ref", true},
		"shouting":           {"SMANA/CLOUD-NATIVE-REF", "Smana/cloud-native-ref", true},
		"not served":         {"smana/other", "", false},
		"empty":              {"", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := canonicalRepo(list, tc.repo)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("canonicalRepo(%q) = %q, %v; want %q, %v", tc.repo, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestParseMentionRepositoryCase(t *testing.T) {
	d, err := ParseMention("<@U0BOT> smana/ai-native-ref the link is dead", "U0BOT", []string{"Smana/ai-native-ref"})
	if err != nil || d.Repo != "Smana/ai-native-ref" || !strings.HasSuffix(d.Title, "the link is dead") || strings.Contains(d.Title, "smana/") {
		t.Fatalf("a lowercased repository naming the channel's only one is that repository, not title text: %+v %v", d, err)
	}
}

// The live case on gcp-0 (v0.43.0): the planner lowercases the repository, the channel lists it as
// GitHub spells it, and the Confirm was refused as "isn't served in this channel any more". The plan
// keeps the planner's form (its hash covers it); what is filed is the channel's spelling.
func TestProposalConfirmRepositoryCase(t *testing.T) {
	for name, listed := range map[string]string{
		"canonical in the channel": "Smana/ai-native-ref",
		"lowercase in the channel": "smana/ai-native-ref",
	} {
		t.Run(name, func(t *testing.T) {
			rig := newAppRig(t, Channel{Repositories: []string{listed}, MaxCostUSD: 2})
			rig.app.Proposals = Proposals{Enabled: true}
			plan := planFixture(t, "slack-one-issue")
			plan.Repository = "smana/ai-native-ref" // what Compose answers with
			rig.slack.seed(Message{TS: proposalThreadTS, User: proposalAuthor, Text: "<@U0BOT> " + mentionWords})
			p, err := newProposal(plan, intake.Hash(plan), proposalAuthor, 1)
			if err != nil {
				t.Fatalf("newProposal: %v", err)
			}
			ts := "1729.0002"
			rig.slack.seed(Message{TS: ts, ThreadTS: proposalThreadTS, User: "U0BOT",
				Text: "Proposal v1 · " + p.Hash, Metadata: metaOf(t, proposalEventType, p)})
			rig.broker.room = "26zfnuxm"
			if st := rig.interact(proposalPressOf(t, ts, proposalPresser, actionProposalConfirm,
				proposalValue(p.Hash, 1))); st != http.StatusOK {
				t.Fatalf("%d, want 200", st)
			}
			calls := rig.broker.runCreates()
			if len(calls) != 1 {
				t.Fatalf("%d run_create calls, want one; the presser saw %+v", len(calls), rig.slack.whispers())
			}
			if calls[0].Repo != listed || calls[0].PlanHash != p.Hash {
				t.Fatalf("filed %q with hash %s; want the channel's %q and the plan's own hash", calls[0].Repo, calls[0].PlanHash, listed)
			}
			edits := rig.slack.proposalEdits()
			if len(edits) != 1 || edits[0].meta == nil {
				t.Fatalf("edits: %+v", edits)
			}
			if mark, ok := edits[0].meta.markerPayload(); !ok || mark.Repo != listed || strings.Contains(edits[0].text, "isn't served") {
				t.Fatalf("marker %+v %v, text %q", mark, ok, edits[0].text)
			}
		})
	}
}
