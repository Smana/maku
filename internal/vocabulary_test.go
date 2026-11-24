// SPDX-License-Identifier: Apache-2.0

package internal_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	reTierWord    = regexp.MustCompile(`(?i)\btier (light|standard|frontier)\b`)
	reProfileWord = regexp.MustCompile(`(?i)\bprofile\b`)
	reTriagerRole = regexp.MustCompile(`\(triager\)`)
)

// vocabularyAllow lists non-user-facing literals (API/JSON keys, action ids, event keys, internal
// log/error keys) that intentionally carry the word "profile". Each entry names its file and exact
// unquoted literal.
var vocabularyAllow = map[string]string{
	// Event / action / narration keys and internal identifiers kept for wire compatibility (WR-O4).
	"factory/narrate/narrate.go|profile-chosen-%d":    "narration outbox dedup key",
	"factory/narrate/narrate.go|profile-chosen-":      "narration pending key prefix",
	"factory/narrate/narrate.go|profile-escalated-%d": "narration outbox dedup key",
	"factory/narrate/narrate.go|profile-escalated-":   "narration pending key prefix",
	"factory/narrate/narrate.go|profile-refused":      "narration outbox dedup key",
	"slackapp/proposal.go|factory_proposal_profile":   "Slack action_id kept for Release A select compatibility",
	"slackapp/pin.go|factory_proposal_profile":        "Slack action_id",
	"slackapp/app.go|profile":                         "internal key",
	"maku/auth.go|profile":                            "OIDC standard scope name",
	"maku/start.go|profile":                           "JSON key on plan issue",
	"roomview/card.go|profile":                        "internal key",
}

func TestHumanVisibleVocabulary(t *testing.T) {
	dirs := []string{
		"slackapp",
		"roomview",
		"factory/narrate",
		"maku",
	}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			rel := filepath.ToSlash(path)
			ast.Inspect(f, func(n ast.Node) bool {
				// Skip struct field tags (JSON/YAML wire keys).
				if _, ok := n.(*ast.Field); ok {
					return true
				}
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil || val == "" {
					return true
				}
				// Skip struct tags (`json:"..."`).
				if strings.Contains(val, `json:"`) || strings.Contains(val, `yaml:"`) {
					return true
				}
				if _, allowed := vocabularyAllow[rel+"|"+val]; allowed {
					return true
				}
				pos := fset.Position(lit.Pos())
				if reTierWord.MatchString(val) {
					t.Errorf("%s:%d: user-facing tier literal %q", rel, pos.Line, val)
				}
				if reTriagerRole.MatchString(val) {
					t.Errorf("%s:%d: user-facing (triager) literal %q", rel, pos.Line, val)
				}
				if reProfileWord.MatchString(val) {
					// Ignore closed profile_* API codes.
					cleaned := regexp.MustCompile(`profile_[a-z_]+`).ReplaceAllString(val, "")
					if reProfileWord.MatchString(cleaned) {
						t.Errorf("%s:%d: user-facing profile literal %q", rel, pos.Line, val)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}
