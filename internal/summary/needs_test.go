// SPDX-License-Identifier: Apache-2.0

package summary_test

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/summary"
)

// needsWant is one item a case expects, as testdata/needs/cases.json writes it.
type needsWant struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Action     string `json:"action"`
	URL        string `json:"url"`
	WhatPrefix string `json:"whatPrefix"`
	Approve    bool   `json:"approve"`
}

// needsCaller is one caller a case is asked about: what the policy says, and their GitHub standing.
type needsCaller struct {
	Login      string `json:"login"`
	WebUI      bool   `json:"webui"`
	CanClose   bool   `json:"canClose"`
	CanDecide  bool   `json:"canDecide"`
	CanRelease bool   `json:"canRelease"`
	MayWrite   bool   `json:"mayWrite"`
	RoomScoped bool   `json:"roomScoped"`
}

// needsCase is one room in one fixture: its facts, its approvals, and what each caller is owed.
type needsCase struct {
	Name       string              `json:"name"`
	Room       string              `json:"room"`
	Repository string              `json:"repository"`
	FactsAt    time.Time           `json:"factsAt"`
	Facts      *envelope.TaskFacts `json:"facts"`
	Reviewers  []string            `json:"reviewers"`
	Approvals  []struct {
		ID        string    `json:"id"`
		What      string    `json:"what"`
		ExpiresAt time.Time `json:"expiresAt"`
		At        time.Time `json:"at"`
	} `json:"approvals"`
	Callers map[string]needsCaller `json:"callers"`
	Want    map[string][]needsWant `json:"want"`
}

// in is the case as caller sees it: one NeedsIn, as every surface builds it (CD21, CD26).
func (c needsCase) in(caller, now string) summary.NeedsIn {
	cu := c.Callers[caller]
	in := summary.NeedsIn{
		Room: c.Room, URL: "https://rooms.example/r/" + c.Room, Repository: c.Repository,
		FactsRead: true, Facts: c.Facts, FactsAt: c.FactsAt,
		GitHub: summary.GitHubStanding{Login: cu.Login, MayWrite: cu.MayWrite, Reviewers: c.Reviewers},
		Caller: summary.Caller{WebUI: cu.WebUI, CanDecide: cu.CanDecide, CanClose: cu.CanClose,
			CanRelease: cu.CanRelease, RoomScoped: cu.RoomScoped},
	}
	in.Now, _ = time.Parse(time.RFC3339, now)
	for _, a := range c.Approvals {
		in.Approvals = append(in.Approvals, summary.Approval{ID: a.ID, What: a.What, ExpiresAt: a.ExpiresAt, At: a.At})
	}
	return in
}

// wantAt is the i-th item the case expects for caller, if it names one.
func (c needsCase) wantAt(i int, caller string) needsWant {
	if w := c.Want[caller]; i < len(w) {
		return w[i]
	}
	return needsWant{}
}

// wantKinds is the case's items for caller as "<kind>/<id>/<action kind>", in the only order (CD21).
func (c needsCase) wantKinds(caller string) []string {
	var out []string
	for _, w := range c.Want[caller] {
		out = append(out, w.Kind+"/"+w.ID+"/"+w.Action)
	}
	return out
}

// loadNeedsCases reads the fixture: the one table every surface's derivation test asserts against.
func loadNeedsCases(t *testing.T) (now string, cases []needsCase) {
	t.Helper()
	b, err := os.ReadFile("testdata/needs/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Now   string      `json:"now"`
		Cases []needsCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("testdata/needs/cases.json: %v", err)
	}
	if f.Now == "" || len(f.Cases) == 0 {
		t.Fatal("testdata/needs/cases.json names no now or no cases")
	}
	return f.Now, f.Cases
}

// allowedURL reports whether a need's link is one a view may render as a link: the room's page or a
// validated GitHub one, https only. NeedsIn builds every action URL from the room URL and the facts'
// validated URLs, never from a note (needs/v1).
func allowedURL(u string) bool {
	return strings.HasPrefix(u, "https://rooms.example/") || strings.HasPrefix(u, "https://github.com/")
}

