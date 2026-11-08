// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"strconv"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/coordinationview"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/summary"
)

func (b *stubBroker) CoordinationPlan(_ context.Context, in CoordinationPlanRequest) (CoordinationPlanResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.coordPlans = append(b.coordPlans, in)
	if b.coordErr != nil {
		return CoordinationPlanResult{}, b.coordErr
	}
	return b.coordResult(in), nil
}

func (b *stubBroker) CoordinationDraft(_ context.Context, parent string, in coordination.CoordinationConfirmation, onBehalfOf string) (coordination.CoordinationConfirmation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.coordDrafts = append(b.coordDrafts, parent+" "+onBehalfOf)
	return in, nil
}

const (
	repoContracts = "Smana/public-contracts"
	repoConsumer  = "Smana/public-consumer"
)

func coordRig(t *testing.T, served ...string) *appRig {
	t.Helper()
	if served == nil {
		served = []string{repoContracts, repoConsumer}
	}
	rig := newAppRig(t, Channel{Repositories: served, MaxCostUSD: 2})
	rig.app.Proposals = Proposals{Enabled: true}
	rig.broker.coordResult = func(in CoordinationPlanRequest) CoordinationPlanResult {
		id, v := in.ID, in.Version
		if id == "" {
			id, v = "pppppppp", 1
		}
		return CoordinationPlanResult{Hash: strings.Repeat("a", 63) + strconv.Itoa(int(v)),
			Plan: v1alpha1.CoordinationPlan{ID: id, Version: v, Title: "v1 schema", Primary: v1alpha1.CoordinationPrimary{Repository: in.Primary},
				Children: []v1alpha1.CoordinationChild{{ID: "contract", Repository: repoContracts}, {ID: "consumer", Repository: repoConsumer}}},
			Planning: coordination.PlanningReconciliation{AttemptID: "att", EffectiveRequester: "Smana"}}
	}
	return rig
}

func TestSlackCoordinationOriginUsesTheSharedPlanner(t *testing.T) {
	rig := coordRig(t)
	rig.mention("C03", "1729.0001", "<@U0BOT> publish the v1 schema in "+repoContracts+" and consume it in "+repoConsumer)
	b := rig.broker
	if len(b.coordPlans) != 1 || b.coordPlans[0].Primary != repoContracts || b.coordPlans[0].ID != "" || b.coordPlans[0].Version != 0 ||
		strings.Join(b.coordPlans[0].Candidates, ",") != repoContracts+","+repoConsumer || b.coordPlans[0].OnBehalfOf != "slack:T01ABC/U02DEF" {
		t.Fatalf("%+v", b.coordPlans)
	}
	if len(b.coordDrafts) != 1 || b.coordDrafts[0] != "pppppppp slack:T01ABC/U02DEF" || len(b.intake) != 0 || len(b.accepted) != 0 {
		t.Fatalf("drafts %v, intake %d, runs %d", b.coordDrafts, len(b.intake), len(b.accepted))
	}
	posts := rig.posts()
	if len(posts) != 1 {
		t.Fatalf("%d posts", len(posts))
	}
	p := posts[0]
	cp, ok := p.meta.coordinationPayload()
	if !ok || cp.ParentTask != "pppppppp" || cp.Version != 1 || cp.Author != "U02DEF" {
		t.Fatalf("%+v %v", cp, ok)
	}
	blocks := string(p.blocks)
	if !strings.Contains(blocks, "https://rooms.example/r/pppppppp") || strings.Contains(blocks, actionProposalConfirm) || strings.Contains(blocks, actionProposalCancel) {
		t.Fatalf("the proposal must link the parent room and carry no Slack decision: %s", blocks)
	}
	if th, found, _ := rig.threads.Get(context.Background(), "pppppppp"); !found || th.TS != "1729.0001" || !th.Coordination {
		t.Fatalf("thread map %+v %v", th, found)
	}
}

