// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

const (
	repoA = "Smana/public-contracts"
	repoB = "Smana/public-consumer"
	ns    = "factory"
)

// fakeChecks is RepositoryChecks over fixed facts: each repository's fence and each login's
// GitHub permission there; err fails one repository's check.
type fakeChecks struct {
	fence map[string]string
	perms map[string]map[string]string // repository → lower-case login → permission
	err   map[string]error
	calls []string
}

func newFakeChecks() *fakeChecks {
	return &fakeChecks{
		fence: map[string]string{repoA: strings.Repeat("a", 40), repoB: strings.Repeat("b", 40)},
		perms: map[string]map[string]string{repoA: {"smana": "admin"}, repoB: {"other-maintainer": "write", "smana": "write"}},
		err:   map[string]error{},
	}
}

func (f *fakeChecks) Check(_ context.Context, _ *config.Config, actor Actor, c v1alpha1.CoordinationChild, auth *v1alpha1.ChildAuthorization) (RepositoryPolicy, error) {
	f.calls = append(f.calls, c.Repository+"|"+actor.LinkedGitHubLogin)
	if err := f.err[c.Repository]; err != nil {
		return RepositoryPolicy{}, err
	}
	fence, ok := f.fence[c.Repository]
	if !ok {
		return RepositoryPolicy{}, ErrRepositoryNotAllowed
	}
	if auth != nil && auth.FenceSHA != fence {
		return RepositoryPolicy{}, ErrPolicyChanged
	}
	if c.Class != "scripts" || c.Tier != "standard" {
		return RepositoryPolicy{}, ErrClassNotAllowed
	}
	pol := RepositoryPolicy{Repository: c.Repository, Class: c.Class, Tier: c.Tier, FenceSHA: fence, Public: true,
		AppRepository: c.Repository, Roles: []string{"implementer", "reviewer"}, RunMicroUSD: 510000, TaskMicroUSD: 5100000}
	if actor.LinkedGitHubLogin == "" {
		return pol, nil
	}
	pol.Permission = f.perms[c.Repository][strings.ToLower(actor.LinkedGitHubLogin)]
	switch pol.Permission {
	case "write", "maintain", "admin":
		return pol, nil
	}
	return RepositoryPolicy{}, ErrRepositoryPermission
}

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

func testConfig(mode string) *config.Config {
	return &config.Config{
		Repositories: []config.Repository{{Name: repoA, Maintainers: []string{"Smana"}}, {Name: repoB, Maintainers: []string{"other-maintainer", "readonly"}}},
		Coordination: config.Coordination{Mode: mode},
	}
}

func testService(t *testing.T, mode string, objs ...client.Object) (*Service, *fakeChecks) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(objs...).WithStatusSubresource(&v1alpha1.Task{}).Build()
	ch := newFakeChecks()
	at := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	return &Service{Client: c, Namespace: ns, Cfg: testConfig(mode), Checks: ch, Now: func() time.Time { return at }}, ch
}

// draftPlan is the example at version 1.
func draftPlan(t *testing.T) v1alpha1.CoordinationPlan {
	t.Helper()
	p := validPlan(t)
	p.Version = 1
	return p
}

var (
	smana = Actor{Principal: "zitadel:smana", LinkedGitHubLogin: "Smana"}
	other = Actor{Principal: "zitadel:other", LinkedGitHubLogin: "other-maintainer"}
)

func parentOf(t *testing.T, s *Service, name string) v1alpha1.Task {
	t.Helper()
	var task v1alpha1.Task
	if err := s.Client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &task); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	return task
}

func mustDraft(t *testing.T, s *Service, p v1alpha1.CoordinationPlan) {
	t.Helper()
	if _, err := s.Draft(context.Background(), smana, p, PlanHash(p), PlanningReconciliation{AttemptID: "a1"}); err != nil {
		t.Fatalf("draft: %v", err)
	}
}

func parentConfirm(p v1alpha1.CoordinationPlan) CoordinationConfirmation {
	h := PlanHash(p)
	return CoordinationConfirmation{Plan: p, PlanHash: h, Parent: &ParentConfirmation{PlanHash: h}}
}

