// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"strings"
	"testing"
)

// The floor (UH10, protected paths table) holds with no repository extension. A pattern without a
// slash matches a base name at any depth, one with a slash is anchored at the root: gitignore's rule.
func TestFloor(t *testing.T) {
	p, err := NewProtectedPaths(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		".github/workflows/ci.yaml", ".github/CODEOWNERS", ".github/chainguard/agent-implementer.sts.yaml",
		".github/agent-factory.yaml", ".gitlab-ci.yml", "Taskfile.yml", "taskfile.yaml", "Makefile",
		".pre-commit-config.yaml",
		"AGENTS.md", "docs/AGENTS.md", "a/b/CLAUDE.md", "GEMINI.md", ".agents/skills/x/SKILL.md",
		".claude/settings.json", ".codex/config.toml", ".cursor/rules/a.mdc",
		"go.mod", "tools/go.sum", "package.json", "web/package-lock.json", "pnpm-lock.yaml", "yarn.lock",
		"requirements.txt", "requirements-dev.txt", "pyproject.toml", "uv.lock", "Cargo.toml", "Cargo.lock",
		"Gemfile", "Gemfile.lock", "pom.xml", "build.gradle", "build.gradle.kts", "mise.toml", ".tool-versions",
		".envrc", "sub/.envrc", ".gitattributes", ".gitmodules", ".husky/pre-commit", ".git-hooks/pre-push",
	} {
		if on, ok := p.Match(f); !ok || on != OnApproval {
			t.Errorf("%s: %q %v, want approval", f, on, ok)
		}
	}
	for _, f := range []string{
		"README.md", "docs/agents.md", "internal/x.go", "src/github/x.go", "x/.github/y", "gomod",
		"agents.md.bak", "requirements/base.in", "MakefileX/a",
	} {
		if on, ok := p.Match(f); ok {
			t.Errorf("%s matched (%q)", f, on)
		}
	}
}

// A repository extends the floor and may raise a floor path to blocked; it never removes one.
func TestExtension(t *testing.T) {
	p, err := NewProtectedPaths([]PathRule{
		{Pattern: "scripts/ci/**"},
		{Pattern: "deploy/*.sh", On: OnBlocked},
		{Pattern: ".github/workflows/**", On: OnBlocked},
	})
	if err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]PathAction{
		"scripts/ci/run.sh":            OnApproval,
		"scripts/ci/a/b/c":             OnApproval,
		"deploy/up.sh":                 OnBlocked,
		".github/workflows/ci.yaml":    OnBlocked, // blocked outranks the floor's approval
		".github/CODEOWNERS":           OnApproval,
		"AGENTS.md":                    OnApproval,
		"deploy/sub/up.sh":             "",
		"scripts/cinema/x":             "",
		"other/scripts/ci/run.sh":      "",
		"scripts/ci":                   OnApproval, // ** matches zero segments
		"deploy/up.sh.orig":            "",
		"deploy/.sh":                   OnBlocked,
		"go.mod":                       OnApproval,
		"internal/hardening/a_test.go": "",
	} {
		on, ok := p.Match(f)
		if on != want || ok != (want != "") {
			t.Errorf("%s: %q %v, want %q", f, on, ok, want)
		}
	}
	if got := p.Worst([]string{"README.md", "go.mod", "deploy/up.sh"}); got != OnBlocked {
		t.Fatalf("Worst = %q", got)
	}
	if got := p.Worst([]string{"README.md"}); got != "" {
		t.Fatalf("Worst = %q", got)
	}
}

func TestExtensionRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		rule PathRule
		want string
	}{
		"empty":             {PathRule{}, "empty"},
		"absolute":          {PathRule{Pattern: "/etc/x"}, "relative"},
		"parent":            {PathRule{Pattern: "a/../b"}, "relative"},
		"empty segment":     {PathRule{Pattern: "a//b"}, "empty segment"},
		"bad glob":          {PathRule{Pattern: "a/[b"}, "glob"},
		"unknown action":    {PathRule{Pattern: "a", On: "never"}, "approval or blocked"},
		"a ** in a segment": {PathRule{Pattern: "a/b**/c"}, "whole segment"},
		"too long":          {PathRule{Pattern: strings.Repeat("a", MaxPatternLen+1)}, "bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewProtectedPaths([]PathRule{tc.rule})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
	many := make([]PathRule, MaxExtraPaths+1)
	for i := range many {
		many[i] = PathRule{Pattern: "a"}
	}
	if _, err := NewProtectedPaths(many); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("got %v", err)
	}
}
