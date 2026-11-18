// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/repoaccess"
)

// Every external draft door refuses a live clarification marker. Composition deliberately remains
// intermediate: its ranked gaps and their markers are what the next clarification round resolves.
func TestFinalDraftDoors(t *testing.T) {
	markerBody := strings.Replace(runDraft, "\nx\n", "\n[NEEDS CLARIFICATION: g1 which check?]\n", 1)

	t.Run("checkFinalDraft", func(t *testing.T) {
		e := setup(t, mcpOn, actorOn(&actLog{}))
		if _, err := e.srv.checkFinalDraft(t.Context(), "docs(x): y", markerBody); err == nil || !strings.Contains(err.Error(), "unresolved_marker") {
			t.Fatalf("checkFinalDraft accepted a marker: %v", err)
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

func TestCheckFinalDraft(t *testing.T) {
	e := setup(t, mcpOn, actorOn(&actLog{}))
	body := "## What is wrong\ndocs/a.md:3 links\u200b a removed runbook\n\n## What it should be\nthe new one\n\n## Acceptance check\nhack/docs.sh passes\n"
	d, err := e.srv.checkFinalDraft(t.Context(), "docs(agents): a dead link", body)
	if err != nil || d.Stripped != 1 || strings.Contains(d.Body, "\u200b") || d.Title != "docs(agents): a dead link" {
		t.Fatalf("%+v, %v", d, err)
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
			if _, err := e.srv.checkFinalDraft(t.Context(), c.title, c.body); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

// canStart says whether the caller may start the factory on the repository (UX3):
// human.mcp.start offers it for the repository, and the caller writes to it on GitHub.
func TestCanStart(t *testing.T) {
	offered := func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.StartDefault = true
		s.StartExcept = map[string]bool{"smana/b": true}
	}
	human := authn.Principal{ID: "dev", Sub: "dev"}
	sponsored := authn.Principal{ID: "slack-app", Sub: "slack-app", Sponsored: true}
	tests := []struct {
		name    string
		w       *gitHubWorld
		opts    []option
		caller  authn.Principal
		repo    string
		want    bool
		wantWhy string
	}{
		{"offered, linked, write", &gitHubWorld{writes: map[string][]string{"Smana/a": {"dev"}}}, []option{offered}, human, "Smana/a", true, ""},
		{"excepted", &gitHubWorld{writes: map[string][]string{"Smana/b": {"dev"}}}, []option{offered}, human, "Smana/b", false, startNotOffered},
		{"offered, Identity nil", &gitHubWorld{}, []option{offered, func(s *Server, _ *fanout.Hub, _ *hubView) { s.Identity = nil }}, human, "Smana/a", false, startAccessUnwired},
		{"offered, unlinked", &gitHubWorld{unlinked: map[string]bool{"dev": true}}, []option{offered}, human, "Smana/a", false, "not_linked"},
		{"offered, read only", &gitHubWorld{}, []option{offered}, human, "Smana/a", false, "no_write"},
		{"offered, the App cannot see it", &gitHubWorld{unseen: map[string]bool{"Smana/a": true}}, []option{offered}, human, "Smana/a", false, "no_write"},
		{"offered, the lookup fails", &gitHubWorld{down: true}, []option{offered}, human, "Smana/a", false, "unverified"},
		{"nil Access", &gitHubWorld{}, []option{offered, func(s *Server, _ *fanout.Hub, _ *hubView) { s.Access = nil }}, human, "Smana/a", false, startAccessUnwired},
		{"StartDefault false", &gitHubWorld{writes: map[string][]string{"Smana/a": {"dev"}}}, nil, human, "Smana/a", false, startNotOffered},
		{"the write check itself says no repository", &gitHubWorld{}, []option{offered, func(s *Server, _ *fanout.Hub, _ *hubView) {
			s.Access.Perm = func(context.Context, string, string, string) (string, error) {
				return "", fmt.Errorf("installation: %w", repoaccess.ErrNoRepository)
			}
		}}, human, "Smana/a", false, "no_write"},
		{"except list is matched case-insensitively", &gitHubWorld{writes: map[string][]string{"Smana/b": {"dev"}}}, []option{offered}, human, "SMANA/B", false, startNotOffered},
		{"a sponsored caller", &gitHubWorld{writes: map[string][]string{"Smana/a": {"slack-app"}}}, []option{offered}, sponsored, "Smana/a", false, startSponsored},
	}
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
			got, why := e.srv.canStart(t.Context(), tc.caller, tc.repo)
			if got != tc.want || why != tc.wantWhy {
				t.Fatalf("canStart(%q) = (%v, %q), want (%v, %q)", tc.repo, got, why, tc.want, tc.wantWhy)
			}
		})
	}
}

// A lookup that never answers is cut at accessWait, as the gate's is: canStart comes back
// unverified, not after mcpTimeout.
func TestCanStartLookupIsBounded(t *testing.T) {
	old := accessWait
	accessWait = 200 * time.Millisecond
	t.Cleanup(func() { accessWait = old })
	w := &gitHubWorld{now: time.Now(), unlinked: map[string]bool{}, inactive: map[string]bool{}}
	hung := func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.StartDefault = true
		s.Access.Perm = func(ctx context.Context, _, _, _ string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}
	}
	e := setup(t, mcpOn, actorOn(&actLog{}), w.option(), hung)
	start := time.Now()
	got, why := e.srv.canStart(t.Context(), authn.Principal{ID: "dev", Sub: "dev"}, "Smana/a")
	if got || why != "unverified" {
		t.Fatalf("got (%v, %q), want (false, unverified)", got, why)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("canStart took %v with a hung lookup", took)
	}
}

func TestStartDefaultsOnWithPlanner(t *testing.T) {
	w := &gitHubWorld{
		now:      time.Now(),
		writes:   map[string][]string{"Smana/ai-native-ref": {"dev"}, "Smana/other": {"dev"}},
		unlinked: map[string]bool{},
		inactive: map[string]bool{},
	}
	e := setup(t, mcpOn, actorOn(&actLog{}), withIntake(t, "session"), w.option(), func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.StartDefault = true
	})
	dev := authn.Principal{ID: "dev", Sub: "dev"}

	if ok, why := e.srv.canStart(t.Context(), dev, "Smana/ai-native-ref"); !ok || why != "" {
		t.Fatalf("planner-served repo: got canStart=%v why=%q, want true", ok, why)
	}
	if why := e.srv.startOffered(t.Context(), dev, "Smana/ai-native-ref"); why != "" {
		t.Fatalf("startOffered(Smana/ai-native-ref) = %q, want offered", why)
	}

	if ok, why := e.srv.canStart(t.Context(), dev, "Smana/other"); ok || why != startNotOffered {
		t.Fatalf("unserved repo: got canStart=%v why=%q, want false/%s", ok, why, startNotOffered)
	}
}

