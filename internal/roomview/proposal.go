// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"errors"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The proposal's sections, in the order a person reads them (CD14). Every section but the header is
// one of the plan's hashed parts, so it is the one that carries the changed mark.
const (
	sectionHeader     = "header"
	sectionRepository = "repository"
	sectionIssue      = "issue"
	sectionRoles      = "roles"
	sectionBudget     = "budget"
	sectionQuestion   = "question"
	sectionControls   = "controls"
)

// changedMark ends a section's heading when the content the hash covers moved since the last
// revision: what the person read is not what a click on this message would file (CD14, CD27).
const changedMark = " ‹changed›"

// The header's and the controls' fixed lines. They are this package's own words, so they hold no
// value that could need escaping.
const (
	degradedLine = "template draft: the plan service is unavailable"
	confirmLine  = "requires a new confirmation"
	oneIssueLine = "one issue: a plan with more is filed from your coding agent or the web UI"
	editingLine  = "Edit issues is a reply: write what should change in this thread and the plan is " +
		"drafted again from your words. Confirm, or a :white_check_mark: reply or reaction, files it as " +
		"shown; Cancel closes it. The plan is not touched until then."
)

// maxProposalEvidence is how many of the plan's reasons for its repository the message shows: the
// evidence is context, and a proposal is read once.
const maxProposalEvidence = 5

// slackUserID is a Slack member's id: the one value this package puts inside a <@…> mention, so it
// may hold nothing but what an id holds.
var slackUserID = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)

// errProposalFormat refuses a format other than Slack: the proposal's revision, its mention of the
// author and its controls are Slack's.
var errProposalFormat = errors.New("roomview: a proposal renders in Slack's mrkdwn only")

// ProposalEvidence is one reason the planner chose the repository: text it wrote, and a URL the
// broker built (or ""). It is the shape slackapp maps intake.Evidence onto.
type ProposalEvidence struct{ Text, URL string }

// ProposalAlternative is another repository that also fits, in the planner's words. It is display
// only: the plan files into the one repository the header names.
type ProposalAlternative struct{ Repository, Why string }

// ProposalQuestion is the at most one thing the broker asks before filing, with its recommendation.
type ProposalQuestion struct{ Text, Answer, Why string }

// ProposalIssue is the issue as it will be filed: the title, the class the broker derived, the roles
// and order it wrote from config, and the body's three parts, or the body whole when it does not
// carry the template.
type ProposalIssue struct {
	Title, Class                      string
	Roles                             []string
	DependsOn                         []int
	What, Should, Acceptance, RawBody string
	// Suggestion is the planner's suggestion and the broker's merge outcome; nil on a plan made
	// without the factory's catalogue, which shows no suggestion line.
	Suggestion *ProposalSuggestion
}

// ProposalSuggestion is what the planner suggests for the issue (display, O1): the profile ("" for
// the factory's choice) and its roles from the catalogue, the merge outcome the broker computed, and
// the model's why, untrusted.
type ProposalSuggestion struct {
	Profile string
	Roles   []string
	Outcome string
	Why     string
}

// ProposalBudget is the ceiling this plan's work is charged against and where the number came from:
// both written by the broker from config (CD17).
type ProposalBudget struct {
	CeilingUSD float64
	Source     string
}

// ProposalView is a plan/v2 as a person reads it before anything is filed. roomview declares it
// rather than taking intake.Plan: the consumer owns the shape, and a plan's struct never enters the
// package that renders text.
type ProposalView struct {
	Repository   string
	Evidence     []ProposalEvidence
	Alternatives []ProposalAlternative
	Question     *ProposalQuestion
	Issue        ProposalIssue
	Budget       ProposalBudget
	Hash         string
	Rev          int
	Author       string
	// Degraded says the plan is the composer's template, not the planner's: the message says so.
	Degraded bool
	// Confirm says a material field moved since the last revision: a new confirmation is required.
	Confirm bool
	// Changed is the keys of the sections whose hashed content moved (CD27's material set).
	Changed []string
}

