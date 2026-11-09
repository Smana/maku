// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/repoaccess"
)

// Every external draft door refuses a live clarification marker. Composition deliberately remains
// intermediate: its ranked gaps and their markers are what the next clarification round resolves.
func TestFinalDraftDoors(t *testing.T) {
	markerBody := strings.Replace(runDraft, "\nx\n", "\n[NEEDS CLARIFICATION: g1 which check?]\n", 1)

	t.Run("issue_draft", func(t *testing.T) {
		e := setup(t, mcpOn, actorOn(&actLog{}))
		got := call(t, e, viaRoomctl("dev"), "issue_draft", map[string]any{
			"repo": "Smana/a", "title": "docs(x): y", "body": markerBody,
		})
		if !got.isError || !strings.Contains(got.text, "unresolved_marker") {
			t.Fatalf("issue_draft accepted a marker: %+v", got)
		}
	})

	t.Run("plan_check", func(t *testing.T) {
		e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}))
		plan := planFixture(t, "single")
		plan.Issues[0].Body = markerBody
		got := call(t, e, viaRoomctl("dev"), "plan_check", map[string]any{
			"plan": plan, "hash": intake.Hash(plan),
		})
		if !got.isError || !strings.Contains(got.text, "unresolved_marker") {
			t.Fatalf("plan_check accepted a marker: %+v", got)
		}
	})

	t.Run("run_create", func(t *testing.T) {
		f := &fakeTaskFactory{}
		e := setup(t, mcpOn, actorOn(&actLog{}), withSponsored(sponsoredP), withIntake(t, "slack"), withTaskFactory(f))
		plan := planFixture(t, "slack-one-issue")
		plan.Issues[0].Body = markerBody
		args := map[string]any{"repo": plan.Repository, "title": plan.Issues[0].Title, "text": markerBody,
			"dataClass": "public", "max_cost": 1.5, "on_behalf_of": "slack:T01ABC/U02DEF",
			"plan": plan, "plan_hash": intake.Hash(plan)}
		for name, callArgs := range map[string]map[string]any{
			"with a plan": args,
			"without a plan": {"repo": plan.Repository, "title": plan.Issues[0].Title, "text": markerBody,
				"dataClass": "public", "max_cost": 1.5, "on_behalf_of": "slack:T01ABC/U02DEF"},
		} {
			t.Run(name, func(t *testing.T) {
				got := call(t, e, bearer("slack-app"), "run_create", callArgs)
				if !got.isError || !strings.Contains(got.text, "unresolved_marker") {
					t.Fatalf("run_create accepted a marker: %+v", got)
				}
			})
		}
		if len(f.got) != 0 {
			t.Fatalf("a marker reached the factory: %+v", f.got)
		}
	})

	t.Run("compose remains intermediate", func(t *testing.T) {
		plan, err := intake.Compose(intake.Draft{Repository: "Smana/ai-native-ref",
			Issues: []intake.DraftIssue{{Title: "docs(x): y", Body: markerBody, Class: "docs-links"}}}, intake.ComposeIn{
			Surface: intake.SurfaceSession,
			Classes: map[string]intake.Class{"docs-links": {Roles: []string{"implementer"}, CeilingUSD: 1.5, Source: "class:docs-links"}},
		})
		if err != nil || !strings.Contains(plan.Issues[0].Body, "NEEDS CLARIFICATION") {
			t.Fatalf("Compose lost its intermediate marker: %+v, %v", plan, err)
		}
	})
}

func TestMCPIssueDraft(t *testing.T) {
	e := setup(t, mcpOn, actorOn(&actLog{}))
	body := "## What is wrong\ndocs/a.md:3 links\u200b a removed runbook\n\n## What it should be\nthe new one\n\n## Acceptance check\nhack/docs.sh passes\n"
	got := call(t, e, viaRoomctl("dev"), "issue_draft", map[string]any{"repo": "Smana/a", "title": "docs(agents): a dead link", "body": body})
	var d issueDraft
	if got.isError || json.Unmarshal(got.structured, &d) != nil || d.Stripped != 1 || strings.Contains(d.Body, "\u200b") || d.Title != "docs(agents): a dead link" {
		t.Fatalf("%+v", got)
	}
	for name, c := range map[string]struct {
		title, body, want string
	}{
		"a title off the template": {"fix the docs", body, "invalid_draft: the title"},
		"a flag for a title":       {"--label factory/ready: x", body, "invalid_draft: the title"},
		"a missing section":        {"docs: x", "## What is wrong\nx\n## Acceptance check\ny", "missing or out of order: ## What it should be, ## Acceptance check"},
		"a secret":                 {"docs: x", body + "token SECRET", "invalid_draft: it holds what looks like a secret (test)"},
	} {
		t.Run(name, func(t *testing.T) {
			got := call(t, e, viaRoomctl("dev"), "issue_draft", map[string]any{"repo": "Smana/a", "title": c.title, "body": c.body})
			if !got.isError || !strings.Contains(got.text, c.want) {
				t.Fatalf("%+v", got)
			}
		})
	}
}

