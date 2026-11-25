// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/decision"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/steering"
)

// head is the pull request's head at the decision and oldHead a head it was before: full
// 40-character shas, which is what the room reports (R52).
var (
	head    = strings.Repeat("a", 40)
	oldHead = strings.Repeat("b", 40)
)

var required = []string{"Pre-commit checks", "Kubernetes validation"}

const testFile = "website/content/docs/a.md"

func armCfg() *config.Config {
	return &config.Config{AgentsLogin: "ogenki-agents[bot]",
		Templates: map[string]config.Template{"solo": {Roles: []string{"implementer"}}, "pair": {Roles: []string{"implementer", "reviewer"}}},
		Repositories: []config.Repository{{Maintainers: []string{"Smana"},
			Classes: map[string]config.Class{"docs-links": {Live: true}, "revert": {}, "docs": {}},
			Merge:   config.Merge{RequiredChecks: required, LeakScanCheck: "Security scanning", PolicyBotLogin: "ogenki-merge-gate[bot]", PolicyBot: new(true), GateStatus: new(false), MaxPerDay: 10}}}}
}

func green(policy string) forge.Checks {
	return forge.Checks{Runs: []forge.Check{
		{Name: "Pre-commit checks", State: "SUCCESS", App: forge.ActionsApp, Conclusion: "SUCCESS"},
		{Name: "Kubernetes validation", State: "SUCCESS", App: forge.ActionsApp, Conclusion: "SUCCESS"},
	},
		Statuses: []forge.Status{{Context: "policy-bot: main", State: policy, Creator: "ogenki-merge-gate[bot]"}}}
}

func armTask(class, template, verdict string) *v1alpha1.Task {
	first, last := metav1.NewTime(now.Add(-50*time.Minute)), metav1.NewTime(now.Add(-30*time.Minute))
	return &v1alpha1.Task{Spec: v1alpha1.TaskSpec{PredictedClass: class, Template: template},
		Status: v1alpha1.TaskStatus{Verdict: verdict, Runs: []v1alpha1.RunRecord{
			{ID: "7f3cq2xz", Role: "implementer", Trigger: "initial", Finished: &first},
			{ID: "3buqdlot", Role: "implementer", Trigger: "review", Finished: &last}}}}
}

// pushedAt is when GitHub recorded the push of head in the rigs' activity: between armTask's two
// implementer runs' ends, so within the task's runs.
var pushedAt = now.Add(-40 * time.Minute)

func agentPR() forge.PR {
	return forge.PR{Author: "ogenki-agents[bot]", BaseRef: "main", HeadSHA: head, HeadMessage: "docs: fix\n\nAgent-Run: 7f3cq2xz"}
}

// changed is the forge.Files read of a diff of one file.
func changed(base, headText string) struct{ Base, Head map[string]string } {
	return struct{ Base, Head map[string]string }{
		Base: map[string]string{testFile: base}, Head: map[string]string{testFile: headText}}
}

// The amendment (external reviews R02, R03; ruling R52), written before the brief's table
// because it supersedes that table's trailer-only rule: the head must be the commit a run of
// the task reported, a verifier's approve must be of that head, and the legitimate path
// decides but is not armed while docs-links is shadow (R32).
func TestHeadAndVerdictMustBeFresh(t *testing.T) {
	cfg := armCfg()
	cfg.Repositories[0].Classes["docs-links"] = config.Class{Shadow: true}
	approve := func(sha string) []v1alpha1.RunRecord {
		return []v1alpha1.RunRecord{{ID: "r3vnwr3v", Role: "reviewer", Trigger: "review", Verdict: "approve", HeadSHA: sha}}
	}
	for name, c := range map[string]struct {
		task            *v1alpha1.Task
		reported        string
		verifs          []v1alpha1.RunRecord
		verdict, reason string
	}{
		// R02: run D pushed H with A's trailer. The trailer is one of the task's own runs,
		// so the old rule would arm it — but no handoff ever named H: the room last reported
		// another commit, and an unreported head is never armed.
		"the pushed head was never reported": {armTask("docs-links", "solo", ""), oldHead, nil, "human", "head_unreported"},
		"no head was reported at all":        {armTask("docs-links", "solo", ""), "", nil, "human", "head_unreported"},
		// R03: the reviewer approved H1; H2 was pushed before the decision.
		"the approve is of a superseded head": {armTask("docs-links", "pair", "approve"), head, approve(oldHead), "human", "verdict_stale"},
		// The legitimate path, for a pair (a fresh approve) and a solo (docs-links is solo;
		// nothing verifies it): decided exactly like a live class, and shadow says would arm.
		"legitimate pair": {armTask("docs-links", "pair", "approve"), head, approve(head), "shadow", "shadow_would_arm"},
		"legitimate solo": {armTask("docs-links", "solo", ""), head, nil, "shadow", "shadow_would_arm"},
	} {
		t.Run(name, func(t *testing.T) {
			d := DecideArm(ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: c.task, Cfg: cfg,
				ReportedHead: c.reported, Verifiers: c.verifs})
			matched := "" // an evidence row refuses before any policy is read (MR1)
			if c.verdict == "shadow" {
				matched = "docs-links"
			}
			if d.Verdict != c.verdict || d.Reason != c.reason || d.Matched != matched {
				t.Fatalf("got %+v, want %s/%s/%s", d, c.verdict, c.reason, matched)
			}
		})
	}
}

