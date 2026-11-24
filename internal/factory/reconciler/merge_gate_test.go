// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/decision"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/triage"
)

// declaredRig is mergeRig's task in a class that declares conditions (MC1): its gate reads the
// diff and the holdout, as dead-links' will.
func declaredRig(t *testing.T) *rig {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	var tk v1alpha1.Task
	if err := g.c.Get(t.Context(), keyFor("3buqdlot"), &tk); err != nil {
		t.Fatal(err)
	}
	tk.Spec.PredictedClass = "dead-links"
	if err := g.c.Update(t.Context(), &tk); err != nil {
		t.Fatal(err)
	}
	g.r.Cfg.Current().Repositories[0].Classes["dead-links"] = declaredClass()
	// MR1: the diff picks the policy, so dead-links is the only one that can hold on it.
	g.r.Cfg.Current().Repositories[0].Classes["docs-links"], g.r.Cfg.Current().Repositories[0].Classes["revert"] = config.Class{}, config.Class{}
	checks := green("SUCCESS")
	checks.Runs = append(checks.Runs, forge.Check{Name: "External links 🔗", State: "SUCCESS", App: forge.ActionsApp, Conclusion: "SUCCESS"})
	g.f.SetChecks(12, checks)
	g.f.SetFiles(map[string]string{testFile: "[x](https://example.com/old)"}, map[string]string{testFile: "[x](https://example.com/new)"})
	return g
}

// The second pass reads the diff for any class with a condition on it, and decides on GitHub's
// own list of the changed files.
func TestADeclaredClassIsDecidedOnItsDiff(t *testing.T) {
	g := declaredRig(t)
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAutoMerging || len(g.f.Armed()) != 1 {
		t.Fatalf("a clean retarget arms: %s %s", tk.Status.Phase, tk.Status.Reason)
	}
	g = declaredRig(t)
	g.f.SetFileStats(forge.FileStat{Path: testFile, Status: "modified", Additions: 15, Deletions: 6})
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != reasonNoPolicy ||
		len(g.f.Armed()) != 0 {
		t.Fatalf("21 lines refuse: %s %s", tk.Status.Phase, tk.Status.Reason)
	}
}

// maxPerDay counts only the class's own merges: another class's merge today leaves room.
func TestMaxPerDayCountsTheClassOnly(t *testing.T) {
	g := declaredRig(t)
	g.f.SetPR(forge.PR{Number: 9, State: "MERGED", MergedBy: "ogenki-agent-merger[bot]", MergedAt: now.Add(-time.Hour),
		Labels: []string{AutoMergedLabel("docs-links")}})
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAutoMerging {
		t.Fatalf("docs-links' merge is not dead-links': %s %s", tk.Status.Phase, tk.Status.Reason)
	}
	g = declaredRig(t)
	g.f.SetPR(forge.PR{Number: 9, State: "MERGED", MergedBy: "ogenki-agent-merger[bot]", MergedAt: now.Add(-time.Hour),
		Labels: []string{AutoMergedLabel("dead-links")}})
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Reason != "auto_merge_cap" {
		t.Fatalf("one dead-links merge today fills its day: %s %s", tk.Status.Phase, tk.Status.Reason)
	}
}

// M1: the daily caps count the factory's own merges only. A maintainer's merge of the class
// today, labelled factory/merged by the monitor, fills no day; the merger's does.
func TestTheDailyCapCountsAutoMergesOnly(t *testing.T) {
	g := declaredRig(t)
	g.f.SetPR(forge.PR{Number: 9, State: "MERGED", MergedBy: "Smana", MergedAt: now.Add(-time.Hour),
		Labels: []string{MergedLabel("dead-links")}})
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAutoMerging {
		t.Fatalf("a maintainer's merge is not the factory's: %s %s", tk.Status.Phase, tk.Status.Reason)
	}
	g = declaredRig(t)
	g.f.SetPR(forge.PR{Number: 9, State: "MERGED", MergedBy: "ogenki-agent-merger[bot]", MergedAt: now.Add(-time.Hour),
		Labels: []string{MergedLabel("dead-links"), AutoMergedLabel("dead-links")}})
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Reason != "auto_merge_cap" {
		t.Fatalf("the merger's merge today fills its day: %s %s", tk.Status.Phase, tk.Status.Reason)
	}
}

// A pending holdout waits in AwaitingCI, like CI itself.
func TestAPendingHoldoutWaits(t *testing.T) {
	g := declaredRig(t)
	checks := green("SUCCESS")
	checks.Runs = append(checks.Runs, forge.Check{Name: "External links 🔗", State: "PENDING", App: forge.ActionsApp})
	g.f.SetChecks(12, checks)
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingCI || tk.Status.Reason != "holdout_pending" {
		t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
	}
}

