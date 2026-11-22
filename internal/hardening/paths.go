// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// PathAction is what T4 does with a push touching a protected path (UH9).
type PathAction string

// The two actions: wait for a maintainer's approval of the bundle digest, or refuse.
const (
	OnApproval PathAction = "approval"
	OnBlocked  PathAction = "blocked"
)

// Bounds on a repository's extension of the floor.
const (
	MaxExtraPaths = 64
	MaxPatternLen = 256
)

// PathRule is one protected path: a pattern and its action, approval when empty.
type PathRule struct {
	Pattern string     `json:"path"`
	On      PathAction `json:"on,omitempty"`
}

// floor is the platform's list (protected paths table), which a repository may extend and never
// shorten (UH10). A pattern without a slash matches a base name at any depth: a nested go.mod or
// AGENTS.md steers a build or a run as much as the root one.
var floor = []string{
	// CI and automation
	".github/**", ".gitlab-ci.yml", "Taskfile*", "taskfile.y*ml", "Makefile", ".pre-commit-config.yaml",
	// Agent instructions
	"AGENTS.md", "CLAUDE.md", "GEMINI.md", ".agents/**", ".claude/**", ".codex/**", ".cursor/**",
	// Dependency manifests and lock files
	"go.mod", "go.sum", "package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml",
	"yarn.lock", "requirements*.txt", "pyproject.toml", "uv.lock", "Cargo.toml", "Cargo.lock", "Gemfile*",
	"pom.xml", "build.gradle*", "mise.toml", ".tool-versions",
	// Execution hooks
	".envrc", ".gitattributes", ".gitmodules", ".husky/**", ".git-hooks/**",
}

// FloorPatterns is a copy of the platform floor.
func FloorPatterns() []string { return append([]string(nil), floor...) }

type rule struct {
	segs     []string
	anywhere bool // no slash: the last segment of any path
	on       PathAction
}

// ProtectedPaths is the floor plus one repository's extension.
type ProtectedPaths struct{ rules []rule }

// NewProtectedPaths compiles the floor with extra, refusing a pattern it cannot read the same way
// everywhere: absolute, with "..", an empty segment, an invalid glob or ** inside a segment.
func NewProtectedPaths(extra []PathRule) (ProtectedPaths, error) {
	if len(extra) > MaxExtraPaths {
		return ProtectedPaths{}, fmt.Errorf("protectedPaths: at most %d entries", MaxExtraPaths)
	}
	var p ProtectedPaths
	for _, f := range floor {
		r, err := compile(PathRule{Pattern: f, On: OnApproval})
		if err != nil {
			return ProtectedPaths{}, fmt.Errorf("floor %q: %w", f, err) // a bug in this file
		}
		p.rules = append(p.rules, r)
	}
	for _, e := range extra {
		r, err := compile(e)
		if err != nil {
			return ProtectedPaths{}, fmt.Errorf("protectedPaths %q: %w", e.Pattern, err)
		}
		p.rules = append(p.rules, r)
	}
	return p, nil
}

func compile(e PathRule) (rule, error) {
	on := e.On
	switch on {
	case "":
		on = OnApproval
	case OnApproval, OnBlocked:
	default:
		return rule{}, fmt.Errorf("on is approval or blocked, not %q", e.On)
	}
	switch {
	case e.Pattern == "":
		return rule{}, errors.New("the pattern is empty")
	case len(e.Pattern) > MaxPatternLen:
		return rule{}, fmt.Errorf("a pattern is at most %d bytes", MaxPatternLen)
	case strings.HasPrefix(e.Pattern, "/"):
		return rule{}, errors.New("a pattern is relative to the repository root")
	}
	segs := strings.Split(e.Pattern, "/")
	collapsed := make([]string, 0, len(segs))
	for _, s := range segs {
		switch {
		case s == "":
			return rule{}, errors.New("a pattern has an empty segment")
		case s == "..", s == ".":
			return rule{}, errors.New("a pattern is relative to the repository root, with no . or .. segment")
		case s != "**" && strings.Contains(s, "**"):
			return rule{}, errors.New("** is a whole segment")
		}
		if _, err := path.Match(s, ""); err != nil {
			return rule{}, fmt.Errorf("an invalid glob: %w", err)
		}
		if s == "**" && len(collapsed) > 0 && collapsed[len(collapsed)-1] == "**" {
			continue
		}
		collapsed = append(collapsed, s)
	}
	return rule{segs: collapsed, anywhere: len(collapsed) == 1 && collapsed[0] != "**", on: on}, nil
}

// Match reports the strictest action of the rules file matches.
func (p ProtectedPaths) Match(file string) (PathAction, bool) {
	segs := strings.Split(file, "/")
	var on PathAction
	for _, r := range p.rules {
		hit := false
		if r.anywhere {
			hit, _ = path.Match(r.segs[0], segs[len(segs)-1])
		} else {
			hit = matchSegs(r.segs, segs)
		}
		if hit && on != OnBlocked {
			on = r.on
		}
	}
	return on, on != ""
}

// Worst is the strictest action over files: blocked, approval, or "" when none is protected.
func (p ProtectedPaths) Worst(files []string) PathAction {
	var worst PathAction
	for _, f := range files {
		if on, ok := p.Match(f); ok && worst != OnBlocked {
			worst = on
		}
	}
	return worst
}

func matchSegs(pat, segs []string) bool {
	p, s := 0, 0
	starP, starS := -1, 0
	for s < len(segs) {
		if p < len(pat) && pat[p] == "**" {
			starP = p
			starS = s
			p++
			continue
		}
		if p < len(pat) {
			if ok, _ := path.Match(pat[p], segs[s]); ok {
				p++
				s++
				continue
			}
		}
		if starP >= 0 {
			starS++
			s = starS
			p = starP + 1
			continue
		}
		return false
	}
	for p < len(pat) && pat[p] == "**" {
		p++
	}
	return p == len(pat)
}
