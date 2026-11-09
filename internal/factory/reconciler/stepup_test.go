// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/triage"
)

// stepCfg is resolveCfg with the spec's review escalation: outOfBudget to review-frontier,
// notConverged to review-search.
func stepCfg() *config.Config {
	c := resolveCfg("apply", 0)
	c.HarnessProfiles["review-frontier"] = config.HarnessProfile{Template: "pair", Tier: "frontier", Description: "a large change"}
	c.Classes["review"] = config.Class{Harness: "review-floor", HarnessAllowed: []string{"review-search", "review-tested", "review-frontier"},
		Escalation: &config.Escalation{OutOfBudget: "review-frontier", NotConverged: "review-search"}}
	return c
}

// stepRig is a review task under cf whose first implementer run, rid(0), has ended: its profile
// resolved to the floor, its room open, unscoped.
func stepRig(t *testing.T, cf *config.Config, edit func(*v1alpha1.Task)) (*rig, *v1alpha1.Task) {
	t.Helper()
	tk := issueTask("3buqdlot", 7, "fix it")
	tk.Spec.PredictedClass, tk.Spec.Template = "review", "pair"
	tk.Spec.Budget = triage.BudgetFor(cf, "standard")
	start := metav1.NewTime(now.Add(-time.Hour))
	tk.Status = v1alpha1.TaskStatus{Phase: v1alpha1.PhaseImplementing, RoomRef: "3buqdlot", PhaseSince: &start,
		Profile: &v1alpha1.ProfileStatus{Name: "review-floor", Source: "floor", Cohort: "treatment", Reason: "none", Checks: true, Resolved: true},
		Runs:    []v1alpha1.RunRecord{{ID: rid(0), Role: "implementer", Trigger: "initial", StartSeq: 1, Started: &start}}}
	if edit != nil {
		edit(tk)
	}
	g := newRig(t, tk, roomOf("3buqdlot"))
	g.r.Cfg = cf
	g.r.Triage = triage.Static{Cfg: cf}
	st := tk.Status
	tk = g.get(t)
	tk.Status = st
	p, _ := cf.ProfileFor(cf.Classes["review"], "review-floor")
	g.r.profileSpec(g.r.Cfg.Current(), tk, "review", p)
	tk.Spec.ScopeMode = string(config.ScopeOff)
	tk.Spec.Budget.Tier = "standard"
	if err := g.c.Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	tk.Status = st
	if err := g.c.Status().Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	return g, g.get(t)
}

func (g *rig) get(t *testing.T) *v1alpha1.Task {
	t.Helper()
	var tk v1alpha1.Task
	if err := g.c.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: "3buqdlot"}, &tk); err != nil {
		t.Fatal(err)
	}
	return &tk
}

// fail ends tk escalated for reason and writes its status, as a reconcile pass would.
func (g *rig) fail(t *testing.T, tk *v1alpha1.Task, reason string) *v1alpha1.Task {
	t.Helper()
	if err := g.r.end(t.Context(), g.r.Cfg.Current(), tk, v1alpha1.PhaseEscalated, reason); err != nil {
		t.Fatal(err)
	}
	if err := g.c.Status().Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	return g.get(t)
}

func outboxHas(tk *v1alpha1.Task, key string) bool {
	return slices.ContainsFunc(tk.Status.Outbox, func(o v1alpha1.Narration) bool { return strings.HasPrefix(o.Key, key) })
}

func wantStepped(t *testing.T, tk *v1alpha1.Task, name, group string) {
	t.Helper()
	p := tk.Status.Profile
	if tk.Status.Phase != v1alpha1.PhaseQueued || tk.Status.NextTrigger != TriggerEscalation || tk.Status.ReviewRounds != 0 ||
		p.Name != name || p.Source != "escalation" || p.Reason != group || p.Cohort != "treatment" || !p.Checks || p.TestFirst {
		t.Fatalf("%s %q %d %+v", tk.Status.Phase, tk.Status.NextTrigger, tk.Status.ReviewRounds, p)
	}
	if tk.Status.FixRunsBeforeStep != tk.Status.FixRuns { // R33: the repairs spent so far are the step's baseline
		t.Fatalf("fix runs %d, baseline %d", tk.Status.FixRuns, tk.Status.FixRunsBeforeStep)
	}
	if !outboxHas(tk, "profile-escalated-") || outboxHas(tk, "end-") {
		t.Fatalf("outbox %+v", tk.Status.Outbox)
	}
}

