// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// The parent's bounded reasons while it is Coordinating (MR2).
const (
	// ReasonAwaitingConfirmation is a draft missing the parent's confirmation or a child's
	// authorization of the current plan.
	ReasonAwaitingConfirmation = "awaiting_confirmation"
	// ReasonConfirmed is every confirmation of the current plan present under plan mode: nothing
	// files.
	ReasonConfirmed = "confirmed"
	// ReasonFilingEligible is every confirmation present under execute, with the prerequisites and
	// every repository check passing at that moment.
	ReasonFilingEligible = "filing_eligible"
)

// The service's own refusals, beside the plan's and the repository checks'.
var (
	// ErrCoordinationOff is any coordination call while the switch is off.
	ErrCoordinationOff = errors.New("coordination_off")
	// ErrParentConflict is a draft id naming a Task that is not a coordination parent.
	ErrParentConflict = errors.New("coordination_parent_conflict")
	// ErrParentNotFound is a confirmation of a plan no draft holds.
	ErrParentNotFound = errors.New("coordination_not_found")
	// ErrParentTerminal is a draft or a confirmation of a parent that has ended.
	ErrParentTerminal = errors.New("coordination_parent_terminal")
	// ErrPlanningBudgetExhausted is planning reserved plus settled above planningMicroUSD (MR8).
	ErrPlanningBudgetExhausted = errors.New("planning_budget_exhausted")
)

// serviceReasons are the sentinels Reason looks for after the repository checks'.
var serviceReasons = []error{ErrCoordinationOff, ErrParentConflict, ErrParentNotFound, ErrParentTerminal,
	ErrPlanningBudgetExhausted, ErrPrerequisites, ErrNotExecuting, ErrArtifactUnverified, ErrRecoveryNotProposed,
	ErrPlanRevisionRequired, ErrIntegrationStale, ErrIntegrationManifest}

// Reason is err's bounded reason code: a repository check's first, since a resolver failure
// carries one under policy_unresolved, then the service's, then the plan's; "" for none.
func Reason(err error) string {
	for _, s := range slices.Concat(checkReasons, serviceReasons) {
		if errors.Is(err, s) {
			return s.Error()
		}
	}
	return ReasonOf(err)
}

// maxAttemptID bounds a planning attempt id as the parent status stores it.
const maxAttemptID = 128

// Service drafts and confirms coordination plans on their parent Task (MR4, MR5): full snapshots
// bound to their hash, the parent's confirmation and each child's authorization as separate
// records by the actor the token resolves to. Execute eligibility re-reads every authority and
// the capability gate; with no Prerequisites port execute always refuses.
type Service struct {
	Client        client.Client
	Namespace     string
	Cfg           config.Source
	Checks        RepositoryChecks
	Prerequisites ExecutionPrerequisites
	Now           func() time.Time
	// Forges is each repository's own factory App connection for filing, nil when it has none.
	Forges func(repository string) IssueForge
	// Metrics records the service's coordination series; nil records nothing.
	Metrics Metrics
}

// Metrics is what the service counts (v4 observability), each after the write that records it:
// *fmetrics.Set implements it. Labels are configured repositories and bounded codes only.
type Metrics interface {
	CoordinationFamily(ctx context.Context, repository, outcome string)
	CoordinationPlanVersion(ctx context.Context, repository, outcome string)
	CoordinationChild(ctx context.Context, repository, class, outcome string)
	CoordinationWait(ctx context.Context, repository, reason string, d time.Duration)
}

// observe runs f on the service's Metrics, when it has one.
func (s *Service) observe(f func(Metrics)) {
	if s.Metrics != nil {
		f(s.Metrics)
	}
}

// authorizationWait is how long cs's current version waited for its confirmations: from its
// acceptance to now, the write that completed them.
func authorizationWait(cs *v1alpha1.CoordinationStatus, now time.Time) time.Duration {
	for i := len(cs.History) - 1; i >= 0; i-- {
		if h := cs.History[i]; h.PlanHash == cs.PlanHash {
			return now.Sub(h.AcceptedAt.Time)
		}
	}
	return 0
}

// config is the config for one decision: one Current per public entry point, handed down.
func (s *Service) config() *config.Config {
	if s.Cfg == nil {
		return nil
	}
	return s.Cfg.Current()
}

