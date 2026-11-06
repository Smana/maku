// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// slackLink is the one link form the renderer builds, and slackMentionForm the one mention: a Slack
// message holds no other "<" than those and the escapes.
var (
	slackLink = regexp.MustCompile(`<[^<>|\s]+\|[^<>]*>`)
	// slackBuilt is what the renderer may put a "<" in front of: one of its links, or a mention of
	// an id the message names.
	slackBuilt = regexp.MustCompile(`<@[A-Za-z0-9]{1,32}>|<[^<>|\s]+\|[^<>]*>`)
	// slackEscapes are the three escapes, so that the text they stand for is not read as markup.
	slackEscapes = strings.NewReplacer("&lt;", "", "&gt;", "", "&amp;", "")
)

// proposalView is one plan as slackapp maps it: every field set, one issue, one question.
func proposalView() ProposalView {
	return ProposalView{
		Repository: "Smana/ai-native-ref",
		Evidence: []ProposalEvidence{
			{Text: "the channel serves it", URL: rooms + "/r/26zfnuxm"},
			{Text: "README.md is its docs"},
		},
		Alternatives: []ProposalAlternative{{
			Repository: "Smana/cloud-native-ref",
			Why:        "it holds the platform's own docs",
		}},
		Question: &ProposalQuestion{
			Text:   "The channel's README points at both; which one?",
			Answer: "Smana/ai-native-ref",
			Why:    "it owns the page the link is on",
		},
		Issue: ProposalIssue{
			Title:      "docs: fix a dead link",
			Class:      "forge.push",
			Roles:      []string{"implementer", "reviewer"},
			DependsOn:  []int{1, 2},
			What:       "the link on the front page 404s",
			Should:     "it resolves to the current page",
			Acceptance: "the page's link check passes",
		},
		Budget: ProposalBudget{CeilingUSD: 1.5, Source: "channel:C1.maxCostUSD"},
		Hash:   "sha256:" + strings.Repeat("a", 64),
		Rev:    3,
		Author: "U07JY4EQNDN",
	}
}

// proposalSections renders p as Slack's mrkdwn: the one format a proposal comes in.
func proposalSections(t *testing.T, p ProposalView) []ProposalSection {
	t.Helper()
	ss, err := Proposal(p, Options{Format: Slack, Rooms: rooms})
	if err != nil {
		t.Fatal(err)
	}
	return ss
}

// section finds the section with this key, and fails when there is none.
func section(t *testing.T, ss []ProposalSection, key string) ProposalSection {
	t.Helper()
	for _, s := range ss {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("no %q section in %v", key, keys(ss))
	return ProposalSection{}
}

func keys(ss []ProposalSection) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Key)
	}
	return out
}

// assertSlackInert fails when body, read as Slack's mrkdwn, holds a link the broker did not build,
// a mention, a heading or table row of its own, a terminal escape or a bidi override. spelled is the
// untrusted value the case put in: a Markdown-looking link the plan spelled is shown as text, and
// the renderer may not make one the plan did not.
func assertSlackInert(t *testing.T, key, body, spelled string) {
	t.Helper()
	spans := slackLink.FindAllStringIndex(body, -1)
	built := make(map[int]bool, len(spans))
	for _, s := range spans {
		built[s[0]] = true
		if url := strings.SplitN(body[s[0]+1:s[1]], "|", 2)[0]; !allowed(url, Options{Rooms: rooms}) {
			t.Errorf("%s: a link to %q, which the broker did not build: %q", key, url, body)
		}
	}
	for _, m := range slackBuilt.FindAllStringIndex(body, -1) {
		built[m[0]] = true
	}
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '<':
			if !built[i] {
				t.Errorf("%s: the \"<\" at %d opens neither one of the renderer's links nor the escaped \"&lt;\": %q", key, i, body)
			}
		case 0x1b:
			t.Errorf("%s: an ANSI escape at %d: %q", key, i, body)
		}
	}
	if strings.ContainsRune(body, '\u202e') {
		t.Errorf("%s: a bidi override: %q", key, body)
	}
	if strings.Contains(body, "<!") {
		t.Errorf("%s: a Slack control form: %q", key, body)
	}
	// Everything outside a link and a mention the renderer built is text: no markup of its own, and
	// no Markdown link the plan did not spell (Slack reads none of these, and the renderer adds none).
	rest := slackEscapes.Replace(slackBuilt.ReplaceAllString(body, ""))
	if strings.ContainsAny(rest, "<>") {
		t.Errorf("%s: markup the renderer did not build: %q", key, rest)
	}
	if n := strings.Count(rest, "](http"); n > strings.Count(spelled, "](http") {
		t.Errorf("%s: the plan spelled %d Markdown links and the rendering holds %d: %q",
			key, strings.Count(spelled, "](http"), n, rest)
	}
	for _, line := range strings.Split(rest, "\n") {
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "|") {
			t.Errorf("%s: a line opens a heading or a table row of its own: %q", key, line)
		}
	}
}

