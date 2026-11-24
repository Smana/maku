// SPDX-License-Identifier: Apache-2.0

package promote

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/forge"
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
		Repositories: []config.Repository{{Name: "Smana/ai-native-ref", Merge: config.Merge{MergerLogin: "ogenki-agent-merger[bot]"}}}}
	git := &fakeGit{raw: manifest, blob: "blob1"}
	return &Service{Cfg: config.NewLive(cfg), Git: func(string) (Git, bool) { return git, true }, Reports: reportFunc(func(context.Context, string) (evidence.Report, error) {
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
	for _, want := range []string{"20 clean decisions (minimum 20)", "0 bad outcomes", "rollback drill passed", "/pull/42", "/pull/43", "never merges it"} {
		if !strings.Contains(git.createBody, want) {
			t.Errorf("body lacks %q:\n%s", want, git.createBody)
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
	if err != nil || !strings.Contains(got.Command, "gh pr create") || !strings.Contains(got.Command, "20 clean decisions") {
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

func TestOpenRefusesBelowTheThreshold(t *testing.T) {
	for _, minimum := range []int{21, -1} {
		s, git := promotionRig("shadow")
		if _, err := s.Open(t.Context(), "Smana/ai-native-ref", "docs", minimum); err == nil || git.updated != nil || git.createTitle != "" {
			t.Fatalf("minimum %d bypassed eligibility: %v %+v", minimum, err, git)
		}
	}
}