func wantEscalated(t *testing.T, tk *v1alpha1.Task, reason string) {
	t.Helper()
	if tk.Status.Phase != v1alpha1.PhaseEscalated || tk.Status.Reason != reason || outboxHas(tk, "profile-escalated-") {
		t.Fatalf("%s %q %+v %+v", tk.Status.Phase, tk.Status.Reason, tk.Status.Profile, tk.Status.Outbox)
	}
}

func TestStepTable(t *testing.T) {
	for reason, want := range map[string][2]string{
		"budget-run": {"review-frontier", "out_of_budget"}, "stalled": {"review-frontier", "out_of_budget"},
		"not_met": {"review-search", "not_converged"}, "review_rounds_exhausted": {"review-search", "not_converged"},
		"ci_red": {}, "judge_impossible": {}, "needs_author": {}, "budget-task": {},
	} {
		t.Run(reason, func(t *testing.T) {
			g, tk := stepRig(t, stepCfg(), func(tk *v1alpha1.Task) {
				tk.Status.ReviewRounds, tk.Status.FixRuns = 2, 1
				if reason == "review_rounds_exhausted" { // the review loop's verdict ends a reviewer's run
					tk.Status.Runs = append(tk.Status.Runs, v1alpha1.RunRecord{ID: rid(1), Role: "reviewer", Trigger: "review", StartSeq: 4})
				}
			})
			got := g.fail(t, tk, reason)
			if want[0] == "" {
				wantEscalated(t, got, reason)
				if got.Spec.Budget.Tier != "standard" {
					t.Fatalf("tier %s", got.Spec.Budget.Tier)
				}
				return
			}
			wantStepped(t, got, want[0], want[1])
			if tier := map[string]string{"review-frontier": "frontier", "review-search": "standard"}[want[0]]; got.Spec.Budget.Tier != tier {
				t.Fatalf("tier %s", got.Spec.Budget.Tier)
			}
			if want[0] == "review-search" && (got.Spec.Loop == nil || got.Spec.Loop.StallSteps != 7) {
				t.Fatalf("loop %+v", got.Spec.Loop)
			}
		})
	}
}

func TestStepOnce(t *testing.T) {
	g, tk := stepRig(t, stepCfg(), func(tk *v1alpha1.Task) {
		tk.Status.Profile.Name, tk.Status.Profile.Source, tk.Status.Profile.Reason = "review-frontier", "escalation", "out_of_budget"
	})
	got := g.fail(t, tk, "stalled")
	wantEscalated(t, got, "stalled")
	i := slices.IndexFunc(got.Status.Outbox, func(o v1alpha1.Narration) bool { return strings.HasPrefix(o.Key, "end-") })
	if i < 0 || !strings.Contains(got.Status.Outbox[i].Body, "`review-frontier`") {
		t.Fatalf("%+v", got.Status.Outbox)
	}
}

func TestNoStepInControl(t *testing.T) {
	g, tk := stepRig(t, stepCfg(), func(tk *v1alpha1.Task) {
		tk.Status.Profile.Source, tk.Status.Profile.Cohort = "control", "control"
	})
	wantEscalated(t, g.fail(t, tk, "stalled"), "stalled")
}

func TestNoStepWithoutAResolvedProfile(t *testing.T) {
	g, tk := stepRig(t, stepCfg(), func(tk *v1alpha1.Task) { tk.Status.Profile.Resolved = false })
	wantEscalated(t, g.fail(t, tk, "stalled"), "stalled")
}

