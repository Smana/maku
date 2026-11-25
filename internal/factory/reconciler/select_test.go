// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

// selectInputs is a pull request whose diff both docs-links (a shape, 20 lines) and docs (no
// shape, 100 lines) admit, both live: a one-line retarget of a link in a page, reviewed and
// approved at its head. The task's label names neither: the diff picks the policy (MR1).
func selectInputs() ArmInputs {
	cfg := armCfg()
	cfg.Repositories[0].Classes = map[string]config.Class{
		"docs-links": {Live: true, AllowedFiles: []string{`^.+\.md$`}, MaxModifiedLines: 20, MaxPerDay: 1},
		"docs":       {Live: true, AllowedFiles: []string{`^.+\.mdx?$`}, MaxModifiedLines: 100, MaxPerDay: 5},
	}
	tk := armTask("review", "pair", "approve")
	return ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: tk, Cfg: cfg, ReportedHead: head,
		Verifiers: []v1alpha1.RunRecord{{ID: "r3vnwr3v", Role: "reviewer", Trigger: "review", Verdict: "approve", HeadSHA: head}},
		Files:     changed("[x](https://example.com/old)", "[x](https://example.com/new)"), DiffRead: true,
		Stats: []forge.FileStat{{Path: testFile, Status: "modified", Additions: 1, Deletions: 1}}}
}

// The selection order (MR1): a shape first, then the lower line cap, then the lower file cap (a 0
// cap sorts after any positive one), then the name.
func TestStricterOrder(t *testing.T) {
	h := func(name string, cl config.Class) held { return held{Name: name, Class: cl} }
	for _, c := range []struct {
		name string
		a, b held
	}{
		{"a shape before none", h("docs-links", config.Class{DiffShape: "links-only", MaxModifiedLines: 20}), h("docs", config.Class{MaxModifiedLines: 100})},
		{"a shape before a lower cap", h("z", config.Class{DiffShape: "links-only", MaxModifiedLines: 200}), h("a", config.Class{MaxModifiedLines: 5})},
		{"two shapes by lines", h("z", config.Class{DiffShape: "links-only", MaxModifiedLines: 10}), h("a", config.Class{DiffShape: "semver-patch", MaxModifiedLines: 20})},
		{"equal lines by files", h("z", config.Class{MaxModifiedLines: 10, MaxPatchFiles: 2}), h("a", config.Class{MaxModifiedLines: 10, MaxPatchFiles: 3})},
		{"equal caps by name", h("a", config.Class{MaxModifiedLines: 10}), h("b", config.Class{MaxModifiedLines: 10})},
		{"a 0 line cap sorts last", h("z", config.Class{MaxModifiedLines: 500}), h("a", config.Class{})},
		{"a 0 file cap sorts last", h("z", config.Class{MaxPatchFiles: 40}), h("a", config.Class{})},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !stricter(c.a, c.b) || stricter(c.b, c.a) {
				t.Fatalf("%s must sort before %s", c.a.Name, c.b.Name)
			}
		})
	}
}

// The strictest live policy that holds decides, whatever the label (MR1).
func TestTheStrictestLivePolicyDecides(t *testing.T) {
	d := DecideArm(selectInputs())
	if d.Verdict != "arm" || d.Matched != "docs-links" {
		t.Fatalf("%+v, want arm under docs-links", d)
	}
}

// Review Focus 3: the strictest policy that holds is capped, or its breaker is open; the looser
// policy that also holds never takes over.
func TestNoFallThrough(t *testing.T) {
	in := selectInputs()
	in.ArmedByClass = map[string]int{"docs-links": 1}
	if d := DecideArm(in); d.Verdict != "human" || d.Reason != "auto_merge_cap" || d.Matched != "docs-links" {
		t.Fatalf("capped: %+v, want human auto_merge_cap under docs-links", d)
	}
	in = selectInputs()
	in.PausedByClass = map[string]bool{"docs-links": true}
	if d := DecideArm(in); d.Verdict != "human" || d.Reason != "class_paused" || d.Matched != "docs-links" {
		t.Fatalf("paused: %+v, want human class_paused under docs-links", d)
	}
}

