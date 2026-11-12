// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/ledger"
	"github.com/Smana/maku/internal/factory/narrate"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/factory/taskid"
	"github.com/Smana/maku/internal/factory/tracing"
)

// runEndGrace is how long a finished run waits for the room's end reason before falling back to
// the AgentRun's phase: SP2 knows why a run ended, the AgentRun only says Failed.
const runEndGrace = time.Minute

// slotFree: the active-task and concurrent-run caps (§6.2), and never while a human drives (C4).
func (r *Reconciler) slotFree(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (bool, string, error) {
	active, err := r.countTasks(ctx, func(o *v1alpha1.Task) bool {
		return o.Name != t.Name && o.Spec.Source.Kind != "adopt" && v1alpha1.ActivePhase(o.Status.Phase)
	})
	if err != nil {
		return false, "", err
	}
	if active >= cfg.Caps.ActiveTasks {
		return false, "waiting_active_tasks", nil
	}
	// A live class may auto-merge its PR, so human review WIP is not its bottleneck; the shadow
	// and unclassified work is exactly what lands on the review queue.
	if len(t.Status.Runs) == 0 && !r.repo(cfg, t).Classes[t.Spec.PredictedClass].Live {
		wip, err := r.countTasks(ctx, func(o *v1alpha1.Task) bool {
			return o.Spec.Source.Kind != "adopt" && o.Status.Phase == v1alpha1.PhaseAwaitingHuman
		})
		if err != nil {
			return false, "", err
		}
		if wip >= cfg.Caps.AwaitingHumanWIP {
			return false, "waiting_review_wip", nil // back-pressure on the reviewers (§6.2)
		}
	}
	all, err := r.Runs.List(ctx)
	if err != nil {
		return false, "", err
	}
	live := 0
	for _, x := range all {
		if runs.Terminal(x.Phase) {
			continue
		}
		if x.Principal == runs.PrincipalFactory {
			live++
		}
		// One live run per room (C3: a room's runs share its branch). The task's own was recorded
		// before this; anyone else's, a run a human started in the room, is waited for.
		if x.RoomRef == t.Status.RoomRef && x.TaskID != t.Name {
			return false, "waiting_room_busy", nil
		}
	}
	if live >= cfg.Caps.ConcurrentRuns {
		return false, "waiting_run_slot", nil
	}
	// CD22: a revision goes first. preferRevisions waits only for a task whose next run continues it and
	// which a *slot* holds: a neighbour stopped by any other cap is not waiting for the slot, and priority
	// never overrides a limit. A first run waits one slot-time; nothing is reserved.
	if cfg.Caps.PreferRevisions && len(t.Status.Runs) == 0 {
		waiting, err := r.revisionWaiting(ctx, t)
		if err != nil {
			return false, "", err
		}
		if waiting {
			record(ctx, func(ctx context.Context) { r.Metrics.RevisionDeferred(ctx) })
			return false, "waiting_revision_first", nil
		}
	}
	var room roomv1.Room
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: t.Status.RoomRef}, &room); err != nil {
		return false, "", err
	}
	if rooms.HumanDriver(&room) {
		return false, "waiting_human_driver", nil
	}
	return true, "", nil
}

// revisionWaiting is CD22's condition: whether another task of the factory is queued for a slot to
// run a run that continues it. Only waiting_run_slot counts: a task a cap holds waits for that cap,
// so yielding to it would starve the first runs while the backlog is already bounded.
func (r *Reconciler) revisionWaiting(ctx context.Context, t *v1alpha1.Task) (bool, error) {
	queued, err := r.countTasks(ctx, func(o *v1alpha1.Task) bool {
		return o.Name != t.Name && o.Status.Phase == v1alpha1.PhaseQueued && len(o.Status.Runs) > 0 &&
			o.Status.Reason == "waiting_run_slot"
	})
	return queued > 0, err
}

// runID is the deterministic id of a task's nth run (R48): the claim a replay recomputes, so a
// lost status write finds the run it created instead of minting another. A C2 id, like a Task
// name, so the claim's name is always <runs.namePrefix><id>.
func runID(task string, n int) string { return taskid.Run(task, n) }

// recordExisting records a claim the task's status does not know (R48): the run was created and
// the write that recorded it was lost, so the replay found the claim by its deterministic id.
// The claim is the record — its own role, start seq, head and tokens, whatever its phase — never
// the replay's spec, which may hold a newer brief or a later room seq.
func (r *Reconciler) recordExisting(cfg *config.Config, t *v1alpha1.Task, x runs.Run) error {
	if x.TaskID != t.Name {
		return fmt.Errorf("record run %s of task %s: the id belongs to task %q", x.ID, t.Name, x.TaskID)
	}
	// A verifier's trigger is always "review", and the lost write is what cleared NextRole; an
	// implementer's survives in NextTrigger, which the same write would have cleared.
	trigger := nextTrigger(t)
	if x.Role == "reviewer" {
		trigger = "review"
	}
	started := metav1.NewTime(r.Now())
	if !x.Created.IsZero() {
		started = metav1.NewTime(x.Created) // when the claim really was created, not the replay
	}
	t.Status.Runs = append(t.Status.Runs, v1alpha1.RunRecord{ID: x.ID, Role: x.Role, Trigger: trigger,
		Round: t.Status.ReviewRounds, Phase: x.Phase, Tokens: x.Tokens, StartSeq: x.StartSeq, HeadSHA: x.Head,
		Started: &started, ChargedTo: x.ChargedTo, Provenance: v1alpha1.RunProvenance{ModelSource: r.modelSourceOf(cfg, t, x)},
		Audited: x.Canary != "" && r.auditsRun(cfg, t, x.Role)}) // the claim says whether it started under audit
	if overlapPending(t) && x.Role == "triager" {
		t.Status.Overlap.ScoperRun = x.ID
	}
	t.Status.NextTrigger, t.Status.NextRole = "", ""
	r.to(t, phaseFor(x.Role), "adopted")
	return nil
}

func phaseFor(role string) string {
	if role == "reviewer" {
		return v1alpha1.PhaseReviewing
	}
	return v1alpha1.PhaseImplementing
}