func childConfirm(p v1alpha1.CoordinationPlan, id string) CoordinationConfirmation {
	return CoordinationConfirmation{Plan: p, PlanHash: PlanHash(p), Child: &ChildAuthorization{ChildID: id, ChildFingerprint: ChildFingerprint(p, id)}}
}

// A draft creates a Coordinating parent with the full plan and nothing to run; the same version
// again is a replay.
func TestCoordinationDraftCreatesAParent(t *testing.T) {
	s, _ := testService(t, config.CoordinationPlan)
	p := draftPlan(t)
	mustDraft(t, s, p)
	mustDraft(t, s, p)
	task := parentOf(t, s, p.ID)
	cs := task.Status.Coordination
	switch {
	case task.Spec.Kind != v1alpha1.TaskKindCoordination || task.Spec.Source.Kind != v1alpha1.SourceKindCoordination:
		t.Fatalf("kinds %q %q", task.Spec.Kind, task.Spec.Source.Kind)
	case task.Spec.Repository != repoA || task.Spec.DataClass != "public" || task.Spec.Source.RequestedBy != "github:smana":
		t.Fatalf("spec %+v", task.Spec)
	case task.Spec.Source.ContentSHA256 != PlanHash(p) || *task.Spec.Coordination != (v1alpha1.CoordinationSpec{MaxChildren: 2, PublicOnly: true}):
		t.Fatalf("source %+v coordination %+v", task.Spec.Source, task.Spec.Coordination)
	case task.Status.Phase != v1alpha1.PhaseCoordinating || task.Status.Reason != ReasonAwaitingConfirmation:
		t.Fatalf("phase %s/%s", task.Status.Phase, task.Status.Reason)
	case task.Spec.Issue != 0 || len(task.Status.Runs) > 0 || task.Status.PullRequest != nil:
		t.Fatal("a draft has an issue, a run or a pull request")
	case cs.PlanHash != PlanHash(p) || cs.PlanVersion != 1 || len(cs.History) != 1:
		t.Fatalf("status %+v", cs)
	case cs.Budget.AllocatedMicroUSD != 510000+5100000*2+4*510000 || cs.Budget.PlanningCost != v1alpha1.PlanningCostNotSupplied:
		t.Fatalf("budget %+v", cs.Budget)
	}
}