func TestNoStepWithoutAGroup(t *testing.T) {
	cf := stepCfg()
	cl := cf.Classes["review"]
	cl.Escalation = &config.Escalation{OutOfBudget: "review-frontier"}
	cf.Classes["review"] = cl
	g, tk := stepRig(t, cf, nil)
	wantEscalated(t, g.fail(t, tk, "not_met"), "not_met")
}

func TestNoStepWithoutReserve(t *testing.T) {
	g, tk := stepRig(t, stepCfg(), func(tk *v1alpha1.Task) {
		tk.Status.Runs[0].Tokens, tk.Status.Runs[0].CostMicroUSD = 5_000_000, 10_000_000
		tk.Status.Usage.Tokens, tk.Status.Usage.CostMicroUSD = 5_000_000, 10_000_000
	})
	got := g.fail(t, tk, "stalled")
	wantEscalated(t, got, "stalled")
	if got.Spec.Budget.Tier != "standard" || got.Status.Profile.Name != "review-floor" {
		t.Fatalf("%s %+v", got.Spec.Budget.Tier, got.Status.Profile)
	}
}

func TestAStepRespectsTheSponsorCeiling(t *testing.T) {
	g, tk := stepRig(t, stepCfg(), func(tk *v1alpha1.Task) { tk.Spec.MaxMicroUSD = 3_000_000 })
	got := g.fail(t, tk, "stalled")
	wantStepped(t, got, "review-frontier", "out_of_budget")
	if got.Spec.Budget.TaskMicroUSD != 3_000_000 {
		t.Fatalf("%+v", got.Spec.Budget)
	}
}

// A refused spec write leaves the status as it was: no step recorded, nothing narrated or queued.
func TestARefusedStepWriteChangesNothing(t *testing.T) {
	g, tk := stepRig(t, stepCfg(), nil)
	refuse := true
	g.r.Client = interceptor.NewClient(g.c.(client.WithWatch), interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			if refuse {
				refuse = false
				return errors.New("spec write refused")
			}
			return cl.Update(ctx, o, opts...)
		}})
	before := tk.Status.DeepCopy()
	if err := g.r.end(t.Context(), g.r.Cfg.Current(), tk, v1alpha1.PhaseEscalated, "stalled"); err == nil {
		t.Fatal("the refusal should surface")
	}
	if tk.Status.Phase != before.Phase || *tk.Status.Profile != *before.Profile || tk.Status.NextTrigger != "" || len(tk.Status.Outbox) != 0 {
		t.Fatalf("%+v", tk.Status)
	}
	stored := g.get(t)
	if stored.Spec.Budget.Tier != "standard" || stored.Status.Profile.Name != "review-floor" || stored.Status.Profile.Source != "floor" {
		t.Fatalf("%s %+v", stored.Spec.Budget.Tier, stored.Status.Profile)
	}
	if err := g.r.end(t.Context(), g.r.Cfg.Current(), stored, v1alpha1.PhaseEscalated, "stalled"); err != nil {
		t.Fatal(err)
	}
	wantStepped(t, stored, "review-frontier", "out_of_budget")
	if got := g.get(t); got.Spec.Budget.Tier != "frontier" {
		t.Fatalf("stored tier %s", got.Spec.Budget.Tier)
	}
}