// modeOf is the coordination mode under cfg (a startup field), off for a nil config.
func modeOf(cfg *config.Config) string {
	if cfg == nil || cfg.Coordination.Mode == "" {
		return config.CoordinationOff
	}
	return cfg.Coordination.Mode
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// check is RepositoryChecks with a nil port failing closed.
func (s *Service) check(ctx context.Context, actor Actor, c v1alpha1.CoordinationChild, auth *v1alpha1.ChildAuthorization) (RepositoryPolicy, error) {
	if s.Checks == nil {
		return RepositoryPolicy{}, fmt.Errorf("%w: no repository checks", ErrAccessUnverified)
	}
	return s.Checks.Check(ctx, actor, c, auth)
}

// resolve is the factory's ResolvePolicy: each repository's own fresh check, no actor.
func (s *Service) resolve() ResolvePolicy {
	return func(ctx context.Context, repository, class, tier string) (RepositoryPolicy, error) {
		return s.check(ctx, Actor{}, v1alpha1.CoordinationChild{Repository: repository, Class: class, Tier: tier}, nil)
	}
}

// Draft stores p as its parent Task's current plan: it creates the parent on the first version,
// replays an identical version, and accepts exactly the next version through the materiality
// table, keeping only the authorizations it allows. The parent is Coordinating with no issue, run
// or pull request. Drafting needs a linked actor; the caller has checked their write on the
// primary.
func (s *Service) Draft(ctx context.Context, actor Actor, p v1alpha1.CoordinationPlan, hash string, planning PlanningReconciliation) (CoordinationConfirmation, error) {
	cfg := s.config()
	if modeOf(cfg) == config.CoordinationOff {
		return CoordinationConfirmation{}, ErrCoordinationOff
	}
	if actor.LinkedGitHubLogin == "" {
		return CoordinationConfirmation{}, fmt.Errorf("%w: no linked GitHub login", ErrRepositoryPermission)
	}
	if hash != PlanHash(p) {
		return CoordinationConfirmation{}, refuse(ErrStalePlan, "planHash", "not the plan's hash")
	}
	if err := planningShape(planning); err != nil {
		return CoordinationConfirmation{}, err
	}
	// A draft may carry an allocation increase as a proposal: execute admits it only once the
	// parent and that child's maintainer confirmed the version carrying it (MR8).
	increase := map[string]bool{}
	for _, c := range p.Children {
		increase[c.ID] = true
	}
	resolved, err := ValidateWithIncrease(ctx, p, ModePlan, s.resolve(), increase)
	if err != nil {
		return CoordinationConfirmation{}, err
	}
	if PlanHash(resolved) != hash {
		return CoordinationConfirmation{}, refuse(ErrPolicyMismatch, "", "the plan is not as the factory resolves it")
	}
	out := CoordinationConfirmation{Plan: resolved, PlanHash: hash, Planning: planning}
	key := types.NamespacedName{Namespace: s.Namespace, Name: p.ID}
	var m Materiality
	var t v1alpha1.Task
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		t = v1alpha1.Task{}
		switch err := s.Client.Get(ctx, key, &t); {
		case apierrors.IsNotFound(err):
			return s.create(ctx, actor, resolved, hash, planning)
		case err != nil:
			return err
		}
		var err error
		m, err = s.revise(ctx, cfg, &t, resolved, hash, planning)
		return err
	})
	if err != nil {
		return CoordinationConfirmation{}, err
	}
	if err := s.repostVersion(ctx, cfg, &t, m); err != nil {
		return CoordinationConfirmation{}, err
	}
	return out, nil
}

// repostVersion reposts an accepted version on a filed parent's issue with its changed sections
// and who reconfirms (MR12), once per version and hash: a replayed draft reposts what a lost post
// missed. Before filing there is no issue: File reposts the version it files.
func (s *Service) repostVersion(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, m Materiality) error {
	cs := t.Status.Coordination
	if t.Spec.Kind != v1alpha1.TaskKindCoordination || t.Spec.Issue == 0 || t.Status.Reason != ReasonFiled || cs == nil || cs.Plan == nil {
		return nil
	}
	f := s.issueForge(cs.Plan.Primary.Repository)
	if f == nil {
		return fmt.Errorf("%w: %s has no App connection", ErrAppUnavailable, cs.Plan.Primary.Repository)
	}
	links := map[string]string{}
	for _, st := range cs.Children {
		if st.Issue > 0 {
			links[st.ID] = issueURL(st.Repository, st.Issue)
		}
	}
	return s.repost(ctx, cfg, f, t.Name, t.Spec.Issue, *cs.Plan, cs.PlanHash, links, m)
}

