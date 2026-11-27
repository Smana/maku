// SPDX-License-Identifier: Apache-2.0

package upkeep

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

const (
	// PromptDir is the default-branch directory holding repo-owned routine prompts.
	PromptDir = ".github/agent-factory/upkeep/"
	// SkillsDir is the default-branch directory holding open-format Agent Skills.
	SkillsDir = ".agents/skills/"
	// MaxPromptBytes bounds repo-owned routine prompts (double steering.MaxBytes).
	MaxPromptBytes = 4096
	// MaxPromptSections bounds "## " headings inside a repo-owned routine prompt.
	MaxPromptSections = 5
	// MaxSkillBytes bounds an attached Agent Skill SKILL.md file.
	MaxSkillBytes = 8192

	verifyHeading = "## Verify"
)

var (
	// ErrPromptInvalid marks a repo-owned routine prompt that failed strict validation or is not a regular file.
	ErrPromptInvalid = errors.New("upkeep: routine prompt invalid")
	// ErrSkillMissing marks a bound Agent Skill whose SKILL.md does not exist on the default branch.
	ErrSkillMissing = errors.New("upkeep: routine skill missing")
	// ErrSkillInvalid marks a bound Agent Skill whose SKILL.md failed strict validation or is not a regular file.
	ErrSkillInvalid = errors.New("upkeep: routine skill invalid")
)

// DefaultBranchReader reads a single file on the repository's default branch.
type DefaultBranchReader interface {
	DefaultBranchFile(ctx context.Context, path string) (text, sha string, found bool, err error)
}

// LoadedPrompt is the resolved prompt and optional Agent Skill pinned on a routine Task.
type LoadedPrompt struct {
	EffectiveSHA string
	PromptSHA    string
	SkillSHA     string
	Source       string // "repo" or "routine"
	Path         string // PromptPath(routine) when Source == "repo", else ""
	PromptText   string
	SkillText    string
}

// PromptPath returns routine's repo-owned prompt path, or "" if routine is not a valid name.
func PromptPath(routine string) string {
	if !config.ValidRoutineName(routine) {
		return ""
	}
	return PromptDir + routine + ".md"
}

// SkillPath returns skill's SKILL.md path, or "" if skill is not a valid Agent Skill name.
func SkillPath(skill string) string {
	if !config.ValidSkillName(skill) {
		return ""
	}
	return SkillsDir + skill + "/SKILL.md"
}

// EffectivePromptSHA computes the 64-char hex combined fingerprint sha256(promptSHA + ":" + skillSHA).
func EffectivePromptSHA(promptSHA, skillSHA string) string {
	sum := sha256.Sum256([]byte(promptSHA + ":" + skillSHA))
	return hex.EncodeToString(sum[:])
}

// ValidatePrompt validates a repo-owned routine prompt (UTF-8, <= 4096 B, no C0/C1/DEL control
// chars except \n and \t, no Unicode Cf chars, optional "# " title on line 1, <= 5 "## " sections,
// mandatory "## Verify" section with at least one "- " or "* " check item).
func ValidatePrompt(text string) error {
	if !utf8.ValidString(text) {
		return fmt.Errorf("%w: not UTF-8", ErrPromptInvalid)
	}
	if len(text) > MaxPromptBytes {
		return fmt.Errorf("%w: %d bytes, at most %d", ErrPromptInvalid, len(text), MaxPromptBytes)
	}
	for _, c := range text {
		if (unicode.IsControl(c) && c != '\n' && c != '\t') || unicode.Is(unicode.Cf, c) {
			return fmt.Errorf("%w: control or invisible character U+%04X", ErrPromptInvalid, c)
		}
	}
	sections, items, in, hasVerify := 0, 0, "", false
	for i, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			sections++
			in = strings.TrimRight(line, " \t")
			hasVerify = hasVerify || in == verifyHeading
		case strings.HasPrefix(line, "# ") && i == 0:
		case in == "" && strings.TrimSpace(line) != "":
			return fmt.Errorf("%w: line %d stands before the first section", ErrPromptInvalid, i+1)
		case in == verifyHeading && (strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ")) && strings.TrimSpace(line[2:]) != "":
			items++
		}
	}
	switch {
	case sections > MaxPromptSections:
		return fmt.Errorf("%w: %d sections, at most %d", ErrPromptInvalid, sections, MaxPromptSections)
	case !hasVerify:
		return fmt.Errorf("%w: no %s section", ErrPromptInvalid, verifyHeading)
	case items == 0:
		return fmt.Errorf("%w: %s lists no check", ErrPromptInvalid, verifyHeading)
	}
	return nil
}