// sourceURL is the issue a task narrates on, the harness footer's Agent-Task (ruling SF).
func sourceURL(t *v1alpha1.Task) string {
	if t.Spec.Issue == 0 {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/issues/%d", t.Spec.Repository, t.Spec.Issue)
}

// implementerSpec: every run of a task writes agent/<taskId> (C3); baseRef main, because the
// harness resumes origin/<branch> when an earlier run pushed it (SP1 R7).
func (r *Reconciler) implementerSpec(cfg *config.Config, t *v1alpha1.Task, text string) runs.Spec {
	s := runs.Spec{TaskID: t.Name, Role: "implementer", Repository: t.Spec.Repository, BaseRef: "main",
		Branch: "agent/" + t.Name, TaskText: text, Principal: runs.PrincipalFactory, DataClass: t.Spec.DataClass,
		Model: t.Spec.Budget.Model, RoomRef: t.Status.RoomRef, SourceURL: sourceURL(t), Queue: runs.QueueFactory,
		MaxTokens: t.Spec.Budget.RunTokens, MaxMinutes: t.Spec.Budget.RunMinutes, Traceparent: traceparent(t), Tier: t.Spec.Budget.Tier,
		CoordinationArtifacts: t.Spec.CoordinationArtifacts}
	r.withChecks(cfg, &s, t)
	return s
}

// withChecks switches the repository's checks on for a public implementer, tester or reviewer run of a class
// that has them (v3 feature 3, RC8): spec.loop.checks and the repository's checks.egress. Any other
// run, an internal one included, is today's.
func (r *Reconciler) withChecks(cfg *config.Config, s *runs.Spec, t *v1alpha1.Task) {
	if !r.checksOn(cfg, t) || t.Spec.DataClass != "public" || (s.Role != "implementer" && s.Role != "reviewer" && s.Role != "tester") {
		s.Checks, s.EgressProfiles = false, nil
		return
	}
	s.Checks, s.EgressProfiles = true, slices.Clone(r.repo(cfg, t).Checks.Egress)
}

// checksOn: a decision frozen at triage survives later config changes; a task without one reads live config.
func (r *Reconciler) checksOn(cfg *config.Config, t *v1alpha1.Task) bool {
	if t.Status.Profile != nil {
		return t.Status.Profile.Checks
	}
	on, _ := r.repo(cfg, t).ChecksFor(t.Spec.PredictedClass)
	return on
}

// reserveFactoryDay is §6.2's "SP3 at admission" for the factory's own principal (R34), admitted
// as the API admits (#78): the run about to start is reserved in the day's ledger under its
// deterministic id before its claim exists, so runs started on one reading cannot overshoot the
// day, and a replay re-reserves the same entry, never a second. The meter drops it when the run
// ends. No room: the factory's room exclusion is slotFree's, on the claims, and a reservation
// that names the room would hold the task's next run until the meter's next tick.
//
// With per-person caps on, the same entry also charges the person who caused the run (D2, D3).
func (r *Reconciler) reserveFactoryDay(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, limit int64) (string, error) {
	if r.Ledger == nil {
		return "", errors.New("the daily cap needs the ledger client")
	}
	person := r.person(cfg, t)
	// F4, ledger ruling I1: a sponsored principal's day is a bound, not a shadow, whatever
	// budgets.enforcePrincipal says, and whatever the fleet's own day.
	enforce := cfg.Budgets.EnforcePrincipal || r.sponsored(cfg, t)
	return ledger.Reserve(ctx, r.Ledger, r.Namespace, r.Now(), runID(t.Name, len(t.Status.Runs)),
		ledger.Reservation{Principal: runs.PrincipalFactory, Person: person, MaxMicroUSD: runMicroUSD(t.Spec.Budget)}, limit,
		cfg.Budgets.DailyMicroUSD(person), enforce)
}

// sponsored is whether the task's runs are charged to a declared sponsored principal.
func (r *Reconciler) sponsored(cfg *config.Config, t *v1alpha1.Task) bool {
	return cfg.PrincipalBy(r.person(cfg, t)) != nil
}

// runMicroUSD is what one run of the task may spend, as the day ledger reserves it: its USD run
// cap, or its token cap at the reference rate for a tier capped in tokens (TC7).
func runMicroUSD(b v1alpha1.Budget) int64 {
	if b.RunMicroUSD > 0 {
		return b.RunMicroUSD
	}
	return config.RefMicroUSD(b.RunTokens)
}

// person is the key the task's runs are charged to beside the fleet: an api task's principal
// always (R7), otherwise the person while per-person caps are on.
func (r *Reconciler) person(cfg *config.Config, t *v1alpha1.Task) string {
	if p := t.Sponsored(); p != "" && t.Status.RetriedBy == "" {
		return p
	}
	if !cfg.Budgets.PerPerson.Enabled {
		return ""
	}
	return t.Payer()
}

// traceparent is the task span's W3C header for its runs (R46); empty when tracing is off.
func traceparent(t *v1alpha1.Task) string {
	if t.Status.Trace == nil {
		return ""
	}
	return tracing.Traceparent(t.Status.Trace.TraceID, t.Status.Trace.SpanID)
}

func (r *Reconciler) startRun(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, s runs.Spec, trigger string) error {
	// The room's lastSeq before the run: its handoff, verdict and end are read after it, never
	// from the start of a long room (EventsSince stops at 10,000 events). It rides the claim
	// (AnnStartSeq), so a replay that finds the claim records the seq the run was given.
	last, err := r.Rooms.LastSeq(ctx, t.Status.RoomRef)
	if err != nil {
		return err
	}
	s.RunID, s.StartSeq, s.ChargedTo = runID(t.Name, len(t.Status.Runs)), last, r.person(cfg, t)
	if p := t.Status.Profile; p != nil { // the meter labels the run's cost with them (R34)
		s.Cohort, s.Profile = p.Cohort, p.Name
	}
	if t.Spec.MaxMicroUSD > 0 { // F4, C-1: the meter holds an api task's run to its own cap, not the tier's
		s.MaxMicroUSD = runMicroUSD(t.Spec.Budget)
	}
	// The loop policy closes implementer runs only (L2); every one of them, revisions and CI fixes
	// included, carries the task's policy, read at triage.
	if l := t.Spec.Loop; l != nil && s.Role == "implementer" {
		s.Loop = &runs.Loop{StallSteps: l.StallSteps, Continuations: l.Continuations, JudgeModel: l.JudgeModel}
	}
	// Search rides every implementer claim of a task triaged with it (v3 feature 5, ST9), the loop's
	// keys only when its loop is on.
	s.Search = t.Spec.Search && s.Role == "implementer"
	source := r.pinModel(cfg, t, &s)
	r.plantCanary(cfg, t, &s)     // hardening audit or enforce only (HR7)
	r.markSafeOutputs(cfg, t, &s) // the repository's enforceOutputs only (HR14)
	err = r.Runs.Create(ctx, s)
	unbriefed := false
	if refusedSchema(err) && s.Role == "reviewer" && s.TaskText != "" && s.TaskURL != "" {
		// HR4: a schema that predates it (the host's XRD, or an AgentRun CRD not upgraded) refuses
		// text beside url. Degrade to today's reviewer, the URL alone, rather than block the task.
		r.log().Warn("the AgentRun schema refuses the reviewer's brief beside its pull request; the reviewer runs without it",
			"task.id", t.Name, "run.id", s.RunID, "err", err)
		s.TaskText, unbriefed = "", true
		err = r.Runs.Create(ctx, s)
	}
	if err != nil {
		// The deterministic id already has a claim: the run was created and its record lost
		// (R48). Recorded, never re-created.
		if !apierrors.IsAlreadyExists(err) {
			// The cluster refused a claim carrying spec.loop, checks included (Strict validation: the host's XRD lacks it).
			// Retrying cannot help; hand the task to a maintainer instead of backing off forever.
			if (s.Loop != nil || s.Checks || s.Search) && refusedSchema(err) {
				r.log().Warn("loop, repository checks or search claim refused by the cluster", "task.id", t.Name, "err", err)
				return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, "loop_unsupported")
			}
			return err
		}
		x, found, gerr := r.Runs.Get(ctx, s.RunID)
		if gerr != nil {
			return errors.Join(err, gerr)
		}
		if !found {
			return err
		}
		return r.recordExisting(cfg, t, x)
	}
	now := metav1.NewTime(r.Now())
	t.Status.Runs = append(t.Status.Runs, v1alpha1.RunRecord{ID: s.RunID, Role: s.Role, Trigger: trigger,
		Round: t.Status.ReviewRounds, StartSeq: last, HeadSHA: s.Head, Started: &now, ChargedTo: s.ChargedTo,
		Provenance: v1alpha1.RunProvenance{ModelSource: source}, Audited: r.auditsRun(cfg, t, s.Role)})
	t.Status.NextTrigger = ""
	r.recordInputTrust(ctx, cfg, t, trigger) // hardening audit or enforce only (UH7)
	r.to(t, phaseFor(s.Role), "")
	narrateLater(t, narrate.Started(t, s, cfg.RoomsURL))
	if unbriefed {
		r.unbriefedReviewer(ctx, t)
	}
	return nil
}

