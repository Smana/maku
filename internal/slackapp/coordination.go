// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/coordinationview"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/roomview"
)

// Multi-repository coordination in Slack (v4 experience plan PR 4): a mention naming exactly two
// repositories plans through the broker's coordination_plan and opens the parent's draft; the parent
// is confirmed on its room page only, and merges stay on GitHub. What a channel sees of a family is
// recomputed at every render: every child only where the channel serves every child's repository,
// the parent's phase and link where it serves the primary, nothing where it does not.

// maxCoordinationRepositories is the most a coordination mention may name (MR1).
const maxCoordinationRepositories = 2

var repoTokenRE = regexp.MustCompile(`(?:^|[\s(<\x60'"])((?:https://github\.com/)?[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100})`)

// explicitRepositories is the distinct repositories text names, in order: a GitHub URL always, a
// plain owner/name only when known holds it (the factory serves it in some channel), so ordinary
// words such as and/or never select a family. The first is the user's primary.
func explicitRepositories(text string, known []string) []string {
	var out []string
	for _, m := range repoTokenRE.FindAllStringSubmatch(text, -1) {
		tok := strings.TrimRight(m[1], ".,;:!?)>'\"\x60")
		name, url := strings.CutPrefix(tok, "https://github.com/")
		name = strings.TrimSuffix(name, ".git")
		if !repoRE.MatchString(name) {
			continue
		}
		if i := slices.IndexFunc(known, func(k string) bool { return strings.EqualFold(k, name) }); i >= 0 {
			name = known[i]
		} else if !url {
			continue
		}
		if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, name) }) {
			out = append(out, name)
		}
	}
	return out
}

// The outcomes of a channel's projection, slack_coordination_projection_total{outcome}.
const (
	projectionFull    = "full"
	projectionCoarse  = "coarse"
	projectionDropped = "dropped"
)

// SlackCoordinationProjection is what one channel may be told of a family, as Slack lines.
type SlackCoordinationProjection struct {
	Outcome string
	Lines   []string
}

// projectCoordination is v as channel ch may see it: false (dropped) when ch does not serve the
// primary; the parent's phase, reason and room link only (coarse) when a child is withheld from the
// App or ch does not serve every child's repository; the card's own lines (full) otherwise.
func projectCoordination(v coordinationview.View, ch Channel, roomsURL string) (SlackCoordinationProjection, bool) {
	served := func(repo string) bool {
		return slices.ContainsFunc(ch.Repositories, func(r string) bool { return strings.EqualFold(r, repo) })
	}
	if v.Plan.Primary.Repository == "" || !served(v.Plan.Primary.Repository) {
		return SlackCoordinationProjection{Outcome: projectionDropped}, false
	}
	room := strings.TrimSuffix(roomsURL, "/") + "/r/" + v.ParentTask
	full := v.RestrictedChildren == 0
	for _, c := range v.Plan.Children {
		full = full && served(c.Repository)
	}
	if !full {
		line := "Coordination " + slackInertToken(v.Phase)
		if v.Reason != "" {
			line += " (" + slackInertToken(v.Reason) + ")"
		}
		return SlackCoordinationProjection{Outcome: projectionCoarse, Lines: []string{line + ". Parent room: " + room}}, true
	}
	lines := roomview.CoordinationLines(v, roomview.Options{Format: roomview.Slack, Rooms: roomsURL})
	return SlackCoordinationProjection{Outcome: projectionFull, Lines: append(lines, "Parent room: "+room)}, true
}

// slackInertToken is a factory code as Slack may show it: its own vocabulary only.
func slackInertToken(s string) string {
	if !coordCodeRE.MatchString(s) {
		return "unknown"
	}
	return s
}

var coordCodeRE = regexp.MustCompile(`^[A-Za-z][A-Za-z_]{0,47}$`)

// coordinationEventType tags a coordination proposal's metadata: a pointer to the core's stored
// draft, never a second plan store and never parsed from the rendered text. A separate tag keeps
// every legacy proposal byte as it was, and no legacy decoder reads it as a proposal to file.
const coordinationEventType = "factory_coordination"

// CoordinationProposal is a coordination proposal's metadata: the parent, the hash and version the
// message shows, and the mention's author, who alone may revise it.
type CoordinationProposal struct {
	ParentTask string `json:"parentTask"`
	PlanHash   string `json:"planHash"`
	Version    int32  `json:"version"`
	Author     string `json:"author"`
}

var planHashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// coordinationPayload decodes a coordination proposal's metadata; false for anything else.
func (m *Metadata) coordinationPayload() (CoordinationProposal, bool) {
	if m == nil || m.EventType != coordinationEventType {
		return CoordinationProposal{}, false
	}
	var p CoordinationProposal
	if json.Unmarshal(m.EventPayload, &p) != nil || !roomRE.MatchString(p.ParentTask) || !planHashRE.MatchString(p.PlanHash) ||
		p.Version < 1 || !userRE.MatchString(p.Author) {
		return CoordinationProposal{}, false
	}
	return p, true
}