// The fixture is the contract: every surface asserts against these same cases (CD21, offline proof 6).
func TestNeedsCases(t *testing.T) {
	now, cases := loadNeedsCases(t)
	for _, c := range cases {
		for caller := range c.Callers {
			got := summary.Ordered(summary.Items(c.in(caller, now)))
			var kinds []string
			for _, n := range got {
				kinds = append(kinds, n.Kind+"/"+n.ID+"/"+n.Action.Kind)
				if n.Action.URL != "" && !allowedURL(n.Action.URL) {
					t.Fatalf("%s/%s: off-allowlist url %q", c.Name, caller, n.Action.URL)
				}
				if !strings.HasPrefix(n.URL, "https://") { // the kind's own link is a validated one too
					t.Fatalf("%s/%s: off-allowlist need url %q", c.Name, caller, n.URL)
				}
			}
			if !slices.Equal(kinds, c.wantKinds(caller)) {
				t.Fatalf("%s/%s: %v, want %v", c.Name, caller, kinds, c.wantKinds(caller))
			}
			for i, n := range got {
				if p := c.wantAt(i, caller).WhatPrefix; p != "" && !strings.HasPrefix(n.What, p) {
					t.Fatalf("%s/%s: %q does not start with %q", c.Name, caller, n.What, p)
				}
				if want := c.wantAt(i, caller).Approve; n.Approve != want {
					t.Fatalf("%s/%s: approve %v, want %v (CD26, AM3)", c.Name, caller, n.Approve, want)
				}
				if u := c.wantAt(i, caller).URL; u != "" && n.Action.URL != u {
					t.Fatalf("%s/%s: action url %q, want %q", c.Name, caller, n.Action.URL, u)
				}
			}
		}
	}
}