// refusedSchema: the cluster refused the claim's shape, which no retry of the same claim changes.
func refusedSchema(err error) bool { return apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) }

// The evidence a revision brief reads from GitHub (CD12, fast-follow 6).
const (
	// maxEvidenceChecks bounds the failing checks a brief reads annotations for: a check's own
	// words are context, and one read is one GitHub call the whole factory shares.
	maxEvidenceChecks = 5
	// evidenceUnread is the one sentence a check the merger App cannot read leaves in the brief
	// (fast-follow 6): what failed, and the permission that fixes it.
	evidenceUnread = "the factory could not read the failing check's details: the merger App needs checks: read on this repository"
)

// evidence is what a revision brief quotes from GitHub (CD12): the open change requests' bodies and
// their comments, and the failing checks' annotations. Every string is a reviewer's or a check's, so
// the block renders it as data. A source that cannot be read yields one plain sentence — one however
// many checks failed, since it is one permission and one fix — and never fails the pass (fast-follow
// 6); with brief.ciEvidence off it reads nothing at all.
func (r *Reconciler) evidence(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (items []evidenceItem, notes []string) {
	if !cfg.Brief.CIEvidence || t.Status.PullRequest == nil || t.Status.PullRequest.Number == 0 {
		return nil, nil
	}
	n := t.Status.PullRequest.Number
	pr, err := r.gh(ctx).PullRequest(ctx, n)
	switch {
	case err != nil:
		r.log().Warn("the revision brief's review is unread", "task.id", t.Name, "pr", n, "err", err)
		notes = append(notes, fmt.Sprintf("the factory could not read pull request #%d's review: it is on the pull request.", n))
	default:
		// The open change requests by a maintainer: the same reviews that send a task back for a
		// revision, whatever its earlier runs have acted on. A stranger's review is advice to a
		// maintainer (Δ5), and a request its author approved over is lifted.
		for _, rv := range openChangeRequests(pr.Reviews, false) {
			if !r.repo(cfg, t).IsMaintainer(rv.Author) {
				continue
			}
			items = append(items, evidenceItem{source: evidenceReview, where: reviewWhere(rv, n), text: rv.Body})
			for _, c := range rv.Comments {
				items = append(items, evidenceItem{source: evidenceReview, where: commentWhere(rv, c), text: c.Body})
			}
		}
	}
	checks, err := r.merger(ctx).PullRequestChecks(ctx, n)
	if err != nil {
		r.log().Warn("the revision brief's checks are unread", "task.id", t.Name, "pr", n, "err", err)
		return items, append(notes, evidenceUnread)
	}
	var failing []forge.Check
	for _, c := range checks.Runs {
		if c.State == "FAILURE" {
			failing = append(failing, c)
		}
	}
	unread := false
	for i, c := range failing {
		if i == maxEvidenceChecks {
			notes = append(notes, fmt.Sprintf("the factory quotes the first %d failing checks; %d more are on the pull request.",
				maxEvidenceChecks, len(failing)-i))
			break
		}
		as, err := r.merger(ctx).CheckAnnotations(ctx, c.ID)
		if err != nil { // the merger App lacks checks: read, or the run is gone: never a lost brief
			r.log().Warn("the revision brief's annotations are unread", "task.id", t.Name, "pr", n, "check", c.ID, "err", err)
			unread = true
			continue
		}
		for _, a := range as {
			items = append(items, evidenceItem{source: evidenceCI, where: annotationWhere(c, a), text: a.Message})
		}
	}
	if unread { // one sentence however many checks failed: it is one permission, and one fix
		notes = append(notes, evidenceUnread)
	}
	return items, notes
}

// revisionBrief is the revision brief as the factory builds it: the room's log and queue, and, with
// brief.ciEvidence, what the review asked for and what CI said (CD12). One place, so a test that
// renders the brief renders what production does.
func (r *Reconciler) revisionBrief(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, evs []envelope.Event, queued []rooms.Queued, sc *Scoped, tail string) (string, []int64) {
	items, notes := r.evidence(ctx, cfg, t)
	return reviseBrief(t, evs, queued, r.Nonce(), sc, evidenceSet{items: items, notes: notes, tail: tail})
}

// reviewWhere is where a review's body came from, as the brief names it.
func reviewWhere(rv forge.Review, n int) string {
	if rv.Author == "" {
		return fmt.Sprintf("a review on #%d", n)
	}
	return fmt.Sprintf("review by @%s on #%d", rv.Author, n)
}

// commentWhere is where one inline comment came from: its reviewer's login and its place in the diff.
func commentWhere(rv forge.Review, c forge.ReviewComment) string {
	at := c.Path
	if c.Line > 0 {
		at = fmt.Sprintf("%s:%d", c.Path, c.Line)
	}
	if rv.Author == "" {
		return "comment on " + at
	}
	return fmt.Sprintf("comment by @%s on %s", rv.Author, at)
}

// annotationWhere is where one annotation came from: the check run, its level, and the line it
// marked. A log the factory's egress policy will not fetch leaves this and the message (CD12).
func annotationWhere(c forge.Check, a forge.Annotation) string {
	level := a.Level
	if level == "" {
		level = "failure"
	}
	at := a.Path
	switch {
	case at == "" && a.StartLine > 0:
		at = fmt.Sprintf("line %d", a.StartLine)
	case at != "" && a.StartLine > 0:
		at = fmt.Sprintf("%s:%d", a.Path, a.StartLine)
	}
	where := fmt.Sprintf("check %s (run %d), %s", c.Name, c.ID, level)
	if at != "" {
		where += " on " + at
	}
	return where
}

// unbriefedReviewer says in the room, once per task and best effort, that a reviewer runs on the
// pull request alone because the AgentRun schema refuses its brief beside it (HR4).
func (r *Reconciler) unbriefedReviewer(ctx context.Context, t *v1alpha1.Task) {
	text := "Agent factory: the reviewer runs without its brief, on the pull request alone: the cluster's AgentRun " +
		"schema refuses task.text beside task.url. The platform's AgentRun schema needs the update that lets a reviewer carry both."
	if err := narrate.Room(ctx, r.Rooms, t, "reviewer-unbriefed", text,
		func(ctx context.Context) error { return r.Client.Status().Update(ctx, t) }); err != nil {
		r.log().Warn("the unbriefed reviewer's room note not posted", "task.id", t.Name, "err", err)
	}
}

// pinModel puts a public run on the provider alias its class's loops.<class>.models names for its
// role and the tier it runs on (v3 F15, F3), and returns which config chose the run's model, for its
// provenance. ModelFor is "" unless the claim's data class is public: an internal run keeps its
// tier's model, or the template's reviewerModel, whatever the row says (F4).
func (r *Reconciler) pinModel(cfg *config.Config, t *v1alpha1.Task, s *runs.Spec) string {
	if m := cfg.ModelFor(t.Spec.PredictedClass, s.DataClass, s.Role, s.Tier); m != "" {
		s.Model = m
		return v1alpha1.ModelSourceClassModels
	}
	if s.Role == "reviewer" && cfg.Templates[t.Spec.Template].ReviewerModel != "" {
		return v1alpha1.ModelSourceReviewerModel
	}
	return v1alpha1.ModelSourceTier
}

// modelSourceOf is pinModel's answer for a run adopted from its claim (R48), derived from the
// claim's model: only loops.<class>.models names a provider alias, and a reviewer on the
// template's reviewerModel ran on it.
func (r *Reconciler) modelSourceOf(cfg *config.Config, t *v1alpha1.Task, x runs.Run) string {
	switch rm := cfg.Templates[t.Spec.Template].ReviewerModel; {
	case config.PublicOnly(x.Model):
		return v1alpha1.ModelSourceClassModels
	case x.Role == "reviewer" && rm != "" && x.Model == rm:
		return v1alpha1.ModelSourceReviewerModel
	}
	return v1alpha1.ModelSourceTier
}

// current is the task's last run record.
func current(t *v1alpha1.Task) *v1alpha1.RunRecord { return &t.Status.Runs[len(t.Status.Runs)-1] }

// observe lists the task's claims once and copies each one's usage into its record, so a
// reading the meter annotated after a run ended — the current run or any before it — still
// lands (R49). A record's tokens never go down, since a stale read of a run must not lower
// them, so neither does the sum; a deleted claim leaves its record's last reading in place.
// Nor does its phase go back to Pending: the composition reports a started run whose Sandbox
// is not Ready as Pending (its harness exited and the sidecars drain, or a probe fails), and
// the record is what remembers it was admitted.
func (r *Reconciler) observe(ctx context.Context, t *v1alpha1.Task) (runs.Run, bool, error) {
	if len(t.Status.Runs) == 0 {
		return runs.Run{}, false, nil
	}
	all, err := r.Runs.List(ctx)
	if err != nil {
		return runs.Run{}, false, err
	}
	x, found := absorb(t, all)
	r.price(ctx, t)
	return x, found, nil
}

// absorb is observe on claims already listed: each record takes its claim's usage, and the
// current run's claim is returned. A stop calls it before deleting the claims (#55).
func absorb(t *v1alpha1.Task, all []runs.Run) (runs.Run, bool) {
	claims := make(map[string]runs.Run, len(all))
	for _, x := range all {
		claims[x.ID] = x
	}
	cur := current(t)
	var sum int64
	for i := range t.Status.Runs {
		rec := &t.Status.Runs[i]
		if x, ok := claims[rec.ID]; ok {
			rec.Tokens = max(rec.Tokens, x.Tokens)
			stamp(&rec.Provenance, x)
			if rec.ID == cur.ID && (!pending(x.Phase) || pending(rec.Phase)) {
				rec.Phase = x.Phase
			}
		}
		sum += rec.Tokens
	}
	t.Status.Usage.Tokens = sum
	x, found := claims[cur.ID]
	return x, found
}

// pending is a claim that exists but never started: not admitted, no phase written yet.
func pending(phase string) bool { return phase == "" || phase == "Pending" }

// boundPending ends a run that never started (P): nothing else bounds Pending —
// activeDeadlineSeconds counts from the pod's start, and Kueue queues unadmitted work forever.
// A run still Pending past caps.maxPendingMinutes whose record never saw it start (observe) and
// whose claim carries no usage never ran, so deleting it loses no usage; the task escalates as
// run_unschedulable, and a maintainer's /factory retry starts a fresh run.
func (r *Reconciler) boundPending(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, run runs.Run) error {
	if !pending(current(t).Phase) || run.Tokens > 0 || run.Created.IsZero() ||
		r.Now().Sub(run.Created) < time.Duration(cfg.Caps.MaxPendingMinutes)*time.Minute {
		return nil
	}
	if err := r.deleteRun(ctx, run.ID); err != nil {
		return err
	}
	if overlapScoperRun(t) {
		return r.finishOverlap(ctx, cfg, t, nil, "overlap scoper claim was never scheduled")
	}
	return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, "run_unschedulable")
}

