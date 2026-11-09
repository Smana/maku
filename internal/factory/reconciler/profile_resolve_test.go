// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/triage"
)

const testsDecl = "tests:\n  paths: [tests/]\n"

// resolveCfg is profileCfg with two variants the review class allows, one it does not, and mode.
func resolveCfg(mode string, percent int) *config.Config {
	c := profileCfg(true)
	steps := int32(7)
	c.Loops["review-search"] = config.Loop{StallSteps: &steps, Search: true}
	c.HarnessProfiles["review-search"] = config.HarnessProfile{Loop: "review-search", Description: "search first"}
	c.HarnessProfiles["review-tested"] = config.HarnessProfile{TestFirst: true, Description: "tests first"}
	c.HarnessProfiles["review-other"] = config.HarnessProfile{Description: "not allowed"}
	c.Classes["review"] = config.Class{Harness: "review-floor", HarnessAllowed: []string{"review-search", "review-tested"}}
	c.Evaluation = config.Evaluation{Mode: mode, ControlPercent: percent}
	return c
}

// triagedRig is a review task past triage under cf, its checks declaration decl.
func triagedRig(t *testing.T, cf *config.Config, label, decl string) (*rig, *v1alpha1.Task) {
	t.Helper()
	g := profileRig(t, cf, issueTask("3buqdlot", 7, "fix it"), label)
	if decl != "" {
		g.f.SetMainFiles(map[string]string{".github/agent-checks.yaml": decl})
	}
	tk := g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Profile == nil {
		t.Fatal("no profile at triage")
	}
	return g, tk
}

func TestResolutionTable(t *testing.T) {
	type want struct {
		name, source, reason, recommended string
		steps                             int32
		testFirst                         bool
	}
	floor := func(source, reason, rec string) want { return want{"review-floor", source, reason, rec, 40, false} }
	for name, c := range map[string]struct {
		mode, label, profile, decl string
		percent                    int
		internal, failed           bool
		want                       want
	}{
		"applied":         {mode: "apply", profile: "review-search", want: want{"review-search", "scoper", "", "review-search", 7, false}},
		"applied tests":   {mode: "apply", profile: "review-tested", decl: testsDecl, want: want{"review-tested", "scoper", "", "review-tested", 40, true}},
		"absent":          {mode: "apply", want: floor("floor", "none", "")},
		"the floor":       {mode: "apply", profile: "review-floor", want: floor("floor", "none", "review-floor")},
		"unknown":         {mode: "apply", profile: "zzz", want: floor("floor", "unknown", "zzz")},
		"not allowed":     {mode: "apply", profile: "review-other", want: floor("floor", "not_allowed", "review-other")},
		"internal":        {mode: "apply", profile: "review-tested", decl: testsDecl, internal: true, want: floor("floor", "internal", "review-tested")},
		"no tests paths":  {mode: "apply", profile: "review-tested", want: floor("floor", "no_tests_paths", "review-tested")},
		"observe":         {mode: "observe", profile: "review-search", want: floor("floor", "observe", "review-search")},
		"control":         {mode: "apply", percent: 100, profile: "review-search", want: floor("control", "", "review-search")},
		"labelled":        {mode: "apply", label: "class:review", profile: "review-search", want: floor("label", "", "")},
		"scoper failed":   {mode: "apply", failed: true, want: floor("floor", "scoper_failed", "")},
		"control, failed": {mode: "apply", percent: 100, failed: true, want: floor("control", "scoper_failed", "")},
	} {
		t.Run(name, func(t *testing.T) {
			g, tk := triagedRig(t, resolveCfg(c.mode, c.percent), c.label, c.decl)
			if c.internal {
				tk.Spec.DataClass = "internal"
			}
			tier := tk.Spec.Budget.Tier
			var sc *envelope.Scope
			if !c.failed {
				s := readyScope()
				s.Profile = c.profile
				if c.profile != "" {
					s.ProfileWhy = "because"
				}
				sc = &s
			}
			if err := g.r.resolveProfile(t.Context(), tk, sc); err != nil {
				t.Fatal(err)
			}
			p := tk.Status.Profile
			if p == nil || !p.Resolved || p.Name != c.want.name || p.Source != c.want.source || p.Reason != c.want.reason ||
				p.Recommended != c.want.recommended || p.TestFirst != c.want.testFirst || !p.Checks {
				t.Fatalf("profile %+v, want %+v", p, c.want)
			}
			if c.want.recommended != "" && p.Why != "because" {
				t.Fatalf("why %q", p.Why)
			}
			if tk.Spec.Template != "pair" || tk.Spec.Loop == nil || tk.Spec.Loop.StallSteps != c.want.steps || tk.Spec.Budget.Tier != tier {
				t.Fatalf("spec %s %+v %s", tk.Spec.Template, tk.Spec.Loop, tk.Spec.Budget.Tier)
			}
			if !slices.ContainsFunc(tk.Status.Outbox, func(o v1alpha1.Narration) bool {
				return o.Key == "profile-chosen-0" && strings.Contains(o.Body, c.want.name)
			}) {
				t.Fatalf("outbox %+v", tk.Status.Outbox)
			}
			var want []string
			if c.label == "" {
				rec := c.want.recommended
				switch rec {
				case "":
					rec = "none"
				case "zzz", "review-other":
					rec = "other"
				}
				cohort := "treatment"
				if c.percent == 100 {
					cohort = "control"
				}
				want = []string{fmt.Sprintf("profile review %s %s %v", cohort, rec, c.want.source == "scoper")}
			}
			got := slices.DeleteFunc(slices.Clone(g.metrics.recorded), func(m string) bool { return !strings.HasPrefix(m, "profile ") })
			if !slices.Equal(got, want) {
				t.Fatalf("metrics %q, want %q", got, want)
			}
		})
	}
}