// Phase G5 order: the five inbox types (question, plan, review, blocked, failed), then deadline
// ascending (none last), then kind within type, then room.
func TestNeedsOrder(t *testing.T) {
	in := []summary.Need{
		{Room: "bbbbbbbb", Kind: "hold", ID: "x"},
		{Room: "aaaaaaaa", Kind: "merge", ID: "12"},
		{Room: "cccccccc", Kind: "merge", ID: "13"},
		{Room: "aaaaaaaa", Kind: "approval", ID: "01", Deadline: time.Date(2026, 11, 4, 19, 20, 0, 0, time.UTC)},
	}
	var got []string
	for _, n := range summary.Ordered(in) {
		got = append(got, n.Key())
	}
	want := []string{"aaaaaaaa/merge/12", "cccccccc/merge/13", "aaaaaaaa/approval/01", "bbbbbbbb/hold/x"}
	if !slices.Equal(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

// Every needs/v1 kind folds into one of the five closed inbox types, and the four room-act kinds
// declare the broker act they accept on the page (phase G2, G5).
func TestEveryNeedKindHasAType(t *testing.T) {
	closed := summary.NeedTypes()
	wantTypes := []string{summary.TypeQuestion, summary.TypePlan, summary.TypeReview, summary.TypeBlocked, summary.TypeFailed}
	if !slices.Equal(closed, wantTypes) {
		t.Fatalf("NeedTypes() = %v, want %v", closed, wantTypes)
	}
	wantMap := map[string]string{
		summary.NeedQuestion:         summary.TypeQuestion,
		summary.NeedPlanConfirmation: summary.TypePlan,
		summary.NeedPlanReview:       summary.TypePlan,
		summary.NeedMerge:            summary.TypeReview,
		summary.NeedReview:           summary.TypeReview,
		summary.NeedHold:             summary.TypeBlocked,
		summary.NeedFailedCheck:      summary.TypeBlocked,
		summary.NeedApproval:         summary.TypeBlocked,
		summary.NeedRecovery:         summary.TypeFailed,
	}
	for _, kind := range summary.NeedKinds() {
		got := summary.NeedType(kind)
		if !slices.Contains(closed, got) {
			t.Errorf("NeedType(%q) = %q, not in closed set %v", kind, got, closed)
		}
		if want, ok := wantMap[kind]; !ok || got != want {
			t.Errorf("NeedType(%q) = %q, want %q", kind, got, want)
		}
	}
	if len(summary.NeedKinds()) != len(wantMap) {
		t.Fatalf("NeedKinds() has %d entries, want %d", len(summary.NeedKinds()), len(wantMap))
	}

	// Verify plan_review derivation and each interactive card's Act.
	prIn := summary.NeedsIn{
		Room: "p1a2b3c4", URL: "https://rooms.example/r/p1a2b3c4", Repository: "Smana/maku",
		FactsRead: true,
		Facts: &envelope.TaskFacts{
			Phase: "AwaitingHuman", Reason: "plan_review",
			Issue: &envelope.IssueFact{Number: 55, URL: "https://github.com/Smana/maku/issues/55", Author: "alice"},
		},
		GitHub: summary.GitHubStanding{Login: "alice"},
	}
	gotPlan := summary.Items(prIn)
	if len(gotPlan) != 1 || gotPlan[0].Kind != summary.NeedPlanReview || gotPlan[0].Type != summary.TypePlan || gotPlan[0].Act != "plan" {
		t.Fatalf("plan_review = %+v", gotPlan)
	}
	splitIn := prIn
	splitIn.Facts = &envelope.TaskFacts{Phase: "Escalated", Reason: "split_proposed", Issue: prIn.Facts.Issue}
	splitIn.Caller.CanClose = true
	if got := summary.Items(splitIn); len(got) != 1 || got[0].Act != "split" || got[0].Type != summary.TypePlan {
		t.Fatalf("split_proposed = %+v", got)
	}
	qIn := splitIn
	qIn.Facts = &envelope.TaskFacts{Phase: "Escalated", Reason: "needs_author", Issue: prIn.Facts.Issue}
	if got := summary.Items(qIn); len(got) != 1 || got[0].Act != "answer" || got[0].Type != summary.TypeQuestion {
		t.Fatalf("question = %+v", got)
	}
	recIn := splitIn
	recIn.Facts = &envelope.TaskFacts{Phase: "Escalated", Reason: "budget_reached", Issue: prIn.Facts.Issue}
	if got := summary.Items(recIn); len(got) != 1 || got[0].Act != "retry" || got[0].Type != summary.TypeFailed {
		t.Fatalf("recovery = %+v", got)
	}
}

// Phase G5: one entry per task (its most urgent type), ordered question, plan, review, blocked, failed.
func TestOneEntryPerTask(t *testing.T) {
	items := []summary.Need{
		{Room: "r1111111", Kind: summary.NeedRecovery, ID: "budget_reached"},
		{Room: "r1111111", Kind: summary.NeedApproval, ID: "ap1"},
		{Room: "r1111111", Kind: summary.NeedQuestion, ID: "10"},
		{Room: "r2222222", Kind: summary.NeedHold, ID: "waiting_human_driver"},
		{Room: "r2222222", Kind: summary.NeedPlanReview, ID: "20"},
		{Room: "r3333333", Kind: summary.NeedMerge, ID: "30"},
		{Room: "r4444444", Kind: summary.NeedFailedCheck, ID: "ci_wait_expired"},
		{Room: "r5555555", Kind: summary.NeedRecovery, ID: "resume_budget"},
	}
	got := summary.Inbox(items)
	var keys, types []string
	for _, n := range got {
		keys = append(keys, n.Key())
		types = append(types, n.Type)
	}
	wantKeys := []string{
		"r1111111/question/10",
		"r2222222/plan_review/20",
		"r3333333/merge/30",
		"r4444444/failed_check/ci_wait_expired",
		"r5555555/recovery/resume_budget",
	}
	wantTypes := []string{summary.TypeQuestion, summary.TypePlan, summary.TypeReview, summary.TypeBlocked, summary.TypeFailed}
	if !slices.Equal(keys, wantKeys) || !slices.Equal(types, wantTypes) {
		t.Fatalf("Inbox() keys=%v types=%v, want keys=%v types=%v", keys, types, wantKeys, wantTypes)
	}
}

// Phase G5: stale needs are dropped when the task ended, its pull request merged or closed, or its
// question was answered elsewhere.
func TestAStaleNeedIsDropped(t *testing.T) {
	pr := &envelope.PRFact{Number: 77, URL: "https://github.com/Smana/maku/pull/77"}
	issue := &envelope.IssueFact{Number: 12, URL: "https://github.com/Smana/maku/issues/12", Author: "alice"}
	base := summary.NeedsIn{
		Room: "r1111111", URL: "https://rooms.example/r/r1111111", Repository: "Smana/maku",
		FactsRead: true,
		Approvals: []summary.Approval{{ID: "ap1", What: "push"}},
		GitHub:    summary.GitHubStanding{Login: "bob", MayWrite: true, Reviewers: []string{"bob"}},
		Caller:    summary.Caller{WebUI: true, CanDecide: true, CanClose: true, CanRelease: true},
	}

	// 1. Task ended (terminal phases): drops facts needs AND lingering approvals.
	for _, phase := range []string{"Done", "NoOp", "Stopped", "Closed", "Rejected", "Reverted"} {
		in := base
		in.Facts = &envelope.TaskFacts{Phase: phase, Reason: "budget_reached", PR: pr, Issue: issue}
		if got := summary.Items(in); len(got) != 0 {
			t.Errorf("terminal phase %s kept stale needs: %+v", phase, got)
		}
	}

	// 2. Pull request merged or closed: drops review, merge, and PR failed_check needs.
	for _, tc := range []struct {
		name     string
		facts    *envelope.TaskFacts
		prClosed bool
	}{
		{"merged phase", &envelope.TaskFacts{Phase: "Merged", Reason: "ci_wait_expired", PR: pr}, false},
		{"closed PR on AwaitingHuman", &envelope.TaskFacts{Phase: "AwaitingHuman", PR: pr}, true},
		{"closed PR on failed_check", &envelope.TaskFacts{Phase: "AwaitingCI", Reason: "ci_wait_expired", PR: pr}, true},
	} {
		in := base
		in.Approvals = nil
		in.Facts = tc.facts
		in.PRClosed = tc.prClosed
		if got := summary.Items(in); len(got) != 0 {
			t.Errorf("%s kept stale PR need: %+v", tc.name, got)
		}
	}

	// 3. Question answered elsewhere: drops question need.
	qAnswered := base
	qAnswered.Approvals = nil
	qAnswered.Facts = &envelope.TaskFacts{Phase: "Escalated", Reason: "needs_author", Issue: issue}
	qAnswered.QuestionAnswered = true
	if got := summary.Items(qAnswered); len(got) != 0 {
		t.Errorf("answered question kept stale need: %+v", got)
	}
	qMoved := base
	qMoved.Approvals = nil
	qMoved.Facts = &envelope.TaskFacts{Phase: "Implementing", Reason: "needs_author", Issue: issue}
	if got := summary.Items(qMoved); len(got) != 0 {
		t.Errorf("restarted task kept stale question need: %+v", got)
	}
}

// A sealed room contributes nothing: its last facts outlive the PR it names (Review Focus 2).
func TestASealedRoomHasNoNeeds(t *testing.T) {
	got := summary.Items(summary.NeedsIn{Room: "26zfnuxm", Sealed: true,
		Facts:  &envelope.TaskFacts{Phase: "AwaitingHuman", PR: &envelope.PRFact{Number: 9, URL: "https://github.com/Smana/ai-native-ref/pull/9"}},
		GitHub: summary.GitHubStanding{Login: "ann", MayWrite: true}})
	if len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

// The facts keep the pull request after it merged, so a review is owed only while a human still waits:
// a requested reviewer of an ended task gets nothing (Review Focus 2).
func TestAReviewIsOwedOnlyWhileAHumanWaits(t *testing.T) {
	pr := &envelope.PRFact{Number: 2245, URL: "https://github.com/Smana/ai-native-ref/pull/2245"}
	in := func(phase string) summary.NeedsIn {
		return summary.NeedsIn{Room: "b3c4d5e6", URL: "https://rooms.example/r/b3c4d5e6", Repository: "Smana/ai-native-ref",
			FactsRead: true, Facts: &envelope.TaskFacts{Phase: phase, PR: pr},
			GitHub: summary.GitHubStanding{Login: "bob", MayWrite: true, Reviewers: []string{"bob"}}}
	}
	for _, phase := range []string{"Done", "Reverted", "Merged", "Verifying", "AwaitingCI", "Reviewing", "Implementing"} {
		if got := summary.Items(in(phase)); len(got) != 0 {
			t.Errorf("%s: a review of work that is not waiting for one: %+v", phase, got)
		}
	}
	got := summary.Items(in("AwaitingHuman"))
	if len(got) != 1 || got[0].Kind != summary.NeedReview || got[0].ID != "2245" {
		t.Fatalf("the reviewer still waiting: %+v", got)
	}
}

func TestAPlanToReviewIsANeed(t *testing.T) {
	if got := summary.StatusWord("AwaitingHuman", "plan_review"); got != "waiting on you" {
		t.Fatalf("StatusWord(AwaitingHuman, plan_review) = %q, want %q", got, "waiting on you")
	}
	facts := &envelope.TaskFacts{
		Phase:  "AwaitingHuman",
		Reason: "plan_review",
		Issue: &envelope.IssueFact{
			Number: 77,
			URL:    "https://github.com/Smana/ai-native-ref/issues/77",
			Author: "alice",
		},
	}
	// Visible to the issue author and to a maintainer, not to an unrelated read-only viewer.
	for _, tc := range []struct {
		name     string
		standing summary.GitHubStanding
		caller   summary.Caller
		want     bool
	}{
		{name: "issue author", standing: summary.GitHubStanding{Login: "alice"}, want: true},
		{name: "maintainer", standing: summary.GitHubStanding{Login: "Smana", MayWrite: true}, want: true},
		{name: "unrelated viewer", standing: summary.GitHubStanding{Login: "bob"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := summary.Items(summary.NeedsIn{
				Room:       "p1anr3vw",
				URL:        "https://rooms.example/r/p1anr3vw",
				Repository: "Smana/ai-native-ref",
				FactsRead:  true,
				Facts:      facts,
				GitHub:     tc.standing,
				Caller:     tc.caller,
			})
			if !tc.want {
				if len(got) != 0 {
					t.Fatalf("expected no need, got %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Kind != summary.NeedPlanReview || got[0].ID != "77" || got[0].Issue != 77 {
				t.Fatalf("expected NeedPlanReview for #77, got %+v", got)
			}
			if got[0].Action.URL != "https://github.com/Smana/ai-native-ref/issues/77" {
				t.Fatalf("unexpected action URL: %+v", got[0].Action)
			}
		})
	}
}

// A step-up offered above the cost confirmed for the task would be refused deeper_over_cap: the
// recovery need offers no Run again deeper for it; one within the confirmed cost keeps it (Maku #720).
func TestARecoveryOffersNoDeeperRunAboveTheConfirmedCost(t *testing.T) {
	in := summary.NeedsIn{Room: "p1a2b3c4", URL: "https://rooms.example/r/p1a2b3c4", Repository: "Smana/maku", FactsRead: true,
		Caller: summary.Caller{CanClose: true}}
	deeper := func(confirmed int64) *summary.DeeperOffer {
		in.Facts = &envelope.TaskFacts{Phase: "Escalated", Reason: "budget_reached",
			Issue: &envelope.IssueFact{Number: 55, URL: "https://github.com/Smana/maku/issues/55"},
			Profile: &envelope.ProfileFact{Name: "standard", Source: "repository", Cohort: "treatment", ConfirmedMicroUSD: confirmed,
				StepUp: &envelope.StepUpFact{From: "standard", To: "careful", Trigger: "budget-run", Mode: "offered", CeilingMicroUSD: 13_600_000}}}
		got := summary.Items(in)
		if len(got) != 1 || got[0].Kind != summary.NeedRecovery {
			t.Fatalf("items %+v", got)
		}
		return got[0].Deeper
	}
	if d := deeper(5_100_000); d != nil {
		t.Fatalf("a deeper run above the confirmed cost is offered: %+v", d)
	}
	if d := deeper(20_000_000); d == nil || d.Workflow != "careful" {
		t.Fatalf("a deeper run within the confirmed cost: %+v", d)
	}
	if d := deeper(0); d == nil {
		t.Fatal("a task with no confirmed cost keeps its deeper run")
	}
}
