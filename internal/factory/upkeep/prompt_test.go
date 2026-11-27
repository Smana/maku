// SPDX-License-Identifier: Apache-2.0

package upkeep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
)

func sha256HexString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestValidatePrompt(t *testing.T) {
	valid := "# Dead code routine\n\n## Scope\nOnly unexported helpers.\n\n## Verify\n- go test ./...\n* golangci-lint run\n"
	if err := ValidatePrompt(valid); err != nil {
		t.Fatalf("valid prompt rejected: %v", err)
	}

	// Exactly 4096 bytes and 5 sections must pass.
	pad := strings.Repeat("a", MaxPromptBytes-len("## S1\n## S2\n## S3\n## S4\n## Verify\n- ok\n"))
	atCap := "## S1\n" + pad + "## S2\n## S3\n## S4\n## Verify\n- ok\n"
	if len(atCap) != MaxPromptBytes {
		t.Fatalf("test setup len=%d, want %d", len(atCap), MaxPromptBytes)
	}
	if err := ValidatePrompt(atCap); err != nil {
		t.Fatalf("prompt at cap rejected: %v", err)
	}

	cases := map[string]string{
		"invalid UTF-8":             "\xff## Verify\n- ok\n",
		"over 4096 bytes":           "## Verify\n- " + strings.Repeat("x", MaxPromptBytes),
		"CR rejected":               "## Verify\r\n- go test ./...\n",
		"NUL rejected":              "## Verify\n- go\x00test\n",
		"Unicode Cf ZWSP":           "## Verify\n- go\u200btest\n",
		"Unicode Cf bidi":           "## Verify\n- go\u202etest\n",
		"Unicode Cf BOM":            "\ufeff## Verify\n- go test\n",
		"text before first sec":     "oops\n## Verify\n- go test\n",
		"title on line 2":           "\n# Title\n## Verify\n- go test\n",
		"over 5 sections":           "## 1\n## 2\n## 3\n## 4\n## 5\n## Verify\n- ok\n",
		"fenced section counts":     "## 1\n```\n## 2\n## 3\n## 4\n## 5\n```\n## Verify\n- ok\n",
		"missing Verify section":    "## Scope\nCheck things.\n",
		"empty Verify section":      "## Scope\nCheck things.\n## Verify\nRun tests manually.\n",
		"empty Verify dash bullet":  "## Verify\n- \n",
		"spaces Verify dash bullet": "## Verify\n-    \n",
		"empty Verify star bullet":  "## Verify\n* \n",
		"spaces Verify star bullet": "## Verify\n* \t \n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidatePrompt(text)
			if !errors.Is(err, ErrPromptInvalid) {
				t.Fatalf("got %v, want ErrPromptInvalid", err)
			}
		})
	}
}

func TestValidateSkill(t *testing.T) {
	if err := ValidateSkill("# Docs Audit\n\nCheck links and headings.\n"); err != nil {
		t.Fatalf("valid skill rejected: %v", err)
	}
	if err := ValidateSkill(strings.Repeat("k", MaxSkillBytes)); err != nil {
		t.Fatalf("skill at 8192 bytes rejected: %v", err)
	}

	for name, text := range map[string]string{
		"empty":           "",
		"whitespace only": "  \n\t \n",
		"invalid UTF-8":   "\xff",
		"over 8192 bytes": strings.Repeat("k", MaxSkillBytes+1),
		"CR rejected":     "hello\r\n",
		"ESC rejected":    "hello\x1b[31m",
		"Unicode Cf":      "hello\u200bworld",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSkill(text); !errors.Is(err, ErrSkillInvalid) {
				t.Fatalf("got %v, want ErrSkillInvalid", err)
			}
		})
	}
}