// create makes a new parent and records its first plan version.
func (s *Service) create(ctx context.Context, actor Actor, p v1alpha1.CoordinationPlan, hash string, planning PlanningReconciliation) error {
	b, err := plannedBudget(nil, p, planning)
	if err != nil {
		return err
	}
	t := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: p.ID, Namespace: s.Namespace},
		Spec: v1alpha1.TaskSpec{Kind: v1alpha1.TaskKindCoordination,
			Source: v1alpha1.Source{Kind: v1alpha1.SourceKindCoordination, Ref: "coordination:" + p.ID, Key: "coordination:" + p.ID,
				RequestedBy: v1alpha1.Person(actor.LinkedGitHubLogin), Trust: "untrusted", ContentSHA256: hash},
			Repository: p.Primary.Repository, Text: p.Title, DataClass: "public",
			Coordination: &v1alpha1.CoordinationSpec{MaxChildren: 2, PublicOnly: true}}}
	if err := s.Client.Create(ctx, t); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return apierrors.NewConflict(v1alpha1.GroupVersion.WithResource("tasks").GroupResource(), p.ID, err)
		}
		return err
	}
	t.Status.Phase, t.Status.Reason = v1alpha1.PhaseCoordinating, ReasonAwaitingConfirmation
	now := metav1.NewTime(s.now())
	t.Status.PhaseSince = &now
	t.Status.Coordination = &v1alpha1.CoordinationStatus{PlanVersion: p.Version, Plan: p.DeepCopy(), PlanHash: hash,
		History: []v1alpha1.CoordinationPlanVersion{{Version: p.Version, PlanHash: hash, AcceptedAt: now}}, Budget: b}
	return s.Client.Status().Update(ctx, t)
}

// revise accepts next on an existing parent: a replay, or exactly the next version.
func (s *Service) revise(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, next v1alpha1.CoordinationPlan, hash string, planning PlanningReconciliation) (Materiality, error) {
	if t.Spec.Kind != v1alpha1.TaskKindCoordination {
		return Materiality{}, ErrParentConflict
	}
	if v1alpha1.TerminalPhase(t.Status.Phase) {
		return Materiality{}, ErrParentTerminal
	}
	cs := t.Status.Coordination
	if cs == nil || cs.Plan == nil { // a create whose status write was lost: record the first version
		return Materiality{}, s.record(ctx, cfg, t, next, hash, planning, Materiality{}, nil)
	}
	old := *cs.Plan
	switch {
	case old.Primary.Repository != next.Primary.Repository:
		return Materiality{}, refuse(ErrPrimaryRequiresNewParent, "primary.repository", "changing the primary starts a new parent (MR3)")
	case next.Version == old.Version && hash == cs.PlanHash:
		return Materiality{}, nil // the same version again: a replay changes nothing
	}
	m, err := ComparePlans(old, next)
	if err != nil {
		return Materiality{}, err
	}
	return m, s.record(ctx, cfg, t, next, hash, planning, m, cs)
}

// record writes next as the current plan on t, carrying what m preserves of old.
func (s *Service) record(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, next v1alpha1.CoordinationPlan, hash string, planning PlanningReconciliation, m Materiality, old *v1alpha1.CoordinationStatus) error {
	if old == nil {
		old = &v1alpha1.CoordinationStatus{}
	}
	if err := s.checkReductions(ctx, old, next); err != nil {
		return err
	}
	b, err := plannedBudget(old.Budget, next, planning)
	if err != nil {
		return err
	}
	if t.Spec.Source.ContentSHA256 != hash {
		t.Spec.Source.ContentSHA256 = hash
		if err := s.Client.Update(ctx, t); err != nil {
			return err
		}
	}
	now := metav1.NewTime(s.now())
	cs := old.DeepCopy()
	cs.PlanVersion, cs.Plan, cs.PlanHash, cs.Budget = next.Version, next.DeepCopy(), hash, b
	cs.History = v1alpha1.AppendNewest(cs.History, v1alpha1.CoordinationPlanVersion{Version: next.Version, PlanHash: hash, AcceptedAt: now}, v1alpha1.MaxCoordinationHistory)
	cs.Authorizations = AuthorizationsFor(next, old.Authorizations, m)
	if old.Plan != nil && !m.ParentRequired {
		// A title or rationale edit needs only the parent's review of the repost (MR12): their
		// confirmation carries to the new hash, still dated when they gave it.
		if c := parentConfirmation(old); c != nil {
			carried := *c
			carried.PlanHash = hash
			cs.Confirmations = v1alpha1.AppendNewest(cs.Confirmations, carried, v1alpha1.MaxCoordinationConfirmations)
		}
	}
	t.Status.Phase = v1alpha1.PhaseCoordinating
	if t.Status.PhaseSince == nil {
		t.Status.PhaseSince = &now
	}
	t.Status.Coordination = cs
	if old.Plan != nil {
		if err := s.applyPlanVersion(ctx, t, next, m); err != nil {
			return err
		}
	}
	reason, _ := s.eligibility(ctx, cfg, cs) // a refused gate is the reason; the draft stands
	setReason(t, reason)
	if err := s.Client.Status().Update(ctx, t); err != nil {
		return err
	}
	if old.Plan != nil {
		s.observe(func(m Metrics) {
			m.CoordinationPlanVersion(ctx, next.Primary.Repository, "superseded")
			for _, c := range old.Plan.Children {
				if _, kept := find(next, c.ID); !kept {
					m.CoordinationChild(ctx, c.Repository, c.Class, "dropped")
				}
			}
		})
	}
	return nil
}