func TestRetryAfterAStepNeverStepsAgain(t *testing.T) {
	g, tk := stepRig(t, stepCfg(), nil)
	tk = g.fail(t, tk, "stalled")
	wantStepped(t, tk, "review-frontier", "out_of_budget")
	tk = g.fail(t, tk, "stalled") // the stepped run fails too
	if tk.Status.Phase != v1alpha1.PhaseEscalated || tk.Status.Profile.Name != "review-frontier" {
		t.Fatalf("%s %+v", tk.Status.Phase, tk.Status.Profile)
	}
	since := metav1.NewTime(now.Add(-10 * time.Minute))
	tk.Status.PhaseSince = &since
	if err := g.c.Status().Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	g.f.SetComments(7, retryBy(77, "smana", time.Minute))
	tk = g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseQueued || tk.Status.Profile.Source != "escalation" {
		t.Fatalf("%s %+v", tk.Status.Phase, tk.Status.Profile)
	}
	tk = g.reconcile(t, "3buqdlot", 1)
	last := tk.Status.Runs[len(tk.Status.Runs)-1]
	if tk.Status.Phase != v1alpha1.PhaseImplementing || last.Trigger != "retry" || g.runs.specs[last.ID].Tier != "frontier" {
		t.Fatalf("%s %+v %s", tk.Status.Phase, last, g.runs.specs[last.ID].Tier)
	}
	if tk = g.fail(t, tk, "stalled"); tk.Status.Phase != v1alpha1.PhaseEscalated || tk.Status.Reason != "stalled" {
		t.Fatalf("%s %q", tk.Status.Phase, tk.Status.Reason)
	}
}

func judgeEvent(seq int64, run, verdict, text string) envelope.Event {
	p, _ := json.Marshal(envelope.MessagePayload{Kind: envelope.KindJudgeVerdict, Verdict: verdict, Text: text, Delivery: envelope.DeliveryNone})
	return envelope.Event{Seq: seq, RunID: run, Type: envelope.Message, Origin: envelope.OriginClient,
		Actor: envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + run}, Payload: p}
}

func TestTheStepBriefCarriesTheJudge(t *testing.T) {
	g, tk := stepRig(t, stepCfg(), nil)
	g.log.evs = append(g.log.evs, judgeEvent(3, rid(0), "not_yet", "an older verdict"),
		judgeEvent(5, rid(0), "not_yet", "check:validate still fails on docs/a.md"))
	wantStepped(t, g.fail(t, tk, "not_met"), "review-search", "not_converged")
	tk = g.reconcile(t, "3buqdlot", 1)
	last := tk.Status.Runs[len(tk.Status.Runs)-1]
	text := g.runs.specs[last.ID].TaskText
	fence := "JUDGE-DATA-n0nce234"
	i, j := strings.Index(text, fence), strings.LastIndex(text, fence)
	if last.Trigger != TriggerEscalation || i < 0 || i == j || !strings.Contains(text[i:j], "> check:validate still fails on docs/a.md") ||
		strings.Contains(text, "an older verdict") {
		t.Fatalf("%s %q", last.Trigger, text)
	}
}

// Out of review rounds after a verifier's run, the task steps, or narrates the last verdict when it declines.
func TestRoundsExhaustedNarratedOnlyWhenNotStepped(t *testing.T) {
	for name, c := range map[string]struct {
		cohort, role string
		want         int
	}{"stepped": {"treatment", "reviewer", 0}, "declined": {"control", "reviewer", 1}} {
		t.Run(name, func(t *testing.T) {
			g, tk := stepRig(t, stepCfg(), func(tk *v1alpha1.Task) {
				tk.Status.Profile.Cohort = c.cohort
				tk.Status.ReviewRounds = 2
				tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12}
				tk.Status.Runs[0].Role = c.role
			})
			if err := g.r.roundsExhausted(t.Context(), g.r.Cfg.Current(), tk, rooms.Verdict{RunID: rid(0), Verdict: "changes", Commit: "abc"}); err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, o := range tk.Status.Outbox {
				if strings.HasPrefix(o.Key, "rounds-") {
					n++
				}
			}
			if n != c.want || (c.want == 1) != (tk.Status.Phase == v1alpha1.PhaseEscalated) {
				t.Fatalf("%s %d %+v", tk.Status.Phase, n, tk.Status.Outbox)
			}
		})
	}
}