// Demo: with policyBot false there is no policy-bot to read, so a green PR is not policy_absent.
func TestDecideArmWithoutPolicyBot(t *testing.T) {
	off, on := false, true
	noStatus := green("SUCCESS")
	noStatus.Statuses = nil
	for name, c := range map[string]struct {
		bot    *bool
		checks forge.Checks
		reason string
	}{
		"off, no status":     {&off, noStatus, ""},
		"off, status red":    {&off, green("ERROR"), ""},
		"on, no status":      {&on, noStatus, "policy_absent"},
		"default, no status": {nil, noStatus, ""}, // v4 step 8: unset is off (D11)
	} {
		t.Run(name, func(t *testing.T) {
			cf := armCfg()
			cf.Repositories[0].Merge.PolicyBot = c.bot
			d := DecideArm(ArmInputs{PR: agentPR(), Checks: c.checks, Task: armTask("docs-links", "solo", ""), Cfg: cf,
				ReportedHead: head, Files: changed("[x](old.md)", "[x](new.md)")})
			if d.Reason != c.reason {
				t.Fatalf("got %+v, want reason %q", d, c.reason)
			}
		})
	}
}

func TestDecideArm(t *testing.T) {
	approved := agentPR()
	approved.Reviews = []forge.Review{{Author: "Smana", State: "APPROVED"}}
	foreign := agentPR()
	foreign.HeadMessage = "docs: sneak\n\nAgent-Run: zzzzzzzz"
	byOwner := agentPR()
	byOwner.Author = "Smana"
	red := green("SUCCESS")
	red.Runs[1].State = "FAILURE"
	errored := green("SUCCESS")
	errored.Runs[1].State = "ERROR" // a state the merger's rollup never emits: recognized or not, it is not green
	pending := green("SUCCESS")
	pending.Runs = pending.Runs[:1] // a required check has not reported yet
	forged := green("SUCCESS")
	forged.Statuses[0].Creator = "ogenki-agents[bot]"
	scanRed := green("SUCCESS")
	scanRed.Runs = append(scanRed.Runs, forge.Check{Name: "Security scanning", State: "FAILURE"})
	scanSkipped := green("SUCCESS")
	scanSkipped.Runs = append(scanSkipped.Runs, forge.Check{Name: "Security scanning", State: "SUCCESS", Conclusion: "SKIPPED"})
	for name, c := range map[string]struct {
		in                       ArmInputs
		verdict, reason, matched string
	}{
		"all conditions": {ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""),
			ReportedHead: head, Files: changed("[x](old.md)", "[x](new.md)")}, "arm", "", "docs-links"},
		// v2 D7: every other condition holds, but the fence names another factory: a human merges.
		"not this factory's repository": {ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""),
			ReportedHead: head, Files: changed("[x](old.md)", "[x](new.md)"), NotOwned: true}, "human", "not_owned", ""},
		"CI red":              {ArmInputs{PR: agentPR(), Checks: red, Task: armTask("docs-links", "solo", "")}, "ci_red", "ci_red", ""},
		"CI errored":          {ArmInputs{PR: agentPR(), Checks: errored, Task: armTask("docs-links", "solo", "")}, "ci_red", "ci_red", ""},
		"CI still running":    {ArmInputs{PR: agentPR(), Checks: pending, Task: armTask("docs-links", "solo", "")}, "wait", "ci_pending", ""},
		"secret scan red":     {ArmInputs{PR: agentPR(), Checks: scanRed, Task: armTask("docs-links", "solo", "")}, "escalate", "secret_scan_red", ""},
		"secret scan skipped": {ArmInputs{PR: agentPR(), Checks: scanSkipped, Task: armTask("docs-links", "solo", "")}, "escalate", "secret_scan_red", ""},
		"gate path":           {ArmInputs{PR: agentPR(), Checks: green("ERROR"), Task: armTask("docs-links", "solo", "")}, "human", "gate_path", "gate"},
		"diff not the class":  {ArmInputs{PR: agentPR(), Checks: green("PENDING"), Task: armTask("docs-links", "solo", "")}, "human", "policy_pending", "review"},
		"a forged status":     {ArmInputs{PR: agentPR(), Checks: forged, Task: armTask("docs-links", "solo", "")}, "human", "policy_absent", ""},
		"no policy matched": {ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: armTask("docs", "pair", "approve"),
			ReportedHead: head, Files: changed("text.", "text, reworded."), DiffRead: true}, "human", "no_policy_matched", ""},
		"maintainer approved":  {ArmInputs{PR: approved, Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", "")}, "human", "maintainer_approved", "review"},
		"not an agent's PR":    {ArmInputs{PR: byOwner, Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", "")}, "human", "not_agent_authored", ""},
		"no approving verdict": {ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: armTask("docs-links", "pair", "none")}, "human", "no_approving_verdict", ""},
		// R52, in table form: each gate is the reason, not the trailer's match.
		"SC-14 foreign trailer": {ArmInputs{PR: foreign, Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""),
			ReportedHead: head}, "human", "foreign_trailer", ""},
		"the head was never reported": {ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", "")}, "human", "head_unreported", ""},
		"an approve of a superseded head": {ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: armTask("docs-links", "pair", "approve"),
			ReportedHead: head, Verifiers: []v1alpha1.RunRecord{{ID: "r3vnwr3v", Role: "reviewer", Trigger: "review", Verdict: "approve", HeadSHA: oldHead}}},
			"human", "verdict_stale", ""},
		"docs-links moved beyond links": {ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""),
			ReportedHead: head, Files: changed("text.", "text <script>alert(1)</script>."), DiffRead: true}, "human", "no_policy_matched", ""},
		"revert moved beyond links": {ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: armTask("revert", "solo", ""),
			ReportedHead: head, Files: changed("The link is broken.", "The link has rotted."), DiffRead: true}, "human", "no_policy_matched", ""},
		"class paused": {ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""),
			ReportedHead: head, Paused: true}, "human", "class_paused", "docs-links"},
		"daily cap": {ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""),
			ReportedHead: head, ArmedToday: 10}, "human", "auto_merge_cap", "docs-links"},
	} {
		t.Run(name, func(t *testing.T) {
			c.in.Cfg = armCfg()
			d := DecideArm(c.in)
			if d.Verdict != c.verdict || d.Reason != c.reason || d.Matched != c.matched {
				t.Errorf("%s: got %+v, want %s/%s/%s", name, d, c.verdict, c.reason, c.matched)
			}
		})
	}
}