// setReason writes a confirmation's reason on parent t, except once filing has started: filed and
// filing_incomplete are control state the children's lineage and the filing replay read (Review
// Focus 1), so a later version's confirmations never rewrite them.
func setReason(t *v1alpha1.Task, reason string) {
	if r := t.Status.Reason; r == ReasonFiled || r == ReasonFilingIncomplete {
		return
	}
	t.Status.Reason = reason
}

// planningShape refuses a planning attempt whose cost nobody can reconcile: an identifier without
// its amount, a negative amount, or an attempt id past its bound. It accepts no AgentRun id: the
// type has none.
func planningShape(a PlanningReconciliation) error {
	switch {
	case a.ReservedMicroUSD < 0 || a.SettledMicroUSD < 0:
		return refuse(ErrPlanningCostUnverified, "planning", "a negative amount")
	case (a.ReservationID != "" && a.ReservedMicroUSD <= 0) || (a.SettlementID != "" && a.SettledMicroUSD <= 0):
		return refuse(ErrPlanningCostUnverified, "planning", "an identifier without its amount")
	case len(a.AttemptID) > maxAttemptID || len(a.ReservationID) > maxAttemptID || len(a.SettlementID) > maxAttemptID:
		return refuse(ErrPlanningCostUnverified, "planning", "an identifier over %d bytes", maxAttemptID)
	}
	return nil
}

// plannedBudget is p's budget status with a's planning cost added once per attempt, refusing a
// planning spend past planningMicroUSD.
func plannedBudget(prev *v1alpha1.CoordinationBudgetStatus, p v1alpha1.CoordinationPlan, a PlanningReconciliation) (*v1alpha1.CoordinationBudgetStatus, error) {
	b := &v1alpha1.CoordinationBudgetStatus{}
	if prev != nil {
		b = prev.DeepCopy()
	}
	b.PlanningMicroUSD, b.TotalMicroUSD = p.Budget.PlanningMicroUSD, p.Budget.TotalMicroUSD
	b.AllocatedMicroUSD, _ = Allocated(p) // Validate held the sum within the total
	if a.AttemptID == "" || !slices.Contains(b.PlanningAttempts, a.AttemptID) {
		ok := add(&b.PlanningReservedMicroUSD, a.ReservedMicroUSD) && add(&b.PlanningSettledMicroUSD, a.SettledMicroUSD)
		if !ok {
			return nil, refuse(ErrPlanningCostUnverified, "planning", "the amounts overflow")
		}
		if a.AttemptID != "" {
			b.PlanningAttempts = v1alpha1.AppendNewest(b.PlanningAttempts, a.AttemptID, v1alpha1.MaxCoordinationHistory)
		}
	}
	b.PlanningCost = v1alpha1.PlanningCostNotSupplied
	if a.ReservationID != "" || a.SettlementID != "" {
		b.PlanningCost = v1alpha1.PlanningCostSupplied
	}
	return b, planningFits(b)
}

