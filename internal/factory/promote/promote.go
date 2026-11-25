// SPDX-License-Identifier: Apache-2.0

// Package promote opens policy-promotion pull requests and rollback drills. It never merges one.
package promote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/forge"
)

// Git is the repository connection needed for a promotion, status check, or drill.
type Git interface {
	BranchFile(ctx context.Context, path, ref string) (string, string, bool, error)
	CreateBranch(ctx context.Context, branch, base string) error
	UpdateFile(ctx context.Context, path, branch, message string, content []byte, blob string) error
	CreatePR(ctx context.Context, base, head, title, body string, draft bool) (forge.PR, error)
	PullRequestForBranch(ctx context.Context, branch string) (int, error)
	PullRequest(ctx context.Context, number int) (forge.PR, error)
	RevertPR(ctx context.Context, of forge.RevertOf) (forge.Revert, error)
	AddLabels(ctx context.Context, number int, labels ...string) error
}

// Reports is the repository's current evidence report.
type Reports interface {
	Report(ctx context.Context, repository string) (evidence.Report, error)
}

// Service opens promotions from the served config and evidence.
type Service struct {
	Cfg     config.Source
	Reports Reports
	Git     func(repository string) (Git, bool)
}

// Result is a promotion PR, or the command to open it when gitops is not configured.
type Result struct {
	Proposal       evidence.Proposal `json:"proposal"`
	Command        string            `json:"command,omitempty"`
	PullRequest    int               `json:"pullRequest,omitempty"`
	PullRequestURL string            `json:"pullRequestURL,omitempty"`
	Branch         string            `json:"branch,omitempty"`
}

// Status is the PR and served-mode facts that close a promotion record.
type Status struct {
	PullRequestState string `json:"pullRequestState"`
	ServedMode       string `json:"servedMode"`
}

func (s *Service) currentClass(ctx context.Context, repository, class string) (*config.Config, evidence.ClassReport, evidence.Report, error) {
	cfg := s.Cfg.Current()
	rep, err := s.Reports.Report(ctx, repository)
	if err != nil {
		return nil, evidence.ClassReport{}, evidence.Report{}, err
	}
	for _, c := range rep.Classes {
		if c.Class == class {
			return cfg, c, rep, nil
		}
	}
	return nil, evidence.ClassReport{}, evidence.Report{}, errors.New("no such policy")
}

func (s *Service) class(ctx context.Context, repository, class string) (*config.Config, evidence.ClassReport, evidence.Report, error) {
	cfg, c, rep, err := s.currentClass(ctx, repository, class)
	if err != nil {
		return nil, evidence.ClassReport{}, evidence.Report{}, err
	}
	if c.Proposal == nil || c.Evidence == nil || rep.EvidenceAt == nil {
		return nil, evidence.ClassReport{}, evidence.Report{}, errors.New("promotion evidence is pending")
	}
	return cfg, c, rep, nil
}

// Open creates the one-line promotion PR, idempotently returning an existing one for its branch.
func (s *Service) Open(ctx context.Context, repository, class string, minimum int) (Result, error) {
	cfg, c, rep, err := s.class(ctx, repository, class)
	if err != nil {
		return Result{}, err
	}
	if minimum < 0 {
		return Result{}, errors.New("promotion minimum must not be negative")
	}
	if minimum == 0 {
		minimum = c.Evidence.Minimum
	}
	if minimum == 0 {
		minimum = rep.Minimum
	}
	if c.Evidence.Clean < minimum || c.Evidence.Bad > 0 || c.Evidence.Drill != evidence.DrillPassed {
		return Result{}, errors.New("promotion is not eligible")
	}
	branch := branchName(repository, class)
	if len(cfg.GitOps) == 0 {
		body := bodyFor(repository, class, c, rep)
		return Result{Proposal: *c.Proposal, Command: fmt.Sprintf("gh pr create --repo %s --base main --head %s --title %q --body %q",
			repository, branch, titleFor(repository, class), body)}, nil
	}
	var target factoryv1.GitOps
	if err := json.Unmarshal(cfg.GitOps, &target); err != nil {
		return Result{}, fmt.Errorf("factory gitops: %w", err)
	}
	if s.Git == nil {
		return Result{}, errors.New("the GitOps repository has no factory connection")
	}
	git, ok := s.Git(target.Repository)
	if !ok {
		return Result{}, errors.New("the GitOps repository has no factory connection")
	}
	base := target.Branch
	if base == "" {
		base = "main"
	}
	raw, blob, found, err := git.BranchFile(ctx, target.Path, base)
	if err != nil {
		return Result{}, fmt.Errorf("read GitOps manifest: %w", err)
	}
	if !found {
		return Result{}, errors.New("the GitOps manifest does not exist")
	}
	updated, line, err := proposalFor(raw, repository, class)
	if err != nil {
		return Result{}, err
	}
	result := Result{Proposal: evidence.Proposal{Path: target.Path, File: string(updated), Line: line}, Branch: branch}
	if n, err := git.PullRequestForBranch(ctx, branch); err != nil {
		return Result{}, err
	} else if n > 0 {
		pr, err := git.PullRequest(ctx, n)
		result.PullRequest, result.PullRequestURL = n, pr.URL
		return result, err
	}
	if err := git.CreateBranch(ctx, branch, base); err != nil && !errors.Is(err, forge.ErrBranchExists) {
		return Result{}, err
	}
	message := titleFor(repository, class)
	if err := git.UpdateFile(ctx, target.Path, branch, message, updated, blob); err != nil {
		return Result{}, err
	}
	pr, err := git.CreatePR(ctx, base, branch, message, bodyFor(repository, class, c, rep), false)
	if err != nil {
		return Result{}, err
	}
	result.PullRequest, result.PullRequestURL = pr.Number, pr.URL
	return result, nil
}

