// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Smana/maku/internal/factory/config"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/summary"
)

// factsOf is what the room's summary shows of a task: phase, current run, budget, issue, PR.
func factsOf(t *v1alpha1.Task) envelope.TaskFacts {
	f := envelope.TaskFacts{Phase: t.Status.Phase, Reason: t.Status.Reason, Origin: t.Spec.Origin, Outcome: t.Spec.Outcome}
	if m := t.Status.Monitor; t.Spec.Origin != "" && m != nil {
		f.Monitor = m.Verdict
	}
	// An unfinished record is over (M4) when a later run superseded it (a retry from Escalated), ended
	// at the next run's start; or, for the last one, when the task ended or escalated as stuck, at its
	// phase's start. Otherwise a view would read a dead run as running.
	over := v1alpha1.TerminalPhase(t.Status.Phase) || t.Status.Phase == v1alpha1.PhaseEscalated
	fact := func(i int) envelope.RunFact {
		r := t.Status.Runs[i]
		rf := runFact(r)
		rf.Persona = personaOf(t, r)
		// The history keeps the models only: 20 runs of full provenance would pass the broker's body
		// bound for facts, which refuses for good.
		if len(r.Provenance.ServedModels) > 0 {
			rf.Provenance = &envelope.ProvenanceFact{ServedModels: slices.Clone(r.Provenance.ServedModels)}
		}
		if r.Finished != nil {
			return rf
		}
		var end *metav1.Time
		switch {
		case i < len(t.Status.Runs)-1:
			end = t.Status.Runs[i+1].Started
		case over:
			end = t.Status.PhaseSince
		default:
			return rf
		}
		rf.Outcome = envelope.OutcomeEnded
		if end != nil && !end.UTC().Truncate(time.Second).Before(rf.StartedAt) {
			rf.EndedAt = end.UTC().Truncate(time.Second)
		}
		return rf
	}
	if len(t.Status.Runs) > 0 {
		rf := fact(len(t.Status.Runs) - 1)
		if p := current(t).Provenance; !p.IsZero() {
			rf.Provenance = &envelope.ProvenanceFact{FactoryVersion: p.FactoryVersion, CompositionRevision: p.CompositionRevision,
				HarnessImage: p.HarnessImage, ServedModels: slices.Clone(p.ServedModels)}
		}
		f.Run = &rf
		for i := max(0, len(t.Status.Runs)-envelope.MaxRuns); i < len(t.Status.Runs); i++ {
			f.Runs = append(f.Runs, fact(i))
		}
	}
	if sp := t.Status.Split; sp != nil && sp.State == SplitApproved {
		f.Split = splitFact(sp)
	}
	f.Parent = parentFact(t.Spec.Parent)
	// Known limit: a task mixing priced and unpriced runs shows the priced dollars only, while its token cap binds.
	if u, limit := t.Status.Usage.CostMicroUSD, t.Spec.Budget.TaskMicroUSD; u > 0 || limit > 0 {
		f.Spend = &envelope.SpendFact{UsedMicroUSD: dollars(u), LimitMicroUSD: dollars(limit)}
	}
	if t.Spec.Budget.TaskTokens > 0 {
		f.Budget = &envelope.BudgetFact{UsedTokens: chargedTokens(t), LimitTokens: t.Spec.Budget.TaskTokens}
	}
	if t.Spec.Issue > 0 {
		f.Issue = &envelope.IssueFact{Number: t.Spec.Issue,
			URL:   fmt.Sprintf("https://github.com/%s/issues/%d", t.Spec.Repository, t.Spec.Issue),
			Title: issueTitle(t.Spec.Text), Author: t.Spec.IssueAuthor}
		if login, ok := strings.CutPrefix(t.Spec.Source.RequestedBy, "github:"); ok { // system:runlore labelled nothing
			f.Issue.LabelledBy = login
		}
	}
	if pr := t.Status.PullRequest; pr != nil && pr.Number > 0 {
		f.PR = &envelope.PRFact{Number: pr.Number, URL: pr.URL, Author: pr.Author, Reviewers: pr.Reviewers}
		// The facts' validation would refuse a login outside GitHub's shape, or a commit not hex, for
		// good: such an approval is left out, and the reason's own words stand.
		if a := pr.Approval; a != nil && envelope.ValidLogin(a.By) && envelope.ValidCommit(a.Commit) {
			f.PR.ApprovedBy, f.PR.ApprovedCommit, f.PR.ApprovalStale = a.By, a.Commit, a.Stale
		}
	}
	if p := t.Status.Profile; p != nil {
		pf := &envelope.ProfileFact{Name: p.Name, Source: p.Source, Cohort: p.Cohort,
			Recommended: p.Recommended, Reason: p.Reason, Why: p.Why, Requested: p.Requested, Refused: p.Refused}
		if p.Requires != nil {
			pf.RequiredBy, pf.RequiredStages = p.Requires.By, slices.Clone(p.Requires.Stages)
		}
		if t.Spec.Budget.TaskMicroUSD > 0 {
			pf.CeilingMicroUSD = dollars(t.Spec.Budget.TaskMicroUSD)
		}
		if p.Source == "label" {
			if t.Spec.Intake != nil && envelope.ValidLogin(t.Spec.Intake.Login) {
				pf.By = t.Spec.Intake.Login
			} else if f.Issue != nil && envelope.ValidLogin(f.Issue.LabelledBy) {
				pf.By = f.Issue.LabelledBy
			}
		}
		if p.StepUp != nil {
			pf.StepUp = &envelope.StepUpFact{
				From:            p.StepUp.From,
				To:              p.StepUp.To,
				Trigger:         p.StepUp.Trigger,
				Mode:            p.StepUp.Mode,
				CeilingMicroUSD: dollars(p.StepUp.CeilingMicroUSD),
			}
		}
		f.Profile = pf
	}
	return f
}

