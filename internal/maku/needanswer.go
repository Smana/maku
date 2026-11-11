// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/mcpwire"
)

// AppendDecision appends one confirmed, single-line answer to an issue's Decisions section. Existing
// text is retained byte for byte; the new issue-local number follows the greatest existing record.
func AppendDecision(body, answer string) (string, error) {
	if strings.ContainsAny(answer, "\r\n") {
		return "", fmt.Errorf("invalid_answer: a decision must be one line")
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "", fmt.Errorf("invalid_answer: a decision must not be blank")
	}
	records, err := sanitize.DecisionRecords(body)
	if err != nil {
		return "", err
	}
	next := 1
	if len(records) > 0 {
		// DecisionRecords requires ascending unique numbers, so the last is the maximum.
		last := records[len(records)-1].Number
		if last == int(^uint(0)>>1) {
			return "", fmt.Errorf("invalid_decisions: no decision number remains")
		}
		next = last + 1
	}
	record := fmt.Sprintf("D%d: %s", next, answer)
	if issueHeading(body, "## Decisions") {
		separator := "\n"
		if strings.HasSuffix(body, "\n") {
			separator = ""
		}
		return body + separator + record, nil
	}
	if !issueHeading(body, "## Acceptance check") {
		return "", fmt.Errorf("invalid_decisions: Acceptance check not found")
	}
	separator := "\n\n"
	switch {
	case strings.HasSuffix(body, "\n\n"):
		separator = ""
	case strings.HasSuffix(body, "\n"):
		separator = "\n"
	}
	return body + separator + "## Decisions\n\n" + record, nil
}

// issueHeading reports whether body has heading on a line of its own, as DecisionRecords reads it.
func issueHeading(body, heading string) bool {
	for line := range strings.SplitSeq(body, "\n") {
		if strings.TrimSpace(line) == heading {
			return true
		}
	}
	return false
}

// IssueComment is one GitHub issue comment as the local answer flow reads it.
type IssueComment struct {
	Author    string
	Body      string
	CreatedAt time.Time
}

// IssueSnapshot is the GitHub issue state read before and after a confirmed decision edit.
type IssueSnapshot struct {
	Body      string
	Labels    []string
	UpdatedAt time.Time
	Comments  []IssueComment
}

// IssueEditor reads an issue and replaces only its body. It never changes labels.
type IssueEditor interface {
	View(context.Context, string, int) (IssueSnapshot, error)
	EditBody(context.Context, string, int, string) error
}

// GHIssues implements IssueEditor with the developer's gh, without a shell.
type GHIssues struct{}

// maxGHIssueViewOutput bounds the JSON snapshot retained from gh, including body and comments.
const maxGHIssueViewOutput = 1 << 20

