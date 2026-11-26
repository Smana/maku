// SPDX-License-Identifier: Apache-2.0

package promote

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/meaning"
)

type fakeGit struct {
	raw, blob     string
	updated       []byte
	updateBlob    string
	open          int
	pr            forge.PR
	createBase    string
	createHead    string
	createTitle   string
	createBody    string
	draft         bool
	labels        []string
	revertOf      forge.RevertOf
	fileCallCount int
}

func (f *fakeGit) BranchFile(_ context.Context, _, _ string) (string, string, bool, error) {
	f.fileCallCount++
	return f.raw, f.blob, true, nil
}
func (f *fakeGit) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (f *fakeGit) UpdateFile(_ context.Context, _, _, _ string, content []byte, blob string) error {
	f.updated, f.updateBlob = content, blob
	return nil
}
func (f *fakeGit) CreatePR(_ context.Context, base, head, title, body string, draft bool) (forge.PR, error) {
	f.createBase, f.createHead, f.createTitle, f.createBody, f.draft = base, head, title, body, draft
	return forge.PR{Number: 91, URL: "https://github.com/Smana/ai-native-ref/pull/91"}, nil
}
func (f *fakeGit) PullRequestForBranch(_ context.Context, _ string) (int, error) { return f.open, nil }
func (f *fakeGit) PullRequest(_ context.Context, _ int) (forge.PR, error) {
	if f.pr.URL == "" {
		f.pr.URL = "https://github.com/Smana/ai-native-ref/pull/91"
	}
	return f.pr, nil
}
func (f *fakeGit) RevertPR(_ context.Context, of forge.RevertOf) (forge.Revert, error) {
	f.revertOf = of
	return forge.Revert{Number: 92, URL: "https://github.com/Smana/ai-native-ref/pull/92"}, nil
}
func (f *fakeGit) AddLabels(_ context.Context, _ int, labels ...string) error {
	f.labels = append(f.labels, labels...)
	return nil
}

type reportFunc func(context.Context, string) (evidence.Report, error)

func (f reportFunc) Report(ctx context.Context, repository string) (evidence.Report, error) {
	return f(ctx, repository)
}

const manifest = `apiVersion: maku.ogenki.io/v1alpha1
kind: Repository
metadata:
  name: ai-native-ref
spec:
  repository: Smana/ai-native-ref
  mergePolicies:
    - name: docs
      mode: shadow
`

func promotionRig(mode string) (*Service, *fakeGit) {
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{GitOps: json.RawMessage(`{"repository":"Smana/ai-native-ref","path":"factory/repository-ai-native-ref.yaml"}`),
		Repositories: []config.Repository{{Name: "Smana/ai-native-ref", Merge: config.Merge{MergerLogin: "ogenki-agent-merger[bot]"},
			Classes: map[string]config.Class{"docs": {Shadow: true, AllowedFiles: []string{`^.+\.md$`}, MaxPatchFiles: 5}}}}}
	git := &fakeGit{raw: manifest, blob: "blob1"}
	return &Service{Cfg: config.NewLive(cfg), Git: func(string) (Git, bool) { return git, true },
		Merger: func(string) (Reverter, bool) { return git, true }, Reports: reportFunc(func(context.Context, string) (evidence.Report, error) {
			return evidence.Report{Repository: "Smana/ai-native-ref", EvidenceAt: &at, Classes: []evidence.ClassReport{{
				Class: "docs", Mode: mode, Evidence: &evidence.Evidence{Clean: 20, Minimum: 20, Drill: evidence.DrillPassed,
					Shadow: evidence.ShadowTally{Judged: []int{42, 43}}},
				Proposal: &evidence.Proposal{Path: "factory/repository-ai-native-ref.yaml", Line: 8, File: manifest},
			}}}, nil
		})}, git
}