// CD13 and FA10/FA11: the plan's text is data. A title or body carrying links, Markdown, ANSI,
// bidi or a code fence yields no link, heading, table or escape in the rendering, and every URL
// in it is one the broker built.
func TestProposalRendersUntrustedTextInert(t *testing.T) {
	for name, tc := range map[string]struct {
		value  string // the one hostile value the case puts in the plan
		set    func(*ProposalView, string)
		extra  func(*ProposalView) // a second value, or fields the case empties
		absent string              // a value that must not survive into the rendering at all
	}{
		"title": {
			value: "docs: [x](https://evil.example) <!channel>",
			set:   func(p *ProposalView, v string) { p.Issue.Title = v },
		},
		"body": {
			value: "## What is wrong\n<script>alert(1)</script>\n```\n*not bold*",
			set:   func(p *ProposalView, v string) { p.Issue.What = v },
		},
		"raw body": {
			value: "## What is wrong\n<script>alert(1)</script>",
			set:   func(p *ProposalView, v string) { p.Issue.RawBody = v },
			extra: func(p *ProposalView) { p.Issue.What, p.Issue.Should, p.Issue.Acceptance = "", "", "" },
		},
		"evidence text": {
			value: "[click](https://evil.example)",
			set:   func(p *ProposalView, v string) { p.Evidence[0].Text = v },
		},
		"evidence url": {
			value:  "https://evil.example/room",
			set:    func(p *ProposalView, v string) { p.Evidence[0].URL = v },
			absent: "evil.example",
		},
		"alternative why": {
			value: "\ansi\x1b[31m",
			set:   func(p *ProposalView, v string) { p.Alternatives[0].Why = v },
		},
		"alternative repository": {
			value: "Smana/x|y <!here>",
			set:   func(p *ProposalView, v string) { p.Alternatives[0].Repository = v },
		},
		"question text": {
			value: "which one\u202e? | first | second",
			set:   func(p *ProposalView, v string) { p.Question.Text = v },
		},
		"question why": {
			value: "<@U123> says so",
			set:   func(p *ProposalView, v string) { p.Question.Why = v },
		},
		"roles": {
			value: "<!--",
			set:   func(p *ProposalView, v string) { p.Issue.Roles = []string{"implementer", v} },
		},
		"class": {
			value: "forge.push | x",
			set:   func(p *ProposalView, v string) { p.Issue.Class = v },
		},
		"budget source": {
			value: "https://evil.example</plain>",
			set:   func(p *ProposalView, v string) { p.Budget.Source = v },
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := proposalView()
			tc.set(&p, tc.value)
			if tc.extra != nil {
				tc.extra(&p)
			}
			for _, s := range proposalSections(t, p) {
				assertSlackInert(t, s.Key, s.Body, tc.value)
				if tc.absent != "" && strings.Contains(s.Body, tc.absent) {
					t.Errorf("%s: %q survived into %q", s.Key, tc.absent, s.Body)
				}
			}
		})
	}
}