// planningFits refuses planning reserved plus settled above planningMicroUSD (MR8).
func planningFits(b *v1alpha1.CoordinationBudgetStatus) error {
	if b == nil {
		return nil
	}
	spent := b.PlanningReservedMicroUSD
	if !add(&spent, b.PlanningSettledMicroUSD) || spent > b.PlanningMicroUSD {
		return fmt.Errorf("%w: planning spent %d of %d", ErrPlanningBudgetExhausted, spent, b.PlanningMicroUSD)
	}
	return nil
}

// Confirm records in's one confirmation of the parent's current plan, by actor: the parent's by a
// configured maintainer of the primary holding GitHub write, or one child's by that child's own
// maintainer holding write on that child's repository. One login confirming both makes two calls
// and two records. Once every confirmation is present under execute, the capability gate and every
// repository check decide whether filing is eligible; a refusal there is returned after the
// confirmation is recorded, and nothing is filed.
func (s *Service) Confirm(ctx context.Context, actor Actor, in CoordinationConfirmation) (CoordinationConfirmation, error) {
	cfg := s.config()
	if modeOf(cfg) == config.CoordinationOff {
		return CoordinationConfirmation{}, ErrCoordinationOff
	}
	if actor.LinkedGitHubLogin == "" {
		return CoordinationConfirmation{}, fmt.Errorf("%w: no linked GitHub login", ErrRepositoryPermission)
	}
	var out CoordinationConfirmation
	var gate error
	var completed *v1alpha1.CoordinationStatus // set when this confirmation completed the version
	key := types.NamespacedName{Namespace: s.Namespace, Name: in.Plan.ID}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var t v1alpha1.Task
		switch err := s.Client.Get(ctx, key, &t); {
		case apierrors.IsNotFound(err):
			return ErrParentNotFound
		case err != nil:
			return err
		case t.Spec.Kind != v1alpha1.TaskKindCoordination:
			return ErrParentConflict
		case v1alpha1.TerminalPhase(t.Status.Phase):
			return ErrParentTerminal
		case t.Status.Coordination == nil || t.Status.Coordination.Plan == nil:
			return ErrParentNotFound
		}
		cs := t.Status.Coordination
		completed = nil
		before := complete(cs)
		fp := ""
		if in.Child != nil {
			fp = ChildFingerprint(*cs.Plan, in.Child.ChildID)
		}
		if err := CheckConfirmation(cs.PlanHash, fp, in); err != nil {
			return err
		}
		if err := planningShape(in.Planning); err != nil {
			return err
		}
		if err := planningFits(cs.Budget); err != nil {
			return err
		}
		var err error
		out = CoordinationConfirmation{Plan: *cs.Plan.DeepCopy(), PlanHash: cs.PlanHash, Planning: in.Planning}
		if in.Parent != nil {
			out.Parent, err = s.confirmParent(ctx, cfg, actor, cs)
		} else {
			out.Child, err = s.authorizeChild(ctx, cfg, actor, cs, in.Child.ChildID, fp)
		}
		if err != nil {
			return err
		}
		var reason string
		reason, gate = s.eligibility(ctx, cfg, cs)
		setReason(&t, reason)
		if !before && complete(cs) {
			completed = cs
		}
		return s.Client.Status().Update(ctx, &t)
	})
	if errors.Is(err, ErrStalePlan) {
		s.observe(func(m Metrics) { m.CoordinationPlanVersion(ctx, in.Plan.Primary.Repository, "stale_refused") })
	}
	if err != nil {
		return CoordinationConfirmation{}, err
	}
	if cs := completed; cs != nil {
		s.observe(func(m Metrics) {
			m.CoordinationPlanVersion(ctx, cs.Plan.Primary.Repository, "confirmed")
			m.CoordinationWait(ctx, cs.Plan.Primary.Repository, "authorization", authorizationWait(cs, s.now()))
		})
	}
	return out, gate
}