// The draft refusals: an id naming another kind of Task, a primary change, a version that is not
// the next one, a hash that is not the plan's.
func TestCoordinationDraftRefusals(t *testing.T) {
	p := draftPlan(t)
	ordinary := &v1alpha1.Task{ObjectMeta: objectMeta(p.ID), Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: "issue"}, Repository: repoA, DataClass: "public"}}
	for _, tc := range []struct {
		name  string
		objs  []client.Object
		setup func(*Service)
		next  func(v1alpha1.CoordinationPlan) v1alpha1.CoordinationPlan
		hash  string
		want  error
	}{
		{name: "an existing ordinary task", objs: []client.Object{ordinary}, next: same, want: ErrParentConflict},
		{name: "the primary changes", setup: func(s *Service) { mustDraft(t, s, p) }, next: func(q v1alpha1.CoordinationPlan) v1alpha1.CoordinationPlan {
			q.Version = 2
			q.Primary.Repository = repoB
			return q
		}, want: ErrPrimaryRequiresNewParent},
		{name: "a skipped version", setup: func(s *Service) { mustDraft(t, s, p) }, next: func(q v1alpha1.CoordinationPlan) v1alpha1.CoordinationPlan {
			q.Version = 3
			return q
		}, want: ErrPlanVersion},
		{name: "another plan at the same version", setup: func(s *Service) { mustDraft(t, s, p) }, next: func(q v1alpha1.CoordinationPlan) v1alpha1.CoordinationPlan {
			q.Title = "Something else"
			return q
		}, want: ErrPlanVersion},
		{name: "a hash that is not the plan's", next: same, hash: strings.Repeat("0", 64), want: ErrStalePlan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testService(t, config.CoordinationPlan, tc.objs...)
			if tc.setup != nil {
				tc.setup(s)
			}
			q := tc.next(*p.DeepCopy())
			h := tc.hash
			if h == "" {
				h = PlanHash(q)
			}
			_, err := s.Draft(context.Background(), smana, q, h, PlanningReconciliation{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func objectMeta(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: ns, Name: name} }

func same(p v1alpha1.CoordinationPlan) v1alpha1.CoordinationPlan { return p }

// The next version goes through the materiality table: a consumer-only scope edit keeps the
// producer's authorization and drops the consumer's and the parent's.
func TestCoordinationDraftNextVersionKeepsOnlyAllowedAuthorizations(t *testing.T) {
	s, _ := testService(t, config.CoordinationPlan)
	p := draftPlan(t)
	mustDraft(t, s, p)
	ctx := context.Background()
	for _, c := range []struct {
		a  Actor
		in CoordinationConfirmation
	}{{smana, parentConfirm(p)}, {smana, childConfirm(p, "contract")}, {other, childConfirm(p, "consumer")}} {
		if _, err := s.Confirm(ctx, c.a, c.in); err != nil {
			t.Fatalf("confirm: %v", err)
		}
	}
	if r := parentOf(t, s, p.ID).Status.Reason; r != ReasonConfirmed {
		t.Fatalf("reason %q, want confirmed", r)
	}
	q := *p.DeepCopy()
	q.Version = 2
	child(t, &q, "consumer").Scope.Task = "Validate the consumer against the landed v1 schema, strictly."
	if _, err := s.Draft(ctx, smana, q, PlanHash(q), PlanningReconciliation{AttemptID: "a2"}); err != nil {
		t.Fatal(err)
	}
	task := parentOf(t, s, p.ID)
	cs := task.Status.Coordination
	if len(cs.Authorizations) != 1 || cs.Authorizations[0].ChildID != "contract" {
		t.Fatalf("authorizations %+v, want contract's alone", cs.Authorizations)
	}
	if parentConfirmation(cs) != nil || task.Status.Reason != ReasonAwaitingConfirmation || len(cs.History) != 2 {
		t.Fatalf("parent confirmation kept, or reason %q, history %d", task.Status.Reason, len(cs.History))
	}
}

// Parent and child authority are separate: the primary's maintainer with write confirms the
// parent; each child's own maintainer with write authorizes it; nothing else does.
func TestCoordinationConfirmAuthority(t *testing.T) {
	p := draftPlan(t)
	for _, tc := range []struct {
		name  string
		actor Actor
		in    func(v1alpha1.CoordinationPlan) CoordinationConfirmation
		setup func(*Service, *fakeChecks)
		want  error
	}{
		{name: "the primary maintainer confirms the parent", actor: smana, in: parentConfirm},
		{name: "the consumer maintainer authorizes the consumer", actor: other, in: func(p v1alpha1.CoordinationPlan) CoordinationConfirmation { return childConfirm(p, "consumer") }},
		{name: "an unlinked user", actor: Actor{Principal: "zitadel:x"}, in: parentConfirm, want: ErrRepositoryPermission},
		{name: "a non-maintainer of the primary", actor: other, in: parentConfirm, want: ErrRepositoryPermission},
		{name: "the primary maintainer authorizing B", actor: smana, in: func(p v1alpha1.CoordinationPlan) CoordinationConfirmation { return childConfirm(p, "consumer") }, want: ErrRepositoryPermission},
		{name: "a read-only B maintainer", actor: Actor{Principal: "zitadel:r", LinkedGitHubLogin: "readonly"}, in: func(p v1alpha1.CoordinationPlan) CoordinationConfirmation {
			return childConfirm(p, "consumer")
		}, want: ErrRepositoryPermission},
		{name: "a B maintainer the config does not list", actor: other, in: func(p v1alpha1.CoordinationPlan) CoordinationConfirmation { return childConfirm(p, "consumer") },
			setup: func(s *Service, _ *fakeChecks) { s.Cfg.Current().Repositories[1].Maintainers = nil }, want: ErrRepositoryPermission},
		{name: "a stale hash", actor: smana, in: func(p v1alpha1.CoordinationPlan) CoordinationConfirmation {
			c := parentConfirm(p)
			c.Parent.PlanHash = strings.Repeat("0", 64)
			return c
		}, want: ErrStalePlan},
		{name: "a stale fingerprint", actor: other, in: func(p v1alpha1.CoordinationPlan) CoordinationConfirmation {
			c := childConfirm(p, "consumer")
			c.Child.ChildFingerprint = strings.Repeat("0", 64)
			return c
		}, want: ErrStalePlan},
		{name: "both at once", actor: smana, in: func(p v1alpha1.CoordinationPlan) CoordinationConfirmation {
			c := parentConfirm(p)
			c.Child = childConfirm(p, "contract").Child
			return c
		}, want: ErrInvalidConfirmation},
		{name: "a permission read that fails", actor: smana, in: parentConfirm,
			setup: func(_ *Service, f *fakeChecks) { f.err[repoA] = ErrAccessUnverified }, want: ErrAccessUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := testService(t, config.CoordinationPlan)
			q := *p.DeepCopy()
			if tc.name == "a read-only B maintainer" {
				child(t, &q, "consumer").Maintainer = "github:readonly"
				f.perms[repoB]["readonly"] = "read"
			}
			mustDraft(t, s, q)
			if tc.setup != nil {
				tc.setup(s, f)
			}
			_, err := s.Confirm(context.Background(), tc.actor, tc.in(q))
			if !errors.Is(err, tc.want) && (tc.want != nil || err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			cs := parentOf(t, s, q.ID).Status.Coordination
			if recorded := len(cs.Confirmations) + len(cs.Authorizations); (tc.want == nil) != (recorded == 1) {
				t.Fatalf("%d records after err %v", recorded, err)
			}
		})
	}
}

// Parent confirmation authorizes no child, and one person holding both roles makes two distinct
// records through two calls.
func TestCoordinationConfirmParentOnlyAuthorizesNoChild(t *testing.T) {
	s, f := testService(t, config.CoordinationPlan)
	p := draftPlan(t)
	child(t, &p, "consumer").Maintainer = "github:Smana"
	s.Cfg.Current().Repositories[1].Maintainers = []string{"Smana"}
	f.perms[repoB]["smana"] = "write"
	mustDraft(t, s, p)
	ctx := context.Background()
	if _, err := s.Confirm(ctx, smana, parentConfirm(p)); err != nil {
		t.Fatal(err)
	}
	task := parentOf(t, s, p.ID)
	if len(task.Status.Coordination.Authorizations) != 0 || task.Status.Reason != ReasonAwaitingConfirmation {
		t.Fatalf("parent confirmation authorized a child: %+v", task.Status.Coordination.Authorizations)
	}
	for _, id := range []string{"contract", "consumer"} {
		out, err := s.Confirm(ctx, smana, childConfirm(p, id))
		if err != nil || out.Child == nil || out.Child.ChildID != id || out.Child.FenceSHA == "" || out.Child.Permission == "" {
			t.Fatalf("authorize %s: %+v %v", id, out.Child, err)
		}
	}
	cs := parentOf(t, s, p.ID).Status.Coordination
	if len(cs.Confirmations) != 1 || len(cs.Authorizations) != 2 || cs.Authorizations[1].Permission != "write" || cs.Authorizations[0].Permission != "admin" {
		t.Fatalf("records %+v / %+v", cs.Confirmations, cs.Authorizations)
	}
}

// Under execute, every confirmation present still files nothing without both capabilities: the
// production wiring has no prerequisite port.
func TestCoordinationConfirmExecuteNeedsPrerequisites(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prereq ExecutionPrerequisites
		want   error
		reason string
	}{
		{name: "no port", want: ErrPrerequisites, reason: ReasonPrerequisites},
		{name: "no hardened outputs", prereq: fakePrereq{classes: true}, want: ErrPrerequisites, reason: ReasonPrerequisites},
		{name: "both capabilities", prereq: fakePrereq{classes: true, hardened: true}, reason: ReasonFilingEligible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testService(t, config.CoordinationExecute)
			s.Prerequisites = tc.prereq
			p := draftPlan(t)
			mustDraft(t, s, p)
			ctx := context.Background()
			if _, err := s.Confirm(ctx, smana, parentConfirm(p)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Confirm(ctx, smana, childConfirm(p, "contract")); err != nil {
				t.Fatal(err)
			}
			_, err := s.Confirm(ctx, other, childConfirm(p, "consumer"))
			if !errors.Is(err, tc.want) && (tc.want != nil || err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			task := parentOf(t, s, p.ID)
			if task.Status.Reason != tc.reason || task.Spec.Issue != 0 {
				t.Fatalf("reason %q issue %d", task.Status.Reason, task.Spec.Issue)
			}
			var l v1alpha1.TaskList
			if err := s.Client.List(ctx, &l); err != nil || len(l.Items) != 1 {
				t.Fatalf("%d tasks (%v), want the parent alone", len(l.Items), err)
			}
		})
	}
}

type fakePrereq struct{ classes, hardened bool }

func (f fakePrereq) Ready(context.Context, string) (bool, bool, error) {
	return f.classes, f.hardened, nil
}

// Planning cost: reserved plus settled past planningMicroUSD is refused, an identifier without
// its amount is unverified, an attempt with no identifiers is recorded as not supplied, and a
// replayed attempt counts once.
func TestPlanningBudget(t *testing.T) {
	p := draftPlan(t) // planningMicroUSD 510000
	for _, tc := range []struct {
		name     string
		attempts []PlanningReconciliation
		want     error
		cost     string
		spent    int64
	}{
		{name: "not supplied", attempts: []PlanningReconciliation{{AttemptID: "a1"}}, cost: v1alpha1.PlanningCostNotSupplied},
		{name: "supplied within", attempts: []PlanningReconciliation{{AttemptID: "a1", ReservationID: "r1", ReservedMicroUSD: 200000, SettlementID: "s1", SettledMicroUSD: 300000}},
			cost: v1alpha1.PlanningCostSupplied, spent: 500000},
		{name: "replayed attempt counts once", attempts: []PlanningReconciliation{
			{AttemptID: "a1", SettlementID: "s1", SettledMicroUSD: 300000}, {AttemptID: "a1", SettlementID: "s1", SettledMicroUSD: 300000}},
			cost: v1alpha1.PlanningCostSupplied, spent: 300000},
		{name: "exhausted", attempts: []PlanningReconciliation{{AttemptID: "a1", ReservationID: "r1", ReservedMicroUSD: 300000, SettlementID: "s1", SettledMicroUSD: 300000}},
			want: ErrPlanningBudgetExhausted},
		{name: "exhausted across attempts", attempts: []PlanningReconciliation{
			{AttemptID: "a1", SettlementID: "s1", SettledMicroUSD: 300000}, {AttemptID: "a2", SettlementID: "s2", SettledMicroUSD: 300000}},
			want: ErrPlanningBudgetExhausted},
		{name: "an identifier without its amount", attempts: []PlanningReconciliation{{AttemptID: "a1", ReservationID: "r1"}}, want: ErrPlanningCostUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testService(t, config.CoordinationPlan)
			var err error
			q := *p.DeepCopy()
			for i, a := range tc.attempts {
				if i > 0 && a.AttemptID != tc.attempts[i-1].AttemptID {
					q.Version++
					q.Title += "."
				}
				_, err = s.Draft(context.Background(), smana, q, PlanHash(q), a)
			}
			if !errors.Is(err, tc.want) && (tc.want != nil || err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.want != nil {
				return
			}
			b := parentOf(t, s, p.ID).Status.Coordination.Budget
			if b.PlanningCost != tc.cost || b.PlanningReservedMicroUSD+b.PlanningSettledMicroUSD != tc.spent {
				t.Fatalf("budget %+v", b)
			}
		})
	}
}

// Off serves nothing.
func TestCoordinationOffRefusesEverything(t *testing.T) {
	s, _ := testService(t, config.CoordinationOff)
	p := draftPlan(t)
	if _, err := s.Draft(context.Background(), smana, p, PlanHash(p), PlanningReconciliation{}); !errors.Is(err, ErrCoordinationOff) {
		t.Fatalf("draft: %v", err)
	}
	if _, err := s.Confirm(context.Background(), smana, parentConfirm(p)); !errors.Is(err, ErrCoordinationOff) {
		t.Fatalf("confirm: %v", err)
	}
}