func TestPathFilteredHoldoutIsReadOnlyAfterTheDiffMatches(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.r.Cfg.Current().Repositories[0].Classes = map[string]config.Class{
		"filtered": {Live: true, AllowedFiles: []string{`^other/`}, HoldoutChecks: []string{"Path filtered"}},
	}
	checks := green("SUCCESS")
	checks.Runs = append(checks.Runs, forge.Check{Name: "Path filtered", State: "PENDING", App: forge.ActionsApp})
	g.f.SetChecks(12, checks)
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != reasonNoPolicy {
		t.Fatalf("%s %s, want the unrelated diff refused without waiting for its holdout", tk.Status.Phase, tk.Status.Reason)
	}
}

// A diff of a full page is unseen files: the task is handed off, not retried in AwaitingCI.
func TestAFullPageDiffRefusesPatchTooLarge(t *testing.T) {
	g := declaredRig(t)
	g.f.SetDiffError(fmt.Errorf("wrapped: %w", forge.ErrDiffTooLarge))
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != "patch_too_large" ||
		len(g.f.Armed()) != 0 {
		t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
	}
	g = declaredRig(t)
	g.f.SetDiffError(fmt.Errorf("wrapped: %w", forge.ErrNotAFile))
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != "diff_unreadable" ||
		len(g.f.Armed()) != 0 {
		t.Fatalf("unreadable file: %s %s", tk.Status.Phase, tk.Status.Reason)
	}
	g = declaredRig(t)
	g.f.SetDiffError(errors.New("boom"))
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "agent-system", Name: "3buqdlot"}}
	if _, err := g.r.Reconcile(t.Context(), req); err == nil || len(g.f.Armed()) != 0 {
		t.Fatalf("another Diff error stays an error, failing closed: %v", err)
	}
}

// Steering on, a class with no condition on its diff: the second pass still reads it, and a pull
// request that changes a steering note is handed to a human.
func TestSteeringReadsTheDiffOfAnyClass(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.r.Cfg.Current().Loops = map[string]config.Loop{"default": {Scope: config.ScopeSteering}}
	g.f.SetChecks(12, green("SUCCESS"))
	g.f.SetFileStats(forge.FileStat{Path: ".github/agent-factory/steering/docs.md", Status: "modified", Additions: 1})
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != "steering_path" ||
		len(g.f.Armed()) != 0 {
		t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
	}
}

// MR1: every shadow policy that holds records its own would-merge beside the decision, whether or
// not a live policy merged: one marker line each, after the decision's, and one latest decision
// each in the recorder.
func TestShadowRecordedForEachHoldingPolicy(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	predict(t, g, "review") // the label names neither policy: the diff picks both
	g.r.Decisions = &decision.Recorder{}
	g.r.Cfg.Current().Repositories[0].Classes = map[string]config.Class{
		"docs-links": {Live: true},
		"docs":       {Shadow: true, AllowedFiles: []string{`^.+\.md$`}, DiffShape: "links-only"},
	}
	g.f.SetChecks(12, green("SUCCESS"))
	g.f.SetFiles(map[string]string{testFile: "[x](https://example.com/old)"}, map[string]string{testFile: "[x](https://example.com/new)"})
	tk := g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseAutoMerging || tk.Status.PullRequest.Policy != "docs-links" {
		t.Fatalf("docs-links merges it: %s %s %q", tk.Status.Phase, tk.Status.Reason, tk.Status.PullRequest.Policy)
	}
	c := strings.Join(g.f.Comments(12), "\n")
	primary := strings.Index(c, "class=docs-links decision=arm:armed ")
	shadow := strings.Index(c, "class=docs decision=shadow:shadow_would_arm ")
	if primary < 0 || shadow < primary {
		t.Fatalf("two marker lines, the decision's first:\n%s", c)
	}
	latest := g.r.Decisions.Latest(tk.Spec.Repository)
	if latest["docs-links"].Verdict != "arm" || latest["docs"].Verdict != "shadow" {
		t.Fatalf("%+v", latest)
	}
}