// confirmParent records actor's confirmation of cs's current hash, once per login and hash.
func (s *Service) confirmParent(ctx context.Context, cfg *config.Config, actor Actor, cs *v1alpha1.CoordinationStatus) (*ParentConfirmation, error) {
	primary := cs.Plan.Primary.Repository
	pc, ok := find(*cs.Plan, primaryChildID(*cs.Plan))
	if !ok {
		return nil, refuse(ErrInvalidPlan, "primary.repository", "no child in the primary")
	}
	if !cfg.RepositoryFor(primary).IsMaintainer(actor.LinkedGitHubLogin) {
		return nil, fmt.Errorf("%w: not a maintainer of the primary", ErrRepositoryPermission)
	}
	if _, err := s.check(ctx, actor, pc, nil); err != nil {
		return nil, err
	}
	for i := range cs.Confirmations {
		c := cs.Confirmations[i]
		if c.PlanHash == cs.PlanHash && strings.EqualFold(c.LinkedGitHubLogin, actor.LinkedGitHubLogin) {
			return &c, nil
		}
	}
	c := ParentConfirmation{PlanHash: cs.PlanHash, Actor: actor.Principal, LinkedGitHubLogin: actor.LinkedGitHubLogin,
		ConfirmedAt: metav1.NewTime(s.now())}
	cs.Confirmations = v1alpha1.AppendNewest(cs.Confirmations, c, v1alpha1.MaxCoordinationConfirmations)
	return &c, nil
}

// authorizeChild records actor's authorization of child id at fingerprint fp, replacing that
// child's older record.
func (s *Service) authorizeChild(ctx context.Context, cfg *config.Config, actor Actor, cs *v1alpha1.CoordinationStatus, id, fp string) (*ChildAuthorization, error) {
	c, ok := find(*cs.Plan, id)
	if !ok {
		return nil, refuse(ErrInvalidConfirmation, "child.childID", "no such child")
	}
	if !strings.EqualFold(MaintainerLogin(c), actor.LinkedGitHubLogin) {
		return nil, fmt.Errorf("%w: not the child's maintainer", ErrRepositoryPermission)
	}
	if !cfg.RepositoryFor(c.Repository).IsMaintainer(actor.LinkedGitHubLogin) {
		return nil, fmt.Errorf("%w: not a maintainer of the child's repository", ErrRepositoryPermission)
	}
	pol, err := s.check(ctx, actor, c, nil)
	if err != nil {
		return nil, err
	}
	for i := range cs.Authorizations {
		a := cs.Authorizations[i]
		if a.ChildID == id && a.ChildFingerprint == fp && a.Repository == c.Repository {
			return &a, nil
		}
	}
	perm := pol.Permission
	if !slices.Contains([]string{"write", "maintain", "admin"}, perm) {
		perm = "write" // the check answered write without naming the level
	}
	a := ChildAuthorization{ChildID: id, ChildFingerprint: fp, PlanVersion: cs.Plan.Version, Actor: actor.Principal,
		Repository: c.Repository, Permission: perm, FenceSHA: pol.FenceSHA, ConfirmedAt: metav1.NewTime(s.now())}
	kept := slices.DeleteFunc(slices.Clone(cs.Authorizations), func(x v1alpha1.ChildAuthorization) bool { return x.ChildID == id })
	cs.Authorizations = v1alpha1.AppendNewest(kept, a, v1alpha1.MaxCoordinationAuthorizations)
	return &a, nil
}

// eligibility is the parent's reason after a confirmation, and execute's refusal when every
// confirmation is present but the gate or a repository check is not.
func (s *Service) eligibility(ctx context.Context, cfg *config.Config, cs *v1alpha1.CoordinationStatus) (string, error) {
	switch {
	case !complete(cs):
		return ReasonAwaitingConfirmation, nil
	case modeOf(cfg) != config.CoordinationExecute:
		return ReasonConfirmed, nil
	}
	if err := s.executeReady(ctx, cfg, cs, nil); err != nil {
		return Reason(err), err
	}
	return ReasonFilingEligible, nil
}

// ExecuteReady is executeReady for one decision of its own: it loads the config once.
func (s *Service) ExecuteReady(ctx context.Context, cs *v1alpha1.CoordinationStatus, ids []string) error {
	return s.executeReady(ctx, s.config(), cs, ids)
}