// R32 (owner, 2026-09-27): before the wave a class is shadow, decided exactly like a live one and
// never armed; a foreign trailer is never even "would arm".
func TestAShadowClassWouldArmAndNeverArms(t *testing.T) {
	cfg := armCfg()
	cfg.Repositories[0].Classes["docs-links"] = config.Class{Shadow: true}
	if d := DecideArm(ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""),
		Cfg: cfg, ReportedHead: head}); d.Verdict != "shadow" || d.Reason != "shadow_would_arm" || d.Matched != "docs-links" {
		t.Fatalf("%+v", d)
	}
	foreign := agentPR()
	foreign.HeadMessage = "docs: sneak\n\nAgent-Run: zzzzzzzz"
	if d := DecideArm(ArmInputs{PR: foreign, Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""),
		Cfg: cfg, ReportedHead: head}); d.Reason != "foreign_trailer" {
		t.Fatalf("%+v", d)
	}
}

// Human path AM4: a shadow class arms nothing, so the breaker and the caps block nothing: it
// records shadow_would_arm and the decision says what a live class would have met.
func TestAShadowDecisionIgnoresThePauseAndTheCaps(t *testing.T) {
	cfg := armCfg()
	cfg.Repositories[0].Classes["docs-links"] = config.Class{Shadow: true}
	cfg.Repositories[0].Merge.MaxPerDay = 1
	d := DecideArm(ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""), Cfg: cfg,
		ReportedHead: head, Paused: true, ArmedToday: 1, Files: changed("[a](https://x/old)", "[a](https://x/new)"), DiffRead: true})
	if d.Verdict != "shadow" || d.Reason != "shadow_would_arm" || !d.Paused || !d.Capped {
		t.Fatalf("%+v", d)
	}
	cfg.Repositories[0].Classes["docs-links"] = config.Class{Live: true}
	if d := DecideArm(ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""), Cfg: cfg,
		ReportedHead: head, Paused: true, Files: changed("[a](https://x/old)", "[a](https://x/new)"), DiffRead: true}); d.Reason != "class_paused" {
		t.Fatalf("a live class is paused: %+v", d)
	}
}

// The merge gate reads the task's repository (v2 D1): the same PR arms on A, where docs-links is
// live, would only on B, where it is shadow, and waits on C, whose own required check has not run.
func TestDecideArmReadsTheTasksRepository(t *testing.T) {
	cfg := armCfg()
	a := config.Repository{Name: "Smana/a", Maintainers: cfg.Repositories[0].Maintainers, Classes: cfg.Repositories[0].Classes, Merge: cfg.Repositories[0].Merge}
	b := a
	b.Name, b.Classes = "Smana/b", map[string]config.Class{"docs-links": {Shadow: true}, "revert": {}}
	strict := b
	strict.Name, strict.Merge.RequiredChecks = "Smana/c", append(slices.Clone(required), "B's own check")
	cfg.Repositories = []config.Repository{a, b, strict}
	for repo, want := range map[string]string{"Smana/a": "arm", "Smana/b": "shadow", "Smana/c": "wait"} {
		tk := armTask("docs-links", "solo", "")
		tk.Spec.Repository = repo
		d := DecideArm(ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: tk, Cfg: cfg, ReportedHead: head,
			Files: changed("[x](old.md)", "[x](new.md)")})
		if d.Verdict != want {
			t.Errorf("%s: %+v, want %s", repo, d, want)
		}
	}
}

// Human path AM1: with merge.approveToMerge, a maintainer's APPROVED review of the current head
// arms the merger; off, it hands off as before. Only the current head counts, the newest review of
// each maintainer counts, and a truncated list is a veto.
func TestAMaintainersApprovalArmsWhenTheSwitchIsOn(t *testing.T) {
	review := func(login, state, commit string, at int) forge.Review {
		return forge.Review{ID: int64(at), Author: login, State: state, Commit: commit, At: now.Add(time.Duration(at) * time.Minute)}
	}
	for name, c := range map[string]struct {
		on              bool
		reviews         []forge.Review
		truncated       bool
		verdict, reason string
	}{
		"off: an approval hands off as today":       {false, []forge.Review{review("Smana", "APPROVED", head, 1)}, false, "human", "maintainer_approved"},
		"on: an approval of the head arms":          {true, []forge.Review{review("Smana", "APPROVED", head, 1)}, false, "arm", "maintainer_approved"},
		"on: an approval of another head is stale":  {true, []forge.Review{review("Smana", "APPROVED", oldHead, 1)}, false, "human", "approval_stale"},
		"on: an approval with no commit is stale":   {true, []forge.Review{review("Smana", "APPROVED", "", 1)}, false, "human", "approval_stale"},
		"on: a newer request for changes vetoes":    {true, []forge.Review{review("Smana", "APPROVED", head, 1), review("Smana", "CHANGES_REQUESTED", head, 2)}, false, "human", "changes_requested"},
		"on: a later approval lifts the request":    {true, []forge.Review{review("Smana", "CHANGES_REQUESTED", head, 1), review("Smana", "APPROVED", head, 2)}, false, "arm", "maintainer_approved"},
		"on: a non-maintainer's approval is advice": {true, []forge.Review{review("alice", "APPROVED", head, 1)}, false, "shadow", "shadow_would_arm"},
		"on: a truncated review list vetoes":        {true, []forge.Review{review("Smana", "APPROVED", head, 1)}, true, "human", "changes_requested"},
		// R-B1: off, today's loop reads any maintainer approval, even one a newer request for changes follows.
		"off: an approval before a request for changes hands off as today": {false, []forge.Review{review("Smana", "APPROVED", head, 1), review("Smana", "CHANGES_REQUESTED", head, 2)}, false, "human", "maintainer_approved"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := armCfg()
			cfg.Repositories[0].Merge.ApproveToMerge = c.on
			cfg.Repositories[0].Classes["docs-links"] = config.Class{Shadow: true}
			pr := agentPR()
			pr.Reviews, pr.ReviewsTruncated = c.reviews, c.truncated
			tk := armTask("docs-links", "solo", "")
			d := DecideArm(ArmInputs{PR: pr, Checks: green("SUCCESS"), Task: tk, Cfg: cfg, ReportedHead: head, HeadPushedAt: pushedAt,
				Files: changed("[a](https://x/old)", "[a](https://x/new)"), DiffRead: true})
			if d.Verdict != c.verdict || d.Reason != c.reason {
				t.Fatalf("%s %s, want %s %s", d.Verdict, d.Reason, c.verdict, c.reason)
			}
			if d.Verdict == "arm" && d.Approver != "Smana" {
				t.Fatalf("approver %q", d.Approver)
			}
		})
	}
}