// The step that resolves fails its status write: the replay writes the spec once, queues one line
// and counts once.
func TestResolutionReplaysOnce(t *testing.T) {
	cf := resolveCfg("apply", 0)
	cf.Tiers["light"] = config.Tier{Model: "agent-default", RunTokens: 300_000, TaskTokens: 600_000, RunMinutes: 20}
	g := profileRig(t, cf, issueTask("3buqdlot", 7, "# Fix the link\n\n## Acceptance check\nthe link resolves"), "")
	g.f.SetFile(".github/agent-factory/steering/review.md", reviewNote)
	if tk := g.reconcile(t, "3buqdlot", 3); len(tk.Status.Runs) != 1 {
		t.Fatalf("%s %s %+v %q", tk.Status.Phase, tk.Spec.ScopeMode, tk.Status.Runs, tk.Status.Reason)
	}
	sc := readyScope()
	sc.Profile, sc.ProfileWhy = "review-search", "because"
	scopeHandoff(g, 0, sc)
	g.runs.set(rid(0), "Succeeded")
	g.log.end(rid(0), "Succeeded", "agent_finished")
	updates, failed := 0, false
	g.r.Client = interceptor.NewClient(g.c.(client.WithWatch), interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			updates++
			return cl.Update(ctx, o, opts...)
		},
		SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
			if tk, ok := o.(*v1alpha1.Task); ok && tk.Status.Profile != nil && tk.Status.Profile.Resolved && !failed {
				failed = true
				return errors.New("status write refused")
			}
			return cl.SubResource(sub).Update(ctx, o, opts...)
		}})
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err == nil || !failed {
		t.Fatalf("the status write should fail: %v", err)
	}
	if n := countProfile(g.metrics.recorded); n != 0 {
		t.Fatalf("counted before the write: %v", g.metrics.recorded)
	}
	tk := g.reconcile(t, "3buqdlot", 1)
	if p := tk.Status.Profile; p == nil || p.Name != "review-search" || !p.Resolved || tk.Status.Phase != v1alpha1.PhaseQueued ||
		tk.Status.Scope == nil || tk.Status.Scope.State != "ready" || tk.Spec.ScopeMode != string(config.ScopeScoper) {
		t.Fatalf("%s %q %+v %+v", tk.Status.Phase, tk.Status.Reason, tk.Status.Runs, p)
	}
	keys := 0
	for _, o := range tk.Status.Outbox {
		if strings.HasPrefix(o.Key, "profile-chosen-") {
			keys++
		}
	}
	if updates != 1 || keys+len(slices.DeleteFunc(slices.Clone(tk.Status.Narrated), func(k string) bool { return !strings.HasPrefix(k, "profile-chosen-") })) != 1 ||
		countProfile(g.metrics.recorded) != 1 {
		t.Fatalf("updates %d, outbox %+v narrated %v, metrics %v", updates, tk.Status.Outbox, tk.Status.Narrated, g.metrics.recorded)
	}
}