// A run's own budget-run, stalled or not_met steps only when it was the implementer's;
// review_rounds_exhausted only after the reviewer's run that exhausted them.
func TestOnlyAnImplementersEndSteps(t *testing.T) {
	for _, c := range []struct{ role, reason string }{{"reviewer", "budget-run"}, {"tester", "budget-run"}, {"reviewer", "stalled"},
		{"tester", "stalled"}, {"reviewer", "not_met"}, {"triager", "budget-run"}, {"", "budget-run"}, {"", "review_rounds_exhausted"},
		{"implementer", "review_rounds_exhausted"}, {"tester", "review_rounds_exhausted"}} {
		role := c.role
		t.Run(role+" "+c.reason, func(t *testing.T) {
			g, tk := stepRig(t, stepCfg(), func(tk *v1alpha1.Task) {
				if role == "" {
					tk.Status.Runs = nil
				} else {
					tk.Status.Runs[0].Role = role
				}
			})
			wantEscalated(t, g.fail(t, tk, c.reason), c.reason)
		})
	}
}

func TestTheJudgeNoteWalksTheResumedChain(t *testing.T) {
	g, tk := stepRig(t, stepCfg(), func(tk *v1alpha1.Task) {
		tk.Status.Runs = append(tk.Status.Runs, v1alpha1.RunRecord{ID: rid(1), Role: "implementer", Trigger: "resume", StartSeq: 6})
	})
	g.log.evs = append(g.log.evs, judgeEvent(2, rid(0), "not_yet", "older"), judgeEvent(4, rid(0), "not_yet", "the lost run's verdict"))
	if got, err := g.r.lastJudge(t.Context(), tk); err != nil || got != "the lost run's verdict" {
		t.Fatalf("%q %v", got, err)
	}
	g.log.evs = append(g.log.evs, judgeEvent(7, rid(1), "not_yet", "the resumed run's verdict"))
	if got, _ := g.r.lastJudge(t.Context(), tk); got != "the resumed run's verdict" {
		t.Fatalf("%q", got)
	}
	g.r.Rooms = endlessLog{g.log}
	if got, err := g.r.lastJudge(t.Context(), tk); err != nil || got != "" {
		t.Fatalf("an incomplete tail gave %q %v", got, err)
	}
}

func TestJudgeBlockFits(t *testing.T) {
	for name, text := range map[string]string{"plain": "check failed", "multiline": "one\ntwo\nthree", "utf-8": strings.Repeat("é", 40)} {
		t.Run(name, func(t *testing.T) {
			whole := judgeBlock(text, "n0nce234", 1<<20)
			if got := judgeBlock(text, "n0nce234", len(whole)); got != whole {
				t.Fatalf("an exact fit clipped: %q", got)
			}
			for room := len(whole) - 1; room > len(whole)-8; room-- {
				got := judgeBlock(text, "n0nce234", room)
				if len(got) > room || !utf8.ValidString(got) {
					t.Fatalf("room %d: %d bytes %q", room, len(got), got)
				}
			}
		})
	}
}

// A saturated revision brief still carries the judge's note, and quotes only the messages it shows.
func TestASaturatedStepBriefKeepsTheJudge(t *testing.T) {
	g, tk := stepRig(t, stepCfg(), nil)
	g.log.evs = append(g.log.evs, judgeEvent(5, rid(0), "not_yet", "check:validate still fails"))
	for i := range 8 {
		g.log.queue = append(g.log.queue, rooms.Queued{Ref: int64(10 + i), Author: "system:factory", Text: strings.Repeat("q", 3000)})
	}
	g.fail(t, tk, "not_met")
	tk = g.reconcile(t, "3buqdlot", 1)
	last := tk.Status.Runs[len(tk.Status.Runs)-1]
	text := g.runs.specs[last.ID].TaskText
	if !strings.Contains(text, "> check:validate still fails") || len(text) > reviseCap+len(ResumeNotice(tk)) {
		t.Fatalf("%d bytes, judge note %v", len(text), strings.Contains(text, "check:validate"))
	}
	if n := strings.Count(text, strings.Repeat("q", 3000)); n == 8 || n == 0 {
		t.Fatalf("%d whole messages quoted", n)
	}
}