func TestPromptAndSkillPaths(t *testing.T) {
	if got := PromptPath("dead-code"); got != ".github/agent-factory/upkeep/dead-code.md" {
		t.Fatalf("PromptPath(dead-code) = %q", got)
	}
	for _, bad := range []string{"", "../evil", "a/b", "Bad", "-lead"} {
		if got := PromptPath(bad); got != "" {
			t.Fatalf("PromptPath(%q) = %q, want empty", bad, got)
		}
	}
	if got := SkillPath("docs-audit"); got != ".agents/skills/docs-audit/SKILL.md" {
		t.Fatalf("SkillPath(docs-audit) = %q", got)
	}
	for _, bad := range []string{"", "../evil", "a/b", "Bad_Skill", "-lead"} {
		if got := SkillPath(bad); got != "" {
			t.Fatalf("SkillPath(%q) = %q, want empty", bad, got)
		}
	}
	eff := EffectivePromptSHA("abc", "def")
	if len(eff) != 64 || eff != sha256HexString("abc:def") {
		t.Fatalf("EffectivePromptSHA = %q, want %q", eff, sha256HexString("abc:def"))
	}
}

func TestIsProtectedPath(t *testing.T) {
	skills := []string{"docs-audit", "go-lint"}
	for _, wantTrue := range []string{
		".github/agent-factory/upkeep",
		".github/agent-factory/upkeep/dead-code.md",
		".github/agent-factory/upkeep/nested/a.md",
		".agents/skills/docs-audit",
		".agents/skills/docs-audit/SKILL.md",
		".agents/skills/docs-audit/scripts/check.sh",
		".agents/skills/go-lint/SKILL.md",
	} {
		if !IsProtectedPath(wantTrue, skills) {
			t.Errorf("IsProtectedPath(%q) = false, want true", wantTrue)
		}
	}
	for _, wantFalse := range []string{
		"",
		".github/agent-factory/steering/review.md",
		".github/agent-factory/upkeep-extra/a.md",
		".agents/skills/unrelated/SKILL.md",
		".agents/skills/docs-audit-2/SKILL.md",
		"README.md",
	} {
		if IsProtectedPath(wantFalse, skills) {
			t.Errorf("IsProtectedPath(%q) = true, want false", wantFalse)
		}
	}
}

type failingReader struct {
	err error
}

func (f failingReader) DefaultBranchFile(context.Context, string) (string, string, bool, error) {
	return "", "", false, f.err
}