// deleteRun deletes a claim and releases its reservation (#55): a claim deleted while live
// never reaches the terminal phase the meter releases on. The release is best effort: end
// releases again by run id, and the meter's sweep is the floor under both.
func (r *Reconciler) deleteRun(ctx context.Context, id string) error {
	if err := r.Runs.Delete(ctx, id); err != nil {
		return err
	}
	r.release(ctx, id)
	return nil
}

// release drops ids' reservations from today's ledger, the only one admission reads. A failure
// is logged, never returned: an ending task must not wait on the ledger, and the meter releases
// an unclaimed reservation after its grace.
func (r *Reconciler) release(ctx context.Context, ids ...string) {
	if r.Ledger == nil || len(ids) == 0 {
		return
	}
	if err := ledger.Release(ctx, r.Ledger, r.Namespace, r.Now(), ids...); err != nil {
		r.log().Warn("reservation not released; the meter will", "run.ids", ids, "err", err)
	}
}

// releaseUnheld releases every reservation of the task that no claim holds (#55): its recorded
// runs whose claims are gone or being deleted, and the next run's id, which Queued may have
// reserved without its claim ever being created. By run id, so a replay of a stop whose claims
// are already gone still releases them. An ended claim's is the meter's, as before.
func (r *Reconciler) releaseUnheld(ctx context.Context, t *v1alpha1.Task) {
	if r.Ledger == nil {
		return
	}
	all, err := r.Runs.List(ctx)
	if err != nil {
		r.log().Warn("reservations not released; the meter will", "task.id", t.Name, "err", err)
		return
	}
	held := map[string]bool{}
	for _, x := range all {
		if !x.Deleting {
			held[x.ID] = true
		}
	}
	var ids []string
	for _, id := range append(recordIDs(t), runID(t.Name, len(t.Status.Runs))) {
		if !held[id] {
			ids = append(ids, id)
		}
	}
	r.release(ctx, ids...)
}