// Status reads the facts that close a promotion record without trusting a button.
func (s *Service) Status(ctx context.Context, repository, class string, number int) (Status, error) {
	cfg, c, _, err := s.currentClass(ctx, repository, class)
	if err != nil {
		return Status{}, err
	}
	var target factoryv1.GitOps
	if len(cfg.GitOps) == 0 || json.Unmarshal(cfg.GitOps, &target) != nil {
		return Status{}, errors.New("gitops is not configured")
	}
	if s.Git == nil {
		return Status{}, errors.New("the GitOps repository has no factory connection")
	}
	git, ok := s.Git(target.Repository)
	if !ok {
		return Status{}, errors.New("the GitOps repository has no factory connection")
	}
	pr, err := git.PullRequest(ctx, number)
	if err != nil {
		return Status{}, err
	}
	return Status{PullRequestState: pr.State, ServedMode: c.Mode}, nil
}

// Drill opens a labelled revert of a merged pull request, leaving its merge to a maintainer.
func (s *Service) Drill(ctx context.Context, repository string, number int) (forge.Revert, error) {
	cfg := s.Cfg.Current()
	rc := cfg.RepositoryFor(repository)
	if rc == nil {
		return forge.Revert{}, errors.New("unknown repository")
	}
	if s.Git == nil {
		return forge.Revert{}, errors.New("the repository has no factory connection")
	}
	git, ok := s.Git(rc.Name)
	if !ok {
		return forge.Revert{}, errors.New("the repository has no factory connection")
	}
	pr, err := git.PullRequest(ctx, number)
	if err != nil || pr.State != "MERGED" {
		return forge.Revert{}, errors.New("a rollback drill needs a merged pull request")
	}
	revert, err := git.RevertPR(ctx, forge.RevertOf{Number: pr.Number, NodeID: pr.NodeID,
		Title: "Revert " + pr.Title, Body: fmt.Sprintf("Reverts %s#%d\n\nRollback drill requested through maku policy drill.", rc.Name, pr.Number), By: rc.Merge.MergerLogin})
	if err != nil {
		return forge.Revert{}, err
	}
	return revert, git.AddLabels(ctx, revert.Number, "factory/drill")
}

func proposalFor(raw, repository, class string) ([]byte, int, error) {
	data := []byte(raw)
	var object struct {
		Kind string `json:"kind"`
		Spec struct {
			Repository string `json:"repository"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(data, &object); err != nil {
		return nil, 0, err
	}
	if object.Kind == "Repository" {
		if !strings.EqualFold(object.Spec.Repository, repository) {
			return nil, 0, fmt.Errorf("GitOps manifest is for %s, not %s", object.Spec.Repository, repository)
		}
		return evidence.ProposeInManifest(data, class)
	}
	return evidence.Propose(data, class)
}

func branchName(repository, class string) string {
	return "factory/promote-" + strings.ReplaceAll(repository, "/", "-") + "-" + class
}

func titleFor(repository, class string) string {
	return fmt.Sprintf("Promote %s to live in %s", class, repository)
}

func bodyFor(repository, class string, c evidence.ClassReport, rep evidence.Report) string {
	e := c.Evidence
	var body strings.Builder
	fmt.Fprintf(&body, "%s.\n\n", titleFor(repository, class))
	fmt.Fprintf(&body, "Evidence read at %s:\n\n", rep.EvidenceAt.UTC().Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(&body, "- %d clean decisions (minimum %d)\n- %d bad outcomes\n- rollback drill %s\n", e.Clean, e.Minimum, e.Bad, e.Drill)
	if len(e.Shadow.Judged) > 0 {
		body.WriteString("\nSupporting pull requests:\n")
		for _, n := range e.Shadow.Judged {
			fmt.Fprintf(&body, "- https://github.com/%s/pull/%d\n", repository, n)
		}
	}
	fmt.Fprintf(&body, "\nOnly `%s` line %d changes, from `mode: shadow` to `mode: live`.\n", c.Proposal.Path, c.Proposal.Line)
	body.WriteString("The factory opens this pull request and never merges it. A maintainer reviews and merges it.")
	return body.String()
}