// View reads the issue body, labels, update time and comments with the developer's gh.
func (GHIssues) View(ctx context.Context, repo string, issue int) (IssueSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "issue", "view", strconv.Itoa(issue), "--repo="+repo, "--json", "body,labels,updatedAt,comments") //nolint:gosec // fixed executable and argument list, never a shell
	var out, errOut bytes.Buffer
	cmd.Stdout = &cappedIssueView{b: &out}
	cmd.Stderr = &capped{b: &errOut}
	if err := cmd.Run(); err != nil {
		return IssueSnapshot{}, fmt.Errorf("gh issue view: %w: %s", err, OneLine(errOut.String()))
	}
	var raw struct {
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		UpdatedAt time.Time `json:"updatedAt"`
		Comments  []struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			Body      string    `json:"body"`
			CreatedAt time.Time `json:"createdAt"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		return IssueSnapshot{}, fmt.Errorf("gh issue view: unreadable JSON: %w", err)
	}
	snapshot := IssueSnapshot{Body: raw.Body, UpdatedAt: raw.UpdatedAt,
		Labels: make([]string, len(raw.Labels)), Comments: make([]IssueComment, len(raw.Comments))}
	for i, label := range raw.Labels {
		snapshot.Labels[i] = label.Name
	}
	for i, comment := range raw.Comments {
		snapshot.Comments[i] = IssueComment{Author: comment.Author.Login, Body: comment.Body, CreatedAt: comment.CreatedAt}
	}
	return snapshot, nil
}

// EditBody replaces only the issue body, passed to gh on stdin, and applies no labels.
func (GHIssues) EditBody(ctx context.Context, repo string, issue int, body string) error {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "issue", "edit", strconv.Itoa(issue), "--repo="+repo, "--body-file=-") //nolint:gosec // fixed executable and argument list, never a shell
	cmd.Stdin = strings.NewReader(body)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &capped{b: &out}, &capped{b: &errOut}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh issue edit: %w: %s", err, OneLine(errOut.String()))
	}
	return nil
}

// cappedIssueView keeps a bounded prefix of gh's JSON while draining the rest of its stdout.
type cappedIssueView struct{ b *bytes.Buffer }

func (c *cappedIssueView) Write(p []byte) (int, error) {
	if room := maxGHIssueViewOutput - c.b.Len(); room > 0 {
		c.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// needAnswerTool answers a scoper's post-filing question without taking label authority.
const (
	needAnswerTool   = "need_answer"
	needsAuthorLabel = "factory/needs-author"
)

// needAnswerEntry is need_answer's tools/list entry. The answer and confirmation stay in the
// elicitation, never in tool arguments an agent can supply itself.
var needAnswerEntry = map[string]any{
	"name": needAnswerTool,
	"description": "Answer the scoper's question on an issue waiting with factory/needs-author. When it asks for the acceptance check, the answer is written into a ## Acceptance check section (created if missing) and recorded as a Decision. " +
		"The authorized room brief supplies the question, with the newest public comment by the configured factory App login as fallback. " +
		"The developer confirms the answer and body append in a form; only the issue body is edited, never its labels. " +
		"A maintainer must re-apply factory/ready afterwards.",
	"inputSchema": json.RawMessage(`{"type":"object","additionalProperties":false,"required":["repo","issue"],` +
		`"properties":{"repo":{"type":"string"},"issue":{"type":"integer","minimum":1},"room":{"type":"string"}}}`),
}

type scopeQuestion struct {
	Text        string `json:"text"`
	Recommended string `json:"recommended"`
}

// needAnswer appends one developer-confirmed answer to an issue whose scoper bounced it. It uses
// the authorized room question first, and never turns an unavailable internal question into a guess.
func (r *Relay) needAnswer(ctx context.Context, args json.RawMessage) any {
	var in struct {
		Repo  string `json:"repo"`
		Issue int    `json:"issue"`
		Room  string `json:"room"`
	}
	if err := mcpwire.DecodeArgs(args, &in); err != nil || !repoRE.MatchString(in.Repo) || in.Issue < 1 ||
		(in.Room != "" && !roomIDRE.MatchString(in.Room)) {
		return mcpwire.ToolError("invalid_arguments: need_answer takes repo (owner/name), issue (a positive number), and optionally one room id")
	}
	if r.Issues == nil {
		return mcpwire.ToolError("unavailable: need_answer has no GitHub issue editor")
	}
	issueURL := fmt.Sprintf("https://github.com/%s/issues/%d", in.Repo, in.Issue)
	result := func(answered bool) map[string]any {
		next := "a maintainer must re-apply factory/ready"
		if !answered {
			next = "the question is unanswered and nothing was written"
		}
		out, _ := mcpwire.ToolResult(map[string]any{"answered": answered, "issue": issueURL, "started": false, "next": next})
		return out
	}

	initial, err := r.Issues.View(ctx, in.Repo, in.Issue)
	if err != nil {
		return mcpwire.ToolError("unavailable: read the issue before answering: " + OneLine(err.Error()))
	}
	if !slices.Contains(initial.Labels, needsAuthorLabel) || slices.Contains(initial.Labels, readyLabel) {
		return mcpwire.ToolError("not_needs_author: need_answer requires factory/needs-author and no factory/ready; nothing was written")
	}

	room := in.Room
	if room == "" {
		rows, err := r.listRows(ctx, map[string]any{})
		if err != nil {
			return mcpwire.ToolError("unavailable: resolve the issue's room: " + OneLine(err.Error()))
		}
		var matches []string
		for _, row := range rows {
			if row.ID != "" && row.Issue == in.Issue && strings.EqualFold(row.Repository, in.Repo) {
				matches = append(matches, row.ID)
			}
		}
		if len(matches) > 1 {
			return mcpwire.ToolError("ambiguous_room: the issue matches " + strconv.Itoa(len(matches)) +
				" rooms; call need_answer again with one room id; nothing was written")
		}
		if len(matches) == 1 {
			room = matches[0]
		}
	}

	var question *scopeQuestion
	if room != "" {
		_, structured, isError, err := r.callBroker(ctx, "room_brief", map[string]any{"room": room})
		if err == nil && !isError {
			var brief struct {
				Question *scopeQuestion `json:"question"`
			}
			if json.Unmarshal(structured, &brief) == nil && brief.Question != nil && strings.TrimSpace(brief.Question.Text) != "" {
				question = brief.Question
			}
		}
	}
	if question == nil {
		if q, ok := publicBounceQuestion(initial.Comments, r.FactoryLogin); ok {
			question = &q
		}
	}
	if question == nil {
		return mcpwire.ToolError("needs_room_access: no authorized room brief or public bounce comment carries the question; " +
			"ask a maintainer or a reader of the room to answer it; nothing was written")
	}

	// <answer> is the one substitution the developer makes in the exact body the tool will write.
	// AnswerBody applies the same layout to the returned one-line answer below.
	acceptance := IsAcceptanceQuestion(question.Text)
	previewBody, err := AnswerBody(initial.Body, "<answer>", acceptance)
	if err != nil {
		return mcpwire.ToolError("invalid_decisions: " + OneLine(err.Error()) + "; nothing was written")
	}
	preview := bodyDiff(initial.Body, previewBody)
	recommended := strings.TrimSpace(question.Recommended)
	if acceptance {
		recommended = AcceptanceFromRecommendation(question.Recommended)
	}
	blankNote := ""
	if recommended != "" {
		blankNote = " Leave the answer blank to accept the recommendation."
	}
	r.mu.Lock()
	elicit := r.forms
	r.mu.Unlock()
	if !elicit {
		return mcpwire.ToolError("needs_confirmation: need_answer asks the developer for an answer and explicit send confirmation, " +
			"and this client cannot ask (no elicitation); nothing was written")
	}
	message := "Answer the scoper's question on " + issueURL + ".\n\nQuestion:\n" + question.Text +
		"\n\nScoper's recommendation:\n" + question.Recommended +
		"\n\nThe full body diff, with <answer> replaced by your trimmed one-line answer:\n" + preview +
		"\n\nOnly the body is edited, never labels." + blankNote + " A maintainer must re-apply factory/ready afterwards."
	ans, err := r.ask(ctx, "elicitation/create", map[string]any{
		"message": message,
		"requestedSchema": map[string]any{"type": "object", "required": []string{"answer", "send"}, "properties": map[string]any{
			"answer": map[string]any{"type": "string", "title": "Your one-line answer (blank accepts the recommendation)"},
			"send":   map[string]any{"type": "boolean", "title": "Append this answer to the issue", "default": false},
		}},
	})
	if err != nil {
		return mcpwire.ToolError("needs_confirmation: the developer was not asked: " + OneLine(err.Error()) + "; nothing was written")
	}
	var form struct {
		Action  string                     `json:"action"`
		Content map[string]json.RawMessage `json:"content"`
	}
	if len(ans.Error) > 0 || json.Unmarshal(ans.Result, &form) != nil {
		return mcpwire.ToolError("needs_confirmation: the client could not ask the developer; nothing was written")
	}
	send, _ := formBool(form.Content["send"])
	if form.Action != "accept" || !send {
		return result(false)
	}
	var answer string
	if json.Unmarshal(form.Content["answer"], &answer) != nil {
		return mcpwire.ToolError("invalid_answer: answer is one line of text; nothing was written")
	}
	if strings.TrimSpace(answer) == "" && recommended != "" {
		answer = recommended
	}
	newBody, err := AnswerBody(initial.Body, answer, acceptance)
	if err != nil {
		return mcpwire.ToolError(OneLine(err.Error()) + "; nothing was written")
	}

	current, err := r.Issues.View(ctx, in.Repo, in.Issue)
	if err != nil {
		return mcpwire.ToolError("unavailable: re-read the issue before answering: " + OneLine(err.Error()) + "; nothing was written")
	}
	if current.Body != initial.Body || !slices.Equal(current.Labels, initial.Labels) || !current.UpdatedAt.Equal(initial.UpdatedAt) ||
		!slices.Contains(current.Labels, needsAuthorLabel) || slices.Contains(current.Labels, readyLabel) {
		return mcpwire.ToolError("issue_changed: the issue body, labels, or updatedAt changed while the answer was open; nothing was written")
	}
	if err := r.Issues.EditBody(ctx, in.Repo, in.Issue, newBody); err != nil {
		return mcpwire.ToolError("not_answered: edit the issue body: " + OneLine(err.Error()))
	}
	return result(true)
}

var needsAuthorMarker = regexp.MustCompile(`<!-- agent-factory scope=[a-z2-7]{8} event=needs-author-[0-9]+ -->`)

// publicBounceQuestion reads only the newest factory needs-author comment. An internal bounce has
// the same marker but deliberately no question, so it fails closed rather than using an older one.
// The newest marker's author must be the configured factory App: a forged newer comment disables the
// fallback entirely instead of exposing an older trusted question or presenting the attacker as the scoper.
func publicBounceQuestion(comments []IssueComment, factoryLogin string) (scopeQuestion, bool) {
	var newest IssueComment
	found := false
	for _, comment := range comments {
		if needsAuthorMarker.MatchString(comment.Body) && (!found || !comment.CreatedAt.Before(newest.CreatedAt)) {
			newest, found = comment, true
		}
	}
	if !found || factoryLogin == "" || !strings.EqualFold(newest.Author, factoryLogin) {
		return scopeQuestion{}, false
	}
	const lead = "The scoper's question, and the assumption it would make otherwise:"
	_, rest, ok := strings.Cut(newest.Body, lead)
	if !ok {
		return scopeQuestion{}, false
	}
	rest = strings.TrimLeft(rest, "\r\n")
	fence, quoted, ok := strings.Cut(rest, "\n")
	if !ok || strings.Trim(fence, "`") != "" || len(fence) < 3 {
		return scopeQuestion{}, false
	}
	quoted, _, ok = strings.Cut(quoted, "\n"+fence)
	if !ok {
		return scopeQuestion{}, false
	}
	question, recommendation, ok := strings.Cut(quoted, "\nAssumption: ")
	if !ok || strings.TrimSpace(question) == "" {
		return scopeQuestion{}, false
	}
	return scopeQuestion{Text: question, Recommended: recommendation}, true
}
