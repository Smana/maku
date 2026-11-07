// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
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

// notPostedLine is a plan Slack cannot carry, as the thread says it: one sentence, from the error that
// refused it, so the mention that drew the plan and the reply that re-planned it say the same thing.
func notPostedLine(err error) string { return "Proposal not posted: " + err.Error() + "." }

// notPostedIssueLine is a plan covering more than one issue, as the thread says it: Slack files one, and
// a plan with more is never trimmed to fit (CD19). The mention's post and the reply's re-plan meet it
// with this one sentence.
func notPostedIssueLine(n int) string {
	return fmt.Sprintf("Proposal not posted: this plan covers %d issues and Slack files one: use your coding agent or the web UI.", n)
}

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
	// Degraded says the composer wrote this plan, not the planner (CD18). It rides the payload so the
	// message says it, and says it again after a restart that only has the message. It is display, not
	// material: the hash binds the plan, and the label is in no section's material set.
	Degraded bool `json:"degraded,omitempty"`
}

// errProposalHash and errProposalRev are a caller's mistake, not a refusal the thread shows: a
// proposal whose hash does not bind its plan, or whose revision is outside the cap, is a message no
// Confirm could ever trust, and no decoder would read it back as a proposal.
var (
	errProposalHash = errors.New("slackapp: proposal: the hash does not bind the plan")
	errProposalRev  = errors.New("slackapp: proposal: the revision is outside 1.." + strconv.Itoa(maxProposalRevisions))
)