func countProfile(ms []string) int {
	n := 0
	for _, m := range ms {
		if strings.HasPrefix(m, "profile ") {
			n++
		}
	}
	return n
}

// A profile frozen at resolution holds when config drops it: checks and the tester as frozen.
func TestADeletedProfileKeepsTheFrozenOne(t *testing.T) {
	cf := resolveCfg("apply", 0)
	g, tk := triagedRig(t, cf, "", testsDecl)
	sc := readyScope()
	sc.Profile = "review-tested"
	if err := g.r.resolveProfile(t.Context(), tk, &sc); err != nil {
		t.Fatal(err)
	}
	delete(cf.HarnessProfiles, "review-tested")
	cf.Classes["review"] = config.Class{Harness: "review-floor"}
	if !g.r.testFirst(tk) || !g.r.implementerSpec(tk, "brief").Checks || !g.r.testerDue(tk) {
		t.Fatalf("testFirst %v checks %v", g.r.testFirst(tk), g.r.implementerSpec(tk, "brief").Checks)
	}
}

func TestResolutionNeverLowersTheTier(t *testing.T) {
	cf := resolveCfg("apply", 0)
	g, tk := triagedRig(t, cf, "", "")
	tk.Spec.Budget = triage.BudgetFor(cf, "frontier")
	sc := readyScope()
	sc.Profile = "review-search"
	if err := g.r.resolveProfile(t.Context(), tk, &sc); err != nil {
		t.Fatal(err)
	}
	if tk.Status.Profile.Name != "review-search" || tk.Spec.Budget.Tier != "frontier" {
		t.Fatalf("%+v %s", tk.Status.Profile, tk.Spec.Budget.Tier)
	}
}

func TestASplitChildKeepsToday(t *testing.T) {
	cf := resolveCfg("apply", 0)
	g, tk := triagedRig(t, cf, "", "")
	tk.Spec.Parent = &v1alpha1.Parent{Task: "parent1"}
	before := *tk.Status.Profile
	sc := readyScope()
	sc.Profile = "review-search"
	if err := g.r.resolveProfile(t.Context(), tk, &sc); err != nil {
		t.Fatal(err)
	}
	if *tk.Status.Profile != before || len(tk.Status.Outbox) != 0 || countProfile(g.metrics.recorded) != 0 {
		t.Fatalf("%+v %+v", tk.Status.Profile, tk.Status.Outbox)
	}
}

// harnessed gives c's review class a floor and one variant, applied.
func harnessed(c *config.Config) {
	c.HarnessProfiles = map[string]config.HarnessProfile{"review-floor": {}, "review-wide": {Checks: true, Description: "wide"}}
	c.Classes["review"] = config.Class{Harness: "review-floor", HarnessAllowed: []string{"review-wide"}}
	c.Evaluation.Mode = config.EvaluationApply
}

// harnessedScoper is a scoper-mode task under harnessed config, edited before triage, past its scoper's start.
func harnessedScoper(t *testing.T, edit func(*v1alpha1.Task)) *rig {
	t.Helper()
	tk := issueTask("3buqdlot", 7, "# Fix the link\n\n## Acceptance check\nthe link resolves")
	if edit != nil {
		edit(tk)
	}
	g := newRig(t, tk)
	g.r.Cfg.Loops = map[string]config.Loop{"review": {Scope: config.ScopeScoper}}
	g.r.Cfg.Tiers["light"] = config.Tier{Model: "agent-default", RunTokens: 300_000, TaskTokens: 600_000, RunMinutes: 20}
	harnessed(g.r.Cfg)
	g.f.SetFile(".github/agent-factory/steering/review.md", reviewNote)
	g.reconcile(t, "3buqdlot", 3)
	return g
}