// runFact is a run record as the room's facts carry it, without provenance (factsOf adds it).
// Times are as the CRD stores them, so the hash of a task read back matches the one posted.
func runFact(r v1alpha1.RunRecord) envelope.RunFact {
	f := envelope.RunFact{ID: r.ID, Role: r.Role, Trigger: r.Trigger, Verdict: r.Verdict, MicroUSD: dollars(r.CostMicroUSD), Outcome: outcomeOf(r)}
	if r.Started != nil {
		f.StartedAt = r.Started.UTC().Truncate(time.Second)
	}
	if r.Finished != nil {
		f.EndedAt = r.Finished.UTC().Truncate(time.Second)
	}
	return f
}

// outcomeOf is how a run ended (R3), empty while it has not (no Finished). A lost run is lost under
// any phase; otherwise the AgentRun's phase decides, so PodFailed under Failed is failed.
func outcomeOf(r v1alpha1.RunRecord) string {
	if r.Finished == nil {
		return ""
	}
	switch r.Reason {
	case runs.ReasonDisrupted, runs.ReasonPodLost, "pod_lost", "run_lost":
		return envelope.OutcomeLost
	}
	switch r.Phase {
	case "Succeeded":
		return envelope.OutcomeSucceeded
	case "Failed":
		return envelope.OutcomeFailed
	case "BudgetExhausted":
		return envelope.OutcomeBudgetExhausted
	case "Revoked":
		return envelope.OutcomeRevoked
	}
	return envelope.OutcomeEnded
}

// maxTitle bounds the title the facts carry, in runes.
const maxTitle = 120

// issueTitle is the first line of the snapshot ("# title", intake.Snapshot), bounded: the
// room list shows it, and the Room status keeps it.
func issueTitle(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	title, ok := strings.CutPrefix(line, "# ")
	if !ok {
		return ""
	}
	title = strings.Join(strings.Fields(title), " ")
	if r := []rune(title); len(r) > maxTitle {
		title = string(r[:maxTitle]) + "…"
	}
	return title
}