// newProposal is p as the proposal at this revision. It refuses what the message could not carry or
// could not be read back from: a hash that is not the plan's own (errProposalHash), a revision
// outside 1..maxProposalRevisions (errProposalRev), and a plan whose canonical JSON is past the
// 7 KiB bound (errProposalTooLarge). The plan is marshalled once, here, and never measured again.
func newProposal(p intake.Plan, hash, author string, rev int) (ProposalPayload, error) {
	if want := intake.Hash(p); hash != want {
		return ProposalPayload{}, fmt.Errorf("%w: %q is not %q", errProposalHash, hash, want)
	}
	if rev < 1 || rev > maxProposalRevisions {
		return ProposalPayload{}, fmt.Errorf("%w: %d", errProposalRev, rev)
	}
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
		Hash:     p.Hash,
		Rev:      p.Rev,
		Author:   p.Author,
		Degraded: p.Degraded,
		Confirm:  len(changed) > 0,
		Changed:  changed,
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
// channel is optional here and required in practice: the payload carries none, and it is the one
// fact a block wants that a plan does not have, so every poster (Task 4's postProposal and
// updateProposal, and Task 6's replan) calls it as proposalSections(p, q, changed, channel). A
// proposal has no room yet, so its context block links no room.
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
// ones are omitted, so a section block carries its text and nothing else. Elements is typed by the
// block that holds it: an actions block carries buttons, whose text is a text object, and a context
// block carries mrkdwn elements, whose text is the string itself.
type slackBlock struct {
	Type     string          `json:"type"`
	Text     *slackBlockText `json:"text,omitempty"`
	Elements any             `json:"elements,omitempty"`
}

// slackButton is an actions block's element: Slack reads its text as a text object (`plain_text`).
type slackButton struct {
	Type     string          `json:"type"`
	Text     *slackBlockText `json:"text"`
	ActionID string          `json:"action_id"`
	Value    string          `json:"value"`
	Style    string          `json:"style,omitempty"`
}

// slackContextText is a context block's element: `{"type":"mrkdwn","text":"<#C…>"}`. Its text is a
// plain string, not a text object — a nested object is refused by Slack and renders nothing.
type slackContextText struct {
	Type string `json:"type"`
	Text string `json:"text"`
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
	blocks = append(blocks, actionsBlock(proposalValue(hash, rev)), contextBlock(channel))
	if len(blocks) > maxSlackBlocks {
		return nil, errProposalTooLarge
	}
	// The bare array: the client puts it under the request's `blocks` key, and an object there is
	// Slack's invalid_blocks_format.
	raw, err := json.Marshal(blocks)
	if err != nil {
		return nil, fmt.Errorf("slackapp: proposal blocks: %w", err)
	}
	if len(raw) > maxSlackReply {
		return nil, errProposalTooLarge
	}
	return raw, nil
}

// actionsBlock is the two controls: Confirm and Cancel, both carrying the value the message showed, so
// a click binds the revision it read.
func actionsBlock(value string) slackBlock {
	return slackBlock{Type: "actions", Elements: []slackButton{
		{Type: "button", Text: &slackBlockText{Type: "plain_text", Text: "Confirm"},
			ActionID: actionProposalConfirm, Value: value, Style: "primary"},
		{Type: "button", Text: &slackBlockText{Type: "plain_text", Text: "Cancel"},
			ActionID: actionProposalCancel, Value: value, Style: "danger"},
	}}
}

// contextBlock names the channel the proposal is in, in Slack's context shape: one mrkdwn element
// whose text is the string itself.
func contextBlock(channel string) slackBlock {
	return slackBlock{Type: "context", Elements: []slackContextText{
		{Type: "mrkdwn", Text: proposalContextText(channel)},
	}}
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
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	var out []string
	var cur []string
	used := 0
	flush := func() {
		if len(cur) == 0 {
			return
		}
		// A chunk of nothing but whitespace is no block: Slack refuses a section whose text is
		// empty, and a blank line between two blocks is not a section.
		if chunk := strings.Join(cur, "\n"); strings.TrimSpace(chunk) != "" {
			out = append(out, chunk)
		}
		cur, used = nil, 0
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

// ---- the mention: a plan through the broker, and one proposal (CD1, CD18) ----

const (
	// maxProposalRequestBytes is intake_plan's text bound, measured on the planner's input — the
	// commandless answer of the message the request comes from, either the mention itself or the
	// thread's first message a reply re-plans — and never on the HTTP body Slack posts, which the
	// verifier already caps at 1 MiB. A request past it is refused before the broker is called, with
	// its own line.
	maxProposalRequestBytes = 16 << 10
	// maxProposalContextBytes is intake_plan's context bound, measured in bytes on what the reply flow
	// sends as context — the requester's replies, numbered — so a thread's whole history never has to
	// fit and nothing is sent that the tool would refuse whole.
	maxProposalContextBytes = 4 << 10
	// planUnsupported is the answer, to a reply or a mention, in a proposal thread whose broker no
	// longer plans. A thread is the kind it started as, so no draft is posted beside the proposal: the
	// line names the two ways left, confirm it as shown, or draft it with a mention in a new thread.
	planUnsupported = "Not re-planned: this deployment no longer plans from Slack; confirm the proposal as it is, or mention me in a new thread with the repository and a title to draft it instead."
	// replyPlannedMost is the reply's answer in a thread whose proposal has reached the revision cap:
	// one message is all a thread edits, and the two things left are the two the line names.
	replyPlannedMost = "This proposal was planned the most it can be here: confirm it, or start a new mention."
	// proposalCappedLine is the mention's answer in a thread whose live proposal has all the revisions
	// one may have: its pre-check and a plan the broker answered at the cap say the same sentence.
	proposalCappedLine = "Not proposed: this thread has all the revisions one proposal may have; confirm it, or start a new mention."
	// replyNoRequest is the reply's answer when Slack's answer does not hold the thread's first
	// message. The request lives nowhere but that message — the proposal's metadata is the plan, its
	// hash, its author and its revision (CD20) — so the App says it cannot re-plan rather than
	// composing a request out of the plan it shows.
	replyNoRequest = "Not re-planned: this thread's first message is not in Slack's answer; mention me again with what should change."
)

// RequestedTextTooLong is a planner request past intake_plan's 16 KiB bound: a mention whose own text
// is, or a reply whose thread's first message is. Its text is the one line the thread shows: the
// person's fix is to hand the request off, not to shorten it in Slack, since a plan drawn from part of
// a request is a plan nobody read. The name and the prose are this package's interface rather than a
// caller's error to test, which is why they are not Go's Err-something form.
//
//nolint:revive,staticcheck // the name and the words are the plan's: RequestedTextTooLong, one line for the person
var RequestedTextTooLong = errors.New("Not proposed: that request is longer than 16 KB; hand it off from your coding agent or the web UI.")

// commandless is a message as the planner reads it: the bot's <@…> taken out and the rest trimmed. It
// has two entry points, and both are the same transformation: the mention's own text (propose) and the
// thread's first message (parentRequest, which is what a reply re-plans from). Each caller measures
// intake_plan's 16 KiB bound on this answer before it calls the broker — never on the HTTP body Slack
// posts or on the rendered message — because the bound is intake_plan's, and the person's own words are
// the request (CD1).
func commandless(text, bot string) string {
	return strings.TrimSpace(strings.ReplaceAll(text, "<@"+bot+">", ""))
}

// propose plans a mention through intake_plan and posts the answer as the thread's one proposal
// (CD1, CD7, CD20). It is the whole of the mention flow with proposals on and a broker that plans:
// the App picks no repository, parses no grammar and files nothing — the plan, its hash and its
// author are the message's, and the person confirms it there.
//
// A thread has one live proposal, not one per mention: when the thread already holds one, the plan is
// that message's next revision, written by the same updateProposal the reply flow uses — edited in
// place, or reposted with the old card made inert when Slack refuses the edit. A second mention is how
// a person revises, so it must not leave two cards whose buttons bind two hashes (Review Focus 1, CD7).
//
// Three things are refused before a plan is shown, each with its own line: a request past
// intake_plan's 16 KiB bound, a thread whose task is already filed or cancelled — the idempotency key is
// the thread, so a second plan there would be a second message about one filing — and a thread whose
// live proposal has reached the revision cap.
//
// rev is the revision a thread's first proposal takes: a thread that already has one continues from
// its own revision, never from the caller's.
//
// The outcome is what the caller counts: postProposal's ("proposed", "too_large", "multi_issue",
// "error"), updateProposal's ("updated", "reposted"), "refused" for a refusal whose fix is a new
// mention, and "error" for a passing one. noPlanner is a broker without intake_plan: nothing was said
// in the thread, and the caller drafts the mention instead.
func (a *App) propose(ctx context.Context, ev slackEvent, ch Channel, thread, text string, rev int) (code string, noPlanner bool) {
	say := func(line string) bool { return a.sayLine(ctx, ev.Channel, thread, line) }
	if len(text) > maxProposalRequestBytes {
		return a.refusedWith(ctx, ev.Channel, thread, RequestedTextTooLong.Error(), "too_large"), false
	}
	// The read, the plan and the write are one step, as a filing is: two mentions that both read "no
	// proposal" would post two live ones, and the whole point of the branch below is that a thread has
	// one. The lock is held across the planner call, exactly as fileProposal holds it across
	// run_create — the cost is that a slow plan waits out its predecessor, and the alternative is two
	// cards binding two hashes.
	a.editMu.Lock()
	defer a.editMu.Unlock()
	msgs, err := a.Slack.Thread(ctx, ev.Channel, thread)
	if err != nil {
		// The App cannot see whether the thread is closed, so it plans anyway rather than leaving the
		// person with no answer at all: the broker's idempotency key is still what refuses a second
		// filing on a thread that already has one.
		a.log().Warn("slack thread fetch failed", "channel", ev.Channel, "err", err)
	}
	latest, closed := a.threadProposal(msgs)
	if closed {
		if !say(a.proposalClosedLine(a.threadRoom(msgs))) {
			return "error", false
		}
		return "refused", false
	}
	// The live proposal this mention revises, when there is one: its payload is the plan the message
	// shows, so the update marks what moved against it (CD14, CD27) and the revision continues.
	var prev *ProposalPayload
	if latest != nil {
		if p, ok := latest.Metadata.proposalPayload(); ok {
			prev = &p
		}
	}
	// v4: exactly two named repositories select a coordination, planned through coordination_plan
	// and confirmed on the parent's room page; any other count keeps this single-repository flow.
	coordLatest, _, coordThread := a.coordinationThread(msgs)
	repos, code := a.coordinationRoute(ctx, ev, ch, thread, text, latest != nil)
	if code != "" {
		return code, false
	}
	if repos != nil {
		return a.proposeCoordination(ctx, ev, thread, text, repos, coordLatest), false
	}
	if coordThread {
		return a.refusedLine(ctx, ev.Channel, thread, coordinationTooManyLine), false
	}
	if prev != nil && prev.Rev >= maxProposalRevisions {
		if !say(proposalCappedLine) {
			return "error", false
		}
		return "refused", false
	}

	res, err := a.Broker.IntakePlan(ctx, IntakeRequest{
		Text:       text,
		Candidates: ch.Repositories,
		OnBehalfOf: "slack:" + a.TeamID + "/" + ev.User,
		CeilingUSD: ch.MaxCostUSD,
	})
	if errors.Is(err, ErrUnknownTool) {
		// A thread is the kind it started as: one that holds a proposal never gets a draft beside it,
		// so its mention is told what its reply is. A thread without one is drafted by the caller.
		if latest != nil {
			return a.refusedLine(ctx, ev.Channel, thread, planUnsupported), false
		}
		return "", true // nothing said: the caller drafts the mention instead
	}
	if code := a.planFailed(ctx, ev.Channel, thread, ev.User, ch.Repositories, err); code != "" {
		return code, false
	}

	// The broker's answer is held to the message's own contract before a byte of it is posted: a plan
	// whose hash does not bind it is a proposal no Confirm could trust (CD20), one Slack cannot carry is
	// refused whole with the same line postProposal uses, and one at the revision cap is refused with the
	// line the reply flow's re-plan meets too.
	nextRev := rev
	if prev != nil {
		nextRev = prev.Rev + 1 // the live proposal's next revision, whatever the caller passed
	}
	prop, err := newProposal(res.Plan, res.Hash, ev.User, nextRev)
	if err != nil {
		return a.notShown(ctx, ev.Channel, thread, err), false
	}
	prop.Degraded = res.Degraded // display, and the one field newProposal does not take (CD18)
	if prev != nil {
		// One proposal per thread: the live message becomes this revision in place, or is reposted and
		// made inert when Slack refuses the edit. Nothing is posted beside it, so the thread never has
		// two cards binding two hashes (Review Focus 1, CD7).
		return a.updateProposal(ctx, ev.Channel, latest.TS, *prev, prop, res.Question), false
	}
	return a.postProposal(ctx, ev.Channel, thread, prop, res.Question), false
}

// proposalClosedLine is the mention's answer in a thread whose task is filed or cancelled: one line,
// naming the room when the thread's marker names one. Nothing is planned there, because the
// idempotency key is the thread and a second plan would describe a filing that cannot happen.
func (a *App) proposalClosedLine(room string) string {
	if !roomRE.MatchString(room) {
		return "Not proposed: this thread's task is already filed or cancelled; start a new mention for another."
	}
	return "Not proposed: this thread's task is already filed, in room " +
		strings.TrimSuffix(a.RoomsURL, "/") + "/r/" + room + "; start a new mention for another."
}

// threadRoom is the room the thread's marker names: the App's own filed message is the one place a
// closed thread says where its task went. "" when no marker of the App's names one.
func (a *App) threadRoom(msgs []Message) string {
	room := ""
	for i := range msgs {
		m := &msgs[i]
		if m.User == "" || m.User != a.BotUserID || m.Metadata == nil {
			continue
		}
		if mark, ok := m.Metadata.markerPayload(); ok && roomRE.MatchString(mark.Room) {
			room = mark.Room
		}
	}
	return room
}

// threadCancelled reports whether the thread's newest marker of the App's is a Cancel's: the
// proposal closed without filing, so the thread answers nothing (CD25).
func (a *App) threadCancelled(msgs []Message) bool {
	cancelled := false
	for i := range msgs {
		m := &msgs[i]
		if m.User == "" || m.User != a.BotUserID || m.Metadata == nil {
			continue
		}
		if mark, ok := m.Metadata.markerPayload(); ok {
			cancelled = mark.Cancelled
		}
	}
	return cancelled
}

// notProposed is an intake_plan refusal as the thread says it: one fixed line per reason of intake's
// closed list, and for the two a person can act on by asking an operator, the repositories this
// channel serves. passing marks the reasons whose fix is to mention again — an outage, not a decision
// — so the caller counts them apart. A reason outside the list, and anything the broker says that is
// not one, is shown as it was given, as every other refusal in this package is (shown).
//
// served is variadic because it is display for two of the lines and because the reason is what
// decides: a caller with no channel list still gets its answer, without the repositories in it.
func notProposed(refusal, user string, served ...string) (code, text string, passing bool) {
	// intake_plan answers `<reason>: <sentence>` (intake.Refusal), and the doors that wrap a refusal
	// answer `rejected: <reason>`: both spellings name the same reason, and the reason is the first
	// field of either.
	code, _, _ = strings.Cut(strings.TrimPrefix(refusal, "rejected: "), ":")
	what := strings.Join(served, ", ")
	switch {
	case code == "repository_not_allowed" && what != "":
		return code, "Not proposed: this channel serves " + slackText(what) +
			" and you may file in none of them; ask an operator to add one you may write in.", false
	case code == "no_write" && what != "":
		return code, "Not proposed: <@" + user + "> cannot write to " + slackText(what) +
			", the repositories this channel serves; ask an operator for one you may write in.", false
	case code == "not_linked":
		// The one refusal that sends the person somewhere they can act here: `link` is the App's own
		// command, and the broker's sentence for it ("link it first") names no way to do that.
		return code, "Not proposed: <@" + user + "> is not linked to a GitHub account here; mention me with " +
			"`link` to link it, then mention me again.", false
	case code == "private_repository_unsupported":
		return code, "Not proposed: the planner reads public repositories only; use your coding agent " +
			"(`issue_draft` then `issue_file`), or wait for the internal route.", false
	case code == "planner_unavailable":
		return code, "Not proposed: the plan service is unavailable; mention me again in a minute.", true
	case code == "planner_timeout":
		return code, "Not proposed: the plan service did not answer in time; mention me again in a minute.", true
	case code == "planner_invalid":
		return code, "Not proposed: the plan service answered something that is not a plan; mention me again, " +
			"or hand it off from your coding agent.", false
	case code == "daily_plans_reached":
		return code, "Not proposed: you have used today's plans; mention me again after 00:00 UTC, " +
			"or hand it off from your coding agent.", false
	case code == "repository_unverified":
		return code, "Not proposed: GitHub could not confirm the repository, so the planner will not guess; " +
			"mention me again in a minute.", true
	}
	return code, "Not proposed: " + shown(refusal), false
}

// ---- the reply: the same one message, planned again from the person's words (CD7, CD14) ----

// followUps is a thread as intake_plan's context: the requester's own replies, in order, each on its
// own line as `Reply 1: …`, so the planner reads which words came when (CD14's "a reply re-plans").
//
// Only the requester's words are an edit of their request, so a bystander's reply is not in it — the
// App reads no one else's message into a plan it files for this one — and the App's own messages are
// not words at all: the proposal, the Filed line and every refusal line it wrote stay out.
//
// budget is intake_plan's context bound, measured in bytes on what is sent. The most recent replies
// are the ones the person just wrote, so those are kept and the older ones go; a reply larger than the
// whole bound is cut to it rather than dropped, because the word that asked for the re-plan is the one
// the plan is re-made from. Nothing longer than the bound ever leaves here.
func followUps(msgs []Message, thread, bot, author string, budget int) string {
	var said []string
	for i := range msgs {
		m := &msgs[i]
		// The parent is the request itself, passed as the plan's text, never a reply.
		if m.TS == "" || m.TS == thread || m.User == "" || m.User == bot || m.User != author {
			continue
		}
		if text := strings.TrimSpace(m.Text); text != "" {
			said = append(said, text)
		}
	}
	if len(said) == 0 || budget <= 0 {
		return ""
	}
	for {
		out := numberReplies(said)
		if len(out) <= budget {
			return out
		}
		if len(said) == 1 {
			return clipReply(out, budget)
		}
		said = said[1:]
	}
}

// numberReplies is the replies as the planner reads them: in order, each `Reply n: ` and its text.
func numberReplies(said []string) string {
	lines := make([]string, len(said))
	for i, text := range said {
		lines[i] = "Reply " + strconv.Itoa(i+1) + ": " + text
	}
	return strings.Join(lines, "\n")
}

// clipReply is s cut to budget bytes without splitting a character: half a rune is noise to the
// planner, and the bound is measured in bytes, as intake_plan measures it.
func clipReply(s string, budget int) string {
	if len(s) <= budget {
		return s
	}
	cut := 0
	for i := range s {
		if i > budget {
			break
		}
		cut = i
	}
	return s[:cut]
}

// parentRequest is the request the thread was planned from: its first message, read from Slack's
// answer alone, with the bot's <@…> taken out as the mention's text is (commandless). ok is false when
// that answer does not hold it, which is a thread the App cannot re-plan: the request is in no
// message, and nothing is composed out of the plan the proposal carries (CD20).
func parentRequest(msgs []Message, thread, bot string) (string, bool) {
	for i := range msgs {
		if msgs[i].TS != thread {
			continue
		}
		if text := commandless(msgs[i].Text, bot); text != "" {
			return text, true
		}
		return "", false
	}
	return "", false
}

// replan answers a reply in a proposal's thread: the thread's request, planned again with the
// requester's replies as context, written back onto the one message that holds the proposal (CD7,
// CD14). A revision is never a new message, so the thread's buttons always bind the plan its metadata
// carries; when Slack refuses the edit, updateProposal reposts and makes the old card inert, which is
// Review Focus 1 reached from here.
//
// It is called by reply with a.editMu held and with the thread read under it, and it takes no lock of
// its own: two replies cannot interleave, so the second one's read already holds the first one's reply
// and the revision it wrote, and the revision it plans is the one the message shows. Everything is
// read from the messages — the request from the thread's parent, the plan, its hash, its author and
// its revision from the proposal's metadata — so an App that has just restarted re-plans exactly as
// one that watched the mention.
//
// Nothing is planned, and the thread says why in one line each, when the reply is not the mention's
// author's (that is "ignored": only their words edit their proposal), when the message has reached
// the revision cap, and when the answer does not hold the thread's first message. The broker's own
// refusals are the mention's lines, one spelling per reason.
//
// The outcome is updateProposal's ("updated", "reposted"), the plan's own ("multi_issue",
// "too_large"), "refused" for a refusal whose fix is a new mention, "error" for a passing one, and
// "ignored" for a reply that is not this proposal's author's.
func (a *App) replan(ctx context.Context, ev slackEvent, ch Channel, thread string, msgs []Message, latest *Message, p ProposalPayload) string {
	if p.Author == "" || p.Author != ev.User {
		return "ignored" // only the mention's author's replies edit it, as the draft path's do
	}
	// The reply is the proposal's own from here on: one action, counted before the cap and the planner
	// are met, since a refusal answers the person who replied and the flow still ran (CD7).
	a.proposalUpdate(ctx, countReply)
	if p.Rev >= maxProposalRevisions {
		return a.refusedLine(ctx, ev.Channel, thread, replyPlannedMost)
	}
	text, ok := parentRequest(msgs, thread, a.BotUserID)
	if !ok {
		a.log().Warn("thread's first message is not in Slack's answer", "channel", ev.Channel)
		return a.refusedLine(ctx, ev.Channel, thread, replyNoRequest)
	}
	// The re-plan's text is the thread's first message, and it was written under no bound of this App's
	// — a mention from before the switch, or the message a mention was typed into — so it is held to
	// intake_plan's 16 KiB bound here, before the broker is called, exactly as a mention's text is
	// (CD1). A request past it is refused whole: a plan drawn from part of a request is a plan nobody
	// read, and the one line says where to hand it off instead.
	if len(text) > maxProposalRequestBytes {
		return a.refusedWith(ctx, ev.Channel, thread, RequestedTextTooLong.Error(), "too_large")
	}
	res, err := a.Broker.IntakePlan(ctx, IntakeRequest{
		Text:       text,
		Context:    followUps(msgs, thread, a.BotUserID, p.Author, maxProposalContextBytes),
		OnBehalfOf: "slack:" + a.TeamID + "/" + ev.User,
		Candidates: ch.Repositories,
		CeilingUSD: ch.MaxCostUSD,
	})
	if code := a.planFailed(ctx, ev.Channel, thread, ev.User, ch.Repositories, err); code != "" {
		return code
	}
	// The reply is the mention's author, so the plan keeps the author it was made for however many
	// times it is revised: who may file it does not move with who last typed.
	next, err := newProposal(res.Plan, res.Hash, p.Author, p.Rev+1)
	if err != nil {
		return a.notShown(ctx, ev.Channel, thread, err)
	}
	next.Degraded = res.Degraded // display, and the one field newProposal does not take (CD18)
	// Slack files one issue and a re-plan is never a trimmed plan (CD19): the reply path meets the
	// same refusal the mention's post does, in the same words.
	if n := len(next.Plan.Issues); n > 1 {
		return a.refusedWith(ctx, ev.Channel, thread, notPostedIssueLine(n), "multi_issue")
	}
	return a.updateProposal(ctx, ev.Channel, latest.TS, p, next, res.Question)
}

// sayLine posts one line in a thread, reporting whether Slack took it: a failure is logged with the
// channel and the flow's answer to it is always "error", never a retry.
func (a *App) sayLine(ctx context.Context, channel, thread, line string) bool {
	if _, err := a.Slack.Post(ctx, channel, thread, line, nil); err != nil {
		a.log().Warn("slack post failed", "channel", channel, "err", err)
		return false
	}
	return true
}

// refusedLine says one refusal in the thread and answers "refused", or "error" when the line itself
// could not be said.
func (a *App) refusedLine(ctx context.Context, channel, thread, line string) string {
	if !a.sayLine(ctx, channel, thread, line) {
		return "error"
	}
	return "refused"
}

// refusedWith is refusedLine for a refusal with an outcome of its own: a plan Slack cannot file
// counts apart from one it could plan.
func (a *App) refusedWith(ctx context.Context, channel, thread, line, code string) string {
	if !a.sayLine(ctx, channel, thread, line) {
		return "error"
	}
	return code
}

// planFailed answers a broker call that gave no plan, as the thread says it (CD18): the refusal's own
// line, a broker that no longer plans, and the planner being unreachable each have
// one sentence. "" means the call did not fail; otherwise the answer is the outcome the caller counts
// — "refused" for a decision, "error" for a passing failure whose fix is to ask again.
func (a *App) planFailed(ctx context.Context, channel, thread, user string, served []string, err error) string {
	var r Refused
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrUnknownTool):
		return a.refusedLine(ctx, channel, thread, planUnsupported)
	case errors.As(err, &r):
		code, line, passing := notProposed(string(r), user, served...)
		a.log().Info("proposal refused", "code", code, "channel", channel) // the reason, never the request's text
		if passing {
			return a.refusedWith(ctx, channel, thread, line, "error") // a passing failure: the line states the fix
		}
		return a.refusedWith(ctx, channel, thread, line, "refused")
	}
	a.log().Warn("intake_plan failed", "err", err)
	if !a.sayLine(ctx, channel, thread, "Not proposed: the planner could not be reached; mention me again in a minute.") {
		return "error"
	}
	return "error"
}

// notShown answers a plan the message cannot hold, with the thread's one line and the outcome the
// caller counts: too long for Slack, past the revision cap, a hash that does not bind the plan, or a
// reason this package has no line of its own for. Nothing is filed from a plan that could not be shown
// (CD20), and the mention and the re-plan say it the same way.
func (a *App) notShown(ctx context.Context, channel, thread string, err error) string {
	switch {
	case errors.Is(err, errProposalTooLarge):
		a.log().Warn("intake_plan answered a plan Slack cannot carry", "channel", channel)
		return a.refusedWith(ctx, channel, thread, notPostedLine(errProposalTooLarge), "too_large")
	case errors.Is(err, errProposalRev):
		// Unreachable from the re-plan, whose cap is its own pre-check: a caller passing a bad
		// revision is the mention's case.
		a.log().Warn("intake_plan answered a plan past the revision cap", "channel", channel)
		return a.refusedLine(ctx, channel, thread, proposalCappedLine)
	case errors.Is(err, errProposalHash):
		a.log().Warn("intake_plan answered a plan that does not hold", "channel", channel)
		return a.refusedLine(ctx, channel, thread,
			"Not proposed: the plan the planner answered does not match its check; mention me again.")
	}
	a.log().Warn("intake_plan answered a plan that cannot be shown", "channel", channel, "err", err)
	return a.refusedLine(ctx, channel, thread,
		"Not proposed: the plan the planner answered cannot be shown here; mention me again.")
}

// ---- the message in the thread: posted once, edited in place, found again ----

// proposalReplacedLine is what the proposal a repost replaced says afterwards: the message keeps its
// place in the thread and its buttons are gone, so no click can bind a hash its metadata no longer
// holds (Review Focus 1).
const proposalReplacedLine = "This proposal was replaced below."

// postProposal posts the proposal as one thread reply (CD7): the plan's sections as Slack blocks, and
// the whole plan, its hash, its author and its revision in the message's metadata. Nothing about the
// plan's text is logged, and nothing a refusal says quotes it.
//
// Two plans Slack cannot carry are refused with their one pointer line and nothing else: one past the
// 7 KiB bound (errProposalTooLarge) and one covering more than one issue (CD19). A refusal is never a
// trimmed plan: nothing may be filed from a message that showed part of one.
//
// The outcome is what the caller counts: "proposed", "too_large", "multi_issue" or "error".
func (a *App) postProposal(ctx context.Context, channel, thread string, p ProposalPayload, q *intake.Question) string {
	say := func(text string) bool { return a.sayLine(ctx, channel, thread, text) }
	tooLarge := func() string {
		if !say(notPostedLine(errProposalTooLarge)) {
			return "error"
		}
		return "too_large"
	}

	// The payload is checked against its own plan before a byte of it is posted: a proposal the
	// message cannot carry, or one no decoder would read back, is never posted (CD20).
	prop, err := newProposal(p.Plan, p.Hash, p.Author, p.Rev)
	if err != nil {
		if errors.Is(err, errProposalTooLarge) {
			return tooLarge()
		}
		a.log().Warn("proposal not built", "channel", channel, "err", err)
		return "error"
	}
	prop.Degraded = p.Degraded // display, and the one field newProposal does not take (CD18)
	if n := len(prop.Plan.Issues); n > 1 {
		if !say(notPostedIssueLine(n)) {
			return "error"
		}
		return "multi_issue"
	}
	text, blocks, err := proposalSections(prop, q, nil, channel)
	if err != nil {
		if errors.Is(err, errProposalTooLarge) {
			return tooLarge()
		}
		a.log().Warn("proposal render failed", "channel", channel, "err", err)
		return "error"
	}
	meta, err := proposalMeta(prop)
	if err != nil {
		if errors.Is(err, errProposalTooLarge) {
			return tooLarge()
		}
		a.log().Warn("proposal metadata failed", "channel", channel, "err", err)
		return "error"
	}
	if _, err := a.Slack.PostCard(ctx, channel, thread, text, blocks, meta); err != nil {
		a.log().Warn("slack post failed", "channel", channel, "err", err)
		return "error"
	}
	return "proposed"
}

// updateProposal rewrites the thread's proposal in place with its next revision (CD7): the sections
// mark what moved since prev (CD14, CD27), and the metadata carries the new plan, so a click on the
// message binds the revision it shows.
//
// Slack refuses an edit it cannot make — an edit window, a metadata shape it rejects, a message
// deleted. The person is then never left with two live proposals and never with buttons binding a
// hash the metadata no longer holds: a fresh proposal is posted and the old one is made inert, once
// (Review Focus 1, D10's rule).
//
// The outcome is "updated", the repost's own code, or "error".
func (a *App) updateProposal(ctx context.Context, channel, ts string, prev, next ProposalPayload, q *intake.Question) string {
	// The next revision is checked against its own plan first, as the first post was: a message no
	// decoder would read back is worse than a message one revision behind, so the old one stands.
	prop, err := newProposal(next.Plan, next.Hash, next.Author, next.Rev)
	if err != nil {
		a.log().Warn("proposal not built", "channel", channel, "ts", ts, "err", err)
		return "error"
	}
	prop.Degraded = next.Degraded
	text, blocks, err := proposalSections(prop, q, proposalChanged(prev, prop), channel)
	if err != nil {
		a.log().Warn("proposal render failed", "channel", channel, "ts", ts, "err", err)
		return "error"
	}
	meta, err := proposalMeta(prop)
	if err != nil {
		a.log().Warn("proposal metadata failed", "channel", channel, "ts", ts, "err", err)
		return "error"
	}
	if err := a.Slack.UpdateProposal(ctx, channel, ts, text, blocks, meta); err != nil {
		a.log().Warn("proposal update failed", "channel", channel, "ts", ts, "err", err)
		return a.repostProposal(ctx, channel, ts, prop, q)
	}
	return "updated"
}

// repostProposal posts the proposal again when it cannot be edited, and makes the message it replaces
// inert. The new message goes first: a failed repost leaves the old one live rather than none at all.
// The inert update is best effort and never retried into a loop — its failure is logged with the
// channel and the ts, and the newest message is the thread's live proposal either way.
//
// The inert write sends an empty block list, not none: chat.update leaves a message's blocks as they
// are when the key is absent, so nil would keep the old Confirm and Cancel live and give the thread
// two sets of working buttons, the older binding a hash its metadata no longer holds. An emptied list
// is what takes a card's buttons off here (App.inert, §6 step 7), and this is the same write.
//
// Slack keeps the message's metadata through an update that carries none, so the replaced message is
// still decodable as a proposal: threadProposal's newest-wins rule is what makes the fresh one the
// live proposal, and the click to act on is always the newest message's.
func (a *App) repostProposal(ctx context.Context, channel, oldTS string, p ProposalPayload, q *intake.Question) string {
	// The old message's own ts is the thread handle here: Slack resolves a reply's ts to the thread
	// it is in, so the repost lands beside the proposal it replaces.
	if code := a.postProposal(ctx, channel, oldTS, p, q); code != "proposed" {
		return code
	}
	a.proposalUpdate(ctx, countRepost) // the fresh proposal is posted; the one it replaces is made inert below
	if err := a.Slack.Update(ctx, channel, oldTS, proposalReplacedLine, json.RawMessage("[]")); err != nil {
		a.log().Warn("proposal inert update failed", "channel", channel, "ts", oldTS, "err", err)
	}
	return "reposted"
}

// threadProposal is the thread's newest proposal the App posted, its hash holding, and whether a
// "Filed" line of its own closes the thread (CD20). It reads the messages alone, so an App that has
// just restarted knows the whole proposal: a message with no metadata, with another tag, or whose
// hash does not bind its plan is not a proposal — exactly how a foreign message already reads — and
// no rendered text is ever parsed to recover a plan.
func (a *App) threadProposal(msgs []Message) (latest *Message, closed bool) {
	for i := range msgs {
		m := &msgs[i]
		if m.User == "" || m.User != a.BotUserID || m.Metadata == nil {
			continue
		}
		if _, ok := m.Metadata.proposalPayload(); ok {
			latest = m
			continue
		}
		if _, ok := m.Metadata.markerPayload(); ok {
			closed = true
		}
	}
	return latest, closed
}

// proposalMeta is the message's metadata: one tag and the payload, measured against maxMetadata
// because a message whose metadata Slack refuses is a proposal nothing could ever find again.
func proposalMeta(p ProposalPayload) (*Metadata, error) {
	payload, err := metaPayload(p)
	if err != nil {
		return nil, err
	}
	meta := &Metadata{EventType: proposalEventType, EventPayload: payload}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("slackapp: proposal: the message metadata: %w", err)
	}
	if len(raw) > maxMetadata {
		return nil, errProposalTooLarge
	}
	return meta, nil
}

