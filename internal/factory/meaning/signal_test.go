// SPDX-License-Identifier: Apache-2.0

package meaning_test

import (
	"slices"
	"testing"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/meaning"
)

var adopt = &config.Author{Adopt: &config.Adopt{Logins: []string{"renovate[bot]"}, Branches: []string{"^renovate/"}}}

func TestOf(t *testing.T) {
	for _, c := range []struct {
		name  string
		cl    config.Class
		want  meaning.Signal
		prose bool
	}{
		{"docs-links", config.Class{ModifyOnly: true, MaxModifiedLines: 20}, meaning.LinksOnly, false}, // built-in shape
		{"dead-links", config.Class{DiffShape: "links-only", Oracle: &config.Oracle{Probe: "dead-links", MaxFindings: 8}}, meaning.Oracle, false},
		{"deps-patch", config.Class{DiffShape: "semver-patch", Author: adopt}, meaning.SemverPatch, false},
		{"deps-minor", config.Class{DiffShape: "semver-minor", Author: adopt}, meaning.SemverMinor, false},
		{"docs", config.Class{AllowedFiles: []string{`^.+\.mdx?$`}}, meaning.None, true},
		{"kb-entries", config.Class{DiffShape: "added-entry", AllowedFiles: []string{`^kb/.+\.md$`}}, meaning.None, true},
		{"alert-rules", config.Class{AllowedFiles: []string{`^observability/.+\.ya?ml$`}, Requires: []string{"plan"}}, meaning.None, false},
		{"alert-rules", config.Class{AllowedFiles: []string{`^observability/.+\.ya?ml$`}, Requires: []string{"plan", "tests"}}, meaning.Contract, false},
		{"tests-only", config.Class{Requires: []string{"tests"}}, meaning.None, false}, // decision 4: no plan, no signal
		{"narrowed", config.Class{AllowedFiles: []string{`^docs/.+$`}, NarrowedFiles: []string{`^docs/.+\.rst$`}}, meaning.None, true},
	} {
		if got := meaning.Of(c.name, c.cl); got != c.want {
			t.Errorf("Of(%s) = %q, want %q", c.name, got, c.want)
		}
		if got := meaning.Prose(c.cl); got != c.prose {
			t.Errorf("Prose(%s) = %v, want %v", c.name, got, c.prose)
		}
	}
}

func TestRecord(t *testing.T) {
	for _, c := range []struct {
		s    meaning.Signal
		held bool
		want string
	}{
		{meaning.LinksOnly, false, "links-only:ok"},
		{meaning.Oracle, false, "oracle:ok"},
		{meaning.Contract, true, "contract:ok"},
		{meaning.Contract, false, "none"}, // no contract on the task: nothing judged the meaning
		{meaning.None, true, "none"},
	} {
		if got := meaning.Record(c.s, c.held); got != c.want {
			t.Errorf("Record(%q, %v) = %q, want %q", c.s, c.held, got, c.want)
		}
	}
}

func TestFiles(t *testing.T) {
	for in, want := range map[string]string{
		`^website/content/.+\.mdx?$`:      "Markdown under `website/content/`",
		`^(docs|website/content)/.+\.md$`: "Markdown under `docs/` or `website/content/`",
		`^README\.md$`:                    "`README.md`",
		`^.+\.mdx?$`:                      "Markdown files",
		`^observability/.+\.ya?ml$`:       "YAML under `observability/`",
		`^docs/`:                          "files under `docs/`",
	} {
		if got := meaning.Files([]string{in}); got != want {
			t.Errorf("Files(%q) = %q, want %q", in, got, want)
		}
	}
	if got := meaning.Files([]string{`^a/.+\.md$`, `^b/.+\.md$`}); got != "Markdown under `a/` or Markdown under `b/`" {
		t.Errorf("two patterns: %q", got)
	}
	if got := meaning.Files(nil); got != "" {
		t.Errorf("no pattern: %q", got)
	}
}

// The envelope carries a signal to the room and Slack from its own copy of the closed list.
func TestTheEnvelopeKnowsEverySignal(t *testing.T) {
	var want []string
	for _, s := range meaning.Signals() {
		want = append(want, string(s))
	}
	if got := envelope.Signals(); !slices.Equal(got, want) {
		t.Fatalf("envelope %v, meaning %v", got, want)
	}
}