// ValidateSkill validates an Agent Skill SKILL.md (non-empty UTF-8, <= 8192 B, no C0/C1/DEL
// control chars except \n and \t, no Unicode Cf chars).
func ValidateSkill(text string) error {
	if !utf8.ValidString(text) {
		return fmt.Errorf("%w: not UTF-8", ErrSkillInvalid)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%w: empty skill", ErrSkillInvalid)
	}
	if len(text) > MaxSkillBytes {
		return fmt.Errorf("%w: %d bytes, at most %d", ErrSkillInvalid, len(text), MaxSkillBytes)
	}
	for _, c := range text {
		if (unicode.IsControl(c) && c != '\n' && c != '\t') || unicode.Is(unicode.Cf, c) {
			return fmt.Errorf("%w: control or invisible character U+%04X", ErrSkillInvalid, c)
		}
	}
	return nil
}

// IsProtectedPath reports whether path lies under .github/agent-factory/upkeep/ or any bound
// Agent Skill directory .agents/skills/<skill>/.
func IsProtectedPath(path string, boundSkills []string) bool {
	if path == "" {
		return false
	}
	if path == strings.TrimSuffix(PromptDir, "/") || strings.HasPrefix(path, PromptDir) {
		return true
	}
	for _, sk := range boundSkills {
		if !config.ValidSkillName(sk) {
			continue
		}
		dir := SkillsDir + sk
		if path == dir || strings.HasPrefix(path, dir+"/") {
			return true
		}
	}
	return false
}

// LoadPrompt loads rt's prompt (defaulting to rt.DefaultPrompt only when the repo file is absent)
// and optional bound Agent Skill on the default branch. Invalid prompts, missing skills, and
// invalid skills fail closed.
func LoadPrompt(ctx context.Context, reader DefaultBranchReader, _ string, rt config.ResolvedRoutine) (LoadedPrompt, error) {
	out := LoadedPrompt{
		Source:     "routine",
		PromptText: rt.DefaultPrompt,
		PromptSHA:  sha256Hex(rt.DefaultPrompt),
	}
	if rt.RepoPrompt {
		p := PromptPath(rt.Name)
		if p == "" {
			return LoadedPrompt{}, fmt.Errorf("%w: invalid routine name %q", ErrPromptInvalid, rt.Name)
		}
		if reader == nil {
			return LoadedPrompt{}, errors.New("upkeep: default branch reader unavailable")
		}
		text, blobSHA, found, err := reader.DefaultBranchFile(ctx, p)
		if errors.Is(err, forge.ErrNotAFile) {
			return LoadedPrompt{}, fmt.Errorf("%w: %w", ErrPromptInvalid, err)
		}
		if err != nil {
			return LoadedPrompt{}, err
		}
		if found {
			if err := ValidatePrompt(text); err != nil {
				return LoadedPrompt{}, err
			}
			out.Source = "repo"
			out.Path = p
			out.PromptText = text
			out.PromptSHA = cmp.Or(blobSHA, sha256Hex(text))
		}
	}
	if rt.Skill != "" {
		skPath := SkillPath(rt.Skill)
		if skPath == "" {
			return LoadedPrompt{}, fmt.Errorf("%w: invalid skill name %q", ErrSkillInvalid, rt.Skill)
		}
		if reader == nil {
			return LoadedPrompt{}, errors.New("upkeep: default branch reader unavailable")
		}
		skText, skSHA, found, err := reader.DefaultBranchFile(ctx, skPath)
		if errors.Is(err, forge.ErrNotAFile) {
			return LoadedPrompt{}, fmt.Errorf("%w: %w", ErrSkillInvalid, err)
		}
		if err != nil {
			return LoadedPrompt{}, err
		}
		if !found {
			return LoadedPrompt{}, fmt.Errorf("%w: %s", ErrSkillMissing, skPath)
		}
		if err := ValidateSkill(skText); err != nil {
			return LoadedPrompt{}, err
		}
		out.SkillText = skText
		out.SkillSHA = cmp.Or(skSHA, sha256Hex(skText))
	}
	out.EffectiveSHA = EffectivePromptSHA(out.PromptSHA, out.SkillSHA)
	return out, nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