// ---- filing a proposal: the ✅ fallback and, from PR 6, the Confirm ----

// fileProposal files the thread's proposal through run_create's plan validation (CD8, acceptance 10):
// a maintainer's ✅ on the message (confirm false) and the Confirm button (confirm true) are one code
// path, differing only in where the verdict goes — the thread for a ✅, the presser alone for a press
// — and in the verb of the passing failures' line, since a check mark is reacted and a button is
// pressed.
//
// The plan is the message's own metadata, re-checked here before anything is sent: a hash that does not
// bind the plan is refused with its own line rather than filed, and the repository the plan names must
// still be one the channel serves (CD24: a channel re-pointed under a live proposal refuses, it does not
// file). Then the thread is read, and it decides: a thread whose proposal was already filed or
// cancelled is ignored — its message already says so, and a second line would be noise — and only the
// thread's newest proposal files, so a ✅ or a press on a message an edit replaced files nothing. That
// read is under the App's edit lock, and so is the comparison of the thread's own payload with the
// caller's: a re-plan that landed between the caller's read and this one keeps the ts and changes the
// plan, so the hash is what tells them apart (CD14, CD20) — a plan that moved is refused, never filed.
//
// The filing carries the plan and its hash, so the broker recomposes and re-hashes it before it acts: a
// plan edited after it was shown is `plan_changed` and files nothing, whatever this App believes. On
// success the proposal message becomes the Filed line in place, which is what makes a thread one
// message from its mention to its room.
//
// The two returns are the outcome the caller counts and the line its author is owed: for a ✅ the line
// has already been posted into the thread (answer is empty), for a press it is the one
// chat.postEphemeral the caller sends to the person who pressed.
func (a *App) fileProposal(ctx context.Context, channel, thread, itemTS, user string, p ProposalPayload, confirm bool) (outcome, answer string) {
	// say is where a verdict goes: a ✅'s into the thread, which watched the plan and reacted on it;
	// a press's into answer — one line to the presser alone, because a press is one person's act on
	// one message and nothing it decides or refuses belongs in the thread.
	say := func(text string, meta *Metadata) {
		if confirm {
			answer = text
			return
		}
		if _, err := a.Slack.Post(ctx, channel, thread, text, meta); err != nil {
			a.log().Warn("slack post failed", "channel", channel, "err", err)
		}
	}
	// refuse says a refusal once per message, user and reason: a re-added ✅ does not repeat it. A
	// press is answered every time it is made — its own dedup key is what stops a repeat — and its
	// line is the caller's to send.
	refuse := func(reason, text string) (string, string) {
		if confirm || a.answered.add(itemTS+"/"+user+"/"+reason) {
			say(text, nil)
		}
		return "refused", answer
	}
	// retry says a passing failure, the fix being to react again: at most once per retryEvery for a
	// message, user and reason, so a burst of ✅ does not spam the thread. Pressing again is its own
	// retry, so a press is never throttled.
	retry := func(reason, text string) (string, string) {
		if confirm || a.retried.allow(itemTS+"/"+user+"/"+reason, a.now()) {
			say(text, nil)
		}
		return "error", answer
	}

	// again is the verb of the passing failures below: a ✅ is reacted, a Confirm is pressed, and that
	// is the whole of what confirm changes about the two callers' wording (CD8's fallback and PR 6's
	// button).
	again := "react again to retry"
	if confirm {
		again = "press Confirm again to retry"
	}

	ch, served := a.Channels[channel]
	switch {
	case !served:
		return refuse("channel", "Not filed: this channel is no longer served; ask an operator to add it to the Slack App's channels.")
	case intake.Hash(p.Plan) != p.Hash:
		return refuse("hash", "Not filed: this proposal no longer matches its check; mention me again to re-plan it.")
	case !slices.Contains(ch.Repositories, p.Plan.Repository):
		return refuse("repository", fmt.Sprintf("Not filed: %s isn't served in this channel; ask an operator to add it to the channel's repositories.",
			slackText(p.Plan.Repository)))
	case len(p.Plan.Issues) != 1: // Slack files one issue: a proposal was posted only if it had one
		return refuse("issue", "Not filed: this proposal does not carry exactly one issue; mention me again to re-plan it.")
	}

	a.editMu.Lock() // a re-plan in flight finishes first, so the thread's newest proposal is the one filed
	defer a.editMu.Unlock()
	msgs, err := a.Slack.Thread(ctx, channel, thread)
	if err != nil {
		a.log().Warn("slack thread fetch failed", "channel", channel, "err", err)
		if errors.Is(err, ErrThreadTooLong) {
			return refuse("too_long", "Not filed: this thread is too long to find its latest proposal; mention me again in a new thread.")
		}
		return retry("slack", "Not filed: Slack could not be read just now; "+again+".")
	}
	latest, closed := a.threadProposal(msgs)
	if closed {
		// The thread's proposal was filed or cancelled and its message says so: nothing to file, no
		// line to add, exactly as a ✅ on a message that is not the App's draft. A press was still
		// made, so its presser is told that the message no longer means anything.
		if confirm {
			return refuse("closed", proposalClosedPressLine)
		}
		return "ignored", ""
	}
	if latest == nil || latest.TS != itemTS {
		// Only the thread's newest proposal files: an earlier one was replaced by an edit or a repost
		// whose inert write Slack refused, and its buttons bind a hash its metadata no longer holds
		// (Review Focus 1). A ✅ says which message is the live one; a press is refused in the
		// presser's own line, which is the same sentence proposalPress reads for the same reason.
		if confirm {
			return refuse("replaced", proposalStaleLine)
		}
		refuse("replaced", "This draft was replaced by a newer one: react on the latest draft.")
		return "ignored", ""
	}
	// The thread is the truth about which plan is live (CD14, CD20). A revision is written in place, so
	// the ts this ✅ or press names is the same ts a re-plan that landed after the caller's read used:
	// the ts check above cannot tell the plan the person acted on from the one the message now shows.
	// The payload read here, under the lock, is what is compared with the caller's — a plan that moved
	// is refused rather than filed blind, and nothing is filed from either version of it.
	current, ok := latest.Metadata.proposalPayload()
	if !ok || current.Hash != p.Hash {
		a.log().Info("proposal plan changed under the filing", "channel", channel, "ts", itemTS)
		return refuse("hash", planChangedLine)
	}

	raw, err := p.Plan.JSON()
	if err != nil {
		a.log().Warn("proposal plan unreadable", "channel", channel, "err", err)
		return retry("plan", "Not filed: the plan could not be read just now; "+again+".")
	}
	// The filed tag is what closes the thread to further edits, so a payload that will not marshal is
	// logged with that consequence named rather than dropped: the tag still goes out, with the empty
	// object every decoder refuses.
	mark := func(room string) *Metadata {
		payload, perr := metaPayload(MarkerPayload{Repo: p.Plan.Repository, Room: room})
		if perr != nil {
			a.log().Error("filed mark payload: the proposal stays editable", "err", perr)
			payload = json.RawMessage(`{}`)
		}
		return &Metadata{EventType: filedEventType, EventPayload: payload}
	}
	res, err := a.Broker.RunCreate(ctx, RunCreate{
		Draft:      Draft{Repo: p.Plan.Repository, Title: p.Plan.Issues[0].Title, Body: p.Plan.Issues[0].Body},
		Plan:       raw,
		PlanHash:   p.Hash,
		MaxCostUSD: ch.MaxCostUSD,
		OnBehalfOf: "slack:" + a.TeamID + "/" + user,
		Ref:        "slack/" + a.TeamID + "/" + channel + "/" + thread,
		Key:        a.TeamID + "/" + channel + "/" + thread, // one issue per thread, whatever the revision
	})
	var r Refused
	if errors.As(err, &r) {
		code, text, passing := notFiled(string(r), user, p.Plan.Repository, confirm)
		if passing {
			return retry(code, text)
		}
		return refuse(code, text)
	}
	if err != nil {
		a.log().Warn("run_create failed", "err", err)
		return retry("unreachable", "Not filed: the factory could not be reached; "+again+".")
	}
	if !roomRE.MatchString(res.Room) {
		a.log().Warn("run_create answered an unusable room id")
		say(fmt.Sprintf("The task is filed (#%d, for <@%s>), but its room cannot be followed here.", res.Issue, user), mark(""))
		return "error", answer
	}
	// Put on a duplicate too (idempotent): a Put that failed the first time is retried by the next ✅.
	if err := a.Threads.Put(ctx, res.Room, Thread{Channel: channel, TS: thread, At: a.now()}); err != nil {
		a.log().Warn("thread map put failed", "room", res.Room, "err", err)
	}
	// The Filed line replaces the proposal in place: one message from the mention to the room. An
	// emptied block list, not none — chat.update leaves a message's blocks as they are when the key is
	// absent, so nil would keep Confirm and Cancel live on the message that filed the plan (Review
	// Focus 1, the same write repostProposal and App.inert make).
	filed, line := mark(res.Room), a.filedText(res, user)
	if err := a.Slack.UpdateProposal(ctx, channel, itemTS, line, json.RawMessage("[]"), filed); err != nil {
		// Slack refused the edit: today's separate Filed line still tells the thread, and the outcome
		// says the message was not the one that said it.
		a.log().Warn("proposal filed update failed", "channel", channel, "ts", itemTS, "err", err)
		a.proposalUpdate(ctx, countUnrewritten)
		say(line, filed)
		return "unrewritten", line
	}
	if res.Duplicate {
		return "duplicate", line
	}
	return "filed", line
}

// filedText is the thread's line for a filed run: today's Filed line, or the duplicate's one, built
// here so the ✅ fallback and PR 6's Confirm say exactly what the draft path has always said. It is a
// method because the room link comes from the App's own rooms URL.
func (a *App) filedText(res RunResult, user string) string {
	url := strings.TrimSuffix(a.RoomsURL, "/") + "/r/" + res.Room
	if res.Duplicate {
		return "Already filed: " + url
	}
	by := ""
	if githubLoginRE.MatchString(res.Sponsor) { // named so a wrong link is seen at its first use
		by = ", sponsored by GitHub @" + res.Sponsor
	}
	return fmt.Sprintf("Filed #%d for <@%s>%s; the factory starts it. Room: %s", res.Issue, user, by, url)
}