// AM1's binding: an approval binds to the head only when the head stopped moving before it. The
// head's push (GitHub's activity, server time) must fall within the task's runs, and the approval
// must follow their end by more than retrySkew. Cases:
//   - #250: a force push moved an approval submitted before it onto the new head;
//   - review I1: a maintainer read A while the run pushed B, and an approval without a commit_id
//     bound to B after B arrived but before the run ended;
//   - review I3/N1: a push after the task's runs ended (another task's run, a late checkpoint),
//     the approval an hour later: no approval can bind that head, so it hands off as a commit
//     not the task's (human_commit_in_pr, with the bypass), not as approval_stale;
//   - anything unknown: no activity read, an unended run;
//   - an approval of the head no run reported (gmmrmund): head_unreported, which the report returns.
//
// The unreported head is not redundant with row 2's head_unreported: row 3 decides before
// agentEvidence is read, so without approvesHead's own check that approval arms (RED on fc87471f).
func TestAnApprovalBindsToTheHeadsPush(t *testing.T) {
	ended := now
	for name, c := range map[string]struct {
		pushed, submitted time.Time
		mutate            func(*ArmInputs)
		verdict, reason   string
	}{
		"#250: an approval before the force push that moved it is stale":                       {now.Add(-time.Minute), now.Add(-2 * time.Minute), nil, "human", "approval_stale"},
		"I1: an approval after the push but before the run ended is stale":                     {now.Add(-10 * time.Minute), now.Add(-time.Minute), nil, "human", "approval_stale"},
		"an approval within the clock skew of the end is stale":                                {now.Add(-10 * time.Minute), now.Add(retrySkew), nil, "human", "approval_stale"},
		"an approval past the end and the skew arms":                                           {now.Add(-10 * time.Minute), now.Add(retrySkew + time.Second), nil, "arm", "maintainer_approved"},
		"a push in the run's last second arms":                                                 {now, now.Add(time.Hour), nil, "arm", "maintainer_approved"},
		"I3: the head pushed a second after the runs ended, approved later, is not the task's": {now.Add(time.Second), now.Add(time.Hour), nil, "human", "human_commit_in_pr"},
		"an unread push (no activity, or its newest move to another commit) is stale":          {time.Time{}, now.Add(time.Hour), nil, "human", "approval_stale"},
		"an approval while a run has not ended is stale": {now.Add(-10 * time.Minute), now.Add(time.Hour), func(in *ArmInputs) {
			in.Task.Status.Runs = append(in.Task.Status.Runs, v1alpha1.RunRecord{ID: "x2x2x2x2", Role: "implementer", Trigger: "ci"})
		}, "human", "approval_stale"},
		"a tester's later end counts": {now.Add(-10 * time.Minute), now.Add(5 * time.Minute), func(in *ArmInputs) {
			late := metav1.NewTime(now.Add(10 * time.Minute))
			in.Task.Status.Runs = append(in.Task.Status.Runs, v1alpha1.RunRecord{ID: "y3y3y3y3", Role: "tester", Trigger: "initial", Finished: &late})
		}, "human", "approval_stale"},
		"a reviewer's run never holds it": {now.Add(-10 * time.Minute), now.Add(5 * time.Minute), func(in *ArmInputs) {
			in.Task.Status.Runs = append(in.Task.Status.Runs, v1alpha1.RunRecord{ID: "z4z4z4z4", Role: "reviewer", Trigger: "review"})
		}, "arm", "maintainer_approved"},
		// gmmrmund (aws-0, Maku v0.39.0): the implementer pushed and ended agent_finished with no handoff, the
		// maintainer approved that head, and the gate said approval_stale. The approval is of the head: what is
		// missing is the room's report of it, and only that report returns the hand-off (reenter).
		"an approval of the head when no run reported any head is head_unreported":       {now.Add(-10 * time.Minute), now.Add(time.Hour), func(in *ArmInputs) { in.ReportedHead = "" }, "human", "head_unreported"},
		"an approval of the head when the room reported an older one is head_unreported": {now.Add(-10 * time.Minute), now.Add(time.Hour), func(in *ArmInputs) { in.ReportedHead = oldHead }, "human", "head_unreported"},
		"an approval of an older head is stale, reported or not": {now.Add(-10 * time.Minute), now.Add(time.Hour), func(in *ArmInputs) {
			in.ReportedHead, in.PR.Reviews[0].Commit = "", oldHead
		}, "human", "approval_stale"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := armCfg()
			cfg.Repositories[0].Merge.ApproveToMerge = true
			cfg.Repositories[0].Classes["docs-links"] = config.Class{Shadow: true}
			pr := agentPR()
			pr.Reviews = []forge.Review{{ID: 1, Author: "Smana", State: "APPROVED", Commit: head, At: c.submitted}}
			tk := armTask("docs-links", "solo", "")
			end := metav1.NewTime(ended)
			tk.Status.Runs[1].Finished = &end
			in := ArmInputs{PR: pr, Checks: green("SUCCESS"), Task: tk, Cfg: cfg, ReportedHead: head, HeadPushedAt: c.pushed,
				Files: changed("[a](https://x/old)", "[a](https://x/new)"), DiffRead: true}
			if c.mutate != nil {
				c.mutate(&in)
			}
			if d := DecideArm(in); d.Verdict != c.verdict || d.Reason != c.reason {
				t.Fatalf("%s %s, want %s %s", d.Verdict, d.Reason, c.verdict, c.reason)
			}
		})
	}
}

