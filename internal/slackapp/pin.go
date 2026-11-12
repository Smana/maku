// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/roomview"
)

// A maintainer pins a harness profile from the proposal (P5, spec "Slack proposal"): a static select
// under the suggestion lists the factory's choice and the class's offered profiles, choosing one is a
// profile-only revision the broker answers with no model call, and the message becomes that revision
// with its new hash. Confirm binds the hash shown, as it always did. The broker checks every pin
// against the catalogue as the presser reads it, and the factory checks it again at triage (P6).

// The lines a pin's press is answered with, to the presser alone.
const (
	pinnedLine         = "Profile %s pinned; the proposal shows the new revision: check it and confirm."
	unpinnedLine       = "Profile back to the factory's choice; the proposal shows the new revision: check it and confirm."
	pinUnchangedLine   = "That profile is already the one this proposal shows."
	pinRefusedPrefix   = "Profile not changed: "
	pinUnreachableLine = "Profile not changed: the planner could not be reached; try again in a minute."
	optionsLine        = "Options not shown: "
)

// pinSelect is what the select shows: the offered profiles and the one the plan pins ("" for the
// factory's choice).
type pinSelect struct {
	Options []string
	Current string
}

// pinOptions is the select's profiles for plan, read through classes_list for its author: the
// class's offered floor and allowed profiles, when the author maintains the repository and the class
// is neither live nor shadow (one profile by its merge policy). None for a plan made without the
// catalogue (no suggestion), for a plan of more than one issue, or when the catalogue cannot be read:
// the proposal then offers no pin, and nothing else about it changes.
func (a *App) pinOptions(ctx context.Context, plan intake.Plan, author string) []string {
	if len(plan.Issues) != 1 || plan.Issues[0].Suggestion == nil || !userRE.MatchString(author) {
		return nil
	}
	class := plan.Issues[0].Class
	c, err := a.Broker.ClassesList(ctx, plan.Repository, class, "slack:"+a.TeamID+"/"+author)
	if err != nil {
		if !errors.Is(err, ErrUnknownTool) {
			a.log().Info("proposal: no profile select, the catalogue is unavailable", "err", err)
		}
		return nil
	}
	cl, ok := c.Class(class)
	if !ok || !c.Maintainer || cl.Mode == catalogue.ModeLive || cl.Mode == catalogue.ModeShadow {
		return nil
	}
	return c.OfferedProfiles(class)
}

// pinsShown is the select p's message carries: none without pins, and none while the plan still asks
// a question, since the broker pins only a plan with every gap answered.
func pinsShown(p ProposalPayload) *pinSelect {
	if len(p.Pins) == 0 || len(p.Plan.Issues) != 1 || p.Plan.Question != nil || len(p.Plan.Gaps) > 0 {
		return nil
	}
	return &pinSelect{Options: p.Pins, Current: p.Plan.Issues[0].Profile}
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

// pinBlock is the select and the Options button, both binding the hash and revision shown.
func pinBlock(hash string, rev int, s pinSelect) slackBlock {
	opt := func(text, profile string) slackOption {
		return slackOption{Text: &slackBlockText{Type: "plain_text", Text: text}, Value: profileValue(hash, rev, profile)}
	}
	sel := slackSelect{Type: "static_select", ActionID: actionProposalProfile,
		Placeholder: &slackBlockText{Type: "plain_text", Text: "Profile"},
		Options:     []slackOption{opt("Profile: factory's choice", "")}}
	for _, name := range s.Options {
		sel.Options = append(sel.Options, opt("Profile: "+name, name))
	}
	for i := range sel.Options {
		if strings.HasSuffix(sel.Options[i].Value, "|"+s.Current) {
			sel.InitialOption = &sel.Options[i]
		}
	}
	return slackBlock{Type: "actions", Elements: []any{sel,
		slackButton{Type: "button", Text: &slackBlockText{Type: "plain_text", Text: "Options"},
			ActionID: actionProposalOptions, Value: proposalValue(hash, rev)}}}
}

// pinProposal is a select's press: the plan shown with only its profile changed, sent to the broker
// as a revision for the presser (whose maintainer flag the broker reads), and the message rewritten
// as the revision it answers. The thread is read under the edit lock first, so a press on a replaced
// or closed proposal changes nothing.
func (a *App) pinProposal(ctx context.Context, in interaction, thread string, p ProposalPayload, profile string) string {
	if len(p.Plan.Issues) != 1 {
		return a.pressAnswer(ctx, in, thread, proposalGoneLine, "refused")
	}
	if p.Plan.Issues[0].Profile == profile {
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
	base := p.Plan
	candidate := base
	candidate.Issues = slices.Clone(base.Issues)
	candidate.Issues[0].Profile = profile
	ch := a.Channels[in.Channel]
	text := "profile: factory's choice"
	if profile != "" {
		text = "profile: " + profile
	}
	res, err := a.Broker.IntakePlan(ctx, IntakeRequest{Text: text, OnBehalfOf: "slack:" + a.TeamID + "/" + in.User,
		Candidates: ch.Repositories, CeilingUSD: ch.MaxCostUSD, Plan: &base, Hash: p.Hash, Candidate: &candidate})
	var r Refused
	switch {
	case errors.As(err, &r):
		return "refused", pinRefusedPrefix + shown(string(r))
	case err != nil:
		a.log().Warn("intake_plan pin failed", "err", err)
		return "error", pinUnreachableLine
	}
	next, err := newProposal(res.Plan, res.Hash, p.Author, p.Rev+1)
	if err != nil {
		a.log().Warn("pinned proposal not built", "channel", in.Channel, "err", err)
		return "error", pinUnreachableLine
	}
	next.Pins = p.Pins // the same class: the offered profiles have not moved
	if code := a.updateProposal(ctx, in.Channel, in.MessageTS, p, next, res.Question); code == "error" {
		return "error", proposalUnreadableLine
	}
	if profile == "" {
		return "pinned", unpinnedLine
	}
	return "pinned", strings.Replace(pinnedLine, "%s", profile, 1)
}

// proposalOptions is the Options button: classes_list's answer for the plan's class, read for the
// presser, to the presser alone.
func (a *App) proposalOptions(ctx context.Context, in interaction, thread string, p ProposalPayload) string {
	if len(p.Plan.Issues) != 1 {
		return a.pressAnswer(ctx, in, thread, proposalGoneLine, "refused")
	}
	class := p.Plan.Issues[0].Class
	c, err := a.Broker.ClassesList(ctx, p.Plan.Repository, class, "slack:"+a.TeamID+"/"+in.User)
	var r Refused
	switch {
	case errors.As(err, &r):
		return a.pressAnswer(ctx, in, thread, optionsLine+shown(string(r)), "refused")
	case err != nil:
		a.log().Warn("classes_list failed", "err", err)
		return a.pressAnswer(ctx, in, thread, optionsLine+"the factory's list of classes could not be read; try again in a minute.", "error")
	}
	return a.pressAnswer(ctx, in, thread, roomview.CatalogueText(c, class), "options")
}