func recordIDs(t *v1alpha1.Task) []string {
	ids := make([]string, 0, len(t.Status.Runs)+1)
	for _, rec := range t.Status.Runs {
		ids = append(ids, rec.ID)
	}
	return ids
}

// roomReason is the broker's end reason for the task's current run, if the room has it.
func (r *Reconciler) roomReason(ctx context.Context, t *v1alpha1.Task) (string, bool) {
	cur := current(t)
	evs, _, err := r.roomTail(ctx, t.Status.RoomRef, cur.StartSeq, func(e envelope.Event) bool {
		return e.Type == envelope.StateChanged && e.RunID == cur.ID
	})
	if err != nil {
		r.log().Warn("room log unreadable", "task.id", t.Name, "run.id", cur.ID, "err", err)
		return "", false
	}
	end, ok := rooms.LastRunEnd(evs, cur.ID)
	return end.Reason, ok
}

// finished returns the run's end reason once the room has recorded it, or after runEndGrace
// without it ("" while waiting).
func (r *Reconciler) finished(ctx context.Context, t *v1alpha1.Task, run runs.Run) string {
	cur := current(t)
	if cur.Finished == nil {
		now := metav1.NewTime(r.Now())
		cur.Finished = &now
		// What the run could read up to its end (UH7): comments posted while it ran count too.
		r.recordInputTrust(ctx, r.Cfg.Current(), t, cur.Trigger)
	}
	if reason, ok := r.roomReason(ctx, t); ok {
		return reason
	}
	if r.Now().Sub(cur.Finished.Time) < runEndGrace {
		return ""
	}
	if run.Revoked != "" {
		return run.Revoked
	}
	switch run.Reason {
	case runs.ReasonDisrupted, runs.ReasonPodLost, runs.ReasonPodFailed:
		return run.Reason // the composition read the pod (disruption design §3)
	}
	return strings.ToLower(run.Phase)
}