// headPush dates the head only by the branch's newest move, and only when that move put the head
// there: an entry whose after is another commit, a move of another kind, or no activity is zero.
func TestHeadPushIsTheNewestMoveToTheHead(t *testing.T) {
	at := now.Add(-time.Minute)
	for name, c := range map[string]struct {
		ps   []forge.Push
		want time.Time
	}{
		"a push to the head":                   {[]forge.Push{{Type: "push", After: head, At: at}, {Type: "branch_creation", After: oldHead}}, at},
		"a force push to the head":             {[]forge.Push{{Type: "force_push", After: head, At: at}}, at},
		"the branch created at the head":       {[]forge.Push{{Type: "branch_creation", After: head, At: at}}, at},
		"the newest move is to another commit": {[]forge.Push{{Type: "push", After: oldHead, At: at}, {Type: "push", After: head}}, time.Time{}},
		"the newest move is a branch deletion": {[]forge.Push{{Type: "branch_deletion", After: head, At: at}}, time.Time{}},
		"no activity":                          {nil, time.Time{}},
	} {
		if got := headPush(c.ps, head); !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// Review M2: GitHub's times are whole seconds. A maintainer's approval and request for changes in
// one second are ordered by id, so the later request is the newest and vetoes, whatever the order
// GitHub lists them in.
func TestASameSecondRequestForChangesVetoes(t *testing.T) {
	at := now.Add(time.Minute)
	approve := forge.Review{ID: 10, Author: "Smana", State: "APPROVED", Commit: head, At: at}
	request := forge.Review{ID: 11, Author: "Smana", State: "CHANGES_REQUESTED", Commit: head, At: at}
	for name, reviews := range map[string][]forge.Review{
		"listed in order": {approve, request},
		"listed reversed": {request, approve},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := armCfg()
			cfg.Repositories[0].Merge.ApproveToMerge = true
			pr := agentPR()
			pr.Reviews = reviews
			d := DecideArm(ArmInputs{PR: pr, Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""), Cfg: cfg, ReportedHead: head,
				HeadPushedAt: pushedAt, Files: changed("[a](https://x/old)", "[a](https://x/new)"), DiffRead: true})
			if d.Verdict != "human" || d.Reason != "changes_requested" {
				t.Fatalf("%s %s, want human changes_requested", d.Verdict, d.Reason)
			}
		})
	}
}

// Review gap: one maintainer's approval was moved onto the head by a force push (submitted before
// it); another's was submitted after the runs ended. The valid one arms, and is the one named, in
// either order.
func TestAValidApprovalArmsBesideAMovedOne(t *testing.T) {
	cfg := armCfg()
	cfg.Repositories[0].Maintainers = append(cfg.Repositories[0].Maintainers, "octocat")
	cfg.Repositories[0].Merge.ApproveToMerge = true
	cfg.Repositories[0].Classes["docs-links"] = config.Class{Shadow: true}
	pr := agentPR()
	moved := forge.Review{ID: 1, Author: "Smana", State: "APPROVED", Commit: head, At: pushedAt.Add(-time.Minute)}
	valid := forge.Review{ID: 2, Author: "octocat", State: "APPROVED", Commit: head, At: now.Add(3 * time.Minute)}
	for _, order := range [][]forge.Review{{moved, valid}, {valid, moved}} {
		pr.Reviews = order
		d := DecideArm(ArmInputs{PR: pr, Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""), Cfg: cfg, ReportedHead: head,
			HeadPushedAt: pushedAt, Files: changed("[a](https://x/old)", "[a](https://x/new)"), DiffRead: true})
		if d.Verdict != "arm" || d.Approver != "octocat" {
			t.Fatalf("%+v, want arm by octocat", d)
		}
	}
}

// AM1, "outside the caps": a maintainer's approval of the head arms even when the day's
// autoMergesPerDay or the class's maxPerDay is spent. The caps bound the factory's own judgement,
// not a human's.
func TestAnApprovalArmsOutsideTheCaps(t *testing.T) {
	for name, spent := range map[string]func(*ArmInputs){
		"the day's cap":   func(in *ArmInputs) { in.ArmedToday = in.Cfg.Current().Repositories[0].Merge.MaxPerDay },
		"the class's cap": func(in *ArmInputs) { in.ClassArmedToday = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := armCfg()
			cfg.Repositories[0].Merge.ApproveToMerge = true
			cfg.Repositories[0].Classes["docs-links"] = config.Class{Live: true, MaxPerDay: 1}
			pr := agentPR()
			pr.Reviews = []forge.Review{{ID: 1, Author: "Smana", State: "APPROVED", Commit: head, At: now}}
			in := ArmInputs{PR: pr, Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""), Cfg: cfg, ReportedHead: head,
				HeadPushedAt: pushedAt, Files: changed("[a](https://x/old)", "[a](https://x/new)"), DiffRead: true}
			spent(&in)
			if d := DecideArm(in); d.Verdict != "arm" || d.Reason != "maintainer_approved" {
				t.Fatalf("%+v, want arm maintainer_approved", d)
			}
			in.PR.Reviews = nil
			if d := DecideArm(in); d.Reason != "auto_merge_cap" {
				t.Fatalf("without the approval: %+v, want auto_merge_cap", d)
			}
		})
	}
}