func TestOpenPromotesOneLineWithItsEvidence(t *testing.T) {
	s, git := promotionRig("shadow")
	got, err := s.Open(t.Context(), "Smana/ai-native-ref", "docs", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.PullRequest != 91 || got.Branch != "factory/promote-Smana-ai-native-ref-docs" || git.updateBlob != "blob1" {
		t.Fatalf("%+v %+v", got, git)
	}
	if strings.Contains(string(git.updated), "repository: Smana/ai-native-ref\n  mergePolicies:\n    - name: docs\n      mode: shadow") ||
		!strings.Contains(string(git.updated), "name: docs\n      mode: live") || git.createBase != "main" || git.draft {
		t.Fatalf("unexpected manifest or PR: %s %+v", git.updated, git)
	}
	// Correctness signals CS7: the body says what the policy will merge and what checks it, and
	// names the judge of every count.
	// W1: the sentence is meaning.Build's, for the binding the pull request turns live.
	in, _ := meaning.For(s.Cfg.Current().RepositoryFor("Smana/ai-native-ref"), "docs")
	in.Mode = config.ModeLive
	for _, want := range []string{"Once merged: " + meaning.Build(in).Text, "**Meaning checked by** nothing: this is prose", "20 agreed decisions (minimum 20)",
		"0 wrong calls", "rollback drill done", "Agreed decisions:", "/pull/42", "/pull/43", "never merges it"} {
		if !strings.Contains(git.createBody, want) {
			t.Errorf("body lacks %q:\n%s", want, git.createBody)
		}
	}
	for _, old := range []string{"clean", "bad outcome"} {
		if strings.Contains(git.createBody, old) {
			t.Errorf("body says %q:\n%s", old, git.createBody)
		}
	}
}

func TestOpenReturnsAnExistingPromotionWithoutUpdating(t *testing.T) {
	s, git := promotionRig("shadow")
	git.open = 91
	got, err := s.Open(t.Context(), "Smana/ai-native-ref", "docs", 0)
	if err != nil || got.PullRequest != 91 || git.updated != nil || git.createTitle != "" {
		t.Fatalf("%+v %+v %v", got, git, err)
	}
}

func TestOpenWithoutGitOpsPrintsTheCommand(t *testing.T) {
	s, _ := promotionRig("shadow")
	s.Cfg = config.NewLive(&config.Config{})
	got, err := s.Open(t.Context(), "Smana/ai-native-ref", "docs", 0)
	if err != nil || !strings.Contains(got.Command, "gh pr create") || !strings.Contains(got.Command, "20 agreed decisions") {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestStatusReadsThePRAndServedMode(t *testing.T) {
	s, git := promotionRig("live")
	s.Reports = reportFunc(func(context.Context, string) (evidence.Report, error) {
		return evidence.Report{Repository: "Smana/ai-native-ref", Classes: []evidence.ClassReport{{Class: "docs", Mode: "live"}}}, nil
	})
	git.pr.State = "CLOSED"
	got, err := s.Status(t.Context(), "Smana/ai-native-ref", "docs", 91)
	if err != nil || got.PullRequestState != "CLOSED" || got.ServedMode != "live" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestDrillOpensAndLabelsARevert(t *testing.T) {
	s, git := promotionRig("live")
	git.pr = forge.PR{Number: 42, NodeID: "PR_42", State: "MERGED", Title: "docs: change"}
	got, err := s.Drill(t.Context(), "Smana/ai-native-ref", 42)
	if err != nil || got.Number != 92 || git.revertOf.Number != 42 || len(git.labels) != 1 || git.labels[0] != "factory/drill" {
		t.Fatalf("%+v %+v %+v", got, git, err)
	}
}

// factoryApp is the factory App's connection when the merger is a separate App: it reads pull
// requests, and GitHub refuses it a revert, which needs contents: write.
type factoryApp struct{ fakeGit }

func (*factoryApp) RevertPR(context.Context, forge.RevertOf) (forge.Revert, error) {
	return forge.Revert{}, errors.New("open a revert: Resource not accessible by integration")
}

// The gcp-0 drill: a pull request a maintainer merged, on a repository without merge.autoRevert,
// with a separate merger App. The factory App reads it; the merger opens and labels the revert. The
// drill once opened the revert with the factory App, which GitHub refuses, and answered a bare 503.
func TestDrillOfAMaintainersMergeRevertsThroughTheMerger(t *testing.T) {
	s, _ := promotionRig("shadow")
	app := &factoryApp{fakeGit{pr: forge.PR{Number: 505, NodeID: "PR_505", State: "MERGED", Title: "docs: link", MergedBy: "Smana"}}}
	merger := &fakeGit{}
	s.Git = func(string) (Git, bool) { return app, true }
	s.Merger = func(string) (Reverter, bool) { return merger, true }
	got, err := s.Drill(t.Context(), "Smana/ai-native-ref", 505)
	if err != nil || got.Number != 92 || merger.revertOf.Number != 505 || merger.revertOf.NodeID != "PR_505" ||
		!slices.Equal(merger.labels, []string{"factory/drill"}) || merger.revertOf.By != "ogenki-agent-merger[bot]" {
		t.Fatalf("%+v %v merger=%+v", got, err, merger)
	}
}

func TestDrillRefusalsAreNamed(t *testing.T) {
	s, git := promotionRig("shadow")
	git.pr = forge.PR{Number: 7, State: "OPEN"}
	if _, err := s.Drill(t.Context(), "Smana/ai-native-ref", 7); !errors.Is(err, ErrNotMerged) {
		t.Fatalf("open PR: %v", err)
	}
	if _, err := s.Drill(t.Context(), "Smana/other", 7); !errors.Is(err, ErrUnknownRepository) {
		t.Fatalf("unknown repository: %v", err)
	}
	s.Merger = func(string) (Reverter, bool) { return nil, false }
	if _, err := s.Drill(t.Context(), "Smana/ai-native-ref", 7); !errors.Is(err, ErrNoMerger) {
		t.Fatalf("no merger: %v", err)
	}
}

func TestOpenRefusesBelowTheThreshold(t *testing.T) {
	for _, minimum := range []int{21, -1} {
		s, git := promotionRig("shadow")
		if _, err := s.Open(t.Context(), "Smana/ai-native-ref", "docs", minimum); err == nil || git.updated != nil || git.createTitle != "" {
			t.Fatalf("minimum %d bypassed eligibility: %v %+v", minimum, err, git)
		}
	}
}