func (r *Reconciler) implementing(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	run, found, err := r.observe(ctx, t)
	if err != nil {
		return err
	}
	if !found {
		if overlapScoperRun(t) {
			return r.finishOverlap(ctx, cfg, t, nil, "overlap scoper claim was missing")
		}
		why := r.lostReason(ctx, t)
		if scoperRun(t) && why == "run_lost" { // SC11, #451: a scoper whose claim vanished is a failed scoper
			return r.scoperFailed(ctx, cfg, t, why)
		}
		return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, why)
	}
	if err := r.boundPending(ctx, cfg, t, run); err != nil {
		return err
	}
	if t.Status.PullRequest == nil {
		if err := r.detectPR(ctx, t, run); err != nil {
			return err
		}
	}
	if !runs.Terminal(run.Phase) {
		if s, err := r.stuck(ctx, t, run); err != nil || s {
			if err != nil {
				return err
			}
			if overlapScoperRun(t) {
				return r.finishOverlap(ctx, cfg, t, nil, "overlap scoper became stuck")
			}
			if scoperRun(t) { // SC11, #451: a stuck scoper is a failed scoper
				return r.scoperFailed(ctx, cfg, t, "stuck")
			}
			return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, "stuck")
		}
		return nil
	}
	reason := r.finished(ctx, t, run)
	if reason == "" {
		return nil
	}
	if run.Role == "implementer" {
		r.shadowPending(ctx, cfg, t) // H1: the run's writes, before its outcome (audit only)
	}
	if reason == "pod_lost" {
		switch run.Reason {
		case runs.ReasonDisrupted, runs.ReasonPodLost, runs.ReasonPodFailed:
			reason = run.Reason // the broker's pod_lost reads the same for a drain, a deletion and a crash; the AgentRun read the pod
		}
	}
	current(t).Reason = reason
	if envelope.ModelUnavailable(reason) { // #450: the provider's refusal holds the task, whatever the role
		return r.holdForModel(ctx, cfg, t, reason)
	}
	clearModelHold(t)
	r.interventions(ctx, cfg, t)
	if infraLost(run) {
		done, err := r.handedOff(ctx, t)
		if err != nil {
			return err
		}
		switch why := r.resumeBlock(cfg, t); {
		case done:
			run.Phase = "Succeeded" // it finished before its sandbox was lost: nothing to resume
		case why == "":
			r.resume(ctx, cfg, t, run)
			return nil
		default:
			reason = why
		}
	}
	if overlapScoperRun(t) {
		return r.overlapScoperEnded(ctx, cfg, t)
	}
	if testerRun(t) {
		return r.testerEnded(ctx, cfg, t, reason)
	}
	if t.Spec.Loop != nil && current(t).Role == "implementer" {
		end := "other"
		switch {
		case envelope.LoopEnd(reason):
			end = reason
		case run.Phase == "Succeeded":
			end = "finished"
		}
		record(ctx, func(ctx context.Context) { r.Metrics.LoopRun(ctx, t.Spec.PredictedClass, end) })
	}
	if scoperRun(t) {
		return r.scoped(ctx, cfg, t, run.Phase, reason)
	}
	// R38: the triager's output reaches a public implementer only through a human. Its summary
	// stays in the room (internal); the issue gets the room link and the next step, nothing else.
	if current(t).Role == "triager" {
		if run.Phase != "Succeeded" {
			return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, reason)
		}
		evs, _, err := r.Rooms.EventsSince(ctx, t.Status.RoomRef, current(t).StartSeq, handoffTo("implementer"))
		if err != nil {
			return err
		}
		if len(evs) == 0 {
			return r.end(ctx, cfg, t, v1alpha1.PhaseNoOp, "no_action")
		}
		narrateLater(t, narrate.ProposalReady(t, cfg.RoomsURL, cfg.PublishStarts(t.Spec.Source.Kind, t.Spec.Source.RequestedBy)))
		return r.end(ctx, cfg, t, v1alpha1.PhaseDone, "proposal_ready")
	}
	// F3-R7: an implementer that holds a contract and hands off to the tester disputes it. No dispute
	// round (TF6): a maintainer decides, whatever the run's phase.
	if t.Status.Contract != nil && current(t).Role == "implementer" {
		evs, _, err := r.roomTail(ctx, t.Status.RoomRef, current(t).StartSeq, func(e envelope.Event) bool {
			return handoffTo("tester")(e) && e.RunID == current(t).ID
		})
		if err != nil {
			return err
		}
		if len(evs) > 0 {
			return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, "contract_disputed")
		}
	}
	switch {
	case run.Phase == "Succeeded" && t.Status.PullRequest != nil:
		return r.afterWriter(ctx, cfg, t)
	case run.Phase == "Succeeded":
		why, err := r.noPRReason(ctx, t)
		if err != nil {
			return err
		}
		if why == "no_pr" {
			return r.end(ctx, cfg, t, v1alpha1.PhaseNoOp, why)
		}
		return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, why) // #476: committed work is never a no-op
	default:
		return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, reason)
	}
}

// Reasons of an implementer run that committed and opened no pull request (#476).
const (
	reasonCommittedNoPR = "committed_no_pr" // the commit never left the sandbox: the work is lost
	reasonPushedNoPR    = "pushed_no_pr"    // the branch is on the remote: a maintainer can open the pull request
)

// committedNotes are the harness's room notes that say the run committed or pushed.
var committedNotes = []string{"Harness (implementer): the agent committed.", "Harness (implementer): the agent pushed its branch."}

// noPRReason is why a run that finished without a pull request ended: no_pr when the agent
// changed nothing (a no-op), pushed_no_pr when its branch reached the remote, committed_no_pr when
// it committed and the commit exists nowhere else. The harness never opens the pull request or
// pushes on a commit: the agent does both, so a note of a commit with no push is a lost commit.
func (r *Reconciler) noPRReason(ctx context.Context, t *v1alpha1.Task) (string, error) {
	cur := current(t)
	pushed, err := r.branchPushed(ctx, "agent/"+t.Name)
	if err != nil {
		return "", err
	}
	if pushed {
		return reasonPushedNoPR, nil
	}
	evs, _, err := r.roomTail(ctx, t.Status.RoomRef, cur.StartSeq, func(e envelope.Event) bool {
		return e.Type == envelope.Message && e.RunID == cur.ID
	})
	if err != nil {
		return "", err
	}
	for _, e := range evs {
		var p envelope.MessagePayload
		if json.Unmarshal(e.Payload, &p) == nil && p.Kind == envelope.KindProgress && slices.Contains(committedNotes, p.Text) {
			return reasonCommittedNoPR, nil
		}
	}
	return "no_pr", nil
}

// branchPushed reports whether the branch exists on the remote: its newest recorded push is not a deletion.
func (r *Reconciler) branchPushed(ctx context.Context, branch string) (bool, error) {
	ps, err := r.gh(ctx).BranchActivity(ctx, branch)
	if err != nil && !errors.Is(err, forge.ErrActivityTruncated) {
		return false, err
	}
	for i := len(ps) - 1; i >= 0; i-- {
		switch ps[i].Type {
		case "push", "force_push", "branch_creation":
			return true, nil
		case "branch_deletion":
			return false, nil
		}
	}
	return false, nil
}

// handoffTo selects the handoffs to role: the triager's proposal exists only if it made one (R38).
func handoffTo(role string) func(envelope.Event) bool {
	return func(e envelope.Event) bool {
		var p envelope.HandoffPayload
		return e.Type == envelope.Handoff && json.Unmarshal(e.Payload, &p) == nil && p.ToRole == role
	}
}

// lostReason is why a run of the task vanished: SP2 records a deleted claim as Revoked, reason
// deleted (its P15); say so when it has.
func (r *Reconciler) lostReason(ctx context.Context, t *v1alpha1.Task) string {
	if why, ok := r.roomReason(ctx, t); ok && why == "deleted" {
		return why
	}
	return "run_lost"
}

// infraLost: the AgentRun says its pod was disrupted or lost (disruption design §3). The room's
// pod_lost is no trigger: it reads the same for a harness that crashed, and a crashing agent must
// never be resumed in a loop.
// A revoked run is never resumed: someone stopped it.
func infraLost(run runs.Run) bool {
	return run.Phase == "Failed" && run.Revoked == "" && (run.Reason == runs.ReasonDisrupted || run.Reason == runs.ReasonPodLost)
}

// resumeBlock is why a run lost to its infrastructure is not resumed, "" when it is: no automatic
// resume is left (resume.maxPerTask), or the task's token cap cannot hold one more run (runFits). The
// cap is enforced here whatever budgets.enforceTask says: a resume is the factory's own decision,
// so it never spends past the cap, even while the cap is shadow elsewhere (disruption design §4).
// The usage it reads can trail the meter by one poll.meter, so a resume can overshoot the cap by
// what the lost run spent in its last poll, and no more.
func (r *Reconciler) resumeBlock(cfg *config.Config, t *v1alpha1.Task) string {
	if int(t.Status.Resumes) >= cfg.Resume.MaxPerTask {
		return "resumes_exhausted"
	}
	if !runFitsFor(t, current(t).Role) {
		return "resume_budget"
	}
	return ""
}

