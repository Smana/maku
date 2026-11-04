// SPDX-License-Identifier: Apache-2.0

package filefence

import (
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

func TestRefusal(t *testing.T) {
	cl := config.Class{AllowedFiles: []string{`apps/.+\.yaml`}, ProtectedFiles: []string{`^apps/secret/`}, ModifyOnly: true,
		MaxPatchFiles: 2, MaxModifiedLines: 4}
	m := func(p string, n int) forge.FileStat {
		return forge.FileStat{Path: p, Status: "modified", Additions: n, Deletions: n}
	}
	for name, c := range map[string]struct {
		stats []forge.FileStat
		want  string
	}{
		"inside":    {[]forge.FileStat{m("apps/a.yaml", 1)}, ""},
		"outside":   {[]forge.FileStat{m("mise.toml", 1)}, "file_not_allowed"},
		"anchored":  {[]forge.FileStat{m("x/apps/a.yaml", 1)}, "file_not_allowed"},
		"protected": {[]forge.FileStat{m("apps/secret/a.yaml", 1)}, "file_protected"},
		"added":     {[]forge.FileStat{{Path: "apps/a.yaml", Status: "added"}}, "file_added_or_deleted"},
		"files":     {[]forge.FileStat{m("apps/a.yaml", 1), m("apps/b.yaml", 1), m("apps/c.yaml", 1)}, "patch_too_large"},
		"lines":     {[]forge.FileStat{m("apps/a.yaml", 3)}, "patch_too_large"},
		"a rename":  {[]forge.FileStat{{Path: "apps/a.yaml", Previous: "mise.toml", Status: "modified"}}, "file_not_allowed"},
	} {
		if got := Refusal(cl, c.stats); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestInferOnTheHostsClasses(t *testing.T) {
	deps := `^(apps|flux|infrastructure|observability|security|tooling)/.+\.ya?ml$`
	adopt := &config.Author{Adopt: &config.Adopt{}}
	classes := func() map[string]config.Class {
		return map[string]config.Class{
			"alert-rules": {AllowedFiles: []string{`^observability/.+\.ya?ml$`}},
			"deps-patch":  {AllowedFiles: []string{deps}, Author: adopt},
			"deps-minor":  {AllowedFiles: []string{deps}, Author: adopt},
			"dead-links":  {AllowedFiles: []string{`^(docs|website/content)/.+\.md$`}, Oracle: &config.Oracle{}},
			"docs-links":  {}, "docs": {}, "review": {}, "revert": {},
		}
	}
	obs := classes()
	obs["obs"] = config.Class{AllowedFiles: []string{`^observability/.+$`}}
	live := classes()
	live["alert-rules"] = config.Class{AllowedFiles: []string{`^observability/.+\.ya?ml$`}, Live: true}
	obsProtected := classes()
	obsProtected["obs"] = config.Class{AllowedFiles: []string{`^observability/.+$`}, ProtectedFiles: []string{`^observability/`}}
	for name, c := range map[string]struct {
		classes map[string]config.Class
		paths   []string
		want    string
	}{
		"alert rules":          {classes(), []string{"observability/base/rules/apps.yaml", "observability/aws-0/rules.yml"}, "alert-rules"},
		"one outside":          {classes(), []string{"observability/base/rules/apps.yaml", "docs/alerts.md"}, "review"},
		"none":                 {classes(), nil, "review"},
		"the oracle class's":   {classes(), []string{"docs/a.md"}, "review"},
		"the adopted classes'": {classes(), []string{"apps/x.yaml"}, "review"},
		"a live class":         {live, []string{"observability/base/rules/apps.yaml"}, "review"},
		"two match":            {obs, []string{"observability/a.yaml"}, "review"},
		"protected breaks tie": {obsProtected, []string{"observability/a.yaml"}, "alert-rules"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Infer(c.classes, c.paths); got != c.want {
				t.Fatalf("Infer = %q, want %q", got, c.want)
			}
		})
	}
}
