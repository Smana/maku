// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/roomview"
)

const (
	// proposalEventType tags the App's proposal message (CD20): the whole plan, its hash, its author
	// and its revision are that message's metadata, and nothing about a proposal lives anywhere else.
	proposalEventType = "factory_proposal"
	// maxProposalPlanBytes bounds the plan's canonical JSON, so the metadata it travels in stays
	// under maxMetadata (§plan's Slack bound). A plan past it is refused whole, never trimmed: a
	// message that showed part of a plan would confirm a plan nobody read.
	maxProposalPlanBytes = 7 << 10
	// maxProposalRevisions bounds the revisions one proposal reaches in a thread; past it the thread
	// says to confirm or to start a new mention. The broker's dailyPlans is the second bound.
	maxProposalRevisions = 40
	// actionProposalConfirm and actionProposalCancel are the two buttons' ids: the dispatcher's
	// common checks know them, and each family decodes its own value.
	actionProposalConfirm = "factory_proposal_confirm"
	actionProposalCancel  = "factory_proposal_cancel"
)

// Slack's own bounds on one message. They are checked here rather than discovered in Slack's
// refusal: a section block carries at most slackSectionLimit characters, a header block at most
// slackHeaderLimit, a message at most maxSlackBlocks blocks and maxSlackText characters of text.
const (
	slackSectionLimit = 3000
	slackHeaderLimit  = 150
	maxSlackBlocks    = 50
	maxSlackText      = 40000
)

// The material set's four keys, in the order the message renders its sections: the keys
// proposalChanged reports, which are the sections roomview marks.
const (
	proposalKeyRepository = "repository"
	proposalKeyIssue      = "issue"
	proposalKeyRoles      = "roles"
	proposalKeyBudget     = "budget"
	// proposalKeyHeader is roomview's header section: it names the revision and the hash, and it is
	// not a part of the plan, so it is never marked.
	proposalKeyHeader = "header"
)

// errProposalTooLarge is a plan the metadata cannot carry: the one line the thread shows, and
// nothing is filed from anything but the plan it refused.
var errProposalTooLarge = errors.New("the plan is too long for Slack; use your coding agent or the web UI")

// proposalHashRE is the shape of a plan hash: a Confirm binds one of these, and anything else binds
// nothing.
var proposalHashRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ProposalPayload is a proposal's metadata (CD20): the plan as the broker drew it, the hash a
// Confirm binds, who asked for it and which revision of the message this is. It is the whole of the
// proposal — no store, no memory of the thread — so an App that has just restarted reads the same
// proposal back from the message.
type ProposalPayload struct {
	Plan   intake.Plan `json:"plan"`
	Hash   string      `json:"hash"`
	Author string      `json:"author"`
	Rev    int         `json:"rev"`
}

// newProposal is p as the proposal at this revision, or errProposalTooLarge when the plan's
// canonical JSON is past the bound. The plan is marshalled once, here, and never measured again.
func newProposal(p intake.Plan, hash, author string, rev int) (ProposalPayload, error) {
	raw, err := p.JSON()
	if err != nil {
		return ProposalPayload{}, fmt.Errorf("slackapp: proposal: %w", err)
	}
	if len(raw) > maxProposalPlanBytes {
		return ProposalPayload{}, errProposalTooLarge
	}
	return ProposalPayload{Plan: p, Hash: hash, Author: author, Rev: rev}, nil
}

// proposalHashHolds reports whether the payload is the plan it claims: the hash's shape is one, and
// the hash of the plan's material set is the hash the payload carries (CD20, CD27). A message whose
// payload fails this is not a proposal, whatever its tag says.
func proposalHashHolds(p ProposalPayload) bool {
	return proposalHashRE.MatchString(p.Hash) && intake.Hash(p.Plan) == p.Hash
}

// proposalValue is a button's value (CD7): the version of the value's shape, the family it belongs
// to, the hash a Confirm binds and the revision it showed. ≤ 2000 characters by construction.
func proposalValue(hash string, rev int) string {
	return "v1|proposal|" + hash + "|" + strconv.Itoa(rev)
}