// wantResolved checks tk's profile and its profile-chosen narrations (queued or posted).
func wantResolved(t *testing.T, tk *v1alpha1.Task, name, source, reason string, narrations int) {
	t.Helper()
	p := tk.Status.Profile
	if p == nil || !p.Resolved || p.Name != name || p.Source != source || p.Reason != reason {
		t.Fatalf("%s %+v, want %s %s %s", tk.Status.Phase, p, name, source, reason)
	}
	n := 0
	for _, o := range tk.Status.Outbox {
		if strings.HasPrefix(o.Key, "profile-chosen-") {
			n++
		}
	}
	for _, k := range tk.Status.Narrated {
		if strings.HasPrefix(k, "profile-chosen-") {
			n++
		}
	}
	if n != narrations {
		t.Fatalf("%d profile-chosen narrations, want %d: %+v %v", n, narrations, tk.Status.Outbox, tk.Status.Narrated)
	}
}

// Every scoper end that queues the task itself resolves its profile first.
func TestEveryRunnableScoperEndResolves(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		g := harnessedScoper(t, nil)
		tk := endScoper(t, g, "Succeeded", "agent_finished")
		if tk.Status.Phase != v1alpha1.PhaseQueued || tk.Status.Scope.State != "missing" {
			t.Fatalf("%s %+v", tk.Status.Phase, tk.Status.Scope)
		}
		wantResolved(t, tk, "review-floor", "floor", "scoper_failed", 1)
	})
	t.Run("assumed, a schedule", func(t *testing.T) {
		g := harnessedScoper(t, func(tk *v1alpha1.Task) { tk.Spec.Issue, tk.Spec.Source.Kind = 0, "schedule" })
		scopeHandoff(g, 0, questionScope())
		tk := endScoper(t, g, "Succeeded", "agent_finished")
		if tk.Status.Phase != v1alpha1.PhaseQueued || tk.Status.Scope.State != "assumed" {
			t.Fatalf("%s %+v", tk.Status.Phase, tk.Status.Scope)
		}
		wantResolved(t, tk, "review-floor", "floor", "none", 1) // no issue yet: the announcement waits for the pull request (R31)
		if o := tk.Status.Outbox[len(tk.Status.Outbox)-1]; o.Number != 0 || !strings.HasPrefix(o.Key, "profile-chosen-") {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("assumed on retry", func(t *testing.T) {
		g := harnessedScoper(t, nil)
		scopeHandoff(g, 0, questionScope())
		if tk := endScoper(t, g, "Succeeded", "agent_finished"); tk.Status.Profile.Resolved {
			t.Fatalf("a bounce waits unresolved: %+v", tk.Status.Profile)
		}
		g.r.Now = func() time.Time { return now.Add(time.Hour) }
		g.f.SetComments(7, retryBy(77, "smana", -30*time.Minute))
		tk := g.reconcile(t, "3buqdlot", 1)
		if tk.Status.Phase != v1alpha1.PhaseQueued || tk.Status.Scope.State != "assumed" {
			t.Fatalf("%s %+v", tk.Status.Phase, tk.Status.Scope)
		}
		wantResolved(t, tk, "review-floor", "floor", "none", 1)
	})
}

// harnessedSplit is a split rig under harnessed config whose scoper recommends review-wide with a
// split of n parts.
func harnessedSplit(t *testing.T, n int) (*rig, *v1alpha1.Task) {
	t.Helper()
	g := splitRigWith(t, "frontier", false, nil)
	harnessed(g.r.Cfg)
	g.reconcile(t, "3buqdlot", 3)
	sc := readyScope()
	sc.Profile, sc.ProfileWhy = "review-wide", "two pages"
	splitHandoffOf(g, 0, sc, parts(n))
	return g, endScoper(t, g, "Succeeded", "agent_finished")
}

func TestASplitResolvesWhenTheTaskRunsWhole(t *testing.T) {
	t.Run("ignored", func(t *testing.T) {
		_, tk := harnessedSplit(t, 4) // over maxChildren 3
		if tk.Status.Phase != v1alpha1.PhaseQueued || tk.Status.Split.State != SplitIgnored {
			t.Fatalf("%s %+v", tk.Status.Phase, tk.Status.Split)
		}
		wantResolved(t, tk, "review-wide", "scoper", "", 1)
	})
	t.Run("declined by a retry", func(t *testing.T) {
		g, tk := harnessedSplit(t, 2)
		if tk.Status.Phase != v1alpha1.PhaseEscalated || tk.Status.Profile.Resolved {
			t.Fatalf("a proposal waits unresolved: %s %+v", tk.Status.Phase, tk.Status.Profile)
		}
		g.r.Now = func() time.Time { return now.Add(time.Hour) }
		g.f.SetComments(7, retryBy(77, "Smana", -30*time.Minute))
		tk = g.reconcile(t, "3buqdlot", 1)
		if tk.Status.Phase != v1alpha1.PhaseQueued || tk.Status.Split.State != "declined" {
			t.Fatalf("%s %+v", tk.Status.Phase, tk.Status.Split)
		}
		wantResolved(t, tk, "review-wide", "scoper", "", 1)
	})
	t.Run("approved", func(t *testing.T) {
		g, _ := harnessedSplit(t, 2)
		g.r.Now = func() time.Time { return now.Add(time.Hour) }
		g.f.SetComments(7, splitBy(80, "Smana"))
		tk := g.reconcile(t, "3buqdlot", 1)
		if tk.Status.Split.State != SplitApproved || tk.Status.Profile == nil || tk.Status.Profile.Resolved {
			t.Fatalf("%s %+v %+v", tk.Status.Phase, tk.Status.Split, tk.Status.Profile)
		}
	})
}

// A harness class whose row runs no scoper resolves as it is queued, once, uncounted.
func TestAnUnscopedTaskResolvesAtAdmission(t *testing.T) {
	cf := resolveCfg("apply", 0)
	steps := int32(40)
	cf.Loops["review"] = config.Loop{StallSteps: &steps}
	g := profileRig(t, cf, issueTask("3buqdlot", 7, "fix it"), "")
	failed := false
	g.r.Client = interceptor.NewClient(g.c.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
			if tk, ok := o.(*v1alpha1.Task); ok && tk.Status.Phase == v1alpha1.PhaseQueued && !failed {
				failed = true
				return errors.New("status write refused")
			}
			return cl.SubResource(sub).Update(ctx, o, opts...)
		}})
	g.reconcile(t, "3buqdlot", 1) // Received → Triaged
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err == nil || !failed {
		t.Fatalf("the Queued write should fail: %v", err)
	}
	tk := g.reconcile(t, "3buqdlot", 2) // Triaged → Queued, then the run
	if tk.Spec.ScopeMode != string(config.ScopeOff) {
		t.Fatalf("scope %s", tk.Spec.ScopeMode)
	}
	wantResolved(t, tk, "review-floor", "floor", "", 1)
	if p := tk.Status.Profile; p.Recommended != "" || p.Why != "" || countProfile(g.metrics.recorded) != 0 {
		t.Fatalf("%+v %v", p, g.metrics.recorded)
	}
}

