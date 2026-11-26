// SPDX-License-Identifier: Apache-2.0

package meaning_test

import (
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/meaning"
)

var alertYAML = []string{`^observability/.+\.ya?ml$`}

func withMerge(in meaning.Input) meaning.Input {
	in.VerifyFor, in.RevertWindow, in.AutoRevert = 30*time.Minute, 7*24*time.Hour, true
	return in
}

func dur(d time.Duration) config.Duration { return config.Duration{Duration: d} }

func TestBuild(t *testing.T) {
	plan2 := meaning.Rules{UncheckedStaysShadow: true, AuditEvery: 5, WrongLabel: true}
	docs := config.Class{AllowedFiles: []string{`^website/content/.+\.mdx?$`}, MaxPatchFiles: 5, MaxModifiedLines: 60, MaxPerDay: 2}
	for _, c := range []struct {
		name string
		in   meaning.Input
		want string
	}{
		{"docs-links live", withMerge(meaning.Input{Name: "docs-links", Mode: "live", Class: config.Class{ModifyOnly: true, MaxModifiedLines: 20,
			HoldoutChecks: []string{"Check the documentation links 🔗"}}}),
			"**Merges without you when** an agent changes only link targets in Markdown, at most 20 lines, and `Check the documentation links 🔗` passes. " +
				"**Meaning checked by** the `links-only` shape: every changed line is a link target on the same host. **Not checked:** what the new page says. " +
				"**After the merge** Maku watches main for 30 minutes and reverts on red. **Undo:** `factory/revert` within 7 days."},
		{"docs shadow, plan 1", meaning.Input{Name: "docs", Mode: "shadow", Class: docs},
			"**Would merge without you when** an agent changes Markdown under `website/content/`, at most 5 files and 60 lines, 2 a day, and a reviewer approves. " +
				"**Meaning checked by** nothing: this is prose."},
		{"docs shadow, plan 2", meaning.Input{Name: "docs", Mode: "shadow", Rules: plan2, Class: docs},
			"**Would merge without you when** an agent changes Markdown under `website/content/`, at most 5 files and 60 lines, 2 a day, and a reviewer approves. " +
				"**Meaning checked by** nothing: this is prose. **Once live** Maku asks you about every fifth one."},
		{"alert-rules today, plan 1", meaning.Input{Name: "alert-rules", Mode: "shadow", Class: config.Class{AllowedFiles: alertYAML, Requires: []string{"plan"}}},
			"**Would merge without you when** an agent changes YAML under `observability/` and a reviewer approves. " +
				"**Meaning checked by** nothing. **Not checked:** what a change means. **To have its meaning checked,** require a plan and tests."},
		{"alert-rules today, plan 2", meaning.Input{Name: "alert-rules", Mode: "shadow", Rules: plan2, Class: config.Class{AllowedFiles: alertYAML, Requires: []string{"plan"}}},
			"**Never merges without you:** nothing checks what a change to YAML under `observability/` means. Maku records when it would have merged. " +
				"**To let it merge alone,** require a plan and tests."},
		{"alert-rules with a contract", withMerge(meaning.Input{Name: "alert-rules", Mode: "live", Class: config.Class{AllowedFiles: alertYAML, Requires: []string{"plan", "tests"},
			Monitor: &config.Monitor{Verification: &config.VerificationWatch{Within: dur(2 * time.Hour)}, FollowUpWindow: dur(2 * time.Hour)}}}),
			"**Merges after you approve the plan, without a review of the diff, when** an agent changes YAML under `observability/` and a reviewer approves. " +
				"**Meaning checked by** the test contract: red before the change, green after. **Not checked:** behaviour the plan did not name. " +
				"**After the merge** Maku watches main for 30 minutes and reverts on red; waits 2 hours for RunLore's verdict; reads follow-up fixes for 2 hours. " +
				"**Undo:** `factory/revert` within 7 days."},
		{"deps-patch live", withMerge(meaning.Input{Name: "deps-patch", Mode: "live", Class: config.Class{Author: adopt, DiffShape: "semver-patch", ModifyOnly: true,
			MaxPatchFiles: 3, MaxModifiedLines: 10, MaxPerDay: 2, ReleaseAgeCheck: "renovate/stability-days",
			Monitor: &config.Monitor{Flux: &config.FluxWatch{Within: dur(time.Hour)}, FollowUpWindow: dur(72 * time.Hour)}}}),
			"**Merges without you when** `renovate[bot]` moves only a patch version, at most 3 files and 10 lines, 2 a day, and `renovate/stability-days` passes. " +
				"**Meaning checked by** the `semver-patch` shape: only a patch version moves. **Not checked:** the release itself; Maku waits for its release age. " +
				"**After the merge** Maku watches main for 30 minutes and reverts on red; watches Flux for 1 hour; reads follow-up fixes for 72 hours. " +
				"**Undo:** `factory/revert` within 7 days."},
		{"deps-minor shadow", meaning.Input{Name: "deps-minor", Mode: "shadow", Class: config.Class{Author: adopt, DiffShape: "semver-minor", ModifyOnly: true}},
			"**Would merge without you when** `renovate[bot]` moves only a minor or patch version. " +
				"**Meaning checked by** the `semver-minor` shape: only a minor or patch version moves. **Not checked:** the release itself, and what the new version renders. This policy is shadow only."},
		{"dead-links", meaning.Input{Name: "dead-links", Mode: "shadow", Class: config.Class{DiffShape: "links-only", ModifyOnly: true,
			Oracle: &config.Oracle{Probe: "dead-links", MaxFindings: 8}, HoldoutChecks: []string{"links"}}},
			"**Would merge without you when** an agent fixes only the links a detector reported in Markdown and `links` passes. " +
				"**Meaning checked by** a detector: the `dead-links` probe named the broken links, and the diff changes exactly those. **Not checked:** the detector's own errors."},
		{"opens a revert without autoRevert", meaning.Input{Name: "docs-links", Mode: "live", VerifyFor: 30 * time.Minute},
			"**Merges without you when** an agent changes only link targets in Markdown. " +
				"**Meaning checked by** the `links-only` shape: every changed line is a link target on the same host. **Not checked:** what the new page says. " +
				"**After the merge** Maku watches main for 30 minutes and opens a revert on red."},
		{"off", meaning.Input{Name: "docs", Mode: "disabled", Class: config.Class{AllowedFiles: []string{`^.+\.md$`}}},
			"**Merges nothing here:** the policy is off in this repository."},
	} {
		if got := meaning.Build(c.in).Text; got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestBuildParts(t *testing.T) {
	s := meaning.Build(meaning.Input{Name: "alert-rules", Mode: "shadow", Class: config.Class{AllowedFiles: alertYAML}})
	if s.Signal != meaning.None || s.Prose || s.CheckedBy != "nothing" || s.WayOut == "" {
		t.Fatalf("%+v", s)
	}
	p := meaning.Build(meaning.Input{Name: "docs", Mode: "live", Class: config.Class{AllowedFiles: []string{`^.+\.md$`}}})
	if !p.Prose || p.NotChecked != "" || p.OnceLive != "" {
		t.Fatalf("plan 1 prose must not promise an audit: %+v", p)
	}
}

func TestSignalLine(t *testing.T) {
	for _, c := range []struct {
		in   meaning.Input
		want string
	}{
		{meaning.Input{Name: "docs-links", Mode: "live"}, "Meaning checked by the `links-only` shape."},
		{meaning.Input{Name: "x", Mode: "shadow", Class: config.Class{AllowedFiles: alertYAML, Requires: []string{"plan", "tests"}}}, "Meaning checked by the test contract."},
		{meaning.Input{Name: "docs", Mode: "live", Class: config.Class{AllowedFiles: []string{`^.+\.md$`}}}, "Meaning not checked: this is prose."},
		{meaning.Input{Name: "x", Mode: "shadow", Class: config.Class{AllowedFiles: alertYAML}}, "Meaning not checked."},
	} {
		if got := meaning.Build(c.in).SignalLine(); got != c.want {
			t.Errorf("%s: %q, want %q", c.in.Name, got, c.want)
		}
	}
}

func TestStages(t *testing.T) {
	if got := meaning.Stages([]string{"plan", "tests", "judge", "review"}); got != "a plan you approve, tests written before the change, a completion judge and a review" {
		t.Error(got)
	}
	if got := meaning.Stages([]string{"plan"}); got != "a plan you approve" {
		t.Error(got)
	}
}