func TestSlackCoordinationRefusesBeforeAnyModelCall(t *testing.T) {
	t.Run("an unserved repository", func(t *testing.T) {
		rig := coordRig(t, repoContracts)
		rig.mention("C03", "1729.0001", "<@U0BOT> change "+repoContracts+" and https://github.com/"+repoConsumer)
		if len(rig.broker.coordPlans) != 0 || len(rig.broker.intake) != 0 || len(rig.posts()) != 1 {
			t.Fatalf("plans %d, intake %d, posts %d", len(rig.broker.coordPlans), len(rig.broker.intake), len(rig.posts()))
		}
	})
	t.Run("a third repository", func(t *testing.T) {
		rig := coordRig(t, repoContracts, repoConsumer, "Smana/third")
		rig.mention("C03", "1729.0001", "<@U0BOT> "+repoContracts+" "+repoConsumer+" Smana/third")
		if len(rig.broker.coordPlans) != 0 || len(rig.broker.intake) != 0 || !strings.Contains(rig.posts()[0].text, "two repositories") {
			t.Fatalf("%+v", rig.posts())
		}
	})
}

func TestSlackCoordinationReplyEditsTheSameSnapshotAndMessage(t *testing.T) {
	rig := coordRig(t)
	rig.mention("C03", "1729.0001", "<@U0BOT> "+repoContracts+" then "+repoConsumer)
	first := rig.posts()[0]
	rig.slack.seed(Message{TS: first.ts, ThreadTS: "1729.0001", User: "U0BOT", Text: first.text, Metadata: first.meta})
	rig.slack.seed(Message{TS: "1729.0001", User: "U02DEF", Text: "<@U0BOT> " + repoContracts + " then " + repoConsumer})
	rig.replySaid("U02DEF", "1729.9201", "also bump the minor version")
	b := rig.broker
	if len(b.coordPlans) != 2 || b.coordPlans[1].ID != "pppppppp" || b.coordPlans[1].Version != 2 || b.coordPlans[1].Primary != repoContracts ||
		!strings.Contains(b.coordPlans[1].Context, "also bump the minor version") {
		t.Fatalf("%+v", b.coordPlans)
	}
	if len(rig.posts()) != 1 || len(rig.slack.proposalUpdates) != 1 {
		t.Fatalf("posts %d, updates %d: one message, edited in place", len(rig.posts()), len(rig.slack.proposalUpdates))
	}
	if cp, ok := rig.slack.proposalUpdates[0].meta.coordinationPayload(); !ok || cp.Version != 2 {
		t.Fatalf("%+v", rig.slack.proposalUpdates[0].meta)
	}
}

func TestSlackCoordinationNoApprovalForgedActionsAreRefused(t *testing.T) {
	rig := coordRig(t)
	rig.mention("C03", "1729.0001", "<@U0BOT> "+repoContracts+" then "+repoConsumer)
	p := rig.posts()[0]
	rig.slack.seed(Message{TS: p.ts, ThreadTS: "1729.0001", User: "U0BOT", Text: p.text, Metadata: p.meta})
	for _, action := range []string{actionProposalConfirm, actionProposalCancel} {
		line := rig.app.coordinationPress(context.Background(), p.meta)
		if !strings.Contains(line, "parent room") || len(rig.broker.accepted) != 0 || len(rig.broker.coordDrafts) != 1 {
			t.Fatalf("%s: %q", action, line)
		}
	}
}

