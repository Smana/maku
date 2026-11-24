// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Smana/maku/internal/roomview"
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

// pinOption is one offered workflow in the plan's select, with its description and ceiling.
// UnmarshalJSON accepts both a plain string (release A's metadata) and the structured object.
type pinOption struct {
	Name             string   `json:"name"`
	Description      string   `json:"description,omitempty"`
	CeilingUSD       float64  `json:"ceilingUSD,omitempty"`
	StepUpCeilingUSD float64  `json:"stepUpCeilingUSD,omitempty"`
	Path             []string `json:"path,omitempty"`
}

func (o *pinOption) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var name string
		if err := json.Unmarshal(b, &name); err != nil {
			return err
		}
		*o = pinOption{Name: name}
		return nil
	}
	type plain pinOption
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*o = pinOption(p)
	return nil
}

// pinSelect is what the select shows: the offered workflows, the one the plan pins ("" for the
// factory's choice), and the plan's resolved workflow name.
type pinSelect struct {
	Options            []pinOption
	Current            string
	Workflow           string
	DefaultDescription string
	DefaultCeilingUSD  float64
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
	wf := proposalWorkflowName(p)
	desc, ceil := p.DefaultDescription, p.DefaultCeilingUSD
	if ceil == 0 {
		ceil = p.Plan.Budget.CeilingUSD
	}
	if desc == "" {
		for _, o := range p.Pins {
			if o.Name == wf {
				desc = o.Description
				if ceil == 0 {
					ceil = o.CeilingUSD
				}
				break
			}
		}
	}
	return &pinSelect{Options: p.Pins, Current: cur, Workflow: wf, DefaultDescription: desc, DefaultCeilingUSD: ceil}
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

const slackOptionLimit = 75

// slackOption is a static select's option.
type slackOption struct {
	Text        *slackBlockText `json:"text"`
	Description *slackBlockText `json:"description,omitempty"`
	Value       string          `json:"value"`
}

// slackSelect is a static select element.
type slackSelect struct {
	Type          string          `json:"type"`
	ActionID      string          `json:"action_id"`
	Placeholder   *slackBlockText `json:"placeholder"`
	Options       []slackOption   `json:"options"`
	InitialOption *slackOption    `json:"initial_option,omitempty"`
}

func trimOptionRunes(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	cut := string(runes[:limit])
	if i := strings.LastIndex(cut, " "); i > 0 {
		cut = strings.TrimRight(cut[:i], " ,;:")
	}
	return cut
}

func formatPinDescription(desc string, ceilingUSD float64) string {
	desc = strings.Join(strings.Fields(desc), " ")
	suffix := ""
	if ceilingUSD > 0 {
		suffix = "Up to " + roomview.Dollars(ceilingUSD)
	}
	if desc == "" {
		return trimOptionRunes(suffix, slackOptionLimit)
	}
	if suffix == "" {
		return trimOptionRunes(desc, slackOptionLimit)
	}
	full := desc + " " + suffix
	if utf8.RuneCountInString(full) <= slackOptionLimit {
		return full
	}
	budget := slackOptionLimit - 1 - utf8.RuneCountInString(suffix)
	if budget <= 0 {
		return trimOptionRunes(suffix, slackOptionLimit)
	}
	return trimOptionRunes(desc, budget) + " " + suffix
}

// pinBlock is the workflow static select, binding the hash and revision shown.
func pinBlock(hash string, rev int, s pinSelect) slackBlock {
	opt := func(text, desc, profile string) slackOption {
		o := slackOption{
			Text:  &slackBlockText{Type: "plain_text", Text: trimOptionRunes(text, slackOptionLimit)},
			Value: profileValue(hash, rev, profile),
		}
		if desc != "" {
			o.Description = &slackBlockText{Type: "plain_text", Text: desc}
		}
		return o
	}
	placeholder := s.Workflow
	firstText := "Factory's choice"
	if placeholder == "" {
		placeholder = "factory's choice"
	} else {
		firstText = "Factory's choice: " + s.Workflow
	}
	sel := slackSelect{Type: "static_select", ActionID: actionProposalProfile,
		Placeholder: &slackBlockText{Type: "plain_text", Text: trimOptionRunes(placeholder, slackOptionLimit)},
		Options:     []slackOption{opt(firstText, formatPinDescription(s.DefaultDescription, s.DefaultCeilingUSD), "")}}
	for _, o := range s.Options {
		if o.Name == "" {
			continue
		}
		sel.Options = append(sel.Options, opt(o.Name, formatPinDescription(o.Description, o.CeilingUSD), o.Name))
	}
	if s.Current != "" {
		for i := range sel.Options {
			if strings.HasSuffix(sel.Options[i].Value, "|"+s.Current) {
				sel.InitialOption = &sel.Options[i]
			}
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
	unlock := a.edits.lock(in.Channel, thread)
	code, line := a.pinInPlace(ctx, in, thread, p, profile)
	unlock()
	return a.pressAnswer(ctx, in, thread, line, code)
}

// pinInPlace is pinProposal's work under the thread's edit lock; it posts nothing.
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
	if opts := checked.pinOptions(); len(opts) > 0 {
		next.Pins = opts
	}
	next.DefaultDescription, next.DefaultCeilingUSD = p.DefaultDescription, p.DefaultCeilingUSD
	if checked.DefaultChoice != nil {
		next.DefaultDescription, next.DefaultCeilingUSD = checked.DefaultChoice.Description, checked.DefaultChoice.CeilingUSD
	}
	next.Path, next.StepUpUSD = p.Path, p.StepUpUSD
	if checked.ActiveChoice != nil {
		next.Path, next.StepUpUSD = checked.ActiveChoice.Path, checked.ActiveChoice.StepUpCeilingUSD
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
