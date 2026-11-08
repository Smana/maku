// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/evidence"
)

// The records come back in the shape the tally reads from gh: a Bot's login with [bot], the
// newest comments, the labels, across pages.
func TestMergedRecordsReadEveryPageInTheTallysShape(t *testing.T) {
	r := newRig(t)
	var queries []string
	r.gh.records = func(q string, after any) string {
		queries = append(queries, q)
		if after == nil {
			return `{"data":{"search":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[{"number":21,"headRefOid":"a21",` +
				`"mergedAt":"2026-10-01T00:00:00Z","mergedBy":{"__typename":"User","login":"Smana"},` +
				`"labels":{"nodes":[{"name":"factory/merged:docs-links"}],"pageInfo":{"hasNextPage":false}},` +
				`"comments":{"nodes":[{"author":{"__typename":"Bot","login":"ogenki-agent-factory"},"createdAt":"2026-10-01T00:00:00Z","body":"x"}]}}]}}}`
		}
		return `{"data":{"search":{"pageInfo":{"hasNextPage":false},"nodes":[{"number":22,"headRefOid":"a22"}]}}}`
	}
	prs, err := r.g.MergedRecords(t.Context(), `author:app/ogenki-agents merged:>=2026-05-01`)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 2 || prs[0].Number != 21 || prs[0].MergedBy.Login != "Smana" || prs[0].Comments[0].Author.Login != "ogenki-agent-factory[bot]" ||
		!prs[0].MergedAt.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || prs[0].Labels[0] != (evidence.Label{Name: "factory/merged:docs-links"}) ||
		prs[1].MergedBy != nil {
		t.Fatalf("%+v", prs)
	}
	if queries[0] != "repo:Smana/demo is:pr is:merged author:app/ogenki-agents merged:>=2026-05-01" {
		t.Fatalf("%q", queries)
	}
	if b := r.gh.graphqlBodies(); !strings.Contains(b[len(b)-1], "comments(last: 50)") {
		t.Fatalf("the query: %s", b[len(b)-1])
	}
}

// A short read is refused, never counted: a pull request with unread labels, or more pages than
// the bound.
func TestRecordsPastTheirBoundAreRefused(t *testing.T) {
	r := newRig(t)
	r.gh.records = func(string, any) string {
		return `{"data":{"search":{"pageInfo":{"hasNextPage":false},"nodes":[{"number":1,"labels":{"nodes":[],"pageInfo":{"hasNextPage":true}}}]}}}`
	}
	if _, err := r.g.MergedRecords(t.Context(), ""); !errors.Is(err, ErrRecordsTruncated) {
		t.Fatalf("unread labels: %v", err)
	}
	r.gh.records = func(string, any) string {
		return `{"data":{"search":{"pageInfo":{"hasNextPage":true,"endCursor":"c"},"nodes":[]}}}`
	}
	if _, err := r.g.RevertRecords(t.Context()); !errors.Is(err, ErrRecordsTruncated) {
		t.Fatalf("endless pages: %v", err)
	}
}

func TestRevertRecordsAndAnyMerged(t *testing.T) {
	r := newRig(t)
	r.gh.records = func(q string, _ any) string {
		if strings.Contains(q, "follow-up-of") {
			return `{"data":{"search":{"issueCount":1}}}`
		}
		return `{"data":{"search":{"pageInfo":{"hasNextPage":false},"nodes":[{"number":30,"state":"MERGED","body":"Reverts Smana/demo#21",` +
			`"labels":{"nodes":[{"name":"factory/drill"}],"pageInfo":{"hasNextPage":false}}}]}}}`
	}
	rs, err := r.g.RevertRecords(t.Context())
	if err != nil || len(rs) != 1 || rs[0].State != "MERGED" || rs[0].Body != "Reverts Smana/demo#21" || rs[0].Labels[0].Name != "factory/drill" {
		t.Fatalf("%+v %v", rs, err)
	}
	if ok, err := r.g.AnyMerged(t.Context(), `label:"factory/follow-up-of:#21"`); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
}