// R-B1 (AM1, "adopted classes keep today's path"): with the switch on, a maintainer's approval of
// an adopted pull request's head still hands off; it never arms the merger.
func TestAnAdoptedPullRequestsApprovalHandsOffWithTheSwitchOn(t *testing.T) {
	cfg := armCfg()
	cfg.Repositories[0].Merge.ApproveToMerge = true
	cfg.Repositories[0].Classes["deps-patch"] = depsPatchClass()
	pr := forge.PR{Author: bot, BaseRef: "main", HeadRef: "renovate/kyverno-3.x", HeadSHA: head,
		Reviews: []forge.Review{{ID: 1, Author: "Smana", State: "APPROVED", Commit: head, At: now}}}
	d := DecideArm(ArmInputs{PR: pr, Checks: green("SUCCESS"), Task: armTask("deps-patch", "solo", ""), Cfg: cfg})
	if d.Verdict != "human" || d.Reason != "maintainer_approved" || d.Approver != "" {
		t.Fatalf("%+v, want human maintainer_approved", d)
	}
}

// Human path AM8: every decision carries its row vector, so the tally can replay a cap and count
// the refusals a human merged unchanged.
func TestADecisionCarriesItsRows(t *testing.T) {
	cfg := armCfg()
	cfg.Repositories[0].Classes["docs-links"] = config.Class{Shadow: true, MaxPatchFiles: 3, MaxModifiedLines: 20}
	tk := armTask("docs-links", "pair", "approve")
	tk.Status.Runs = append(tk.Status.Runs, v1alpha1.RunRecord{ID: "r3vnwr3v", Role: "reviewer", Verdict: "approve", HeadSHA: head,
		Provenance: v1alpha1.RunProvenance{ServedModels: []string{"glm-5.3"}}})
	d := DecideArm(ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: tk, Cfg: cfg, ReportedHead: head,
		Verifiers: []v1alpha1.RunRecord{tk.Status.Runs[2]}, Stats: []forge.FileStat{{Path: testFile, Additions: 1, Deletions: 1}},
		Files: changed("[a](https://x/old)", "[a](https://x/new)"), DiffRead: true})
	want := decision.Rows{Files: 1, MaxFiles: 3, Lines: 2, MaxLines: 20, Counted: true, Shape: "ok", Holdout: "n/a", Oracle: "n/a", Age: "n/a", Evidence: "ok", Gated: true, Reviewer: "glm-5.3"}
	if d.Verdict != "shadow" || d.Rows != want {
		t.Fatalf("%s %+v\nwant shadow %+v", d.Verdict, d.Rows, want)
	}
}

// A refusal records the rows it read and leaves the later ones unread (""): the counts too
// until the diff is read (Counted false), so the marker prints
// "-" for them (R-m7); the reviewer model is known whatever the refusal, "none" without a run.
func TestARefusalLeavesItsLaterRowsUnread(t *testing.T) {
	cfg := armCfg()
	cfg.Repositories[0].Classes["docs-links"] = config.Class{Shadow: true, MaxPatchFiles: 3, MaxModifiedLines: 20}
	tk := armTask("docs-links", "pair", "approve")
	d := DecideArm(ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: tk, Cfg: cfg, ReportedHead: oldHead})
	want := decision.Rows{Evidence: "head_unreported", Reviewer: "none"} // no policy read yet: no caps (MR1)
	if d.Reason != "head_unreported" || d.Rows != want {
		t.Fatalf("%s %+v\nwant head_unreported %+v", d.Reason, d.Rows, want)
	}
	d = DecideArm(ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: tk, Cfg: cfg, ReportedHead: head, DiffRead: true,
		Stats: []forge.FileStat{{Path: testFile, Additions: 30}}, Files: changed("[a](https://x/old)", "[a](https://x/new)")})
	// No policy held: the counts and the evidence are the decision's, the refusal is in considered=.
	want = decision.Rows{Files: 1, Lines: 30, Counted: true, Evidence: "ok", Reviewer: "none", Considered: "docs-links:patch_too_large"}
	if d.Reason != reasonNoPolicy || d.Rows != want {
		t.Fatalf("%s %+v\nwant patch_too_large %+v", d.Reason, d.Rows, want)
	}
}

// steering_path refuses before the declared rows, on a diff it read: the record keeps the counts.
func TestASteeringRefusalKeepsTheCounts(t *testing.T) {
	cfg := armCfg()
	cfg.Repositories[0].Classes["docs-links"] = config.Class{Shadow: true}
	cfg.Loops = map[string]config.Loop{"default": {Scope: config.ScopeSteering}}
	d := DecideArm(ArmInputs{PR: agentPR(), Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""), Cfg: cfg, ReportedHead: head,
		DiffRead: true, Stats: []forge.FileStat{{Path: steering.Dir + "docs.md", Additions: 2, Deletions: 1}}})
	want := decision.Rows{Files: 1, Lines: 3, Counted: true, Evidence: "ok", Reviewer: "none"}
	if d.Reason != "steering_path" || d.Rows != want {
		t.Fatalf("%s %+v\nwant steering_path %+v", d.Reason, d.Rows, want)
	}
}