func TestLoadPrompt(t *testing.T) {
	ctx := t.Context()
	rt := config.ResolvedRoutine{
		Name:          "dead-code",
		DefaultPrompt: "Remove dead code.",
		RepoPrompt:    true,
	}

	t.Run("loads valid repo prompt with blob SHA", func(t *testing.T) {
		f := forgetest.NewFake()
		repoText := "## Scope\nCheck pkg/\n\n## Verify\n- go test ./...\n"
		f.SetFile(".github/agent-factory/upkeep/dead-code.md", repoText)
		_, wantBlobSHA, _, _ := f.DefaultBranchFile(ctx, ".github/agent-factory/upkeep/dead-code.md")

		got, err := LoadPrompt(ctx, f, "acme/app", rt)
		if err != nil {
			t.Fatal(err)
		}
		if got.Source != "repo" || got.Path != ".github/agent-factory/upkeep/dead-code.md" ||
			got.PromptText != repoText || got.PromptSHA != wantBlobSHA || got.SkillSHA != "" ||
			got.EffectiveSHA != sha256HexString(wantBlobSHA+":") {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("missing repo prompt falls back to DefaultPrompt", func(t *testing.T) {
		f := forgetest.NewFake()
		got, err := LoadPrompt(ctx, f, "acme/app", rt)
		if err != nil {
			t.Fatal(err)
		}
		wantPromptSHA := sha256HexString(rt.DefaultPrompt)
		if got.Source != "routine" || got.Path != "" || got.PromptText != rt.DefaultPrompt ||
			got.PromptSHA != wantPromptSHA || got.EffectiveSHA != sha256HexString(wantPromptSHA+":") {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("repoPrompt false ignores existing repo prompt", func(t *testing.T) {
		f := forgetest.NewFake()
		f.SetFile(".github/agent-factory/upkeep/dead-code.md", "invalid prompt without verify")
		r2 := rt
		r2.RepoPrompt = false
		got, err := LoadPrompt(ctx, f, "acme/app", r2)
		if err != nil {
			t.Fatal(err)
		}
		if got.Source != "routine" || got.PromptText != rt.DefaultPrompt {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("invalid repo prompt fails closed and never falls back", func(t *testing.T) {
		for name, text := range map[string]string{
			"missing verify": "## Scope\nOnly scope.\n",
			"too many secs":  "## 1\n## 2\n## 3\n## 4\n## 5\n## Verify\n- x\n",
			"over 4096":      "## Verify\n- " + strings.Repeat("a", MaxPromptBytes),
			"cf char":        "## Verify\n- \u200b\n",
			"cr":             "## Verify\r\n- x\n",
			"not a file":     forgetest.NotAFile,
		} {
			t.Run(name, func(t *testing.T) {
				f := forgetest.NewFake()
				f.SetFile(".github/agent-factory/upkeep/dead-code.md", text)
				if _, err := LoadPrompt(ctx, f, "acme/app", rt); !errors.Is(err, ErrPromptInvalid) {
					t.Fatalf("got %v, want ErrPromptInvalid", err)
				}
			})
		}
	})

	t.Run("bound skill attaches, hashes combined SHA, and fails closed", func(t *testing.T) {
		rSkill := rt
		rSkill.Skill = "docs-audit"
		f := forgetest.NewFake()
		repoText := "## Verify\n- go test ./...\n"
		skillText := "# Docs Audit\nCheck docs.\n"
		f.SetFile(".github/agent-factory/upkeep/dead-code.md", repoText)
		f.SetFile(".agents/skills/docs-audit/SKILL.md", skillText)
		_, wantPromptSHA, _, _ := f.DefaultBranchFile(ctx, ".github/agent-factory/upkeep/dead-code.md")
		_, wantSkillSHA, _, _ := f.DefaultBranchFile(ctx, ".agents/skills/docs-audit/SKILL.md")

		got, err := LoadPrompt(ctx, f, "acme/app", rSkill)
		if err != nil {
			t.Fatal(err)
		}
		if got.SkillText != skillText || got.SkillSHA != wantSkillSHA ||
			got.EffectiveSHA != sha256HexString(wantPromptSHA+":"+wantSkillSHA) {
			t.Fatalf("got %+v", got)
		}

		// Missing skill -> ErrSkillMissing.
		fMissing := forgetest.NewFake()
		fMissing.SetFile(".github/agent-factory/upkeep/dead-code.md", repoText)
		if _, err := LoadPrompt(ctx, fMissing, "acme/app", rSkill); !errors.Is(err, ErrSkillMissing) {
			t.Fatalf("missing skill: got %v, want ErrSkillMissing", err)
		}

		// Invalid skill -> ErrSkillInvalid.
		for _, bad := range []string{strings.Repeat("s", MaxSkillBytes+1), "bad\r\n", "bad\u200b", forgetest.NotAFile} {
			fBad := forgetest.NewFake()
			fBad.SetFile(".agents/skills/docs-audit/SKILL.md", bad)
			if _, err := LoadPrompt(ctx, fBad, "acme/app", rSkill); !errors.Is(err, ErrSkillInvalid) {
				t.Fatalf("invalid skill: got %v, want ErrSkillInvalid", err)
			}
		}
	})

	t.Run("transient reader error propagates raw", func(t *testing.T) {
		sentinel := errors.New("github 502")
		_, err := LoadPrompt(ctx, failingReader{err: sentinel}, "acme/app", rt)
		if !errors.Is(err, sentinel) || errors.Is(err, ErrPromptInvalid) || errors.Is(err, ErrSkillInvalid) {
			t.Fatalf("got %v, want raw %v", err, sentinel)
		}
		if _, err := LoadPrompt(ctx, failingReader{err: forge.ErrNotAFile}, "acme/app", rt); !errors.Is(err, ErrPromptInvalid) {
			t.Fatalf("ErrNotAFile: got %v, want ErrPromptInvalid", err)
		}
	})
}