// executeReady re-reads every authority execute rests on, for the children ids names (every
// child when nil): the full plan validates for execute with each confirmed increase, the
// capability gate passes for every repository, the parent confirmer still maintains and writes the
// primary, and each child's authorizer still maintains and writes its repository at the fence they
// authorized. Nothing is cached: a regression refuses the next filing or admission.
func (s *Service) executeReady(ctx context.Context, cfg *config.Config, cs *v1alpha1.CoordinationStatus, ids []string) error {
	if cs == nil || cs.Plan == nil {
		return ErrParentNotFound
	}
	// The whole family needs every confirmation of the current plan; one child needs only its own
	// standing authorization under a parent confirmation of a version it is unchanged in, so a
	// newer version waiting for confirmations pauses only the children it affected (MR12).
	if ids == nil && !complete(cs) {
		return refuse(ErrInvalidConfirmation, "", "a confirmation of the current plan is missing")
	}
	pc := parentConfirmation(cs)
	for _, id := range ids {
		c := coveringConfirmation(cs, id)
		if c == nil {
			return refuse(ErrInvalidConfirmation, "children."+id, "a confirmation of the current plan is missing")
		}
		if pc == nil {
			pc = c
		}
	}
	if pc == nil {
		return refuse(ErrInvalidConfirmation, "", "no parent confirmation")
	}
	p := *cs.Plan
	increase := map[string]bool{}
	for _, c := range p.Children {
		increase[c.ID] = true // complete: the parent and every child confirmed this version
	}
	if _, err := ValidateWithIncrease(ctx, p, ModeExecute, s.resolve(), increase); err != nil {
		return err
	}
	repos := make([]string, 0, len(p.Children))
	for _, c := range p.Children {
		repos = append(repos, c.Repository)
	}
	if err := GateExecute(ctx, modeOf(cfg), s.Prerequisites, repos); err != nil {
		return err
	}
	if pp, ok := find(p, primaryChildID(p)); ok {
		if !cfg.RepositoryFor(p.Primary.Repository).IsMaintainer(pc.LinkedGitHubLogin) {
			return fmt.Errorf("%w: the parent confirmer no longer maintains the primary", ErrRepositoryPermission)
		}
		if _, err := s.check(ctx, Actor{Principal: pc.Actor, LinkedGitHubLogin: pc.LinkedGitHubLogin}, pp, nil); err != nil {
			return err
		}
	}
	for _, c := range p.Children {
		if ids != nil && !slices.Contains(ids, c.ID) {
			continue
		}
		if err := s.childAuthority(ctx, cfg, cs, c); err != nil {
			return err
		}
	}
	return nil
}

// childAuthority re-reads c's authorizer's authority at c's repository, bound to their fence.
func (s *Service) childAuthority(ctx context.Context, cfg *config.Config, cs *v1alpha1.CoordinationStatus, c v1alpha1.CoordinationChild) error {
	a := currentAuthorization(cs, c.ID)
	if a == nil {
		return refuse(ErrInvalidConfirmation, "children."+c.ID, "not authorized")
	}
	login := MaintainerLogin(c)
	if !cfg.RepositoryFor(c.Repository).IsMaintainer(login) {
		return fmt.Errorf("%w: %s's authorizer no longer maintains it", ErrRepositoryPermission, c.ID)
	}
	_, err := s.check(ctx, Actor{Principal: a.Actor, LinkedGitHubLogin: login}, c, a)
	return err
}

// MaintainerLogin is c's maintainer's GitHub login, without its github: prefix.
func MaintainerLogin(c v1alpha1.CoordinationChild) string {
	return strings.TrimPrefix(c.Maintainer, "github:")
}

// primaryChildID is the id of p's child in the primary repository, "" for none.
func primaryChildID(p v1alpha1.CoordinationPlan) string {
	for _, c := range p.Children {
		if c.Repository == p.Primary.Repository {
			return c.ID
		}
	}
	return ""
}

// parentConfirmation is cs's newest parent confirmation of the current hash, nil for none.
func parentConfirmation(cs *v1alpha1.CoordinationStatus) *ParentConfirmation {
	for i := len(cs.Confirmations) - 1; i >= 0; i-- {
		if cs.Confirmations[i].PlanHash == cs.PlanHash {
			return &cs.Confirmations[i]
		}
	}
	return nil
}

// currentAuthorization is the authorization of child id at its current fingerprint, nil for none.
func currentAuthorization(cs *v1alpha1.CoordinationStatus, id string) *ChildAuthorization {
	fp := ChildFingerprint(*cs.Plan, id)
	for i := len(cs.Authorizations) - 1; i >= 0; i-- {
		a := &cs.Authorizations[i]
		if a.ChildID == id && a.ChildFingerprint == fp {
			return a
		}
	}
	return nil
}

// complete is whether the current plan has its parent's confirmation and every child's
// authorization at its current fingerprint.
func complete(cs *v1alpha1.CoordinationStatus) bool {
	if cs == nil || cs.Plan == nil || parentConfirmation(cs) == nil {
		return false
	}
	for _, c := range cs.Plan.Children {
		if currentAuthorization(cs, c.ID) == nil {
			return false
		}
	}
	return true
}