func TestLivePolicyGuardsAreRepositoryWide(t *testing.T) {
	for name, test := range map[string]struct {
		guard  config.Class
		checks *forge.Check
		reason string
	}{
		"protected files": {config.Class{Live: true, AllowedFiles: []string{`^.+\.md$`}, MaxModifiedLines: 200,
			ProtectedFiles: []string{`website/content`}}, nil, "file_protected"},
		"approval subset": {config.Class{Live: true, AllowedFiles: []string{`^.+\.md$`}, MaxModifiedLines: 200,
			RequiresApproval: []config.ApprovalLeaf{{ChangedFiles: []string{`website/content`}}}}, nil, reasonApprovalRequired},
		"red holdout": {config.Class{Live: true, AllowedFiles: []string{`^.+\.md$`}, MaxModifiedLines: 200,
			HoldoutChecks: []string{"Policy guard"}}, &forge.Check{Name: "Policy guard", State: "FAILURE", Conclusion: "FAILURE", App: forge.ActionsApp}, "holdout_red"},
	} {
		t.Run(name, func(t *testing.T) {
			in := selectInputs()
			in.Cfg.Current().Repositories[0].Classes = map[string]config.Class{
				"docs":  {Live: true, AllowedFiles: []string{`^.+\.md$`}, MaxModifiedLines: 100},
				"guard": test.guard,
			}
			if test.checks != nil {
				in.Checks.Runs = append(in.Checks.Runs, *test.checks)
			}
			if d := DecideArm(in); d.Verdict != "human" || d.Reason != test.reason || d.Matched != "guard" {
				t.Fatalf("%+v, want human/%s under guard", d, test.reason)
			}
		})
	}
}

// A task with findings (a schedule's probe) or an adopted pull request is decided on its one
// policy, as before: no other policy is read, whatever would hold.
func TestOracleAndAdoptedKeepOnePolicy(t *testing.T) {
	in := selectInputs()
	in.Cfg.Current().Repositories[0].Classes["dead-links"] = config.Class{Live: true, AllowedFiles: []string{`^.+\.md$`},
		DiffShape: "links-only", Oracle: &config.Oracle{Probe: "dead-links"}}
	in.Task.Spec.PredictedClass = "dead-links"
	in.Task.Spec.Findings = []v1alpha1.Finding{{File: testFile, URL: "https://example.com/gone"}}
	d := DecideArm(in)
	if d.Matched != "dead-links" || d.Reason != "oracle_finding_open" {
		t.Fatalf("%+v, want dead-links alone, refused on its oracle", d)
	}
	// Without findings, an oracle policy is never a diff's candidate.
	in.Task.Spec.Findings = nil
	if d := DecideArm(in); d.Matched != "docs-links" {
		t.Fatalf("%+v: an oracle policy is chosen by the task, not the diff", d)
	}
}

// Approve-to-merge comes before any policy is read: an approved head arms whatever the diff.
func TestApproveToMergeStillFirst(t *testing.T) {
	in := selectInputs()
	in.Cfg.Current().Repositories[0].Merge.ApproveToMerge = true
	in.Cfg.Current().Repositories[0].Classes = map[string]config.Class{}
	in.HeadPushedAt = pushedAt
	in.PR.Reviews = []forge.Review{{ID: 1, Author: "Smana", State: "APPROVED", Commit: head, At: now.Add(time.Minute)}}
	if d := DecideArm(in); d.Verdict != "arm" || d.Reason != "maintainer_approved" {
		t.Fatalf("%+v, want arm maintainer_approved", d)
	}
}

func TestPauseStopsPoliciesButApproveToMergeStillWorks(t *testing.T) {
	in := selectInputs()
	in.Cfg.Current().Repositories[0].Merge.Paused = &config.MergePause{Reason: "Incident freeze"}
	in.Cfg.Current().Repositories[0].Classes = map[string]config.Class{"docs-links": {Shadow: true}}
	d := DecideArm(in)
	if d.Verdict != "human" || d.Reason != "paused" || d.Detail != "Incident freeze" {
		t.Fatalf("%+v, want paused", d)
	}
	in.Cfg.Current().Repositories[0].Merge.ApproveToMerge = true
	in.HeadPushedAt = pushedAt
	in.PR.Reviews = []forge.Review{{ID: 1, Author: "Smana", State: "APPROVED", Commit: head, At: now.Add(time.Minute)}}
	if d := DecideArm(in); d.Verdict != "arm" || d.Reason != "maintainer_approved" {
		t.Fatalf("%+v, want approve-to-merge to take precedence", d)
	}
}