func TestStartExcept(t *testing.T) {
	w := &gitHubWorld{
		now:      time.Now(),
		writes:   map[string][]string{"Smana/ai-native-ref": {"dev"}},
		unlinked: map[string]bool{},
		inactive: map[string]bool{},
	}
	e := setup(t, mcpOn, actorOn(&actLog{}), withIntake(t, "session"), w.option(), func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.StartDefault = true
		s.StartExcept = map[string]bool{"smana/ai-native-ref": true}
	})
	if ok, why := e.srv.canStart(t.Context(), authn.Principal{ID: "dev", Sub: "dev"}, "Smana/ai-native-ref"); ok || why != startNotOffered {
		t.Fatalf("excepted repo: got canStart=%v why=%q, want false/repository_not_offered", ok, why)
	}
}

func TestStartNeedsLinkAndWrite(t *testing.T) {
	w := &gitHubWorld{
		now:      time.Now(),
		writes:   map[string][]string{"Smana/ai-native-ref": {"dev"}},
		unlinked: map[string]bool{"unlinked-user": true},
		inactive: map[string]bool{},
	}
	e := setup(t, mcpOn, actorOn(&actLog{}), withIntake(t, "session"), w.option(), func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.StartDefault = true
	})

	if ok, why := e.srv.canStart(t.Context(), authn.Principal{ID: "unlinked-user", Sub: "unlinked-user"}, "Smana/ai-native-ref"); ok || why != "not_linked" {
		t.Fatalf("unlinked caller: got canStart=%v why=%q, want false/not_linked", ok, why)
	}

	if ok, why := e.srv.canStart(t.Context(), authn.Principal{ID: "reader", Sub: "reader"}, "Smana/ai-native-ref"); ok || why != "no_write" {
		t.Fatalf("read-only caller: got canStart=%v why=%q, want false/no_write", ok, why)
	}

	if ok, why := e.srv.canStart(t.Context(), authn.Principal{ID: "dev", Sub: "dev"}, "Smana/ai-native-ref"); !ok || why != "" {
		t.Fatalf("linked writer: got canStart=%v why=%q, want true", ok, why)
	}
}