// flakyLog fails the next EventsSince once when armed, as a broker outage would.
type flakyLog struct {
	*fakeLog
	fail bool
}

func (l *flakyLog) EventsSince(ctx context.Context, room string, after int64, keep func(envelope.Event) bool) ([]envelope.Event, int64, error) {
	if l.fail {
		l.fail = false
		return nil, 0, errors.New("broker down")
	}
	return l.fakeLog.EventsSince(ctx, room, after, keep)
}

// A ready scope's recommendation bounced for want of an acceptance check runs assumed, on the floor.
func TestAnAssumedRetryAppliesNoRecommendation(t *testing.T) {
	g := harnessedScoper(t, func(tk *v1alpha1.Task) { tk.Spec.Text = "fix it" })
	sc := readyScope()
	sc.Profile, sc.ProfileWhy = "review-wide", "two pages"
	scopeHandoff(g, 0, sc)
	if tk := endScoper(t, g, "Succeeded", "agent_finished"); tk.Status.Reason != ReasonNeedsAuthor {
		t.Fatalf("%s %q", tk.Status.Phase, tk.Status.Reason)
	}
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	g.f.SetComments(7, retryBy(77, "smana", -30*time.Minute))
	tk := g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Scope.State != "assumed" || tk.Status.Profile.Recommended != "" {
		t.Fatalf("%+v %+v", tk.Status.Scope, tk.Status.Profile)
	}
	wantResolved(t, tk, "review-floor", "floor", "none", 1)
}

