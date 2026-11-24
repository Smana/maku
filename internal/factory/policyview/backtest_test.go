// SPDX-License-Identifier: Apache-2.0

package policyview

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/decision"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/forge"
)

type fakeBacktestSource struct{ lines map[int]int }

func (f fakeBacktestSource) PullRequest(_ context.Context, number int) (forge.PR, error) {
	return forge.PR{Number: number, Author: "ogenki-agents[bot]", BaseRef: "main", HeadRef: "agent/recorded",
		HeadSHA: fmt.Sprintf("%040x", number)}, nil
}

func (f fakeBacktestSource) PullRequestChecks(_ context.Context, _ int) (forge.Checks, error) {
	return forge.Checks{Runs: []forge.Check{{Name: "lint", State: "SUCCESS", Conclusion: "SUCCESS", App: forge.ActionsApp}}}, nil
}

func (f fakeBacktestSource) Diff(_ context.Context, _, head string) (forge.Diff, error) {
	number, _ := strconv.ParseInt(head[len(head)-1:], 16, 0)
	n := f.lines[int(number)]
	return forge.Diff{Base: map[string]string{"docs/a.md": "old\n"}, Head: map[string]string{"docs/a.md": "new\n"},
		Files: []forge.FileStat{{Path: "docs/a.md", Status: "modified", Additions: n}}}, nil
}

func replayFixture(t *testing.T) (*config.Config, Recent) {
	t.Helper()
	cfg := boundRig(t)
	cfg.FactoryLogin = "ogenki-agent-factory[bot]"
	cfg.AgentsLogin = "ogenki-agents[bot]"
	cfg.Templates = map[string]config.Template{"solo": {Roles: []string{"reviewer"}}}
	for i := range cfg.Repositories {
		cfg.Repositories[i].Merge.MaxPerDay = 5
	}
	var prs []evidence.PR
	for _, tc := range []struct {
		number, lines int
		merge         bool
	}{{1, 12, true}, {2, 5, true}, {3, 5, false}} {
		head := fmt.Sprintf("%040x", tc.number)
		marker := decision.Marker(head, "docs", "shadow", "shadow_would_arm", decision.Rows{Counted: true, Files: 1, MaxFiles: 5,
			Lines: tc.lines, MaxLines: 20, Shape: "ok", Holdout: "ok", Reviewer: "glm-5.3"})
		pr := evidence.PR{Number: tc.number, HeadRefOid: head, BaseRefName: "main", Comments: []evidence.Comment{{
			Author: evidence.Actor{Login: cfg.FactoryLogin}, CreatedAt: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC), Body: marker}}}
		if tc.merge {
			pr.MergeCommitOid = fmt.Sprintf("%040x", tc.number+100)
		}
		prs = append(prs, pr)
	}
	return cfg, ReadRecent("Smana/cloud-native-ref", "2026-11-01", cfg.FactoryLogin, prs)
}

func TestBacktestChangesOnlyRealDecisionsAndMarksUnknown(t *testing.T) {
	cfg, recent := replayFixture(t)
	spec := cfg.Policies["docs"].Spec
	spec.MaxModifiedLines = 10
	source := fakeBacktestSource{lines: map[int]int{1: 12, 2: 5, 3: 5}}
	got := Replay(t.Context(), cfg, &cfg.Repositories[0], evidence.Report{}, BacktestRequest{Kind: "policy", Policy: "docs", Spec: &spec}, recent, source)
	if !got.Valid || got.Counts != (BacktestCounts{Considered: 3, Changed: 1, Unchanged: 1, Unknown: 1, Matched: 1}) {
		t.Fatalf("%+v", got)
	}
	if len(got.Rows) != 2 || got.Rows[0].PullRequest != 1 || got.Rows[0].Before.Verdict != "shadow" ||
		got.Rows[0].After.Verdict != "human" || got.Rows[0].After.Reason != "patch_too_large" || got.Rows[0].URL == "" {
		t.Fatalf("%+v", got.Rows)
	}
	if got.Rows[1].Change != "unknown" || got.Rows[1].After.Verdict != "unknown" {
		t.Fatalf("an unrecorded diff became unchanged: %+v", got.Rows[1])
	}
}

func TestBacktestWarnsWhenTheEditedRuleMatchesNone(t *testing.T) {
	cfg, recent := replayFixture(t)
	spec := cfg.Policies["docs"].Spec
	spec.MaxModifiedLines = 1
	got := Replay(t.Context(), cfg, &cfg.Repositories[0], evidence.Report{}, BacktestRequest{Kind: "policy", Policy: "docs", Spec: &spec}, recent,
		fakeBacktestSource{lines: map[int]int{1: 12, 2: 5, 3: 5}})
	if got.Counts.Matched != 0 || got.Counts.Changed != 2 || got.Counts.Unchanged != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestBacktestReplaysTheProposedMode(t *testing.T) {
	cfg, recent := replayFixture(t)
	bindings := []config.Binding{{Name: "docs", Mode: "live"}}
	rep := evidence.Report{RepositoryObject: "cloud-native-ref", RepositoryManifest: "apiVersion: maku.ogenki.io/v1alpha1\nkind: Repository\nspec: {}\n"}
	got := Replay(t.Context(), cfg, &cfg.Repositories[0], rep, BacktestRequest{Kind: "binding", Bindings: bindings}, recent,
		fakeBacktestSource{lines: map[int]int{1: 12, 2: 5, 3: 5}})
	if got.Counts.Changed != 2 || got.Rows[0].After.Verdict != "arm" {
		t.Fatalf("%+v", got)
	}
}