// issue_draft says whether the caller may start the factory on the repository (R11): the
// repository is in human.mcp.sessionStart, and the caller writes to it on GitHub.
func TestIssueDraftCanStart(t *testing.T) {
	listed := func(s *Server, _ *fanout.Hub, _ *hubView) { s.SessionStart = map[string]bool{"smana/a": true} }
	tests := []struct {
		name    string
		w       *gitHubWorld
		opts    []option
		caller  http.Header
		repo    string
		want    bool
		wantWhy string
	}{
		{"listed, linked, write", &gitHubWorld{writes: map[string][]string{"Smana/a": {"dev"}}}, []option{listed}, viaRoomctl("dev"), "Smana/a", true, ""},
		{"not listed", &gitHubWorld{writes: map[string][]string{"Smana/b": {"dev"}}}, []option{listed}, viaRoomctl("dev"), "Smana/b", false, "off"},
		{"listed, Identity nil", &gitHubWorld{}, []option{listed, func(s *Server, _ *fanout.Hub, _ *hubView) { s.Identity = nil }}, viaRoomctl("dev"), "Smana/a", false, "off"},
		{"listed, unlinked", &gitHubWorld{unlinked: map[string]bool{"dev": true}}, []option{listed}, viaRoomctl("dev"), "Smana/a", false, "not_linked"},
		{"listed, read only", &gitHubWorld{}, []option{listed}, viaRoomctl("dev"), "Smana/a", false, "no_write"},
		{"listed, the App cannot see it", &gitHubWorld{unseen: map[string]bool{"Smana/a": true}}, []option{listed}, viaRoomctl("dev"), "Smana/a", false, "no_write"},
		{"listed, the lookup fails", &gitHubWorld{down: true}, []option{listed}, viaRoomctl("dev"), "Smana/a", false, "unverified"},
		{"nil Access", &gitHubWorld{}, []option{listed, func(s *Server, _ *fanout.Hub, _ *hubView) { s.Access = nil }}, viaRoomctl("dev"), "Smana/a", false, "off"},
		{"empty SessionStart", &gitHubWorld{writes: map[string][]string{"Smana/a": {"dev"}}}, []option{func(s *Server, _ *fanout.Hub, _ *hubView) { s.SessionStart = map[string]bool{} }}, viaRoomctl("dev"), "Smana/a", false, "off"},
		{"nil SessionStart", &gitHubWorld{writes: map[string][]string{"Smana/a": {"dev"}}}, nil, viaRoomctl("dev"), "Smana/a", false, "off"},
		{"the write check itself says no repository", &gitHubWorld{}, []option{listed, func(s *Server, _ *fanout.Hub, _ *hubView) {
			s.Access.Perm = func(context.Context, string, string, string) (string, error) {
				return "", fmt.Errorf("installation: %w", repoaccess.ErrNoRepository)
			}
		}}, viaRoomctl("dev"), "Smana/a", false, "no_write"},
		{"the list is matched case-insensitively", &gitHubWorld{writes: map[string][]string{"Smana/a": {"dev"}}}, []option{func(s *Server, _ *fanout.Hub, _ *hubView) { s.SessionStart = map[string]bool{"smana/a": true} }}, viaRoomctl("dev"), "Smana/a", true, ""},
		{"a sponsored caller", &gitHubWorld{writes: map[string][]string{"Smana/a": {"slack-app"}}}, []option{listed, withSponsored(sponsoredP)}, bearer("slack-app"), "Smana/a", false, "off"},
	}
	body := "## What is wrong\nx\n\n## What it should be\ny\n\n## Acceptance check\nz\n"
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.w.now = time.Now()
			if tc.w.unlinked == nil {
				tc.w.unlinked = map[string]bool{}
			}
			if tc.w.inactive == nil {
				tc.w.inactive = map[string]bool{}
			}
			e := setup(t, append([]option{mcpOn, actorOn(&actLog{}), tc.w.option()}, tc.opts...)...)
			got := call(t, e, tc.caller, "issue_draft", map[string]any{"repo": tc.repo, "title": "docs(agents): a dead link", "body": body})
			var d issueDraft
			if got.isError || json.Unmarshal(got.structured, &d) != nil || d.CanStart != tc.want || d.CanStartWhy != tc.wantWhy {
				t.Fatalf("%+v: %+v", got, d)
			}
		})
	}
}

// A lookup that never answers is cut at accessWait, as the gate's is: the draft comes back
// unverified, not after mcpTimeout.
func TestIssueDraftCanStartLookupIsBounded(t *testing.T) {
	old := accessWait
	accessWait = 200 * time.Millisecond
	t.Cleanup(func() { accessWait = old })
	w := &gitHubWorld{now: time.Now(), unlinked: map[string]bool{}, inactive: map[string]bool{}}
	hung := func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.SessionStart = map[string]bool{"smana/a": true}
		s.Access.Perm = func(ctx context.Context, _, _, _ string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}
	}
	e := setup(t, mcpOn, actorOn(&actLog{}), w.option(), hung)
	body := "## What is wrong\nx\n\n## What it should be\ny\n\n## Acceptance check\nz\n"
	start := time.Now()
	got := call(t, e, viaRoomctl("dev"), "issue_draft", map[string]any{"repo": "Smana/a", "title": "docs(agents): a dead link", "body": body})
	var d issueDraft
	if got.isError || json.Unmarshal(got.structured, &d) != nil || d.CanStart || d.CanStartWhy != "unverified" {
		t.Fatalf("%+v: %+v", got, d)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the draft took %v with a hung lookup", took)
	}
}