// A broker error reading the scope leaves the retry unhandled; the next reconcile replays it once.
func TestABrokerErrorReplaysTheRetry(t *testing.T) {
	g := harnessedScoper(t, nil)
	scopeHandoff(g, 0, questionScope())
	endScoper(t, g, "Succeeded", "agent_finished")
	fl := &flakyLog{fakeLog: g.log, fail: true}
	g.r.Rooms = fl
	g.r.Now = func() time.Time { return now.Add(time.Hour) }
	g.f.SetComments(7, retryBy(77, "smana", -30*time.Minute))
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err == nil {
		t.Fatal("the broker error should surface")
	}
	tk := g.reconcile(t, "3buqdlot", 0)
	if tk.Status.Phase != v1alpha1.PhaseEscalated || tk.Status.Retries != 0 || slices.Contains(tk.Status.Handled, 77) ||
		tk.Status.Scope.State != "question" || tk.Status.Profile.Resolved {
		t.Fatalf("the retry changed state: %s %d %v %+v %+v", tk.Status.Phase, tk.Status.Retries, tk.Status.Handled, tk.Status.Scope, tk.Status.Profile)
	}
	tk = g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseQueued || tk.Status.Retries != 1 {
		t.Fatalf("%s %d", tk.Status.Phase, tk.Status.Retries)
	}
	wantResolved(t, tk, "review-floor", "floor", "none", 1)
}

// A refused spec write leaves the profile unresolved, unnarrated and uncounted; the next try applies once.
func TestARefusedSpecWriteLeavesItUnresolved(t *testing.T) {
	g, tk := triagedRig(t, resolveCfg("apply", 0), "", "")
	refuse := true
	g.r.Client = interceptor.NewClient(g.c.(client.WithWatch), interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			if refuse {
				refuse = false
				return errors.New("spec write refused")
			}
			return cl.Update(ctx, o, opts...)
		}})
	sc := readyScope()
	sc.Profile = "review-search"
	if err := g.r.resolveProfile(t.Context(), tk, &sc); err == nil {
		t.Fatal("the refusal should surface")
	}
	if p := tk.Status.Profile; p.Resolved || p.Name != "review-floor" || p.Recommended != "" || len(tk.Status.Outbox) != 0 || countProfile(g.metrics.recorded) != 0 {
		t.Fatalf("%+v %+v %v", p, tk.Status.Outbox, g.metrics.recorded)
	}
	if err := g.r.resolveProfile(t.Context(), tk, &sc); err != nil {
		t.Fatal(err)
	}
	if p := tk.Status.Profile; !p.Resolved || p.Name != "review-search" || tk.Spec.Loop.StallSteps != 7 || len(tk.Status.Outbox) != 1 || countProfile(g.metrics.recorded) != 1 {
		t.Fatalf("%+v %+v %v", p, tk.Status.Outbox, g.metrics.recorded)
	}
}