// AM1 through the reconciler: the gate reads the head's push from the branch's activity. When the
// activity cannot be read, or its newest move is to another commit, no approval binds: the task
// hands off approval_stale and nothing is armed. Read and at the head, the same approval arms.
func TestTheGateReadsTheHeadsPushForAnApproval(t *testing.T) {
	for name, c := range map[string]struct {
		activity func(*rig)
		reason   string
	}{
		"unreadable activity": {func(g *rig) { g.f.SetActivityErr("agent/3buqdlot", errors.New("502 Bad Gateway")) }, "approval_stale"},
		"the newest move is to another commit": {func(g *rig) {
			g.f.SetActivity("agent/3buqdlot", forge.Push{Type: "push", Actor: "ogenki-agents[bot]", Before: "abc", After: "def", At: now.Add(-40 * time.Minute)},
				forge.Push{Type: "branch_creation", Actor: "ogenki-agents[bot]", After: "abc", At: now.Add(-time.Hour)})
		}, "approval_stale"},
		"the head's push, within the run": {func(*rig) {}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			g := mergeRig(t, v1alpha1.PhaseAwaitingCI)
			g.r.Cfg.Current().Repositories[0].Merge.ApproveToMerge = true
			g.r.Cfg.Current().Repositories[0].Classes["docs-links"] = config.Class{}                                           // unarmed: only the approval can merge it
			g.r.Cfg.Current().Repositories[0].Classes["revert"] = config.Class{Live: true, Oracle: &config.Oracle{Probe: "x"}} // live, never a diff's (MR1)
			pr, _ := g.f.PullRequest(t.Context(), 12)
			pr.Reviews = []forge.Review{{ID: 77, Author: "Smana", State: "APPROVED", Commit: "abc", At: now}}
			g.f.SetPR(pr)
			g.f.SetChecks(12, green("SUCCESS"))
			c.activity(g)
			got := g.reconcile(t, "3buqdlot", 1)
			if c.reason == "" {
				if got.Status.Phase != v1alpha1.PhaseAutoMerging || got.Status.PullRequest.MergedHow != "approved" {
					t.Fatalf("arms on the approval: %s %s", got.Status.Phase, got.Status.Reason)
				}
				return
			}
			if got.Status.Phase != v1alpha1.PhaseAwaitingHuman || got.Status.Reason != c.reason || len(g.f.Armed()) != 0 {
				t.Fatalf("%s %s armed %v, want AwaitingHuman %s", got.Status.Phase, got.Status.Reason, g.f.Armed(), c.reason)
			}
		})
	}
}

// MR12 (Review Focus 4): a policy with no diff shape and no oracle merges only on a reviewer's
// favourable verdict. A workflow without a reviewer (quick, whose template is solo) leaves a
// path-bound policy reviewer_verdict_missing, never arm; a shape or an oracle checks the diff
// instead. A workflow with a reviewer (standard, pair) needs its approve whatever the policy.
func TestVerdictRequiredWithoutShape(t *testing.T) {
	workflows, templates, _ := config.ShippedWorkflows()
	cfg := armCfg()
	cfg.Templates = templates
	policies := map[string]config.Class{
		"docs":       {Live: true, AllowedFiles: []string{`^.+\.md$`}},
		"docs-links": {Live: true, AllowedFiles: []string{`^.+\.md$`}},
		"dead-links": {Live: true, AllowedFiles: []string{`^.+\.md$`}, DiffShape: "links-only", Oracle: &config.Oracle{Probe: "dead-links"}},
	}
	for _, c := range []struct {
		workflow, verdict, policy, want string
	}{
		{config.WorkflowQuick, "", "docs", "reviewer_verdict_missing"},
		{config.WorkflowQuick, "", "docs-links", ""},
		{config.WorkflowQuick, "", "dead-links", ""},
		{config.WorkflowStandard, "reject", "docs", "no_approving_verdict"},
		{config.WorkflowStandard, "reject", "docs-links", "no_approving_verdict"},
		{config.WorkflowStandard, "reject", "dead-links", "no_approving_verdict"},
	} {
		t.Run(c.workflow+"/"+c.policy, func(t *testing.T) {
			in := ArmInputs{PR: agentPR(), Task: armTask(c.policy, workflows[c.workflow].Template, c.verdict), ReportedHead: head}
			cl := withShape(policies[c.policy], c.policy)
			if got := agentEvidence(in, cfg, cl); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// Through the gate: a quick task whose diff only a path-bound policy admits waits for a human,
// with the policy's refusal in considered=; the same diff with a shaped policy beside it merges there.
func TestAPathBoundPolicyWithoutAReviewerNeverArms(t *testing.T) {
	workflows, templates, _ := config.ShippedWorkflows()
	in := selectInputs()
	in.Cfg.Templates = templates
	in.Task = armTask("review", workflows[config.WorkflowQuick].Template, "")
	in.Verifiers = nil
	d := DecideArm(in)
	if d.Verdict != "arm" || d.Matched != "docs-links" {
		t.Fatalf("the shaped policy holds: %+v", d)
	}
	delete(in.Cfg.Current().Repositories[0].Classes, "docs-links")
	d = DecideArm(in)
	if d.Verdict != "human" || d.Reason != reasonNoPolicy || d.Rows.Considered != "docs:reviewer_verdict_missing" {
		t.Fatalf("%+v, want no_policy_matched with docs:reviewer_verdict_missing", d)
	}
}

// SEC-H3: Diff compares against main, so any PR not targeting main (including an empty BaseRef)
// must fail closed to a human before any class or approval can arm it.
func TestDecideArmRefusesNonMainBase(t *testing.T) {
	for name, base := range map[string]string{
		"release branch": "release-1",
		"empty baseRef":  "",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := armCfg()
			pr := agentPR()
			pr.BaseRef = base
			d := DecideArm(ArmInputs{PR: pr, Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""),
				Cfg: cfg, ReportedHead: head, Files: changed("[x](old.md)", "[x](new.md)")})
			if d.Verdict != "human" || d.Reason != "base_not_main" {
				t.Fatalf("base %q: got %+v, want human/base_not_main", base, d)
			}
			// Even with approveToMerge and a valid maintainer approval, non-main never arms.
			cfg.Repositories[0].Merge.ApproveToMerge = true
			pr.Reviews = []forge.Review{{ID: 1, Author: "Smana", State: "APPROVED", Commit: head, At: now}}
			d = DecideArm(ArmInputs{PR: pr, Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""),
				Cfg: cfg, ReportedHead: head, HeadPushedAt: pushedAt, Files: changed("[x](old.md)", "[x](new.md)"), DiffRead: true})
			if d.Verdict != "human" || d.Reason != "base_not_main" {
				t.Fatalf("approved non-main %q: got %+v, want human/base_not_main", base, d)
			}
		})
	}
}