// A pair task out of review rounds steps to the notConverged profile and runs its implementer again.
func TestARoundsExhaustedReviewSteps(t *testing.T) {
	g := pairRig(t)
	steps := int32(7)
	g.r.Cfg.Current().Loops = map[string]config.Loop{"review-search": {StallSteps: &steps}}
	g.r.Cfg.Current().HarnessProfiles = map[string]config.HarnessProfile{"review-floor": {}, "review-search": {Loop: "review-search"}}
	g.r.Cfg.Current().Classes["review"] = config.Class{Harness: "review-floor", HarnessAllowed: []string{"review-search"},
		Escalation: &config.Escalation{NotConverged: "review-search"}}
	tk := g.get(t)
	tk.Status.Profile = &v1alpha1.ProfileStatus{Name: "review-floor", Source: "floor", Cohort: "treatment", Resolved: true}
	if err := g.c.Status().Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	for i, rv := range []string{rid(1), rid(3), rid(5)} {
		g.log.verdict(rv, "changes", head1, "still wrong")
		g.finish(rv, "Succeeded", "agent_finished")
		tk = g.reconcile(t, "3buqdlot", 2)
		if i < 2 {
			g.finish(rid(2*i+2), "Succeeded", "agent_finished")
			g.reconcile(t, "3buqdlot", 2)
		}
	}
	p := tk.Status.Profile
	if tk.Status.Phase != v1alpha1.PhaseImplementing || tk.Status.ReviewRounds != 0 || p.Name != "review-search" ||
		p.Source != "escalation" || p.Reason != "not_converged" || tk.Status.Runs[6].Trigger != TriggerEscalation ||
		tk.Spec.Loop == nil || tk.Spec.Loop.StallSteps != 7 || strings.Contains(strings.Join(g.f.Comments(12), "\n"), "review rounds") {
		t.Fatalf("%s %d %+v %+v %+v", tk.Status.Phase, tk.Status.ReviewRounds, p, tk.Status.Runs, tk.Spec.Loop)
	}
}

// Out of review rounds, the last run is the verifier's: the note is still the implementer's verdict.
func TestAReviewEndedStepBriefCarriesTheImplementersJudge(t *testing.T) {
	g, tk := stepRig(t, stepCfg(), func(tk *v1alpha1.Task) {
		tk.Status.ReviewRounds = 2
		tk.Status.Runs = append(tk.Status.Runs, v1alpha1.RunRecord{ID: rid(1), Role: "reviewer", Trigger: "review", StartSeq: 4})
	})
	g.log.evs = append(g.log.evs, judgeEvent(3, rid(0), "not_yet", "the implementer's verdict"))
	if err := g.r.roundsExhausted(t.Context(), g.r.Cfg.Current(), tk, rooms.Verdict{RunID: rid(1), Verdict: "changes"}); err != nil {
		t.Fatal(err)
	}
	if err := g.c.Status().Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	wantStepped(t, g.get(t), "review-search", "not_converged")
	// R37: the reviewer's last findings are in the stepped brief too, after the reviewer's start.
	g.log.evs = append(g.log.evs, envelope.Event{Seq: 5, RunID: rid(1), Type: envelope.Message, Origin: envelope.OriginClient,
		Actor:   envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + rid(1), Role: "reviewer"},
		Payload: envelope.Must(envelope.MessagePayload{Kind: envelope.KindReviewVerdict, Verdict: "changes", Commit: "abc1234", Text: "the reviewer's findings", Delivery: envelope.DeliveryNone})})
	tk = g.reconcile(t, "3buqdlot", 1)
	last := tk.Status.Runs[len(tk.Status.Runs)-1]
	if text := g.runs.specs[last.ID].TaskText; last.Trigger != TriggerEscalation || !strings.Contains(text, "> the implementer's verdict") ||
		!strings.Contains(text, "Last review verdict (changes") || !strings.Contains(text, "the reviewer's findings") {
		t.Fatalf("%s %q", last.Trigger, text)
	}
}