// The spec write landed and the status write did not; the config then turns the mode to observe.
// The replay recovers the persisted decision: spec and status agree, one narration, one count.
func TestAReplayRecoversThePersistedResolution(t *testing.T) {
	cf := resolveCfg("apply", 0)
	cf.Tiers["light"] = config.Tier{Model: "agent-default", RunTokens: 300_000, TaskTokens: 600_000, RunMinutes: 20}
	g := profileRig(t, cf, issueTask("3buqdlot", 7, "# Fix the link\n\n## Acceptance check\nthe link resolves"), "")
	g.f.SetFile(".github/agent-factory/steering/review.md", reviewNote)
	g.reconcile(t, "3buqdlot", 3)
	sc := readyScope()
	sc.Profile, sc.ProfileWhy = "review-search", "because"
	scopeHandoff(g, 0, sc)
	g.runs.set(rid(0), "Succeeded")
	g.log.end(rid(0), "Succeeded", "agent_finished")
	failed := false
	g.r.Client = interceptor.NewClient(g.c.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
			if tk, ok := o.(*v1alpha1.Task); ok && tk.Status.Profile != nil && tk.Status.Profile.Resolved && !failed {
				failed = true
				return errors.New("status write refused")
			}
			return cl.SubResource(sub).Update(ctx, o, opts...)
		}})
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err == nil || !failed {
		t.Fatalf("the status write should fail: %v", err)
	}
	if stored := g.get(t); stored.Spec.Loop == nil || stored.Spec.Loop.StallSteps != 7 || stored.Status.Profile.Resolved {
		t.Fatalf("the spec write landed alone: %+v %+v", stored.Spec.Loop, stored.Status.Profile)
	}
	// Between the two writes the config changes under the task: observe, and the class offers no
	// variant any more. The persisted decision binds: status, narration, the recommendation count
	// and the outcome's label all say what was decided.
	cf.Evaluation.Mode = config.EvaluationObserve
	cf.Classes["review"] = config.Class{Harness: "review-floor"}
	tk := g.reconcile(t, "3buqdlot", 1)
	wantResolved(t, tk, "review-search", "scoper", "", 1)
	if tk.Spec.Loop == nil || tk.Spec.Loop.StallSteps != 7 || tk.Status.Profile.Recommended != "review-search" || tk.Status.Profile.Why != "because" {
		t.Fatalf("%+v %+v", tk.Spec.Loop, tk.Status.Profile)
	}
	if c := g.f.Comments(7); !strings.Contains(c[len(c)-1], "`review-search`") || !strings.Contains(c[len(c)-1], "the scoper: because") {
		t.Fatalf("narration %q", c[len(c)-1])
	}
	if got := slices.DeleteFunc(slices.Clone(g.metrics.recorded), func(m string) bool { return !strings.HasPrefix(m, "profile ") }); !slices.Equal(got, []string{"profile review treatment review-search true"}) {
		t.Fatalf("metrics %q", got)
	}
	tk.Annotations[v1alpha1.AnnotationStop] = "true"
	if err := g.c.Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	g.reconcile(t, "3buqdlot", 1)
	if got := taskOutcomes(g.metrics.recorded); len(got) != 1 || !strings.Contains(got[0], "review-search scoper review-search stopped") {
		t.Fatalf("outcome %q", got)
	}
	if got := interventions(g.metrics.recorded); !slices.Equal(got, []string{"intervention stop treatment review-search review-search"}) {
		t.Fatalf("interventions %q", got)
	}
}