// ProposalSection is one part of the proposal message. Key names what it holds (the caller maps it
// to a block), Heading is the plain label the caller marks up, Body is Slack mrkdwn with every
// untrusted value already inert, and Changed is whether the revision moved what it shows.
type ProposalSection struct {
	Key, Heading, Body string
	Changed            bool
}

// Proposal writes a plan as the proposal message's sections (CD7, CD13): the header naming the
// plan, its hash and its author, then the repository with its evidence and the alternatives, the
// issue as it will be filed, the roles, the ceiling, the one question when there is one, and the
// controls. Every value is escaped here once — the plan's text as data, the broker's own lines
// stripped — so a title holding a link, a heading, an ANSI escape or a bidi override is the text it
// is, and this renderer builds a link only from a field the broker approved (an evidence URL that
// `allowed` admits) and a mention only from an id's shape.
//
// Escaping is the whole of what this package can do about links: Slack auto-links a bare URL in a
// message's text, and no escape of `&`, `<` or `>` and no code span disables that, so a URL the plan
// spelled is shown as the text it is and Slack may still make it clickable. Nothing the plan wrote
// ever becomes a link of this renderer's.
//
// Slack is the one format: the message is Slack's, and a caller asking for another is told so
// rather than given a second rendering to keep true.
func Proposal(p ProposalView, o Options) ([]ProposalSection, error) {
	if o.Format != Slack {
		return nil, errProposalFormat
	}
	c := &card{o: o}
	sections := []ProposalSection{
		{Key: sectionHeader, Body: c.proposalHeader(p)},
		{Key: sectionRepository, Heading: "Repository", Body: c.proposalRepository(p)},
		{Key: sectionIssue, Heading: "Issue", Body: c.proposalIssue(p)},
		{Key: sectionRoles, Heading: "Roles", Body: c.proposalRoles(p)},
		{Key: sectionBudget, Heading: "Ceiling", Body: c.proposalBudget(p)},
	}
	if p.Question != nil {
		sections = append(sections, ProposalSection{
			Key: sectionQuestion, Heading: "One question", Body: c.proposalQuestion(*p.Question),
		})
	}
	sections = append(sections, ProposalSection{Key: sectionControls, Heading: "Editing", Body: editingLine})
	for i := range sections {
		// A key is marked when it is one of the plan's sections and moved: an unknown key in
		// Changed marks nothing, and the header is not a part of the plan.
		if sections[i].Key != sectionHeader && slices.Contains(p.Changed, sections[i].Key) {
			sections[i].Changed = true
			sections[i].Heading += changedMark
		}
	}
	return sections, nil
}

// proposalHeader is the line above everything else: which revision this is, the hash a Confirm
// binds, and who asked — plus the two facts that change what the message means, that the plan is
// the composer's template and that a field moved.
func (c *card) proposalHeader(p ProposalView) string {
	head := "Proposal v" + strconv.Itoa(p.Rev)
	if h := slackInert(p.Hash); h != "" {
		head += " · " + h
	}
	if who := slackMention(p.Author); who != "" {
		head += " · by " + who
	}
	lines := []string{head}
	if p.Degraded {
		lines = append(lines, degradedLine)
	}
	if p.Confirm {
		lines = append(lines, confirmLine)
	}
	return strings.Join(lines, "\n")
}

// slackMention is member as Slack's mention, or "" when it does not look like an id: a mention is
// markup, and one built from anything else would be markup of the caller's choosing.
func slackMention(member string) string {
	if !slackUserID.MatchString(member) {
		return ""
	}
	return "<@" + member + ">"
}