// R37: a maintainer's class label puts a task in the labeled cohort, not the control one, so its
// class's escalation steps it as any treatment task.
func TestALabelledTaskSteps(t *testing.T) {
	g, tk := stepRig(t, stepCfg(), func(tk *v1alpha1.Task) {
		tk.Status.Profile.Source, tk.Status.Profile.Cohort = "label", "labeled"
	})
	got := g.fail(t, tk, "budget-run")
	p := got.Status.Profile
	if got.Status.Phase != v1alpha1.PhaseQueued || got.Status.NextTrigger != TriggerEscalation || p.Name != "review-frontier" ||
		p.Source != "escalation" || p.Reason != "out_of_budget" || p.Cohort != "labeled" || !outboxHas(got, "profile-escalated-") {
		t.Fatalf("%s %+v %+v", got.Status.Phase, p, got.Status.Outbox)
	}
}

// R37: the stepped run continues the task's work: the same branch, the same pull request, and the
// revision brief, not a first brief.
func TestAStepKeepsTheBranchAndThePullRequest(t *testing.T) {
	g, tk := stepRig(t, stepCfg(), func(tk *v1alpha1.Task) {
		tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", HeadSHA: "abc"}
	})
	g.f.SetBranch("agent/3buqdlot", 12)
	g.f.SetPR(pr12At("abc"))
	g.log.handoff(rid(0), "abc")
	wantStepped(t, g.fail(t, tk, "budget-run"), "review-frontier", "out_of_budget")
	tk = g.reconcile(t, "3buqdlot", 1)
	last := tk.Status.Runs[len(tk.Status.Runs)-1]
	s := g.runs.specs[last.ID]
	if tk.Status.Phase != v1alpha1.PhaseImplementing || last.Trigger != TriggerEscalation || s.Branch != "agent/3buqdlot" || s.BaseRef != "main" ||
		s.Tier != "frontier" || tk.Status.PullRequest == nil || tk.Status.PullRequest.Number != 12 || tk.Status.PullRequest.URL != "https://github.com/Smana/cloud-native-ref/pull/12" {
		t.Fatalf("%s %+v %+v", tk.Status.Phase, s, tk.Status.PullRequest)
	}
	if !strings.Contains(s.TaskText, "keep working on branch agent/3buqdlot and update its pull request #12") || strings.Contains(s.TaskText, "TASK-DATA-") {
		t.Fatalf("a revision brief, not a first one: %q", s.TaskText)
	}
}

// The step's spec write landed and its status write did not; the class then loses its escalation
// mapping. The replay recovers the persisted step: spec and status agree, one narration.
func TestAReplayRecoversThePersistedStep(t *testing.T) {
	g, tk := stepRig(t, stepCfg(), nil)
	if err := g.r.end(t.Context(), g.r.Cfg.Current(), tk, v1alpha1.PhaseEscalated, "stalled"); err != nil {
		t.Fatal(err)
	}
	stored := g.get(t) // the spec write landed, the status write never happened
	if stored.Spec.Budget.Tier != "frontier" || stored.Status.Profile.Source != "floor" {
		t.Fatalf("%s %+v", stored.Spec.Budget.Tier, stored.Status.Profile)
	}
	cl := g.r.Cfg.Current().Classes["review"]
	cl.Escalation = nil
	g.r.Cfg.Current().Classes["review"] = cl
	if err := g.r.end(t.Context(), g.r.Cfg.Current(), stored, v1alpha1.PhaseEscalated, "stalled"); err != nil {
		t.Fatal(err)
	}
	wantStepped(t, stored, "review-frontier", "out_of_budget")
	n := 0
	for _, o := range stored.Status.Outbox {
		if strings.HasPrefix(o.Key, "profile-escalated-") {
			n++
		}
	}
	if n != 1 || stored.Spec.Budget.Tier != "frontier" {
		t.Fatalf("%d narrations, tier %s", n, stored.Spec.Budget.Tier)
	}
}