// parseProposalValue decodes a button's value: exactly four parts, the current version, this family,
// a hash of the right shape and a revision inside the cap. Anything else is this family's refusal —
// the dispatcher's common checks stop at the value's `v1|` prefix.
func parseProposalValue(v string) (hash string, rev int, ok bool) {
	parts := strings.Split(v, "|")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != "proposal" {
		return "", 0, false
	}
	if !proposalHashRE.MatchString(parts[2]) {
		return "", 0, false
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n < 1 || n > maxProposalRevisions {
		return "", 0, false
	}
	return parts[2], n, true
}

// proposalMaterial is intake.Material's canonical bytes, projected onto the four groups the message
// marks. It reads the bytes the hash covers rather than the plan's display fields: a ceiling the
// message rounds to the same dollars is still a different ceiling when its millionth differs (CD27).
type proposalMaterial struct {
	Repository string `json:"repository"`
	Budget     struct {
		CeilingMicroUSD int64  `json:"ceilingMicroUSD"`
		Source          string `json:"source"`
	} `json:"budget"`
	Issues []struct {
		Index     int      `json:"index"`
		Title     string   `json:"title"`
		Body      string   `json:"body"`
		Class     string   `json:"class"`
		Roles     []string `json:"roles"`
		DependsOn []int    `json:"dependsOn"`
	} `json:"issues"`
}

// proposalMaterialOf is a plan's material bytes as proposalMaterial; false when they do not decode,
// which is a plan this package cannot read rather than one that did not change.
func proposalMaterialOf(p intake.Plan) (proposalMaterial, bool) {
	raw, err := intake.Material(p)
	if err != nil {
		return proposalMaterial{}, false
	}
	var m proposalMaterial
	if err := json.Unmarshal(raw, &m); err != nil {
		return proposalMaterial{}, false
	}
	return m, true
}

// proposalChanged is the materiality table (CD14, CD27): which of the four sections the person reads
// moved, in the message's own order. It is exactly the hashed set — the repository, the budget, each
// issue's index, title, body and class, and each issue's roles and order — so the sections marked
// are the sections a new confirmation would file differently. `apiVersion` is in the hash and marks
// nothing: it is constant in one thread, and a change to it is refused earlier by intake.Check.
//
// A plan whose material bytes do not decode marks everything: an unreadable change must never read
// as no change.
func proposalChanged(prev, next ProposalPayload) []string {
	a, aok := proposalMaterialOf(prev.Plan)
	b, bok := proposalMaterialOf(next.Plan)
	if !aok || !bok {
		return []string{proposalKeyRepository, proposalKeyIssue, proposalKeyRoles, proposalKeyBudget}
	}
	changed := make([]string, 0, 4)
	if a.Repository != b.Repository {
		changed = append(changed, proposalKeyRepository)
	}
	if !sameIssues(a, b) {
		changed = append(changed, proposalKeyIssue)
	}
	if !sameRoles(a, b) {
		changed = append(changed, proposalKeyRoles)
	}
	if a.Budget != b.Budget {
		changed = append(changed, proposalKeyBudget)
	}
	return changed
}

// sameIssues compares the issue group: how many issues there are, and each one's place, title, body
// and class, in array order.
func sameIssues(a, b proposalMaterial) bool {
	if len(a.Issues) != len(b.Issues) {
		return false
	}
	for i := range a.Issues {
		if a.Issues[i].Index != b.Issues[i].Index ||
			a.Issues[i].Title != b.Issues[i].Title ||
			a.Issues[i].Body != b.Issues[i].Body ||
			a.Issues[i].Class != b.Issues[i].Class {
			return false
		}
	}
	return true
}

// sameRoles compares the roles group: each issue's roles and its dependencies, in array order, so a
// reordered role is a change and a nil list is not a different fact from an empty one.
func sameRoles(a, b proposalMaterial) bool {
	if len(a.Issues) != len(b.Issues) {
		return false
	}
	for i := range a.Issues {
		if !slices.Equal(a.Issues[i].Roles, b.Issues[i].Roles) ||
			!slices.Equal(a.Issues[i].DependsOn, b.Issues[i].DependsOn) {
			return false
		}
	}
	return true
}

// proposalView maps the payload onto the renderer's shape: the one place a plan becomes text, with
// the issue's body split by the draft path's own parseBody (or shown whole when it is not the
// template), and the one question the broker asks before filing.
func proposalView(p ProposalPayload, q *intake.Question, changed []string) roomview.ProposalView {
	v := roomview.ProposalView{
		Repository: p.Plan.Repository,
		Budget: roomview.ProposalBudget{
			CeilingUSD: p.Plan.Budget.CeilingUSD,
			Source:     p.Plan.Budget.Source,
		},
		Hash:    p.Hash,
		Rev:     p.Rev,
		Author:  p.Author,
		Confirm: len(changed) > 0,
		Changed: changed,
	}
	for _, e := range p.Plan.RepositoryEvidence {
		v.Evidence = append(v.Evidence, roomview.ProposalEvidence{Text: e.Text, URL: e.URL})
	}
	for _, a := range p.Plan.Alternatives {
		v.Alternatives = append(v.Alternatives, roomview.ProposalAlternative{Repository: a.Repository, Why: a.Why})
	}
	if question := proposalQuestion(p, q); question != nil {
		v.Question = question
	}
	if len(p.Plan.Issues) > 0 {
		is := p.Plan.Issues[0] // Slack files one issue: a plan with more is refused before it is posted
		issue := roomview.ProposalIssue{Title: is.Title, Class: is.Class, Roles: is.Roles, DependsOn: is.DependsOn}
		if parts, ok := parseBody(is.Body); ok {
			issue.What, issue.Should, issue.Acceptance = parts[0], parts[1], parts[2]
		} else {
			issue.RawBody = is.Body
		}
		v.Issue = issue
	}
	return v
}

// proposalQuestion is the question the message asks: the caller's, when it has one, else the one the
// plan itself carries. A plan whose question is not shown is a plan somebody confirms without
// answering what the broker asked.
func proposalQuestion(p ProposalPayload, q *intake.Question) *roomview.ProposalQuestion {
	if q == nil {
		q = p.Plan.Question
	}
	if q == nil {
		return nil
	}
	return &roomview.ProposalQuestion{Text: q.Text, Answer: q.Recommended.Answer, Why: q.Recommended.Why}
}

// proposalSections is the proposal message: the fallback text Slack shows in a notification and the
// Block Kit blocks it renders. Both come from the payload through roomview — no rendered text is
// ever parsed back into a plan, and the buttons' value is the hash and the revision this message
// shows, so a click binds exactly what was read (CD7, CD20).
//
// channel is optional: the payload carries none, and it is the one fact a block wants that a plan
// does not have. A proposal has no room yet, so its context block links no room.
func proposalSections(p ProposalPayload, q *intake.Question, changed []string, channel ...string) (text string, blocks json.RawMessage, err error) {
	sections, err := roomview.Proposal(proposalView(p, q, changed), roomview.Options{Format: roomview.Slack})
	if err != nil {
		return "", nil, err
	}
	text = proposalText(sections)
	if utf8.RuneCountInString(text) > maxSlackText {
		return "", nil, errProposalTooLarge
	}
	blocks, err = proposalBlocks(sections, p.Hash, p.Rev, firstChannel(channel))
	if err != nil {
		return "", nil, err
	}
	return text, blocks, nil
}

// firstChannel is the channel the caller named, or "": a variadic parameter cannot hold two.
func firstChannel(channel []string) string {
	if len(channel) == 0 {
		return ""
	}
	return channel[0]
}

// proposalText is every section's heading and body joined by newlines: the message's fallback text,
// which is what a client without blocks shows and what a notification quotes. It is never parsed.
func proposalText(sections []roomview.ProposalSection) string {
	lines := make([]string, 0, 2*len(sections))
	for _, s := range sections {
		if s.Heading != "" {
			lines = append(lines, s.Heading)
		}
		if s.Body != "" {
			lines = append(lines, s.Body)
		}
	}
	return strings.Join(lines, "\n")
}

// slackBlockText is a block's text object: plain_text where Slack renders proportions, mrkdwn where it
// reads the message's markup.
type slackBlockText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// slackBlock is one Block Kit block. Only the fields this package writes are declared; the empty
// ones are omitted, so a section block carries its text and nothing else.
type slackBlock struct {
	Type     string          `json:"type"`
	Text     *slackBlockText `json:"text,omitempty"`
	Elements []slackElement  `json:"elements,omitempty"`
}

// slackElement is a block's element: a button in an actions block, a piece of text in a context
// block.
type slackElement struct {
	Type     string          `json:"type"`
	Text     *slackBlockText `json:"text,omitempty"`
	ActionID string          `json:"action_id,omitempty"`
	Value    string          `json:"value,omitempty"`
	Style    string          `json:"style,omitempty"`
}

// proposalBlocks is the message's blocks: a header block naming the revision, the header's other
// lines and then one section block per section, the two buttons, and a context block naming the
// channel. A section longer than Slack's limit is carried whole across several blocks — the plan is
// bounded at 7 KiB, so a message cannot exceed the block count — because cutting what a Confirm
// binds would confirm something else.
func proposalBlocks(sections []roomview.ProposalSection, hash string, rev int, channel string) (json.RawMessage, error) {
	blocks := make([]slackBlock, 0, len(sections)+2)
	for _, s := range sections {
		if s.Key == proposalKeyHeader {
			blocks = append(blocks, slackBlock{
				Type: "header",
				Text: &slackBlockText{Type: "plain_text", Text: "Proposal v" + strconv.Itoa(rev)},
			})
			for _, chunk := range splitSlackText(proposalHeaderRest(s.Body, rev), slackSectionLimit) {
				blocks = append(blocks, sectionBlock(chunk))
			}
			continue
		}
		for _, chunk := range splitSlackText(slackSectionText(s.Heading, s.Body), slackSectionLimit) {
			blocks = append(blocks, sectionBlock(chunk))
		}
	}
	value := proposalValue(hash, rev)
	blocks = append(blocks, slackBlock{Type: "actions", Elements: []slackElement{
		{Type: "button", Text: &slackBlockText{Type: "plain_text", Text: "Confirm"},
			ActionID: actionProposalConfirm, Value: value, Style: "primary"},
		{Type: "button", Text: &slackBlockText{Type: "plain_text", Text: "Cancel"},
			ActionID: actionProposalCancel, Value: value, Style: "danger"},
	}})
	blocks = append(blocks, slackBlock{Type: "context", Elements: []slackElement{
		{Type: "mrkdwn", Text: &slackBlockText{Type: "mrkdwn", Text: proposalContextText(channel)}},
	}})
	if len(blocks) > maxSlackBlocks {
		return nil, errProposalTooLarge
	}
	raw, err := json.Marshal(struct {
		Blocks []slackBlock `json:"blocks"`
	}{blocks})
	if err != nil {
		return nil, fmt.Errorf("slackapp: proposal blocks: %w", err)
	}
	if len(raw) > maxSlackReply {
		return nil, errProposalTooLarge
	}
	return raw, nil
}

// sectionBlock is one section block of mrkdwn.
func sectionBlock(text string) slackBlock {
	return slackBlock{Type: "section", Text: &slackBlockText{Type: "mrkdwn", Text: text}}
}

// slackSectionText is a section as Slack's mrkdwn: the heading in bold, its body under it. The
// heading may carry the renderer's changed mark, which is Slack's own glyph and not markup.
func slackSectionText(heading, body string) string {
	if heading == "" {
		return body
	}
	return "*" + heading + "*\n" + body
}

// proposalHeaderRest is the header section's text once its first line is the header block's: the
// hash a Confirm binds, the author's mention and any "requires a new confirmation" follow under it,
// because Slack's header type is plain text and cannot carry a mention. The line it cuts is this
// package's own, written by roomview; when it is not there the whole body follows, so nothing is
// lost.
func proposalHeaderRest(body string, rev int) string {
	rest, ok := strings.CutPrefix(body, "Proposal v"+strconv.Itoa(rev))
	if !ok {
		return body
	}
	return strings.TrimPrefix(strings.TrimSpace(rest), "· ")
}

// proposalContextText is the context block's line: the channel the proposal is in, when the caller
// knows it. A proposal has no room yet, so the block links none (CD7). An id off the channel shape
// is not mentioned at all — a mention is markup, and markup built from an arbitrary string is the
// caller's markup.
func proposalContextText(channel string) string {
	if channelRE.MatchString(channel) {
		return "<#" + channel + ">"
	}
	return "Slack proposal · confirm or cancel in this thread"
}

// splitSlackText cuts text into pieces of at most limit characters, at line boundaries where it can
// and inside a line only when one line is longer than a whole block. Nothing is dropped: what a
// Confirm binds is shown whole.
func splitSlackText(text string, limit int) []string {
	if text == "" {
		return nil
	}
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	var out []string
	var cur []string
	used := 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, "\n"))
			cur, used = nil, 0
		}
	}
	for _, line := range strings.Split(text, "\n") {
		for _, piece := range cutRunes(line, limit) {
			n := utf8.RuneCountInString(piece)
			if len(cur) > 0 {
				n++ // the newline the piece joins with
			}
			if len(cur) > 0 && used+n > limit {
				flush()
				n--
			}
			cur = append(cur, piece)
			used += n
		}
	}
	flush()
	return out
}

// cutRunes cuts a line into pieces of at most limit runes: a line longer than a whole block is
// carried across blocks by count of characters, never by count of bytes, so no character is split in
// two.
func cutRunes(s string, limit int) []string {
	r := []rune(s)
	if len(r) <= limit {
		return []string{s}
	}
	out := make([]string, 0, len(r)/limit+1)
	for len(r) > 0 {
		n := min(limit, len(r))
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return out
}