// R31: a task with no issue (a schedule's) resolves its profile before it has a pull request, so
// the announcement has nowhere to go yet. It stays in the outbox, is posted once the pull request
// exists, and once only.
func TestAProfileAnnouncementWaitsForATarget(t *testing.T) {
	cf := resolveCfg("apply", 0)
	cf.Tiers["light"] = config.Tier{Model: "agent-default", RunTokens: 300_000, TaskTokens: 600_000, RunMinutes: 20}
	tk := issueTask("3buqdlot", 0, "# Fix the link\n\n## Acceptance check\nthe link resolves")
	tk.Spec.Source = v1alpha1.Source{Kind: "schedule", Ref: "weekly-links", Key: "k", RequestedBy: "schedule:weekly-links", Trust: "trusted", ContentSHA256: strings.Repeat("b", 64)}
	g := profileRig(t, cf, tk, "")
	g.f.SetFile(".github/agent-factory/steering/review.md", reviewNote)
	g.reconcile(t, "3buqdlot", 3)
	sc := readyScope()
	sc.Profile, sc.ProfileWhy = "review-search", "because"
	scopeHandoff(g, 0, sc)
	g.runs.set(rid(0), "Succeeded")
	g.log.end(rid(0), "Succeeded", "agent_finished")
	tk = g.reconcile(t, "3buqdlot", 2)
	if p := tk.Status.Profile; p == nil || !p.Resolved || p.Name != "review-search" {
		t.Fatalf("%+v", p)
	}
	pending := slices.IndexFunc(tk.Status.Outbox, func(o v1alpha1.Narration) bool { return strings.HasPrefix(o.Key, "profile-chosen-") })
	if pending < 0 || tk.Status.Outbox[pending].Number != 0 || len(g.f.Comments(12)) != 0 || slices.ContainsFunc(tk.Status.Narrated, func(k string) bool { return strings.HasPrefix(k, "profile-chosen-") }) {
		t.Fatalf("pending: %+v %v", tk.Status.Outbox, tk.Status.Narrated)
	}
	g.f.SetBranch("agent/3buqdlot", 12)
	g.f.SetPR(forge.PR{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", HeadRef: "agent/3buqdlot", HeadSHA: "abc", Author: "ogenki-agents[bot]", State: "OPEN"})
	g.log.handoff(rid(1), "abc")
	g.finish(rid(1), "Succeeded", "agent_finished")
	tk = g.reconcile(t, "3buqdlot", 2)
	if tk.Status.PullRequest == nil || tk.Status.PullRequest.Number != 12 {
		t.Fatalf("%+v", tk.Status.PullRequest)
	}
	announced := func() int {
		n := 0
		for _, c := range g.f.Comments(12) {
			if strings.Contains(c, "runs under profile `review-search`") {
				n++
			}
		}
		return n
	}
	if got := g.f.Comments(12); announced() != 1 || !strings.Contains(got[0], "runs under profile") {
		t.Fatalf("posted first, once: %q", got)
	}
	tk = g.reconcile(t, "3buqdlot", 2)
	if announced() != 1 || slices.ContainsFunc(tk.Status.Outbox, func(o v1alpha1.Narration) bool { return strings.HasPrefix(o.Key, "profile-chosen-") }) {
		t.Fatalf("once: %q %+v", g.f.Comments(12), tk.Status.Outbox)
	}
}

// R31's other end: a task that ends with no target drops the waiting announcement, so its outbox
// empties and the task closes.
func TestAWaitingAnnouncementIsDroppedAtTheEnd(t *testing.T) {
	cf := resolveCfg("apply", 0)
	cf.Tiers["light"] = config.Tier{Model: "agent-default", RunTokens: 300_000, TaskTokens: 600_000, RunMinutes: 20}
	tk := issueTask("3buqdlot", 0, "# Fix the link\n\n## Acceptance check\nthe link resolves")
	tk.Spec.Source = v1alpha1.Source{Kind: "schedule", Ref: "weekly-links", Key: "k", RequestedBy: "schedule:weekly-links", Trust: "trusted", ContentSHA256: strings.Repeat("b", 64)}
	g := profileRig(t, cf, tk, "")
	g.f.SetFile(".github/agent-factory/steering/review.md", reviewNote)
	g.reconcile(t, "3buqdlot", 3)
	sc := readyScope()
	sc.Profile = "review-search"
	scopeHandoff(g, 0, sc)
	g.finish(rid(0), "Succeeded", "agent_finished")
	tk = g.reconcile(t, "3buqdlot", 2)
	if !slices.ContainsFunc(tk.Status.Outbox, func(o v1alpha1.Narration) bool { return strings.HasPrefix(o.Key, "profile-chosen-") }) {
		t.Fatalf("%+v", tk.Status.Outbox)
	}
	tk.Annotations[v1alpha1.AnnotationStop] = "true"
	if err := g.c.Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	tk = g.reconcile(t, "3buqdlot", 3)
	if tk.Status.Phase != v1alpha1.PhaseStopped || len(tk.Status.Outbox) != 0 || !slices.ContainsFunc(tk.Status.Narrated, func(k string) bool { return strings.HasPrefix(k, "profile-chosen-") }) {
		t.Fatalf("%s %+v %v", tk.Status.Phase, tk.Status.Outbox, tk.Status.Narrated)
	}
}
