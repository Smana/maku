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
	for _, s := range segs {
		switch {
		case s == "":
			return rule{}, errors.New("a pattern has an empty segment")
		case s == "..", s == ".":
			return rule{}, errors.New("a pattern is relative to the repository root, without . or ..")
		case s != "**" && strings.Contains(s, "**"):
			return rule{}, errors.New("** is a whole segment")
		}
		if _, err := path.Match(s, ""); err != nil {
			return rule{}, fmt.Errorf("an invalid glob: %w", err)
		}
	}
	return rule{segs: segs, anywhere: len(segs) == 1 && segs[0] != "**", on: on}, nil
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
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegs(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, _ := path.Match(pat[0], segs[0])
	return ok && matchSegs(pat[1:], segs[1:])
}