// factsKey is what decides that the room's facts changed. Token usage counts in steps (R12): 10% of
// the cap, an overrun one step, or 100 k tokens without a cap; dollars step the same way, $0.10 without one. Every meter tick would otherwise post
// facts and write status twice, and a room seals at its event limit. Facts posted carry the exact
// count.
func factsKey(f envelope.TaskFacts) string {
	if b := f.Budget; b != nil {
		step := *b
		if step.LimitTokens > 0 {
			step.UsedTokens = min(step.UsedTokens*10/step.LimitTokens, 10)
		} else {
			step.UsedTokens /= 100_000
		}
		f.Budget = &step
	}
	if sp := f.Spend; sp != nil && !f.SpendFinal { // the final figure is a claim about money: never bucketed (CD23)
		step := *sp
		if step.LimitMicroUSD > 0 {
			step.UsedMicroUSD = min(step.UsedMicroUSD*10/step.LimitMicroUSD, 10)
		} else {
			step.UsedMicroUSD /= 100_000
		}
		f.Spend = &step
	}
	// An ended task's key leaves out what only the room's rail reads, the config-derived plan among
	// them, and the config-derived policy. Every retained task would otherwise read as changed after an upgrade, or a config change:
	// each would rewrite its room, and a sealed room's would post on every reconcile. A task that
	// ends still posts them, since its phase moves its key.
	ended := v1alpha1.TerminalPhase(f.Phase) || f.Phase == v1alpha1.PhaseEscalated
	if ended {
		f.Plan, f.Parent, f.Policy = nil, nil, nil
		if f.Profile != nil {
			p := *f.Profile
			p.Description = ""
			f.Profile = &p
		}
		// An escalated parent still waits on its parts: tellParent can revert one under it, with no
		// other field moving, and the rail must not keep drawing that part as landed.
		if f.Phase != v1alpha1.PhaseEscalated {
			f.Split = nil
		}
	}
	// A run's dollars count once it has ended: a running run's tick never posts (R4).
	f.Run = settled(f.Run)
	if ended && f.Run != nil {
		c := *f.Run
		c.Persona = ""
		f.Run = &c
	}
	f.Runs = slices.Clone(f.Runs)
	for i := range f.Runs {
		f.Runs[i] = *settled(&f.Runs[i])
		if ended {
			f.Runs[i].Persona, f.Runs[i].Provenance = "", nil
		}
	}
	if f.Split != nil {
		s := *f.Split
		if len(s.Parts) > 0 {
			s.Parts = slices.Clone(s.Parts)
			for i := range s.Parts {
				p := &s.Parts[i]
				if p.CostMicroUSD > 0 {
					p.CostMicroUSD /= 100_000
				}
				if len(p.Runs) > 0 {
					p.Runs = slices.Clone(p.Runs)
					for j := range p.Runs {
						if p.Runs[j].Outcome == "" {
							p.Runs[j].MicroUSD = 0
						}
					}
				}
			}
		}
		f.Split = &s
	}
	b, _ := json.Marshal(f) // strings, ints, a time and slices: Marshal cannot fail
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// settled is the run with its dollars left out while it has not ended.
func settled(r *envelope.RunFact) *envelope.RunFact {
	if r == nil || !r.EndedAt.IsZero() || r.Outcome != "" {
		return r
	}
	c := *r
	c.MicroUSD = 0
	return &c
}

// factsDue reports facts the room does not have yet.
func (r *Reconciler) factsDue(cfg *config.Config, t *v1alpha1.Task) bool {
	return r.factsDueCtx(context.Background(), cfg, t)
}

// factsDueCtx reports facts the room does not have yet, projecting live child facts for split parents.
func (r *Reconciler) factsDueCtx(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) bool {
	if t.Status.RoomRef == "" || r.Rooms == nil {
		return false
	}
	return !r.factsPosted(t, r.factsForCtx(ctx, cfg, t))
}

// factsPosted: the room's ledger records exactly these facts, posted.
func (r *Reconciler) factsPosted(t *v1alpha1.Task, f envelope.TaskFacts) bool {
	l := t.Status.Facts
	return l != nil && l.Posted && l.Hash == factsKey(f)
}

// factsFor is factsOf with what only the reconciler's config knows: whether an approval returns
// an AwaitingHuman hand-off (approvalReturns), so the room's merge need says what the issue says,
// whether the task's dollars are final (CD23), which is what stops the key bucketing them, and the
// merge policy that governs the task (policyOf).
func (r *Reconciler) factsFor(cfg *config.Config, t *v1alpha1.Task) envelope.TaskFacts {
	return r.factsForCtx(context.Background(), cfg, t)
}

func (r *Reconciler) factsForCtx(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) envelope.TaskFacts {
	f := factsOf(t)
	f.Plan = r.planOf(cfg, t)
	f.MergeByApproval = t.Status.Phase == v1alpha1.PhaseAwaitingHuman && r.approvalReturns(cfg, t, t.Status.Reason)
	f.SpendFinal = r.spendIsFinal(cfg, t)
	f.Policy = policyOf(cfg, r.repo(cfg, t), t)
	f.RetryHeld = RetryHeld(cfg, t)
	if f.Profile != nil {
		if wf, ok := cfg.Workflow(f.Profile.Name); ok {
			if wf.Description != "" {
				f.Profile.Description = wf.Description
			}
			if t.Spec.MaxMicroUSD > 0 {
				f.Profile.ConfirmedMicroUSD = dollars(t.Spec.MaxMicroUSD)
				f.Profile.WorkflowCeilingMicroUSD = dollars(int64(cfg.Tiers[cfg.EffectiveBudgetTier(r.repo(cfg, t), f.Profile.Name)].TaskUSD * 1e6))
			}
			if wf.StepUp != "" && !alreadyStepped(t.Status.Profile) && r.repo(cfg, t).StepUpMode() != "off" {
				if target, ok := cfg.Workflow(wf.StepUp); ok {
					f.Profile.StepUpCeilingMicroUSD = dollars(targetCeilingMicroUSD(cfg, t, target))
					f.Profile.StepUpTo = wf.StepUp
				}
			}
		}
	}
	if f.Split != nil {
		r.enrichSplitParts(ctx, cfg, t, f.Split)
	}
	return f
}

// enrichSplitParts fills each opened part's live state, phase, dependencies, cost, path strip and
// runs from its newest child Task so every surface (room rail, parent comment, Slack, CLI) shows
// one consistent tree (phase H5).
func (r *Reconciler) enrichSplitParts(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, sp *envelope.SplitFact) {
	if sp == nil || len(sp.Parts) == 0 || r.Client == nil {
		return
	}
	newest, _, err := r.family(ctx, t)
	if err != nil {
		return
	}
	byIndex := make(map[int]*v1alpha1.Task, len(newest))
	for k, c := range newest {
		byIndex[int(k)] = c
	}
	for i := range sp.Parts {
		p := &sp.Parts[i]
		c := byIndex[p.Index]
		if c == nil {
			continue
		}
		p.Phase = c.Status.Phase
		p.State = partStateForChild(p.Outcome, c)
		cost := c.Status.Usage.CostMicroUSD
		if cost == 0 {
			for _, rec := range c.Status.Runs {
				cost += rec.CostMicroUSD
			}
		}
		p.CostMicroUSD = dollars(cost)
		if len(c.Spec.DependsOn) > 0 {
			deps := make([]int, 0, len(c.Spec.DependsOn))
			for _, d := range c.Spec.DependsOn {
				if d > 0 {
					deps = append(deps, int(d))
				}
			}
			p.DependsOn = deps
		}
		cf := factsOf(c)
		cf.Plan = r.planOf(cfg, c)
		p.Strip = partStripOf(cf)
		if len(cf.Runs) > 0 {
			start := max(0, len(cf.Runs)-envelope.MaxPartRuns)
			p.Runs = make([]envelope.PartRunFact, 0, len(cf.Runs)-start)
			for _, rf := range cf.Runs[start:] {
				p.Runs = append(p.Runs, envelope.PartRunFact{
					ID:       rf.ID,
					Role:     rf.Role,
					Persona:  rf.Persona,
					Outcome:  rf.Outcome,
					Verdict:  rf.Verdict,
					MicroUSD: rf.MicroUSD,
				})
			}
		}
	}
}

func partStateForChild(outcome string, c *v1alpha1.Task) string {
	switch outcome {
	case ChildLanded:
		return "merged"
	case ChildDropped:
		return "dropped"
	case ChildReverted:
		return "reverted"
	}
	switch c.Status.Phase {
	case v1alpha1.PhaseDone, v1alpha1.PhaseMerged:
		return "merged"
	case v1alpha1.PhaseReverted:
		return "reverted"
	case v1alpha1.PhaseEscalated:
		return "escalated"
	case v1alpha1.PhaseStopped:
		return "stopped"
	case v1alpha1.PhaseClosed:
		return "closed"
	case v1alpha1.PhaseAwaitingHuman:
		return "waiting"
	case v1alpha1.PhaseTriaged:
		if c.Status.Reason == ReasonWaitingDependency || len(c.Spec.DependsOn) > 0 {
			return "waiting"
		}
		return "queued"
	case v1alpha1.PhaseReceived, v1alpha1.PhaseQueued:
		if len(c.Spec.DependsOn) > 0 {
			return "waiting"
		}
		return "queued"
	default:
		return "working"
	}
}

func partStripOf(cf envelope.TaskFacts) string {
	fl := &summary.Flow{Plan: cf.Plan, Nodes: make([]summary.FlowNode, 0, len(cf.Runs))}
	for _, rf := range cf.Runs {
		fl.Nodes = append(fl.Nodes, summary.FlowNode{
			Run: rf.ID, Persona: rf.Persona, Role: rf.Role,
			StartedAt: rf.StartedAt, EndedAt: rf.EndedAt, Outcome: rf.Outcome,
		})
	}
	if v1alpha1.TerminalPhase(cf.Phase) || cf.Phase == v1alpha1.PhaseEscalated || cf.Phase == v1alpha1.PhaseVerifying {
		fl.Ghosts = []string{}
	}
	return summary.Strip(fl)
}

// postFacts writes the task's facts into its room. New facts take roomSeq + 1, persisted before
// the post (ruling SK); a retry of the same facts resends that seq, which the broker keeps once.
// The caller's status write records Posted. A room the broker has no log for yet, or a broker that
// does not allow the factory yet (FR-1), is a wait, as for the snapshot: the facts stay due. A sealed
// room never takes them, so they are settled: waiting would post and log on every later reconcile,
// and an error would hold the task in backoff instead of its poll.
func (r *Reconciler) postFacts(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	if !r.factsDueCtx(ctx, cfg, t) {
		return nil
	}
	f := r.factsForCtx(ctx, cfg, t)
	h := factsKey(f)
	if l := t.Status.Facts; l == nil || l.Hash != h {
		prevSeq, prevLedger := t.Status.RoomSeq, t.Status.Facts
		t.Status.RoomSeq++
		t.Status.Facts = &v1alpha1.FactsLedger{Seq: t.Status.RoomSeq, Hash: h}
		if err := r.Client.Status().Update(ctx, t); err != nil {
			t.Status.RoomSeq, t.Status.Facts = prevSeq, prevLedger
			return fmt.Errorf("facts: persist room seq: %w", err)
		}
	}
	err := r.Rooms.TaskFacts(ctx, t.Status.RoomRef, f, t.Status.Facts.Seq)
	switch {
	case errors.Is(err, rooms.ErrNoRoute):
		// A broker older than the factory (R28): every task would say so on every reconcile.
		r.oldBroker.Do(func() {
			r.log().Warn("task facts wait for a broker that serves them: upgrade the broker", "err", err)
		})
		return nil
	case errors.Is(err, rooms.ErrSealed):
		r.log().Info("task facts dropped: the room is sealed", "task.id", t.Name, "seq", t.Status.Facts.Seq)
	case errors.Is(err, rooms.ErrNoRoom), errors.Is(err, rooms.ErrNotPermitted):
		r.log().Info("task facts wait for the broker", "task.id", t.Name, "seq", t.Status.Facts.Seq, "err", err)
		return nil
	case err != nil:
		return fmt.Errorf("facts: room %s seq %d: %w", t.Status.RoomRef, t.Status.Facts.Seq, err)
	}
	t.Status.Facts.Posted = true
	return nil
}

// dollars is a micro-USD amount clamped to the envelope's bound: a task past it still posts its facts,
// read as the cap.
func dollars(v int64) int64 { return min(max(v, 0), envelope.MaxMicroUSD) }

// personaOf is what a run is for. The triager role is the scoper on every template but investigate,
// where it triages; the role itself stays what the run may do.
func personaOf(t *v1alpha1.Task, r v1alpha1.RunRecord) string {
	if r.Role == "triager" && t.Spec.Template != "investigate" {
		return "scoper"
	}
	return r.Role
}

// planOf is the personas the task is expected to run, in order, for the room's stage strip.
func (r *Reconciler) planOf(cfg *config.Config, t *v1alpha1.Task) []string {
	if cfg.Investigates(t.Spec.Template) {
		return []string{"triager"}
	}
	var plan []string
	add := func(p string) {
		if !slices.Contains(plan, p) && len(plan) < envelope.MaxPlan {
			plan = append(plan, p)
		}
	}
	if scoperPlanned(t) {
		add("scoper")
	}
	if r.testerPlanned(cfg, t) {
		add("tester")
	}
	for _, role := range cfg.Templates[t.Spec.Template].Roles {
		add(role)
	}
	return plan
}

// splitFact is an approved split's parts, each with the room of its child task.
func splitFact(s *v1alpha1.SplitStatus) *envelope.SplitFact {
	f := &envelope.SplitFact{Of: int(s.Of)}
	for _, c := range s.Children {
		f.Parts = append(f.Parts, envelope.PartFact{Index: int(c.Index), Issue: c.Issue, Room: c.Task, Outcome: c.Outcome})
	}
	return f
}

// parentFact is the split parent a part belongs to; nil for a task that is no part.
func parentFact(p *v1alpha1.Parent) *envelope.ParentFact {
	if p == nil {
		return nil
	}
	return &envelope.ParentFact{Room: p.Task, Issue: p.Issue, Index: int(p.Index), Of: int(p.Of)}
}