// resumable: the run was lost to its infrastructure and resumeBlock lets it resume.
func (r *Reconciler) resumable(cfg *config.Config, t *v1alpha1.Task, run runs.Run) bool {
	return infraLost(run) && r.resumeBlock(cfg, t) == ""
}

// handedOff: the room holds a handoff of the task's current run, so its agent finished its work
// before the sandbox was lost: a resume would run finished work again.
func (r *Reconciler) handedOff(ctx context.Context, t *v1alpha1.Task) (bool, error) {
	cur := current(t)
	evs, _, err := r.roomTail(ctx, t.Status.RoomRef, cur.StartSeq, func(e envelope.Event) bool {
		return e.Type == envelope.Handoff && e.RunID == cur.ID
	})
	return len(evs) > 0, err
}

// resumeBrief is a resumed run's brief: ResumeNotice before the lost run's own, read back from its
// claim. A brief built now would miss what the lost one carried: the queued messages it quoted are
// consumed, and the room after its start holds no verdict it revises. ok is false for a run that
// resumes nothing, and once the lost run's claim is gone. A scoper's marker line stays first, the
// notice after it (HR2): the harness reads it at offset 0 only.
func (r *Reconciler) resumeBrief(ctx context.Context, t *v1alpha1.Task) (string, bool, error) {
	if nextTrigger(t) != "resume" || len(t.Status.Runs) == 0 {
		return "", false, nil
	}
	x, found, err := r.Runs.Get(ctx, current(t).ID)
	if err != nil || !found || x.TaskText == "" {
		return "", false, err
	}
	marker, text := "", x.TaskText
	if (scoperRun(t) || overlapScoperRun(t)) && strings.HasPrefix(text, runs.ScoperMarkerPrefix) {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			marker, text = text[:i+1], text[i+1:]
		}
	}
	return marker + ResumeNotice(t) + strings.TrimPrefix(text, ResumeNotice(t)), true, nil
}

// resume sends the task back to Queued for a new run of the lost run's role, on the same branch
// and in the same room (disruption design §4). The new run is a new AgentRun with the task's next
// deterministic id (R48), so a replay after a lost status write never starts two; the lost one
// stays Failed. A reviewer's re-run spends no review round.
func (r *Reconciler) resume(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, run runs.Run) {
	t.Status.Resumes++
	n, why := int(t.Status.Resumes), run.Reason
	record(ctx, func(ctx context.Context) { r.Metrics.Resumed(ctx, why) })
	if run.Role == "reviewer" {
		r.requestVerifier(t, run.Role) // no ReviewRounds++: the loss was the platform's, not the review's
	} else {
		t.Status.NextTrigger = "resume"
		r.to(t, v1alpha1.PhaseQueued, "")
	}
	narrateLater(t, narrate.Resuming(t, run.ID, run.Role, n, cfg.Resume.MaxPerTask))
}

// cause is why the current implementer run's work exists: a resumed run carries on the run it
// replaces, so it takes that run's trigger, and a revision a maintainer asked for still goes back
// to the maintainer.
func cause(t *v1alpha1.Task) string {
	for i := len(t.Status.Runs) - 1; i >= 0; i-- {
		if tr := t.Status.Runs[i].Trigger; tr != "resume" {
			return tr
		}
	}
	return "initial"
}

// afterWriter: a revision a maintainer asked for goes straight back to the maintainer; otherwise
// the template's first verifier after the implementer runs, or the task is ready (solo). A CI fix
// is verified like any revision: it moved the head, and the gate only arms on a verdict of the
// head it decides (R03), so skipping the review would hand it off as verdict_stale (N1).
func (r *Reconciler) afterWriter(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	if cause(t) == "human" {
		pr, err := r.readPR(ctx, t)
		if err != nil {
			return err
		}
		rv := r.revision(ctx, t, pr)
		reason := ""
		if len(rv.Dropped) > 0 { // never "ready" silently: the maintainer confirms the scope change
			reason = "scope_changed"
		}
		r.to(t, v1alpha1.PhaseAwaitingHuman, reason)
		narrateOn(t, reviewTarget(t), narrate.Revised(t, rv, cfg.RoomsURL))
		return nil
	}
	if cause(t) == "ci" { // a push after the fix's own is a human's: never reviewed into a merge
		pr, err := r.readPR(ctx, t)
		if err != nil {
			return err
		}
		if handed, err := r.humanCommit(ctx, cfg, t, pr.HeadSHA); err != nil || handed {
			return err
		}
	}
	if next := r.nextVerifier(cfg, t, "implementer"); next != "" {
		r.requestVerifier(t, next)
		return nil
	}
	return r.ready(ctx, t)
}

func (r *Reconciler) detectPR(ctx context.Context, t *v1alpha1.Task, run runs.Run) error {
	n, err := r.gh(ctx).PullRequestForBranch(ctx, "agent/"+t.Name)
	if err != nil || n == 0 {
		return err
	}
	pr, err := r.gh(ctx).PullRequest(ctx, n)
	if err != nil {
		return err
	}
	t.Status.PullRequest = &v1alpha1.PullRequestRef{Number: pr.Number, URL: pr.URL, NodeID: pr.NodeID, HeadSHA: pr.HeadSHA}
	notePR(t.Status.PullRequest, pr)
	if err := r.Runs.Annotate(ctx, run.ID, map[string]string{runs.AnnPullRequest: pr.URL}); err != nil {
		return err
	}
	if err := r.gh(ctx).AddLabels(ctx, pr.Number, "factory/class:"+t.Spec.PredictedClass); err != nil {
		return err
	}
	d, source, tier, tmpl := r.Now().Sub(t.CreationTimestamp.Time), t.Spec.Source.Kind, t.Spec.Budget.Tier, t.Spec.Template
	record(ctx, func(ctx context.Context) {
		r.Metrics.TimeToPR(ctx, d, source, tier, tmpl)
		r.Metrics.PROpened(ctx)
	})
	narrateLater(t, narrate.PROpened(t, pr.Number, pr.URL, run.ID))
	return nil
}

