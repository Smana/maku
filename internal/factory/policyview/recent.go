// SPDX-License-Identifier: Apache-2.0

package policyview

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/factory/evidence"
)

// DecisionRows are the measured values a decision record carries. Counts is false when the diff was
// not read, so an API consumer never presents an unread zero as a measurement.
type DecisionRows struct {
	Counted  bool   `json:"counted"`
	Files    int    `json:"files"`
	MaxFiles int    `json:"maxFiles"`
	Lines    int    `json:"lines"`
	MaxLines int    `json:"maxLines"`
	Shape    string `json:"shape,omitempty"`
	Holdout  string `json:"holdout,omitempty"`
	Oracle   string `json:"oracle,omitempty"`
	Age      string `json:"age,omitempty"`
	Reviewer string `json:"reviewer,omitempty"`
}

// RecentDecision is one per-policy decision line on a merged pull request.
type RecentDecision struct {
	At          time.Time    `json:"at"`
	PullRequest int          `json:"pullRequest"`
	Head        string       `json:"head"`
	Outcome     string       `json:"outcome"`
	Policy      string       `json:"policy"`
	Reason      string       `json:"reason"`
	Rows        DecisionRows `json:"rows"`
	Considered  string       `json:"considered,omitempty"`
	CommentURL  string       `json:"commentURL"`
}

// Recent is a repository's recorded decisions, newest first.
type Recent struct {
	Repository string           `json:"repository"`
	Since      string           `json:"since"`
	Decisions  []RecentDecision `json:"decisions"`
}

// ReadRecent reads every per-policy decision line in merged pull requests since a date accepted by
// GitHub's merged:>= search. Only the configured factory's own comments count.
func ReadRecent(repository, since, factory string, prs []evidence.PR) Recent {
	out := Recent{Repository: repository, Since: since, Decisions: []RecentDecision{}}
	for _, pr := range prs {
		for _, comment := range pr.Comments {
			if !sameFactory(comment.Author.Login, factory) {
				continue
			}
			for _, match := range record.FindAllStringSubmatch(comment.Body, -1) {
				d := RecentDecision{At: comment.CreatedAt, PullRequest: pr.Number, Head: match[1], Outcome: match[3],
					Policy: match[2], Reason: match[4], CommentURL: fmt.Sprintf("https://github.com/%s/pull/%d", repository, pr.Number)}
				fields := map[string]string{}
				for _, f := range field.FindAllStringSubmatch(match[5], -1) {
					fields[f[1]] = f[2]
				}
				d.Rows = recentRows(fields)
				d.Considered = fields["considered"]
				out.Decisions = append(out.Decisions, d)
			}
		}
	}
	sort.SliceStable(out.Decisions, func(i, j int) bool { return out.Decisions[i].At.After(out.Decisions[j].At) })
	return out
}

func sameFactory(author, factory string) bool {
	if strings.EqualFold(author, factory) {
		return true
	}
	if strings.HasSuffix(factory, "[bot]") {
		return false
	}
	normalize := func(s string) string {
		return strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(s), "app/"), "[bot]")
	}
	return normalize(author) == normalize(factory)
}

func recentRows(fields map[string]string) DecisionRows {
	r := DecisionRows{Shape: fields["shape"], Holdout: fields["holdout"], Oracle: fields["oracle"], Age: fields["age"], Reviewer: fields["reviewer"]}
	files, maxFiles := recentPair(fields["files"])
	lines, maxLines := recentPair(fields["lines"])
	r.Files, r.MaxFiles, r.Lines, r.MaxLines = recentCount(files), recentCount(maxFiles), recentCount(lines), recentCount(maxLines)
	r.Counted = files != "" && files != "-" && lines != "" && lines != "-"
	return r
}

func recentPair(value string) (string, string) {
	value, limit, _ := strings.Cut(value, "/")
	return value, limit
}

func recentCount(value string) int {
	n, _ := strconv.Atoi(value)
	return n
}