func TestSlackCoordinationDeliveryFollowsTheChannelRule(t *testing.T) {
	view := func(restricted int) *summary.Summary {
		v := coordinationview.View{CoordinationView: coordination.CoordinationView{ParentTask: "pppppppp", Phase: "Coordinating", PlanHash: "h",
			Plan: v1alpha1.CoordinationPlan{ID: "pppppppp", Version: 1, Primary: v1alpha1.CoordinationPrimary{Repository: repoContracts},
				Children: []v1alpha1.CoordinationChild{{ID: "contract", Repository: repoContracts}, {ID: "consumer", Repository: repoConsumer}}},
			Children: []coordination.CoordinationChildView{{ID: "contract", Repository: repoContracts, Phase: "Merged"}, {ID: "consumer", Repository: repoConsumer, Phase: "Running"}}},
			RestrictedChildren: restricted}
		if restricted > 0 {
			v.Plan.Children, v.Children = v.Plan.Children[:1], v.Children[:1]
		}
		s := summary.Summary{Room: "pppppppp"}
		coordinationview.Attach(&s, v)
		return &s
	}
	for name, c := range map[string]struct {
		served     []string
		restricted int
		want       string
		posts      int
	}{
		"a channel serving every child":     {[]string{repoContracts, repoConsumer}, 0, "consumer (" + repoConsumer + "): Running", 1},
		"a channel serving the primary":     {[]string{repoContracts}, 0, "Coordination Coordinating", 1},
		"a child withheld from the App":     {[]string{repoContracts, repoConsumer}, 1, "Coordination Coordinating", 1},
		"a channel not serving the primary": {[]string{repoConsumer}, 0, "", 0},
	} {
		t.Run(name, func(t *testing.T) {
			rig := coordRig(t, c.served...)
			_ = rig.threads.Put(context.Background(), "pppppppp", Thread{Channel: "C03", TS: "1729.0001", Coordination: true})
			rig.broker.summary = view(c.restricted)
			rig.app.deliver(context.Background(), []byte(`{"type":"room.merged","room":"pppppppp","pr":2}`))
			posts := rig.posts()
			if len(posts) != c.posts {
				t.Fatalf("%d posts: %+v", len(posts), posts)
			}
			if c.posts == 1 && (!strings.Contains(posts[0].text, c.want) || (c.restricted > 0 || len(c.served) == 1) && strings.Contains(posts[0].text, "consumer")) {
				t.Fatalf("%q", posts[0].text)
			}
		})
	}
}

// Review Focus 2: a channel that stops serving a child, or loses its read, is told only the coarse
// line from the next delivery on: nothing is kept from an earlier full message.
func TestSlackProjectionRecomputedAfterChannelOrAccessChange(t *testing.T) {
	rig := coordRig(t)
	_ = rig.threads.Put(context.Background(), "pppppppp", Thread{Channel: "C03", TS: "1729.0001", Coordination: true})
	full := summary.Summary{Room: "pppppppp"}
	v := coordinationview.View{CoordinationView: coordination.CoordinationView{ParentTask: "pppppppp", Phase: "Coordinating", PlanHash: "h",
		Plan: v1alpha1.CoordinationPlan{ID: "pppppppp", Version: 1, Primary: v1alpha1.CoordinationPrimary{Repository: repoContracts},
			Children: []v1alpha1.CoordinationChild{{ID: "contract", Repository: repoContracts}, {ID: "consumer", Repository: repoConsumer}}},
		Children: []coordination.CoordinationChildView{{ID: "contract", Repository: repoContracts}, {ID: "consumer", Repository: repoConsumer, Phase: "Running"}}}}
	coordinationview.Attach(&full, v)
	rig.broker.summary = &full
	event := []byte(`{"type":"room.merged","room":"pppppppp","pr":2}`)
	rig.app.deliver(context.Background(), event)
	rig.app.Channels["C03"] = Channel{Repositories: []string{repoContracts}}
	rig.app.deliver(context.Background(), event)
	restricted := summary.Summary{Room: "pppppppp"}
	v.Plan.Children, v.Children, v.RestrictedChildren = v.Plan.Children[:1], v.Children[:1], 1
	coordinationview.Attach(&restricted, v)
	rig.broker.summary = &restricted
	rig.app.Channels["C03"] = Channel{Repositories: []string{repoContracts, repoConsumer}}
	rig.app.deliver(context.Background(), event)
	posts := rig.posts()
	if len(posts) != 3 || !strings.Contains(posts[0].text, "consumer") || strings.Contains(posts[1].text, "consumer") || strings.Contains(posts[2].text, "consumer") {
		t.Fatalf("%+v", posts)
	}
	if len(rig.slack.proposalUpdates) != 0 {
		t.Fatal("an earlier message was edited")
	}
}