// proposalRepository is the plan's repository, why, and what else would fit: the evidence is
// escaped in its place, at most maxProposalEvidence lines of it, each linked only to a URL the
// broker built.
func (c *card) proposalRepository(p ProposalView) string {
	lines := []string{c.text(p.Repository, false)}
	for i, e := range p.Evidence {
		if i == maxProposalEvidence {
			break
		}
		lines = append(lines, "- "+c.url(c.text(e.Text, false), e.URL))
	}
	for _, a := range p.Alternatives {
		line := "also serves this channel: " + c.text(a.Repository, false)
		if why := c.text(a.Why, false); why != "" {
			line += " — " + why
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// proposalIssue is the issue as it will be filed. Its title is one uncut line; its body is the
// template's three labelled parts, or the body whole in a code block when it carries no template —
// Slack's headings are not mrkdwn, so the parts are labels rather than the body's own.
func (c *card) proposalIssue(p ProposalView) string {
	lines := []string{c.quoted(p.Issue.Title, false)}
	parts := [3][2]string{
		{"What is wrong", p.Issue.What},
		{"What it should be", p.Issue.Should},
		{"Acceptance check", p.Issue.Acceptance},
	}
	if strip(p.Issue.What) == "" {
		// A body that is not the template's is shown whole and never edited: a code block keeps
		// Slack from reading the body's own markup, and the escape above closed no fence it opened.
		if raw := slackInert(p.Issue.RawBody); raw != "" {
			lines = append(lines, "```"+raw+"```")
		}
	} else {
		for _, part := range parts {
			if v := slackInert(part[1]); v != "" {
				lines = append(lines, part[0]+": "+v)
			}
		}
	}
	var tail []string
	if class := slackInert(p.Issue.Class); class != "" {
		tail = append(tail, "class "+class)
	}
	if len(p.Issue.DependsOn) > 0 {
		deps := make([]string, 0, len(p.Issue.DependsOn))
		for _, d := range p.Issue.DependsOn {
			deps = append(deps, "#"+strconv.Itoa(d))
		}
		tail = append(tail, "depends on "+strings.Join(deps, ", "))
	}
	if len(tail) > 0 {
		lines = append(lines, strings.Join(tail, " · "))
	}
	if sg := p.Issue.Suggestion; sg != nil {
		lines = append(lines, "Suggested: "+slackInert(SuggestionLine(p.Issue.Class, sg.Profile, sg.Roles, sg.Outcome)))
		if why := slackInert(sg.Why); why != "" {
			lines = append(lines, "why (the planner's reading): "+why)
		}
	}
	return strings.Join(lines, "\n")
}

// SuggestionLine is the suggestion in the broker's words: `class docs-links · profile links-solo
// (implementer) · merges on its own when the gate's conditions hold`, or `profile: the factory's
// choice` with none. Plain text: each surface makes it inert its own way.
func SuggestionLine(class, profile string, roles []string, outcome string) string {
	p := "profile: the factory's choice"
	if profile != "" {
		p = "profile " + profile
		if len(roles) > 0 {
			p += " (" + strings.Join(roles, ", ") + ")"
		}
	}
	return "class " + class + " · " + p + " · " + MergeWords(outcome)
}

// proposalRoles is the roles the broker wrote from config, in their order, and the one bound Slack
// adds: a plan with more than one issue is filed from the coding agent or the web UI (CD19), never
// trimmed here.
func (c *card) proposalRoles(p ProposalView) string {
	roles := make([]string, 0, len(p.Issue.Roles))
	for _, r := range p.Issue.Roles {
		if v := c.text(r, false); v != "" {
			roles = append(roles, v)
		}
	}
	if len(roles) == 0 {
		return oneIssueLine
	}
	return strings.Join(roles, ", ") + " · " + oneIssueLine
}

// proposalBudget is the ceiling and the config value it came from.
func (c *card) proposalBudget(p ProposalView) string {
	out := dollars(int64(math.Round(p.Budget.CeilingUSD * 1e6)))
	if source := slackInert(p.Budget.Source); source != "" {
		out += " (" + source + ")"
	}
	return out
}

// proposalQuestion is the one question the broker asks before filing, with its recommendation: a
// plan with several candidates is answered here and nowhere else.
func (c *card) proposalQuestion(q ProposalQuestion) string {
	lines := []string{c.quoted(q.Text, false)}
	rec := "Recommended: " + c.text(q.Answer, false)
	if why := c.text(q.Why, false); why != "" {
		rec += " — " + why
	}
	lines = append(lines, rec)
	return strings.Join(lines, "\n")
}
