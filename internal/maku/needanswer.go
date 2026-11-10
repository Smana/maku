// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/factory/sanitize"
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