func (r *Reconciler) awaitingHuman(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	pr, err := r.readPR(ctx, t)
	if err != nil {
		return err
	}
	if ended, err := r.prEnded(ctx, cfg, t, pr); ended || err != nil {
		return err
	}
	r.countApproves(ctx, cfg, t, pr)
	if adoptedTask(t) {
		return r.awaitingHumanAdopted(ctx, cfg, t, pr)
	}
	if rvs := r.changesRequested(cfg, t, pr); len(rvs) > 0 {
		return r.revise(ctx, cfg, t, pr, rvs)
	}
	// A return the factory cannot read is no return: it never blocks the reminder and stale close.
	if back, err := r.reenter(ctx, cfg, t, pr); err != nil {
		r.log().Warn("merge gate return unreadable", "task.id", t.Name, "reason", t.Status.Reason, "err", err)
	} else if back {
		return nil
	}
	r.showApproval(cfg, t, pr)
	return r.remind(ctx, cfg, t, pr)
}

// showApproval shows a maintainer's approval of an agents pull request with merge.approveToMerge off,
// where an approval returns nothing to the gate (AM2) and so would go unsaid: status.pullRequest
// .approval names who approved which commit, and whether it is stale, for the room's facts, and the
// approval is narrated once per review (its key). It never changes the reason: reenter's returns and
// every by-hand reason's guidance read it. A dismissed or vetoed approval is shown no longer. It
// reads the pull request awaitingHuman already holds: no GitHub call of its own. With approveToMerge
// on, or an adopted class, an approval is the gate's to read (approvalReturn, reenter).
func (r *Reconciler) showApproval(cfg *config.Config, t *v1alpha1.Task, pr forge.PR) {
	if r.repo(cfg, t).Merge.ApproveToMerge || gateOf(r.repo(cfg, t), t.Spec.PredictedClass).Adopted() {
		return
	}
	ref := t.Status.PullRequest
	rv, st := shownApproval(pr, r.repo(cfg, t), runsEnded(t))
	if st != "approved" && st != envelope.ApprovalOlderCommit && st != envelope.ApprovalBeforeRunsEnded {
		ref.Approval = nil
		return
	}
	first := ref.Approval == nil
	stale := st // the cause of a stale approval; a current one has none
	if st == "approved" {
		stale = ""
	}
	ref.Approval = &v1alpha1.ApprovalRef{By: rv.Author, Commit: rv.Commit, Stale: stale}
	ev := narrate.ApprovalShown(t, rv.ID, rv.Author, rv.Commit, stale)
	if first && st == "approved" && t.Status.Reason == "maintainer_approved" {
		// The gate's own hand-off already said a maintainer approved: record the key, post nothing.
		if !slices.Contains(t.Status.Narrated, ev.Key) {
			narrate.Abandon(t, ev.Key)
		}
		return
	}
	narrateLater(t, ev)
}

// countApproves counts each maintainer's APPROVED review once (§7: how dark the factory is):
// a gate that still needed a human's approve is not dark. The review id rides status.handled,
// the same list that dedups request-changes reviews and commands.
func (r *Reconciler) countApproves(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, pr forge.PR) {
	for _, rv := range pr.Reviews {
		if rv.State == "APPROVED" && r.repo(cfg, t).IsMaintainer(rv.Author) && !slices.Contains(t.Status.Handled, rv.ID) {
			markHandled(t, rv.ID)
			r.intervention(ctx, cfg, t, "approve")
		}
	}
}

// prEnded ends the task when its pull request was merged or closed. A closed one carrying
// factory/stale was the stale close's (remind): its replay after a lost status write still says so.
func (r *Reconciler) prEnded(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, pr forge.PR) (bool, error) {
	class := t.Spec.PredictedClass
	switch pr.State {
	case "MERGED":
		if err := r.mergedHumanCommits(ctx, cfg, t, pr); err != nil {
			return false, err
		}
		t.Status.PullRequest.MergedBy, t.Status.PullRequest.MergeCommitSHA = pr.MergedBy, pr.MergeCommitSHA
		noteMergedHead(t, pr)
		// R41: like the CI gate's bypass record, the timestamp is what puts the merge in the
		// class's breaker window — without it a demoted class' human merges never refill it.
		now := r.mergedAt(pr)
		t.Status.PullRequest.MergedAt = &now
		record(ctx, func(ctx context.Context) { r.Metrics.PROutcome(ctx, class, "human_merged") })
		r.handedOffMerged(ctx, t, "human")
		return true, r.end(ctx, cfg, t, v1alpha1.PhaseDone, "merged")
	case "CLOSED":
		reason := "pr_closed"
		if slices.Contains(pr.Labels, labelStale) {
			reason = "stale"
		}
		record(ctx, func(ctx context.Context) { r.Metrics.PROutcome(ctx, class, "closed") })
		return true, r.end(ctx, cfg, t, v1alpha1.PhaseClosed, reason)
	}
	return false, nil
}

// stamp fills what the run ran on from its claim (R10): each field once, never changed, so a later
// read of a claim a newer CompositionRevision re-bound still records the one the run started on.
// A claim without a factory version predates the field: its run stays empty, as the spec says. A
// value over its CRD bound is not recorded, since the API server would refuse every status write.
func stamp(p *v1alpha1.RunProvenance, x runs.Run) {
	fill := func(dst *string, v string, bound int) {
		if len(v) <= bound {
			*dst = cmp.Or(*dst, v)
		}
	}
	fill(&p.FactoryVersion, x.FactoryVersion, maxFactoryVersionLen)
	if x.FactoryVersion == "" {
		return
	}
	fill(&p.CompositionRevision, x.CompositionRevision, maxCompositionRevisionLen)
	fill(&p.HarnessImage, x.HarnessImage, maxHarnessImageLen)
}

// served adds the models the meter saw serve the run (R10): a set that only grows, sorted, and
// bounded by the CRD's maxItems and maxLength. A longer name is skipped, never truncated: a
// truncated name would be a guess, and an unbounded label would wedge the task's status writes.
func served(p *v1alpha1.RunProvenance, models []string) {
	for _, m := range models {
		if m != "" && len(m) <= maxServedModelLen && !slices.Contains(p.ServedModels, m) && len(p.ServedModels) < maxServedModels {
			p.ServedModels = append(p.ServedModels, m)
		}
	}
	slices.Sort(p.ServedModels)
}

// The CRD's bounds on RunProvenance (Interfaces table of the run-provenance spec).
const (
	maxFactoryVersionLen      = 64
	maxCompositionRevisionLen = 253
	maxHarnessImageLen        = 320
	maxServedModelLen         = 64
)

// maxServedModels is RunProvenance.ServedModels' maxItems.
const maxServedModels = 8
