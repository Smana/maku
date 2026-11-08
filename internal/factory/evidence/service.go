// SPDX-License-Identifier: Apache-2.0

package evidence

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

// Source is the part of a repository's GitHub connection the evidence reads; *forge.GitHub.
type Source interface {
	MergedRecords(ctx context.Context, filter string) ([]PR, error)
	RevertRecords(ctx context.Context) ([]Revert, error)
	AnyMerged(ctx context.Context, filter string) (bool, error)
	FenceFile(ctx context.Context) ([]byte, bool, error)
	DefaultBranch(ctx context.Context) (string, error)
}

// PolicyPath is the repository file a promotion edits.
const PolicyPath = ".github/agent-factory.yaml"

// The read's bounds. Lookback is how far back merges are read: the decision index's 180 days.
// TTL is how long a repository's report is served before GitHub is read again: the page may be
// reloaded at will, GitHub is read at most once per TTL per repository and replica.
const (
	Lookback      = 180 * 24 * time.Hour
	TTL           = 10 * time.Minute
	defaultWindow = 168 * time.Hour
)

// Proposal is the promotion's one-line change: the file as it would be, the line it changes, and
// the GitHub page the maintainer submits it from. Maku never opens the pull request (MP1).
type Proposal struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	File    string `json:"file"`
	EditURL string `json:"editURL"`
}

// ClassReport is one class: its mode where it is declared, its evidence, and, for a shadow class
// of the repository's own file, the promotion's proposal.
type ClassReport struct {
	Class string `json:"class"`
	// Source is where the class is declared: "repository" (the policy file) or "central".
	Source   string    `json:"source"`
	Mode     string    `json:"mode"`
	Evidence Evidence  `json:"evidence"`
	Proposal *Proposal `json:"proposal,omitempty"`
	// Unproposed says why a class has no proposal: not_shadow, not_in_repository_policy.
	Unproposed string `json:"unproposed,omitempty"`
}

// Report is a repository's promotion evidence as read at ReadAt.
type Report struct {
	Repository  string        `json:"repository"`
	Minimum     int           `json:"minimum"`
	PolicyError string        `json:"policyError,omitempty"`
	Classes     []ClassReport `json:"classes"`
	ReadAt      time.Time     `json:"readAt"`
}

// ErrUnknownRepository is a repository the config does not list.
var ErrUnknownRepository = errors.New("evidence: the repository is not configured")

// Service reads and caches each configured repository's report. Sources is the repository's
// connection, false without one.
type Service struct {
	Cfg     *config.Config
	Sources func(repository string) (Source, bool)
	Now     func() time.Time

	mu    sync.Mutex // one read at a time: the cache bounds GitHub, and so does this
	cache map[string]Report
}