// Review Focus 5: a diff no policy admits, on a task whose label names a shadow class, is
// no_policy_matched: never class_mismatch, and no shadow record for the labelled class.
func TestNoPolicyMatched(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	predict(t, g, "docs")
	g.r.Cfg.Current().Repositories[0].Classes = map[string]config.Class{"docs-links": {Live: true},
		"docs": {Shadow: true, AllowedFiles: []string{`^.+\.md$`}}, "revert": {}}
	g.f.SetChecks(12, green("SUCCESS"))
	g.f.SetFiles(map[string]string{"main.go": "package main\n"}, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	tk := g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseAwaitingHuman || tk.Status.Reason != reasonNoPolicy || tk.Status.PullRequest.Policy != "" {
		t.Fatalf("%s %s %q", tk.Status.Phase, tk.Status.Reason, tk.Status.PullRequest.Policy)
	}
	c := strings.Join(append(g.f.Comments(12), g.f.Comments(7)...), "\n")
	if strings.Contains(c, "class=docs decision=shadow") || !strings.Contains(c, "class=- decision=human:no_policy_matched ") ||
		!strings.Contains(c, " considered=docs:file_not_allowed,docs-links:class_mismatch ") {
		t.Fatalf("%s", c)
	}
	for _, m := range g.metrics.recorded {
		if strings.HasPrefix(m, "class_mismatch") {
			t.Fatalf("no label is a mismatch: %q", g.metrics.recorded)
		}
	}
}

// Review Focus 4 (MR12 + WF4): a task pinned workflow:quick runs solo (no reviewer); when its diff
// touches prose that only a live path-bound policy (docs) admits, the gate refuses docs with
// reviewer_verdict_missing and hands off as no_policy_matched, never arm.
func TestQuickWorkflowPathBoundPolicyNeverArms(t *testing.T) {
	workflows, templates, _ := config.ShippedWorkflows()
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	g.r.Cfg.Current().HarnessProfiles, g.r.Cfg.Current().Templates = workflows, templates
	g.r.Cfg.Current().Repositories[0].Classes = map[string]config.Class{
		"docs": {Live: true, AllowedFiles: []string{`^.+\.md$`}},
	}
	var tk v1alpha1.Task
	if err := g.c.Get(t.Context(), keyFor("3buqdlot"), &tk); err != nil {
		t.Fatal(err)
	}
	tk.Spec.PredictedClass = "review"
	tk.Spec.Template = workflows[config.WorkflowQuick].Template
	if err := g.c.Update(t.Context(), &tk); err != nil {
		t.Fatal(err)
	}
	tk.Status.Profile = &v1alpha1.ProfileStatus{Name: config.WorkflowQuick, Source: "label", Cohort: triage.CohortLabeled,
		Requested: config.WorkflowQuick, Resolved: true}
	if err := g.c.Status().Update(t.Context(), &tk); err != nil {
		t.Fatal(err)
	}
	g.f.SetChecks(12, green("SUCCESS"))
	g.f.SetFiles(map[string]string{testFile: "# Heading\n\nOld prose.\n"}, map[string]string{testFile: "# Heading\n\nNew prose.\n"})
	got := g.reconcile(t, "3buqdlot", 1)
	if got.Status.Phase != v1alpha1.PhaseAwaitingHuman || got.Status.Reason != reasonNoPolicy || len(g.f.Armed()) != 0 {
		t.Fatalf("%s %s armed=%v, want AwaitingHuman no_policy_matched", got.Status.Phase, got.Status.Reason, g.f.Armed())
	}
	c := strings.Join(append(g.f.Comments(12), g.f.Comments(7)...), "\n")
	if !strings.Contains(c, " considered=docs:reviewer_verdict_missing ") {
		t.Fatalf("missing considered=docs:reviewer_verdict_missing:\n%s", c)
	}
}

// #628: a no_policy_matched hand-off writes its decision record on the pull request, where the
// decision timeline and the evidence tally read it, with each evaluated policy's refusal in
// considered=, as a matched decision's record is.
func TestNoPolicyMatchedRecordsOnThePullRequest(t *testing.T) {
	g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
	predict(t, g, "docs")
	g.r.Cfg.Current().Repositories[0].Classes = map[string]config.Class{"docs-links": {Live: true},
		"docs": {Shadow: true, AllowedFiles: []string{`^.+\.md$`}}, "revert": {}}
	g.f.SetChecks(12, green("SUCCESS"))
	g.f.SetFiles(map[string]string{"main.go": "package main\n"}, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	if tk := g.reconcile(t, "3buqdlot", 1); tk.Status.Reason != reasonNoPolicy {
		t.Fatalf("%s %s", tk.Status.Phase, tk.Status.Reason)
	}
	// The timeline's own pattern (policyview cannot be imported here: it imports this package).
	record := regexp.MustCompile(`agent-factory-decision head=([^ ]+) class=([^ ]+) decision=([a-z_]+):([a-z_]+) (.*?) -->`)
	var reasons []string
	for _, body := range g.f.Comments(12) {
		if m := record.FindStringSubmatch(body); m != nil {
			reasons = append(reasons, m[4])
		}
	}
	if len(reasons) != 1 || reasons[0] != reasonNoPolicy {
		t.Fatalf("the pull request's records: %q\n%q", reasons, g.f.Comments(12))
	}
	if c := strings.Join(g.f.Comments(12), "\n"); !strings.Contains(c, " considered=docs:file_not_allowed,docs-links:class_mismatch ") {
		t.Fatalf("no considered= on the pull request:\n%s", c)
	}
}