// CD14 and CD27: a section whose hashed content changed is marked, and only those; the header says
// a new confirmation is required exactly when Confirm is set.
func TestProposalMarksTheChangedSections(t *testing.T) {
	p := proposalView()
	p.Changed = []string{"repository", "budget"}
	p.Confirm = true
	ss := proposalSections(t, p)
	marked := map[string]bool{"repository": true, "budget": true}
	for _, s := range ss {
		if s.Changed != marked[s.Key] {
			t.Errorf("%s: Changed = %v, want %v", s.Key, s.Changed, marked[s.Key])
		}
		if has := strings.Contains(s.Heading, "‹changed›"); has != marked[s.Key] {
			t.Errorf("%s: heading %q carries the mark: %v, want %v", s.Key, s.Heading, has, marked[s.Key])
		}
		if marked[s.Key] && !strings.HasSuffix(s.Heading, " ‹changed›") {
			t.Errorf("%s: heading %q must end with the mark", s.Key, s.Heading)
		}
	}
	if h := section(t, ss, "header"); !strings.HasSuffix(h.Body, "requires a new confirmation") {
		t.Errorf("header = %q, want it to end with the confirmation line", h.Body)
	}

	p.Changed, p.Confirm = nil, false
	for _, s := range proposalSections(t, p) {
		if s.Changed || strings.Contains(s.Heading, "‹changed›") {
			t.Errorf("%s: nothing changed, but %q is marked", s.Key, s.Heading)
		}
	}
	if h := section(t, proposalSections(t, p), "header"); strings.Contains(h.Body, "requires a new confirmation") {
		t.Errorf("header = %q, want no confirmation line", h.Body)
	}
}

// CD7: the sections come in the order a person reads them, the header names the plan, its hash and
// its author, the question is there exactly when the plan asks one, and Slack is the only format.
func TestProposalSectionsInOrder(t *testing.T) {
	p := proposalView()
	ss := proposalSections(t, p)
	want := []string{"header", "repository", "issue", "roles", "budget", "question", "controls"}
	if got := keys(ss); !slices.Equal(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if h := section(t, ss, "header"); h.Body != "Proposal v3 · "+p.Hash+" · by <@U07JY4EQNDN>" {
		t.Errorf("header = %q", h.Body)
	}
	if h := section(t, ss, "header"); h.Heading != "" {
		t.Errorf("header heading = %q, want none", h.Heading)
	}

	p.Degraded = true
	if h := section(t, proposalSections(t, p), "header"); !strings.Contains(h.Body, "template draft: the plan service is unavailable") {
		t.Errorf("degraded header = %q", h.Body)
	}

	p = proposalView()
	p.Question = nil
	if got := keys(proposalSections(t, p)); !slices.Equal(got, []string{"header", "repository", "issue", "roles", "budget", "controls"}) {
		t.Errorf("without a question: keys = %v", got)
	}

	// The controls name the one way to change the plan, and both ways to file it.
	body := section(t, proposalSections(t, proposalView()), "controls").Body
	for _, want := range []string{"Edit issues", ":white_check_mark:", "not touched"} {
		if !strings.Contains(body, want) {
			t.Errorf("controls %q does not name %q", body, want)
		}
	}

	// The author is a mention only when it is Slack's own shape: an id that could carry anything
	// else is not a mention.
	p = proposalView()
	p.Author = "U123> <!channel"
	if h := section(t, proposalSections(t, p), "header"); strings.Contains(h.Body, "<!") || strings.Contains(h.Body, "<@") {
		t.Errorf("header = %q: an author that is not Slack's own shape made a mention", h.Body)
	}

	if _, err := Proposal(proposalView(), Options{Format: Markdown, Rooms: rooms}); err == nil {
		t.Error("Proposal renders in Slack's mrkdwn only, and a Markdown caller must be told so")
	}
}