// Report is repository's report, from the cache while younger than TTL.
func (s *Service) Report(ctx context.Context, repository string) (Report, error) {
	rc := s.repository(repository)
	if rc == nil {
		return Report{}, ErrUnknownRepository
	}
	src, ok := s.Sources(rc.Name)
	if !ok {
		return Report{}, ErrUnknownRepository
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	if r, ok := s.cache[rc.Name]; ok && now.Sub(r.ReadAt) < TTL {
		return r, nil
	}
	r, err := s.read(ctx, rc, src, now)
	if err != nil {
		return Report{}, err
	}
	if s.cache == nil {
		s.cache = map[string]Report{}
	}
	s.cache[rc.Name] = r
	return r, nil
}

func (s *Service) repository(name string) *config.Repository {
	for _, r := range s.Cfg.Repos() {
		if strings.EqualFold(r.Name, name) {
			return &r
		}
	}
	return nil
}

// class is what reading one class needs: its mode and source, the logins whose pull requests it
// judges, and its follow-up window.
type class struct {
	name, source, mode string
	authors            []string
	window             time.Duration
}

func (s *Service) read(ctx context.Context, rc *config.Repository, src Source, now time.Time) (Report, error) {
	r := Report{Repository: rc.Name, Minimum: s.Cfg.MergeGuardrails.MinPromotion(), ReadAt: now}
	body, found, err := src.FenceFile(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("evidence: %w", err)
	}
	var file *mergepolicy.File
	if found {
		if file, err = mergepolicy.Parse(body); err != nil {
			r.PolicyError = "invalid" // the reason, never the parser's text: the file is the repository's
			file = nil
		}
	}
	reverts, err := src.RevertRecords(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("evidence: %w", err)
	}
	branch := ""
	if file != nil {
		if branch, err = src.DefaultBranch(ctx); err != nil {
			return Report{}, fmt.Errorf("evidence: %w", err)
		}
	}
	l := Logins{Factory: s.Cfg.FactoryLogin, Merger: rc.Merge.MergerLogin}
	since := now.Add(-Lookback).UTC().Format("2006-01-02")
	for _, c := range s.classes(rc, file) {
		e, err := collect(ctx, src, c, reverts, rc.Name, l, now, since, r.Minimum)
		if err != nil {
			return Report{}, err
		}
		cr := ClassReport{Class: c.name, Source: c.source, Mode: c.mode, Evidence: e}
		switch {
		case c.source != sourceRepository:
			cr.Unproposed = "not_in_repository_policy"
		case c.mode != string(mergepolicy.ModeShadow):
			cr.Unproposed = "not_shadow"
		default:
			out, line, err := Propose(body, c.name)
			if err != nil {
				return Report{}, fmt.Errorf("evidence: propose %s: %w", c.name, err)
			}
			cr.Proposal = &Proposal{Path: PolicyPath, Line: line, File: string(out),
				EditURL: "https://github.com/" + rc.Name + "/edit/" + branch + "/" + PolicyPath}
		}
		r.Classes = append(r.Classes, cr)
	}
	return r, nil
}

const (
	sourceRepository = "repository"
	sourceCentral    = "central"
)

// classes are the repository file's classes when it declares any, else the central config's.
func (s *Service) classes(rc *config.Repository, f *mergepolicy.File) []class {
	agents := []string{searchLogin(s.Cfg.AgentsLogin)}
	var out []class
	if f != nil && len(f.Classes) > 0 {
		for _, name := range slices.Sorted(maps.Keys(f.Classes)) {
			p := f.Classes[name]
			c := class{name: name, source: sourceRepository, mode: string(p.Mode), authors: agents, window: defaultWindow}
			if p.Adopt != nil {
				c.authors = adopters(p.Adopt.Logins)
			}
			if p.Monitor != nil && p.Monitor.FollowUpWindow.Duration > 0 {
				c.window = p.Monitor.FollowUpWindow.Duration
			}
			out = append(out, c)
		}
		return out
	}
	for _, name := range slices.Sorted(maps.Keys(rc.Classes)) {
		cl := rc.Classes[name]
		mode := string(mergepolicy.ModeOff)
		switch {
		case cl.Live:
			mode = string(mergepolicy.ModeLive)
		case cl.Shadow:
			mode = string(mergepolicy.ModeShadow)
		}
		c := class{name: name, source: sourceCentral, mode: mode, authors: agents, window: defaultWindow}
		if cl.Adopted() {
			c.authors = adopters(cl.Author.Adopt.Logins)
		}
		if cl.Monitor != nil && cl.Monitor.FollowUpWindow.Duration > 0 {
			c.window = cl.Monitor.FollowUpWindow.Duration
		}
		out = append(out, c)
	}
	return out
}

func adopters(logins []string) []string {
	out := make([]string, 0, len(logins))
	for _, l := range logins {
		out = append(out, searchLogin(l))
	}
	return out
}

// searchLogin is a login as GitHub's search names an author: an App's <slug>[bot] is app/<slug>.
func searchLogin(l string) string {
	if s, ok := strings.CutSuffix(l, "[bot]"); ok {
		return "app/" + s
	}
	return l
}

// collect reads one class: the script's two modes, then MP7.
func collect(ctx context.Context, src Source, c class, reverts []Revert, repo string, l Logins, now time.Time, since string, minimum int) (Evidence, error) {
	var judged []PR
	for _, a := range c.authors {
		prs, err := src.MergedRecords(ctx, fmt.Sprintf("author:%s merged:>=%s", a, since))
		if err != nil {
			return Evidence{}, fmt.Errorf("evidence: %s: %w", c.name, err)
		}
		judged = append(judged, prs...)
	}
	var merges []PR
	for _, label := range []string{"factory/merged:", "factory/auto-merged:"} {
		prs, err := src.MergedRecords(ctx, fmt.Sprintf(`label:"%s%s" merged:>=%s`, label, c.name, since))
		if err != nil {
			return Evidence{}, fmt.Errorf("evidence: %s: %w", c.name, err)
		}
		merges = append(merges, prs...)
	}
	followed := map[int]bool{}
	for _, p := range merges {
		if _, seen := followed[p.Number]; seen {
			continue
		}
		hit, err := src.AnyMerged(ctx, fmt.Sprintf(`label:"factory/follow-up-of:#%d"`, p.Number))
		if err != nil {
			return Evidence{}, fmt.Errorf("evidence: %s: %w", c.name, err)
		}
		followed[p.Number] = hit
	}
	sh := Shadow(judged, reverts, c.name, repo, l)
	out := Outcomes(merges, reverts, followed, repo, l, now, c.window)
	return Evaluate(sh, out, reverts, repo, minimum), nil
}