// SEC-H4: CIState and baseChecks fail closed on truncated rollups, inspect every check run of a
// matching name (including LeakScanCheck), and require ActionsApp on required check runs.
func TestCIStateAndBaseChecksHardening(t *testing.T) {
	t.Run("truncated checks never pass CIState or arm", func(t *testing.T) {
		c := green("SUCCESS")
		c.Truncated = true
		if got := CIState(c, required); got == "SUCCESS" {
			t.Fatalf("CIState on truncated rollup = %q, must not be SUCCESS", got)
		}
		d := DecideArm(ArmInputs{PR: agentPR(), Checks: c, Task: armTask("docs-links", "solo", ""),
			Cfg: armCfg(), ReportedHead: head, Files: changed("[x](old.md)", "[x](new.md)")})
		if d.Verdict != "human" || d.Reason != "checks_truncated" {
			t.Fatalf("DecideArm on truncated rollup = %+v, want human/checks_truncated", d)
		}
	})
	t.Run("duplicate check runs inspect all matches", func(t *testing.T) {
		for name, tc := range map[string]struct {
			second          forge.Check
			ci              string
			verdict, reason string
		}{
			"passing then failing":   {forge.Check{Name: required[0], State: "FAILURE", App: forge.ActionsApp, Conclusion: "FAILURE"}, "FAILURE", "ci_red", "ci_red"},
			"passing then pending":   {forge.Check{Name: required[0], State: "PENDING", App: forge.ActionsApp}, "PENDING", "wait", "ci_pending"},
			"passing then cancelled": {forge.Check{Name: required[0], State: "CANCELLED", App: forge.ActionsApp, Conclusion: "CANCELLED"}, "CANCELLED", "ci_cancelled", "ci_cancelled"},
		} {
			t.Run(name, func(t *testing.T) {
				c := green("SUCCESS")
				c.Runs = append(c.Runs, tc.second)
				if got := CIState(c, required); got != tc.ci {
					t.Fatalf("CIState = %q, want %q", got, tc.ci)
				}
				d := DecideArm(ArmInputs{PR: agentPR(), Checks: c, Task: armTask("docs-links", "solo", ""),
					Cfg: armCfg(), ReportedHead: head, Files: changed("[x](old.md)", "[x](new.md)")})
				if d.Verdict != tc.verdict || d.Reason != tc.reason {
					t.Fatalf("DecideArm = %+v, want %s/%s", d, tc.verdict, tc.reason)
				}
			})
		}
	})
	t.Run("duplicate LeakScanCheck passing first then failing second escalates", func(t *testing.T) {
		c := green("SUCCESS")
		c.Runs = append(c.Runs,
			forge.Check{Name: "Security scanning", State: "SUCCESS", App: forge.ActionsApp, Conclusion: "SUCCESS"},
			forge.Check{Name: "Security scanning", State: "FAILURE", App: forge.ActionsApp, Conclusion: "FAILURE"})
		d := DecideArm(ArmInputs{PR: agentPR(), Checks: c, Task: armTask("docs-links", "solo", ""),
			Cfg: armCfg(), ReportedHead: head, Files: changed("[x](old.md)", "[x](new.md)")})
		if d.Verdict != "escalate" || d.Reason != "secret_scan_red" {
			t.Fatalf("DecideArm = %+v, want escalate/secret_scan_red", d)
		}
	})
	t.Run("forged wrong-App check run cannot satisfy required check", func(t *testing.T) {
		for name, app := range map[string]int64{"foreign app": 99999, "unknown app 0": 0} {
			t.Run(name, func(t *testing.T) {
				c := green("SUCCESS")
				c.Runs[0].App = app
				if got := CIState(c, required); got != "PENDING" {
					t.Fatalf("CIState = %q, want PENDING", got)
				}
				d := DecideArm(ArmInputs{PR: agentPR(), Checks: c, Task: armTask("docs-links", "solo", ""),
					Cfg: armCfg(), ReportedHead: head, Files: changed("[x](old.md)", "[x](new.md)")})
				if d.Verdict != "wait" || d.Reason != "ci_pending" {
					t.Fatalf("DecideArm = %+v, want wait/ci_pending", d)
				}
			})
		}
		// Forged passing check before real failing Actions check still reads FAILURE.
		c := green("SUCCESS")
		c.Runs = append([]forge.Check{{Name: required[0], State: "SUCCESS", App: 99999, Conclusion: "SUCCESS"}}, c.Runs...)
		c.Runs[1].State = "FAILURE"
		if got := CIState(c, required); got != "FAILURE" {
			t.Fatalf("forged pass + real fail: CIState = %q, want FAILURE", got)
		}
	})
}

// SEC-M2: when GitHub truncated the pull request's label list, factory/hold may be unseen; fail
// closed to a human.
func TestDecideArmFailsClosedWhenLabelsTruncated(t *testing.T) {
	pr := agentPR()
	pr.LabelsTruncated = true
	d := DecideArm(ArmInputs{PR: pr, Checks: green("SUCCESS"), Task: armTask("docs-links", "solo", ""),
		Cfg: armCfg(), ReportedHead: head, Files: changed("[x](old.md)", "[x](new.md)"), DiffRead: true})
	if d.Verdict != "human" || d.Reason != "labels_truncated" {
		t.Fatalf("DecideArm = %+v, want human/labels_truncated", d)
	}
}
