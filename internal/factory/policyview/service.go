// SPDX-License-Identifier: Apache-2.0

package policyview

import (
	"context"
	"fmt"
	"strings"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
)

// Reports is the repository's evidence report, cached; *evidence.Service implements it.
type Reports interface {
	Report(ctx context.Context, repository string) (evidence.Report, error)
}

// CommentReader reads a pull request's newest comments.
type CommentReader func(ctx context.Context, pr int) ([]Comment, error)

// Service serves the editor's check and the decision timeline for configured repositories.
// Comments is the repository's comment reader, false without a connection.
type Service struct {
	Cfg      config.Source
	Reports  Reports
	Comments func(repository string) (CommentReader, bool)
	// Namespace is the MergePolicy objects' namespace, for a policy edit's manifest.
	Namespace string
}

func (s *Service) repository(cfg *config.Config, name string) (*config.Repository, error) {
	for _, r := range cfg.Repos() {
		if strings.EqualFold(r.Name, name) {
			return &r, nil
		}
	}
	return nil, evidence.ErrUnknownRepository
}

// Preview checks file against repository's current policy (Check).
func (s *Service) Preview(ctx context.Context, repository, file string) (Preview, error) {
	cfg := s.Cfg.Current()
	rc, err := s.repository(cfg, repository)
	if err != nil {
		return Preview{}, err
	}
	rep, err := s.Reports.Report(ctx, rc.Name)
	if err != nil {
		return Preview{}, err
	}
	return Check(cfg, rc, rep, file), nil
}

// PreviewBindings checks bindings as repository's spec.mergePolicies (CheckBindings, B16).
func (s *Service) PreviewBindings(ctx context.Context, repository string, bindings []config.Binding) (BindingPreview, error) {
	cfg := s.Cfg.Current()
	rc, err := s.repository(cfg, repository)
	if err != nil {
		return BindingPreview{}, err
	}
	rep, err := s.Reports.Report(ctx, rc.Name)
	if err != nil {
		return BindingPreview{}, err
	}
	return CheckBindings(cfg, rc, rep, bindings), nil
}

// PreviewPolicy checks spec as the MergePolicy name, with the consumers readable lets through
// (CheckPolicy, B11, B16).
func (s *Service) PreviewPolicy(_ context.Context, name string, spec config.PolicySpec, readable func(string) bool) (PolicyPreview, error) {
	return CheckPolicy(s.Cfg.Current(), name, spec, s.Namespace, readable), nil
}

// Timeline is pr's decisions, from the factory's own comments on it.
func (s *Service) Timeline(ctx context.Context, repository string, pr int) (Timeline, error) {
	cfg := s.Cfg.Current()
	rc, err := s.repository(cfg, repository)
	if err != nil {
		return Timeline{}, err
	}
	read, ok := s.Comments(rc.Name)
	if !ok {
		return Timeline{}, evidence.ErrUnknownRepository
	}
	cs, err := read(ctx, pr)
	if err != nil {
		return Timeline{}, fmt.Errorf("policyview: %w", err)
	}
	return Decisions(rc.Name, pr, cfg.FactoryLogin, cs), nil
}
