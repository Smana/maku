// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

// A maintainer pins a workflow (a harness profile) from the proposal (P5, UX2, spec "Slack plan"): a
// static select lists the factory's choice and the repository's offered workflows, choosing one calls
// handoff_check with {id, on_behalf_of, workflow} and no model call, and the message becomes that
// revision with its new hash. Confirm binds the hash shown, as it always did.

// The lines a pin's press is answered with, to the presser alone.
const (
	pinnedLine         = "Workflow %s pinned; the proposal shows the new revision: check it and confirm."
	unpinnedLine       = "Workflow back to the factory's choice; the proposal shows the new revision: check it and confirm."
	pinUnchangedLine   = "That workflow is already the one this proposal shows."
	pinRefusedPrefix   = "Workflow not changed: "
	pinUnreachableLine = "Workflow not changed: the planner could not be reached; try again in a minute."
)

// pinSelect is what the select shows: the offered workflows, the one the plan pins ("" for the
// factory's choice), and the plan's resolved workflow name.
type pinSelect struct {
	Options  []string
	Current  string
	Workflow string
}

// pinsShown is the select p's message carries: none without pins, and none while the plan still asks
// a question, since the broker pins only a plan with every gap answered.
func pinsShown(p ProposalPayload) *pinSelect {
	if len(p.Pins) == 0 {
		return nil
	}
	if p.Plan.APIVersion != "" && (len(p.Plan.Issues) != 1 || p.Plan.Question != nil || len(p.Plan.Gaps) > 0) {
		return nil
	}
	cur := p.Pinned
	if p.Plan.APIVersion != "" && len(p.Plan.Issues) == 1 {
		cur = p.Plan.Issues[0].Profile
	}
	return &pinSelect{Options: p.Pins, Current: cur, Workflow: p.Workflow}
}

// profileValue is a select option's value: the proposal's value and the profile ("" for the
// factory's choice). At most 150 characters: a profile's name is at most 63.
func profileValue(hash string, rev int, profile string) string {
	return proposalValue(hash, rev) + "|" + profile
}

// cutProfileValue splits an option's value into the proposal's value and the profile.
func cutProfileValue(v string) (value, profile string, ok bool) {
	i := strings.LastIndex(v, "|")
	if i < 0 {
		return "", "", false
	}
	value, profile = v[:i], v[i+1:]
	if profile != "" && !profileNameRE.MatchString(profile) {
		return "", "", false
	}
	return value, profile, true
}

// profileNameRE is a harness profile's name as config spells it.
var profileNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// slackOption is a static select's option.
type slackOption struct {
	Text  *slackBlockText `json:"text"`
	Value string          `json:"value"`
}

// slackSelect is a static select element.
type slackSelect struct {
	Type          string          `json:"type"`
	ActionID      string          `json:"action_id"`
	Placeholder   *slackBlockText `json:"placeholder"`
	Options       []slackOption   `json:"options"`
	InitialOption *slackOption    `json:"initial_option,omitempty"`
}

// pinBlock is the workflow static select, binding the hash and revision shown.
func pinBlock(hash string, rev int, s pinSelect) slackBlock {
	opt := func(text, profile string) slackOption {
		return slackOption{Text: &slackBlockText{Type: "plain_text", Text: text}, Value: profileValue(hash, rev, profile)}
	}
	sel := slackSelect{Type: "static_select", ActionID: actionProposalProfile,
		Placeholder: &slackBlockText{Type: "plain_text", Text: "Workflow"},
		Options:     []slackOption{opt("Workflow: factory's choice", "")}}
	for _, name := range s.Options {
		sel.Options = append(sel.Options, opt("Workflow: "+name, name))
	}
	for i := range sel.Options {
		if strings.HasSuffix(sel.Options[i].Value, "|"+s.Current) {
			sel.InitialOption = &sel.Options[i]
		}
	}
	return slackBlock{Type: "actions", Elements: []any{sel}}
}

// pinProposal is a select's press: handoff_check with {id, on_behalf_of, workflow} for the presser
// (whose maintainer flag the broker reads), and the message rewritten as the revision it answers. The
// thread is read under the edit lock first, so a press on a replaced or closed proposal changes nothing.
func (a *App) pinProposal(ctx context.Context, in interaction, thread string, p ProposalPayload, profile string) string {
	if !proposalHashHolds(p) {
		return a.pressAnswer(ctx, in, thread, proposalGoneLine, "refused")
	}
	if p.Pinned == profile {
		return a.pressAnswer(ctx, in, thread, pinUnchangedLine, "ignored")
	}
	a.editMu.Lock()
	code, line := a.pinInPlace(ctx, in, thread, p, profile)
	a.editMu.Unlock()
	return a.pressAnswer(ctx, in, thread, line, code)
}

// pinInPlace is pinProposal's work under a.editMu; it posts nothing.
func (a *App) pinInPlace(ctx context.Context, in interaction, thread string, p ProposalPayload, profile string) (code, line string) {
	msgs, err := a.Slack.Thread(ctx, in.Channel, thread)
	if err != nil {
		a.log().Warn("slack thread fetch failed", "channel", in.Channel, "err", err)
		return "error", proposalUnreadableLine
	}
	if code, line := a.threadPress(in, msgs); code != "" {
		return code, line
	}
	latest, _ := a.threadProposal(msgs)
	if latest == nil {
		return "refused", proposalStaleLine
	}
	current, ok := latest.Metadata.proposalPayload()
	if !ok || current.Hash != p.Hash || current.ID != p.ID {
		return "refused", planChangedLine
	}
	checked, err := a.Broker.HandoffCheck(ctx, p.ID, "slack:"+a.TeamID+"/"+in.User, &profile)
	var r Refused
	switch {
	case errors.As(err, &r):
		return "refused", pinRefusedPrefix + shown(string(r))
	case err != nil:
		a.log().Warn("handoff_check pin failed", "err", err)
		return "error", pinUnreachableLine
	}
	if checked.Hash == p.Hash {
		return "ignored", pinUnchangedLine
	}
	next, err := newProposal(checked.Plan, checked.Hash, p.Author, p.Rev+1)
	if err != nil {
		a.log().Warn("pinned proposal not built", "channel", in.Channel, "err", err)
		return "error", pinUnreachableLine
	}
	next.ID = p.ID
	next.Degraded = p.Degraded
	next.Pins = p.Pins
	if len(checked.Workflows) > 0 {
		next.Pins = checked.Workflows
	}
	next.Workflow = p.Workflow
	if profile != "" {
		next.Workflow = profile
	}
	prev := p
	prev.Plan = checked.Plan
	if code := a.updateProposal(ctx, in.Channel, in.MessageTS, prev, next, checked.Plan.Question); code == "error" {
		return "error", proposalUnreadableLine
	}
	if profile == "" {
		return "pinned", unpinnedLine
	}
	return "pinned", strings.Replace(pinnedLine, "%s", profile, 1)
}