// The coordination lines a thread is told. Fixed words, and a room link the App builds.
const (
	coordinationTooManyLine  = "A coordination names exactly two repositories: name two, or one for an ordinary task."
	coordinationUnservedLine = "This channel does not serve every repository named, so nothing was planned: name repositories this channel serves."
	coordinationMixedLine    = "This thread already holds a single-repository proposal: start a new mention for a coordination."
	coordinationPressLine    = "A coordination is confirmed on its parent room page, never in Slack, and its merges stay on GitHub: open the parent room."
	coordinationPrepareLine  = "Parent room is being prepared."
)

// knownRepositories is every repository some channel serves: a plain owner/name counts as named only
// when it is one of these (explicitRepositories).
func (a *App) knownRepositories() []string {
	var out []string
	for _, ch := range a.Channels {
		for _, r := range ch.Repositories {
			if !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	return out
}

// coordinationRoute is what a mention's text selects: 0 for the single-repository flow, 2 for a
// coordination, -1 for a refusal already said.
func (a *App) coordinationRoute(ctx context.Context, ev slackEvent, ch Channel, thread, text string, legacy bool) (repos []string, code string) {
	repos = explicitRepositories(text, a.knownRepositories())
	switch {
	case len(repos) < maxCoordinationRepositories:
		return nil, ""
	case len(repos) > maxCoordinationRepositories:
		return nil, a.refusedLine(ctx, ev.Channel, thread, coordinationTooManyLine)
	case legacy:
		return nil, a.refusedLine(ctx, ev.Channel, thread, coordinationMixedLine)
	}
	for _, r := range repos {
		if !slices.ContainsFunc(ch.Repositories, func(s string) bool { return strings.EqualFold(s, r) }) {
			return nil, a.refusedLine(ctx, ev.Channel, thread, coordinationUnservedLine)
		}
	}
	return repos, ""
}

// proposeCoordination plans a two-repository mention through coordination_plan, opens the parent's
// draft for its author, maps the parent room to the thread and posts the one proposal: a link to the
// parent room, where alone it is confirmed. Held under editMu by the caller.
func (a *App) proposeCoordination(ctx context.Context, ev slackEvent, thread, text string, repos []string, prev *Message) string {
	in := CoordinationPlanRequest{Text: text, Primary: repos[0], Candidates: repos, OnBehalfOf: "slack:" + a.TeamID + "/" + ev.User}
	var cur CoordinationProposal
	if prev != nil {
		cur, _ = prev.Metadata.coordinationPayload()
		in.ID, in.Version = cur.ParentTask, cur.Version+1
	}
	return a.planCoordination(ctx, ev.Channel, thread, ev.User, in, prev)
}

// replanCoordination revises a thread's coordination proposal from its author's reply: the same
// parent at the next version, with the replies as bounded context, edited in place.
func (a *App) replanCoordination(ctx context.Context, ev slackEvent, thread string, msgs []Message, latest *Message, cur CoordinationProposal) string {
	if cur.Author != ev.User {
		return "ignored"
	}
	a.proposalUpdate(ctx, countReply)
	text, ok := parentRequest(msgs, thread, a.BotUserID)
	if !ok {
		return a.refusedLine(ctx, ev.Channel, thread, replyNoRequest)
	}
	repos := explicitRepositories(text, a.knownRepositories())
	if len(repos) != maxCoordinationRepositories {
		return a.refusedLine(ctx, ev.Channel, thread, coordinationTooManyLine)
	}
	in := CoordinationPlanRequest{Text: text, Context: followUps(msgs, thread, a.BotUserID, cur.Author, maxProposalContextBytes),
		Primary: repos[0], Candidates: repos, OnBehalfOf: "slack:" + a.TeamID + "/" + ev.User, ID: cur.ParentTask, Version: cur.Version + 1}
	return a.planCoordination(ctx, ev.Channel, thread, cur.Author, in, latest)
}

func (a *App) planCoordination(ctx context.Context, channel, thread, author string, in CoordinationPlanRequest, prev *Message) string {
	res, err := a.Broker.CoordinationPlan(ctx, in)
	if err != nil {
		return a.refusedLine(ctx, channel, thread, "Not planned: "+refusalWords(err)+".")
	}
	if !roomRE.MatchString(res.Plan.ID) || !planHashRE.MatchString(res.Hash) {
		return a.refusedLine(ctx, channel, thread, "Not planned: the planner's answer was malformed.")
	}
	if _, err := a.Broker.CoordinationDraft(ctx, res.Plan.ID, coordination.CoordinationConfirmation{Plan: res.Plan, PlanHash: res.Hash,
		Planning: res.Planning}, in.OnBehalfOf); err != nil {
		return a.refusedLine(ctx, channel, thread, "Not drafted: "+refusalWords(err)+".")
	}
	if err := a.Threads.Put(ctx, res.Plan.ID, Thread{Channel: channel, TS: thread, At: a.now(), Coordination: true}); err != nil {
		a.log().Warn("thread map put failed", "room", res.Plan.ID, "err", err)
	}
	cp := CoordinationProposal{ParentTask: res.Plan.ID, PlanHash: res.Hash, Version: res.Plan.Version, Author: author}
	text, blocks, meta, err := a.coordinationMessage(res, cp)
	if err != nil {
		a.log().Warn("coordination proposal not built", "channel", channel, "err", err)
		return "error"
	}
	if prev != nil {
		if err := a.Slack.UpdateProposal(ctx, channel, prev.TS, text, blocks, meta); err != nil {
			a.log().Warn("coordination proposal update failed", "channel", channel, "err", err)
			return "error"
		}
		return "updated"
	}
	if _, err := a.Slack.PostCard(ctx, channel, thread, text, blocks, meta); err != nil {
		a.log().Warn("slack post failed", "channel", channel, "err", err)
		return "error"
	}
	return "proposed"
}

// coordinationMessage is the proposal as Slack shows it: the just-returned plan (whose two
// repositories this channel serves, or nothing was planned), the room-preparing line and one URL
// button to the parent room. No Confirm or Cancel: Slack decides nothing of a family.
func (a *App) coordinationMessage(res CoordinationPlanResult, cp CoordinationProposal) (string, json.RawMessage, *Metadata, error) {
	room := strings.TrimSuffix(a.RoomsURL, "/") + "/r/" + cp.ParentTask
	v := coordinationview.View{CoordinationView: coordination.CoordinationView{ParentTask: cp.ParentTask, PlanHash: res.Hash, Plan: res.Plan}}
	lines := roomview.CoordinationLines(v, roomview.Options{Format: roomview.Slack, Rooms: a.RoomsURL})
	lines = append(lines, coordinationPrepareLine+" Open the parent room to confirm: "+room)
	text := strings.Join(lines, "\n")
	type button struct {
		Type string          `json:"type"`
		Text *slackBlockText `json:"text"`
		URL  string          `json:"url"`
	}
	blocks, err := json.Marshal([]slackBlock{sectionBlock(text), {Type: "actions", Elements: []button{
		{Type: "button", Text: &slackBlockText{Type: "plain_text", Text: "Open the parent room"}, URL: room}}}})
	if err != nil {
		return "", nil, nil, err
	}
	payload, err := json.Marshal(cp)
	if err != nil {
		return "", nil, nil, err
	}
	return text, blocks, &Metadata{EventType: coordinationEventType, EventPayload: payload}, nil
}

// coordinationThread is the thread's coordination proposal, the App's latest message carrying one.
func (a *App) coordinationThread(msgs []Message) (*Message, CoordinationProposal, bool) {
	var latest *Message
	var cp CoordinationProposal
	for i := range msgs {
		m := &msgs[i]
		if m.User != a.BotUserID || m.Metadata == nil {
			continue
		}
		if p, ok := m.Metadata.coordinationPayload(); ok {
			latest, cp = m, p
		}
	}
	return latest, cp, latest != nil
}

// coordinationPress is a Confirm or Cancel press on a coordination proposal — a forged one, since
// the message has no such button: the fixed parent-UI line, and nothing decided.
func (a *App) coordinationPress(_ context.Context, meta *Metadata) string {
	if _, ok := meta.coordinationPayload(); !ok {
		return ""
	}
	return coordinationPressLine
}

// deliverCoordination posts a parent's event to its thread as the channel may see it, recomputed
// from the App's own filtered read of the parent now: full, coarse or nothing (dropped).
func (a *App) deliverCoordination(ctx context.Context, th Thread, ch Channel, room string) string {
	s, err := a.Broker.RoomSummary(ctx, room)
	if err != nil {
		a.countProjection(ctx, projectionDropped)
		return "error"
	}
	v, ok := coordinationview.Of(s)
	if !ok {
		a.countProjection(ctx, projectionDropped)
		return "ignored"
	}
	p, ok := projectCoordination(v, ch, a.RoomsURL)
	a.countProjection(ctx, p.Outcome)
	if !ok {
		return "ignored"
	}
	if _, err := a.Slack.Post(ctx, th.Channel, th.TS, strings.Join(p.Lines, "\n"), nil); err != nil {
		a.log().Warn("slack post failed", "channel", th.Channel, "err", err)
		return "error"
	}
	return "posted"
}

// ProjectionRecorder counts slack_coordination_projection_total{outcome}; a Recorder that is not one
// counts nothing.
type ProjectionRecorder interface {
	CoordinationProjection(ctx context.Context, outcome string)
}

func (a *App) countProjection(ctx context.Context, outcome string) {
	if r, ok := a.Metrics.(ProjectionRecorder); ok {
		r.CoordinationProjection(ctx, outcome)
	}
}

// refusalWords is a coordination tool's refusal as the thread may say it: the broker's bounded
// reason code (the first word of its tool error), never its free text.
func refusalWords(err error) string {
	var r Refused
	switch {
	case errors.Is(err, ErrUnknownTool):
		return "coordination is off on this factory"
	case errors.As(err, &r):
		code, _, _ := strings.Cut(strings.TrimSpace(string(r)), ":")
		if coordCodeRE.MatchString(code) {
			return code
		}
		return "refused"
	}
	return "the broker could not be reached; mention me again in a minute"
}
