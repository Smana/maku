// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"context"
	"errors"
	"fmt"

	"github.com/shurcooL/githubv4"

	"github.com/Smana/maku/internal/factory/evidence"
)

// ErrRecordsTruncated is a promotion-evidence read past its bound: records past it are unread, so
// the evidence would be short and is refused instead (stage 6).
var ErrRecordsTruncated = errors.New("forge: more pull request records than the evidence reads")

// recordPages bounds the record reads: shadow-merge-tally.sh's --limit 500, in pages of 50 small
// enough that the newest 50 comments of each stay a bounded answer. The newest comment decides every count, so
// older ones are never needed.
const recordPages = 10

type recordNode struct {
	PullRequest struct {
		Number     int
		HeadRefOid string
		State      string
		Body       string
		MergedAt   *githubv4.DateTime
		MergedBy   *actor
		Labels     struct {
			Nodes    []struct{ Name string }
			PageInfo struct{ HasNextPage bool }
		} `graphql:"labels(first: 50)"`
		Comments struct {
			Nodes []struct {
				Author    *actor
				CreatedAt githubv4.DateTime
				Body      string
			}
		} `graphql:"comments(last: 50)"`
	} `graphql:"... on PullRequest"`
}

// search runs q over at most recordPages pages and returns every pull request node.
func (g *GitHub) search(ctx context.Context, q string) ([]recordNode, error) {
	var out []recordNode
	var after *githubv4.String
	for range recordPages {
		var r struct {
			Search struct {
				Nodes    []recordNode
				PageInfo struct {
					HasNextPage bool
					EndCursor   githubv4.String
				}
			} `graphql:"search(type: ISSUE, first: 50, after: $after, query: $q)"`
		}
		err := g.v4.Query(ctx, &r, map[string]any{"q": githubv4.String(q), "after": after})
		g.mark(err)
		if err != nil {
			return nil, wrap("search pull request records", err)
		}
		for _, n := range r.Search.Nodes {
			if n.PullRequest.Labels.PageInfo.HasNextPage {
				return nil, ErrRecordsTruncated
			}
		}
		out = append(out, r.Search.Nodes...)
		if !r.Search.PageInfo.HasNextPage {
			return out, nil
		}
		c := r.Search.PageInfo.EndCursor
		after = &c
	}
	return nil, ErrRecordsTruncated
}

func labels(n recordNode) []evidence.Label {
	var out []evidence.Label
	for _, l := range n.PullRequest.Labels.Nodes {
		out = append(out, evidence.Label{Name: l.Name})
	}
	return out
}

// MergedRecords are the merged pull requests matching filter (search qualifiers the caller builds
// from config, never from a request), in the shape shadow-merge-tally.sh reads from gh.
func (g *GitHub) MergedRecords(ctx context.Context, filter string) ([]evidence.PR, error) {
	nodes, err := g.search(ctx, fmt.Sprintf("repo:%s/%s is:pr is:merged %s", g.owner, g.name, filter))
	if err != nil {
		return nil, err
	}
	out := make([]evidence.PR, 0, len(nodes))
	for _, n := range nodes {
		p := n.PullRequest
		pr := evidence.PR{Number: p.Number, HeadRefOid: p.HeadRefOid, Labels: labels(n)}
		if p.MergedAt != nil {
			pr.MergedAt = p.MergedAt.Time
		}
		if p.MergedBy != nil {
			pr.MergedBy = &evidence.Actor{Login: p.MergedBy.login()}
		}
		for _, c := range p.Comments.Nodes {
			pr.Comments = append(pr.Comments, evidence.Comment{Author: evidence.Actor{Login: c.Author.login()}, CreatedAt: c.CreatedAt.Time, Body: c.Body})
		}
		out = append(out, pr)
	}
	return out, nil
}

// RevertRecords are the pull requests titled Revert, any state: the script's reverts_of.
func (g *GitHub) RevertRecords(ctx context.Context) ([]evidence.Revert, error) {
	nodes, err := g.search(ctx, fmt.Sprintf("repo:%s/%s is:pr Revert in:title", g.owner, g.name))
	if err != nil {
		return nil, err
	}
	out := make([]evidence.Revert, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, evidence.Revert{Body: n.PullRequest.Body, State: n.PullRequest.State, Labels: labels(n)})
	}
	return out, nil
}

// AnyMerged is whether a merged pull request matches filter: one follow-up search of the script.
func (g *GitHub) AnyMerged(ctx context.Context, filter string) (bool, error) {
	var r struct {
		Search struct{ IssueCount int } `graphql:"search(type: ISSUE, first: 1, query: $q)"`
	}
	err := g.v4.Query(ctx, &r, map[string]any{"q": githubv4.String(fmt.Sprintf("repo:%s/%s is:pr is:merged %s", g.owner, g.name, filter))})
	g.mark(err)
	if err != nil {
		return false, wrap("search merged pull requests", err)
	}
	return r.Search.IssueCount > 0, nil
}
