// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/decision"
	"github.com/Smana/maku/internal/factory/fluxwatch"
	"github.com/Smana/maku/internal/factory/fmetrics"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/killswitch"
	"github.com/Smana/maku/internal/factory/ledger"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/meter"
	"github.com/Smana/maku/internal/factory/narrate"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/factory/tracing"
	"github.com/Smana/maku/internal/factory/triage"
	"github.com/Smana/maku/internal/runwatch"
)

// RunClient is the AgentRun API as the reconciler uses it (runs.Client).
type RunClient interface {
	Create(ctx context.Context, s runs.Spec) error
	Get(ctx context.Context, id string) (runs.Run, bool, error)
	List(ctx context.Context) ([]runs.Run, error)
	Annotate(ctx context.Context, id string, kv map[string]string) error
	Delete(ctx context.Context, id string) error
}

// RoomLog is the broker's system API as the reconciler uses it (rooms.Client): the room's log
// from afterSeq with its resume cursor, its current seq, the room's queue, task_state messages and
// the task's facts.
type RoomLog interface {
	EventsSince(ctx context.Context, room string, afterSeq int64, keep func(envelope.Event) bool) ([]envelope.Event, int64, error)
	LastSeq(ctx context.Context, room string) (int64, error)
	Enqueue(ctx context.Context, room, stream, text string, clientSeq int64) error
	Queue(ctx context.Context, room string) ([]rooms.Queued, error)
	Consume(ctx context.Context, room string, refs []int64, runID string) error
	TaskState(ctx context.Context, room, text string, clientSeq int64) error
	TaskFacts(ctx context.Context, room string, f envelope.TaskFacts, clientSeq int64) error
	// Output posts the factory's answer to, or shadow of, one of a run's outputs (hardening HR2).
	Output(ctx context.Context, room, runID string, kind envelope.MessageKind, rec envelope.OutputRecord) error
}

// TaskForge is the part of the forge the reconciler uses, as the factory App.
type TaskForge interface {
	RecentComments(ctx context.Context, number int) ([]forge.Comment, error)
	AllComments(ctx context.Context, number int) ([]forge.Comment, error)
	Comment(ctx context.Context, number int, body string) error
	CommentWithID(ctx context.Context, number int, body string) (int64, error)
	EditComment(ctx context.Context, id int64, body string) error
	AddLabels(ctx context.Context, number int, labels ...string) error
	PullRequestForBranch(ctx context.Context, branch string) (int, error)
	PullRequest(ctx context.Context, number int) (forge.PR, error)
	PullRequestCommits(ctx context.Context, number int) ([]forge.Commit, error)
	BranchActivity(ctx context.Context, branch string) ([]forge.Push, error)
	// Commit reads one commit by sha: the tester never opens a pull request (R14).
	Commit(ctx context.Context, sha string) (forge.Commit, error)
	ClosePR(ctx context.Context, number int) error
	RemoveLabel(ctx context.Context, number int, label string) error
	DefaultBranchFile(ctx context.Context, path string) (text, sha string, found bool, err error)
	// FileAtCommit reads a producer's landed artifact at its exact merge commit (v4 MR7).
	FileAtCommit(ctx context.Context, sha, path string) (forge.FileContent, bool, error)
	Issue(ctx context.Context, number int) (forge.Issue, error)
	// LabelEvents verifies who added factory/drill to a revert PR (AM6), as intake verifies factory/revert.
	LabelEvents(ctx context.Context, number int, label string) ([]forge.LabelEvent, error)
	// LabelEventsBy and IssuesByAuthorSince read back the labels and issues an agent run wrote
	// (hardening HR1's shadow pass).
	LabelEventsBy(ctx context.Context, number int, actor string, since time.Time) ([]forge.LabelEvent, error)
	IssuesByAuthorSince(ctx context.Context, login string, since time.Time) ([]forge.Issue, error)
	CreateIssue(ctx context.Context, title, body string, labels []string) (forge.Issue, error)
	IssuesLabelled(ctx context.Context, label string) ([]forge.Issue, error)
	IssuesLabelledSince(ctx context.Context, label string, since time.Time) ([]forge.Issue, error)
	AddSubIssue(ctx context.Context, parent int, childID int64) error
	CloseIssue(ctx context.Context, number int) error
	// Public reports whether the repository is public: demo mode serves public repositories only.
	Public(ctx context.Context) (bool, error)
}

// metrics is the part of fmetrics.Set the reconciler records.
type metrics interface {
	TimeToPR(ctx context.Context, d time.Duration, source, tier, template string)
	PROpened(ctx context.Context)
	PROutcome(ctx context.Context, class, outcome string)
	MonitorVerdict(ctx context.Context, class, verdict string)
	MonitorVerification(ctx context.Context, class, verdict string)
	HumanWait(ctx context.Context, phase, reason, class string, d time.Duration)
	HandoffToMerge(ctx context.Context, class, reason, how string, d time.Duration)
	TaskTokens(ctx context.Context, tokens int64, tier, template, predictedClass string)
	Intervention(ctx context.Context, kind, cohort, profile, recommended string)
	TaskOutcome(ctx context.Context, class, cohort, profile, source, recommended, outcome string, usd float64)
	TierFit(ctx context.Context, classifier, tier, fit string, control bool)
	Revoked(ctx context.Context, reason string)
	AdmissionRefused(ctx context.Context, reason string)
	PrincipalShadow(ctx context.Context, principal string)
	RunRevoked(ctx context.Context, reason, limit string)
	RunBudgetFallback(ctx context.Context, scope, why string)
	TraceExportAbandoned(ctx context.Context)
	ClassMismatch(ctx context.Context, predicted, matched string)
	MergeDecision(ctx context.Context, class, verdict, reason string)
	PolicyDualDecision(ctx context.Context, repository, class, agreement string)
	ApprovalGrantsActive(ctx context.Context, repository, class string, n int)
	ApprovalGrantEvent(ctx context.Context, repository, class, event string)
	PolicyLoadError(ctx context.Context, repository, reason string)
	ClassPaused(ctx context.Context, repository, class string, paused bool)
	Resumed(ctx context.Context, reason string)
	ModelRefused(ctx context.Context, cause, role string)
	LoopRun(ctx context.Context, class, end string)
	ContractEnd(ctx context.Context, class, end string)
	RoomClosedAtCap(ctx context.Context, reason string)
	SteeringRead(ctx context.Context, class, read string)
	ScoperRun(ctx context.Context, class, outcome string)
	ProfileRecommendation(ctx context.Context, class, cohort, recommended string, applied bool)
	ScopeBounce(ctx context.Context, class, exit string)
	Split(ctx context.Context, class, outcome string)
	SplitChild(ctx context.Context, class, outcome string)
	// RevisionDeferred counts a first run that yielded its slot-time to a revision (CD22).
	RevisionDeferred(ctx context.Context)
	// TaskSpendFinal records how long a terminal task's dollars took to settle, from its last
	// run's end to the pass that posted them (CD23).
	TaskSpendFinal(ctx context.Context, d time.Duration)
	// The coordination series (v4 observability), each on the repository it is about.
	CoordinationFamily(ctx context.Context, repository, outcome string)
	CoordinationAdmissionRefused(ctx context.Context, repository, reason string)
	OverlapEvaluation(ctx context.Context, classification, source string)
	OverlapScoperRun(ctx context.Context, outcome string)
	OverlapEvaluationDuration(ctx context.Context, duration time.Duration)
	CoordinationChild(ctx context.Context, repository, class, outcome string)
	CoordinationIntegrationRun(ctx context.Context, repository, purpose, result string)
	CoordinationWait(ctx context.Context, repository, reason string, d time.Duration)
}

// A task's span unexported at its end is retried every spanRetry, and given up spanGiveUp after
// the end (ruling ST2): each try costs the single worker up to tracing.ExportTimeout, so a
// collector outage must not grow an unbounded backlog of them.
const (
	spanRetry  = 15 * time.Minute
	spanGiveUp = 24 * time.Hour
)

// A pull request no maintainer has touched for RemindAfter gets a reminder, and is closed with
// factory/stale after StaleAfter (§6.3), never sooner than reminderNotice after that reminder.
const (
	RemindAfter    = 48 * time.Hour
	StaleAfter     = 14 * 24 * time.Hour
	reminderNotice = 24 * time.Hour
)

// escalatedPoll is how often an escalated task is reconciled. It waits only for a maintainer's
// /factory retry, the end of its pull request, or caps.escalatedHours (#61), and each poll costs
// GitHub calls the whole factory shares.
const escalatedPoll = 5 * time.Minute

// Reconciler is the Task state machine (§4). One reconcile per task every poll interval and on
// every change of one of its AgentRuns; one worker, so the caps are counted without a race
// between two tasks starting at once. Run ids are derived from the task (R48), never random.
type Reconciler struct {
	Client client.Client
	// Ledger reads and writes R50's day ledgers live, as the API and the meter do: the cached
	// client watches one ConfigMap, the stop object. Queued reserves its runs there (#78).
	Ledger    client.Client
	Namespace string
	Cfg       config.Source
	Forge     TaskForge
	Merger    forge.Merger // the merger App: checks, merges and reverts (R16)
	// Flux reads Flux's objects on the factory's cluster for the post-merge monitor (MC7). nil:
	// the Flux signal reads inconclusive.
	Flux interface {
		Source(ctx context.Context, ns, name string) (fluxwatch.Source, error)
		NotReadySince(ctx context.Context, ns, name string, since, until time.Time) ([]string, error)
	}
	// Clients is v2 D8: a repository's two connections, the factory App's and the merger's, each
	// bound to that repository. Set, every reconcile binds its task's repository's pair, and a
	// task on a repository it does not know is refused before any call; nil, Forge and Merger
	// serve every task (one repository).
	Clients func(repository string) (TaskForge, forge.Merger, bool)
	// Owned is v2 D7's fence: the merge gate hands a repository this factory does not own to a
	// human. nil owns nothing.
	Owned func(repository string) bool
	// Policy is a repository's merge policy as the fence last read it (mergepolicy.Watch), which
	// policyManagement.dualEvaluation decides each merge decision under a second time, deciding
	// nothing on it (stage 3). nil: no dual evaluation.
	Policy func(repository string) (mergepolicy.Snapshot, bool)
	// Grants is the broker's saved approval grants, which policyManagement.approvalMemory consults
	// where a live class's requiresApproval subset would ask a human (stage 5). nil: none is read.
	Grants GrantClient
	// Authority is a cut-over repository's policy as the gate decides on it (merge policy stage
	// 4, mergepolicy.Watch.Authority): read once per pass, never cached here. nil: a cut-over
	// repository's policy is unreadable, and its auto-merge pauses.
	Authority func(repository string) (mergepolicy.Snapshot, error)
	Runs      RunClient
	// Overlap retrieves outcome-overlap candidates; nil reads the factory's own sources.
	Overlap overlapEvaluator
	// Kind is the runs kind, required: SetupWithManager watches its AgentRuns.
	Kind    runwatch.Kind
	Rooms   RoomLog
	Triage  triage.Triager
	Metrics metrics
	Now     func() time.Time
	// Decisions remembers each class's latest merge decision for the resources' status; nil: off.
	Decisions *decision.Recorder
	Nonce     func() string
	Log       *slog.Logger
	Trace     tracing.Sink // nil: tracing off (R46)
	// Costs is the run meter's per-run USD costs, which a task capped in USD sums (OD-6 (b));
	// nil: every task is held to TaskTokens.
	Costs costReader
	// CanWrite is v2 D4's GitHub check, as the label intake's: a /factory retry needs its
	// maintainer to hold write or higher on the task's repository. nil: nobody writes.
	CanWrite func(ctx context.Context, repository, login string) (bool, error)
	// Coordination is the multi-repository coordination service (v4 MR2): a parent files through
	// it and a child's authority is re-read through it. nil files nothing and holds every child.
	Coordination coordinationPort
	// Hardening is the untrusted-input hardening track's ports (H1): with a repository in audit,
	// a writer run's brief gets a canary and the task records its input trust. nil is off.
	Hardening *HardeningPorts

	oldBroker sync.Once // the broker serves no task route: said once per process
	notServed onceSet   // tasks whose repository is not served, said once each
	ledger    mergeLedger
	mon       monitorCache
}

// SetupWithManager watches Tasks and the AgentRuns that carry a task label.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.Kind.Validate(); err != nil {
		return fmt.Errorf("reconciler: the runs kind: %w", err)
	}
	run := &unstructured.Unstructured{}
	run.SetGroupVersionKind(r.Kind.GVK())
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.Task{}).
		Watches(run, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			id := o.GetLabels()[runs.LabelTask]
			if id == "" {
				return nil
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: r.Namespace, Name: id}}}
		})).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}

// Reconcile moves one task one step and writes its status when the step changed it.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cfg := r.Cfg.Current()
	var t v1alpha1.Task
	if err := r.Client.Get(ctx, req.NamespacedName, &t); err != nil {
		if apierrors.IsNotFound(err) {
			r.notServed.forget(req.Name) // deleted while its repository was unserved
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if _, migrating := t.Annotations[roomv1.AnnotationMigrating]; migrating {
		return ctrl.Result{}, nil // a migrate-group copy without its status yet: lifting the guard requeues it
	}
	// Factory resources D12: a publish may remove the task's repository. Nothing about it is
	// decided under another repository's settings, and it is no error: a later publish may serve it.
	if r.repo(cfg, &t) == nil {
		if r.notServed.once(t.Name) {
			r.log().Info("repository not served: the task waits for a publish that serves it", "task", t.Name, "repository", t.Spec.Repository)
		}
		return ctrl.Result{RequeueAfter: notServedRequeue}, nil
	}
	r.notServed.forget(t.Name)
	// v2 D10 and spend per identity SI4: the pass's series are the task's repository's and its payer's.
	ctx, err := r.bind(fmetrics.WithPrincipal(fmetrics.WithRepository(ctx, t.Spec.Repository), r.payer(cfg, &t)), &t)
	if err != nil {
		return ctrl.Result{}, err
	}
	ended := v1alpha1.TerminalPhase(t.Status.Phase)
	// A requested revert, an open revert PR and a merge under watch are the three exceptions to
	// leaving a terminal task alone (§6.4, MC7): the factory owes the revert its watch, a
	// maintainer's factory/revert its action, and a monitored merge its verdict, even after the
	// task itself has ended.
	watchWait := r.revertable(cfg, &t) || r.revertPending(&t) || r.monitorDue(cfg, &t)
	var closeDue bool
	var closeWait time.Duration
	if ended {
		var rerr error
		if closeDue, _, closeWait, rerr = r.roomState(ctx, cfg, &t); rerr != nil {
			return ctrl.Result{}, rerr
		}
	}
	if ended && !watchWait && len(t.Status.Outbox) == 0 && !r.spanDue(&t) && !r.settling(&t) && !r.factsDue(cfg, &t) && !r.spendFinalDue(cfg, &t) && !r.statusCommentDue(cfg, &t) && !closeDue {
		left := closeWait
		// CD23's one timed requeue: the instant a terminal task's dollars are final, never a poll.
		// Only an instant still ahead is scheduled — past it the pass below owes the fact, and
		// scheduling it again would wake the task every second for a figure already posted.
		if at, ok := r.spendFinalAt(cfg, &t); ok && r.Now().Before(at) {
			left = soonest(left, max(at.Sub(r.Now()), time.Second))
		}
		return ctrl.Result{RequeueAfter: left}, nil // closeWait: the revert window still open (#56)
	}
	before := t.Status.DeepCopy()
	if t.Status.Phase == "" && t.Spec.Kind == "" { // a parent's and an integration Task's path never starts at Received
		r.to(&t, v1alpha1.PhaseReceived, "")
	}
	fx := &effects{}
	switch {
	case !ended, watchWait:
		err = r.step(context.WithValue(ctx, effectsKey{}, fx), cfg, &t)
	case r.settling(&t):
		err = r.settle(context.WithValue(ctx, effectsKey{}, fx), cfg, &t)
	}
	if err == nil && ended && watchWait && r.settling(&t) {
		// A task kept alive by a watch still settles its usage: the switch above took the watch.
		err = r.settle(context.WithValue(ctx, effectsKey{}, fx), cfg, &t)
	}
	// CD23: the final spend. The pass that owes it prices the task's runs again — price is
	// idempotent, a record only ever takes the larger cost — so the fact posted below carries the
	// figure the meter settled on after the run left its active set.
	finalSpend := r.spendFinalDue(cfg, &t)
	if finalSpend {
		r.price(ctx, &t)
	}
	if !equality.Semantic.DeepEqual(*before, t.Status) {
		r.capOutbox(&t)
		if uerr := r.Client.Status().Update(ctx, &t); uerr != nil {
			r.mon.forget(t.Name) // the pass was not recorded: its retry must not wait out the throttle
			if err == nil && apierrors.IsConflict(uerr) {
				// #452: the task changed under the pass: an expected retry, not an error. Nothing
				// was recorded, so the replay re-reads the task and decides again.
				r.log().Debug("the task changed during the pass: replayed", "task.id", t.Name)
				return ctrl.Result{RequeueAfter: conflictRequeue}, nil
			}
			return ctrl.Result{}, errors.Join(err, uerr)
		}
		for _, f := range fx.after {
			f(ctx)
		}
		r.waited(ctx, &t, before, &t.Status)
		r.needed(ctx, cfg, &t, before, &t.Status)
	}
	// Only a written outbox is posted (review M-a), only written facts reach the room, and only a
	// written end exports the task's span: a write that conflicts posts and exports nothing, and its
	// replay may take another path. The second write records what was posted and exported. Facts
	// are advisory: a failed post is returned for a retry, and no phase waits on it.
	if len(t.Status.Outbox) > 0 || r.spanDue(&t) || r.factsDue(cfg, &t) || r.statusCommentDue(cfg, &t) {
		queued := t.Status.DeepCopy()
		err = errors.Join(err, r.drain(ctx, cfg, &t))
		perr := r.postFacts(ctx, cfg, &t)
		err = errors.Join(err, perr)
		if r.statusCommentDue(cfg, &t) {
			err = errors.Join(err, r.syncStatusComment(ctx, cfg, &t))
		}
		r.endTrace(ctx, &t)
		// CD23: the pass that landed the final fact records how long its dollars took to settle,
		// from the last run's end. postFacts persists the fact's hash before it calls the room, so a
		// post the room refused — or a write that conflicted after it — leaves the ledger with the
		// final hash and Posted false: the Posted flip is what counts that retry, once, and a later
		// reconcile that posts nothing counts nothing. A fact the pass could not claim (an unpriced
		// run) counts nothing either.
		final := finalSpend && perr == nil && r.spendIsFinal(cfg, &t) && t.Status.Facts != nil && t.Status.Facts.Posted &&
			(queued.Facts == nil || !queued.Facts.Posted || queued.Facts.Hash != t.Status.Facts.Hash)
		if !equality.Semantic.DeepEqual(*queued, t.Status) {
			if uerr := r.Client.Status().Update(ctx, &t); uerr != nil {
				if err == nil && apierrors.IsConflict(uerr) { // #452: as above; the drain is idempotent
					r.log().Debug("the task changed while its outbox posted: replayed", "task.id", t.Name)
					return ctrl.Result{RequeueAfter: conflictRequeue}, nil
				}
				return ctrl.Result{}, errors.Join(err, uerr)
			}
			if final {
				if end, ok := lastRunEnd(&t); ok {
					r.Metrics.TaskSpendFinal(ctx, r.Now().Sub(end))
				}
			}
			if t.Status.Trace != nil && t.Status.Trace.ExportAbandoned && !queued.Trace.ExportAbandoned {
				r.Metrics.TraceExportAbandoned(ctx) // once: counted with the write that records it
			}
		}
	}
	// A span the collector refused is retried every spanRetry (ST2). It is folded into the terminal
	// block's requeue: an early return here kept an ended task's room open, and its revert watch,
	// settle and monitor on the span's clock, for as long as the collector was down (F-9).
	var spanWait time.Duration
	if err == nil && r.spanDue(&t) {
		spanWait = spanRetry
	}
	if v1alpha1.TerminalPhase(t.Status.Phase) {
		// CD23: the one wake-up the final spend is owed, while its instant is still ahead. It is
		// folded into every requeue below, as the settle's is: a task whose room closed before the
		// instant would otherwise never be woken for it.
		var finalWait time.Duration
		if at, ok := r.spendFinalAt(cfg, &t); ok && r.Now().Before(at) {
			finalWait = max(at.Sub(r.Now()), time.Second)
		}
		// #56: the room closes once everything the task owes it has landed. Re-evaluated on the
		// state this pass left, so the pass that posts the last fact closes it.
		var closeLeft time.Duration
		if err == nil {
			if due, capped, wait, serr := r.roomState(ctx, cfg, &t); serr != nil {
				err = serr
			} else if due {
				var closed bool
				if closed, err = r.closeRoom(ctx, &t, capped != ""); closed && capped != "" {
					r.log().Info("room closed at the cap", "task.id", t.Name, "reason", capped)
					r.Metrics.RoomClosedAtCap(ctx, capped)
				}
			} else {
				closeLeft = wait
			}
		}
		// The revert watch polls: a revert PR merges, closes or stalls on GitHub's clock, not ours.
		if err == nil && (r.revertable(cfg, &t) || r.revertPending(&t)) {
			return ctrl.Result{RequeueAfter: soonest(soonest(cfg.Poll.Tasks.Duration, spanWait), finalWait)}, nil
		}
		if err == nil && r.settling(&t) {
			wait := r.settleLeft(cfg, &t) // for the settle, at its window, unless a merge under watch is due sooner
			if r.monitorDue(cfg, &t) {
				wait = min(wait, monitorEvery)
			}
			return ctrl.Result{RequeueAfter: soonest(soonest(wait, spanWait), finalWait)}, nil
		}
		if err == nil && r.monitorDue(cfg, &t) && (closeLeft <= 0 || closeLeft > monitorEvery) {
			return ctrl.Result{RequeueAfter: soonest(soonest(monitorEvery, spanWait), finalWait)}, nil // a merge under watch is read on its own clock
		}
		return ctrl.Result{RequeueAfter: soonest(soonest(closeLeft, spanWait), finalWait)}, err // closeLeft: the revert window or a driving person (#56)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if t.Status.Phase == v1alpha1.PhaseEscalated {
		return ctrl.Result{RequeueAfter: escalatedPoll}, nil
	}
	return ctrl.Result{RequeueAfter: cfg.Poll.Tasks.Duration}, nil
}

// conflictRequeue is the replay of a pass whose status write lost an optimistic-concurrency race (#452).
const conflictRequeue = 100 * time.Millisecond

func (r *Reconciler) step(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	// MR2: a coordination parent and an integration Task take their own path before anything an
	// ordinary task does, the stop machinery included: neither has a run to stop.
	switch t.Spec.Kind {
	case v1alpha1.TaskKindCoordination:
		if v1alpha1.TerminalPhase(t.Status.Phase) {
			return nil
		}
		return r.coordinating(ctx, cfg, t)
	case v1alpha1.TaskKindIntegration:
		return r.integrating(ctx, cfg, t)
	}
	// A terminal task reaches step only for its revert (§6.4); the stop machinery must not
	// rewrite an ended task into Stopped, and its runs are already over.
	if !v1alpha1.TerminalPhase(t.Status.Phase) {
		stop, why, err := r.stopRequested(ctx, t)
		if err != nil {
			return err
		}
		if stop {
			return r.stop(ctx, cfg, t, why)
		}
	}
	// H1: a writer run whose shadow pass failed is retried here, on any later pass (audit only).
	r.shadowPending(ctx, cfg, t)
	switch t.Status.Phase {
	case v1alpha1.PhaseReceived:
		return r.received(ctx, cfg, t)
	case v1alpha1.PhaseTriaged:
		return r.triaged(ctx, cfg, t)
	case v1alpha1.PhaseQueued:
		return r.queued(ctx, cfg, t)
	case v1alpha1.PhaseSplit:
		return r.splitting(ctx, cfg, t)
	case v1alpha1.PhaseImplementing:
		return r.implementing(ctx, cfg, t)
	case v1alpha1.PhaseReviewing:
		return r.reviewing(ctx, cfg, t)
	case v1alpha1.PhaseAwaitingHuman:
		return r.awaitingHuman(ctx, cfg, t)
	case v1alpha1.PhaseEscalated:
		return r.escalated(ctx, cfg, t)
	case v1alpha1.PhaseAwaitingCI:
		return r.awaitingCI(ctx, cfg, t)
	case v1alpha1.PhaseAutoMerging:
		return r.autoMerging(ctx, cfg, t)
	case v1alpha1.PhaseVerifying:
		return r.verifying(ctx, cfg, t)
	case v1alpha1.PhaseReverted:
		return r.revertWatch(ctx, cfg, t) // reached only while revertPending
	case v1alpha1.PhaseDone:
		if r.revertable(cfg, t) {
			return r.revert(ctx, cfg, t, "revert_requested")
		}
		return r.monitor(ctx, cfg, t) // reached only when monitorDue
	}
	return nil
}

// effects are what a step records outside the Task: metrics, run once the status that caused them
// is written. A conflicting write replays the step, which would otherwise count it twice.
type effects struct{ after []func(context.Context) }

type effectsKey struct{}

// record defers f until the step's status is written; outside Reconcile it runs at once.
func record(ctx context.Context, f func(context.Context)) {
	if fx, ok := ctx.Value(effectsKey{}).(*effects); ok {
		fx.after = append(fx.after, f)
		return
	}
	f(ctx)
}

func (r *Reconciler) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}

// payer is the key the task's costs and yield count for (spend per identity SI1): the person its
// next run is charged to, else the fleet.
func (r *Reconciler) payer(cfg *config.Config, t *v1alpha1.Task) string {
	return runs.TaskPayer(t, cfg.Budgets.PerPerson.Enabled)
}

// waited adds a written exit from AwaitingHuman or Escalated to the payer's waits (SI8): the time
// from the phase's start to the next phase's, both as the status records them, by the hand-off's
// reason and the task's class (AM9).
func (r *Reconciler) waited(ctx context.Context, t *v1alpha1.Task, before, after *v1alpha1.TaskStatus) {
	if before.Phase == after.Phase || before.PhaseSince == nil || after.PhaseSince == nil {
		return
	}
	if before.Phase == v1alpha1.PhaseAwaitingHuman || before.Phase == v1alpha1.PhaseEscalated {
		r.Metrics.HumanWait(ctx, before.Phase, before.Reason, t.Spec.PredictedClass, after.PhaseSince.Sub(before.PhaseSince.Time))
	}
}

// needed counts the interventions a written pass showed the task needs (§7): its entry into
// Escalated — a bounce as needs_author, the author's and not a maintainer's — and the first human
// commit on its pull request, from whichever site set HumanCommits. Read off the write like waited,
// so a refused write counts nothing and its replay counts once.
func (r *Reconciler) needed(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, before, after *v1alpha1.TaskStatus) {
	if after.Phase == v1alpha1.PhaseEscalated && before.Phase != after.Phase {
		kind := "escalated"
		if after.Reason == ReasonNeedsAuthor {
			kind = "needs_author"
		}
		r.Metrics.Intervention(ctx, kind, cohortOf(t), profileOf(t), r.recommendedOf(cfg, t))
	}
	if after.PullRequest != nil && after.PullRequest.HumanCommits && (before.PullRequest == nil || !before.PullRequest.HumanCommits) {
		r.Metrics.Intervention(ctx, "human_commit", cohortOf(t), profileOf(t), r.recommendedOf(cfg, t))
	}
}

// maxHandedOffReason is HandedOffReason's CRD bound.
const maxHandedOffReason = 64

func (r *Reconciler) to(t *v1alpha1.Task, phase, reason string) {
	if t.Status.Phase != phase {
		now := metav1.NewTime(r.Now())
		t.Status.PhaseSince = &now
	}
	t.Status.Phase, t.Status.Reason = phase, reason
	if ref := t.Status.PullRequest; ref != nil {
		ref.Approval = nil // what showApproval showed stands with its reason only
	}
	// AM9: the pull request's first hand-off to a human starts its hand-off to merge clock, from
	// every site that hands off.
	if ref := t.Status.PullRequest; phase == v1alpha1.PhaseAwaitingHuman && ref != nil && ref.HandedOffAt == nil {
		ref.HandedOffAt, ref.HandedOffReason = t.Status.PhaseSince.DeepCopy(), reason[:min(len(reason), maxHandedOffReason)]
	}
	if ref := t.Status.PullRequest; phase == v1alpha1.PhaseAwaitingHuman && ref != nil && reason == "human_commit_in_pr" {
		ref.HumanCommits = true // every site where the gate's provenance refuses a commit
	}
}

// handedOffMerged records a merged pull request's wait from its first hand-off to its merge (AM9),
// how being approved or human; a pull request never handed off records nothing.
func (r *Reconciler) handedOffMerged(ctx context.Context, t *v1alpha1.Task, how string) {
	ref := t.Status.PullRequest
	if ref == nil || ref.HandedOffAt == nil || ref.MergedAt == nil {
		return
	}
	class, reason, d := t.Spec.PredictedClass, ref.HandedOffReason, ref.MergedAt.Sub(ref.HandedOffAt.Time)
	record(ctx, func(ctx context.Context) { r.Metrics.HandoffToMerge(ctx, class, reason, how, d) })
}

// notServedRequeue is when a task whose repository the published config does not serve is looked
// at again: a later publish may serve it again (factory resources D12).
const notServedRequeue = 5 * time.Minute

// onceSet holds names already reported, so a condition that persists is logged once per name.
type onceSet struct {
	mu   sync.Mutex
	seen map[string]bool
}

// once reports whether name is new, and records it.
func (s *onceSet) once(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[name] {
		return false
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	s.seen[name] = true
	return true
}

func (s *onceSet) forget(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.seen, name)
}

// repo is the config entry of the task's repository (v2 D1); nil when the config does not serve it.
func (r *Reconciler) repo(cfg *config.Config, t *v1alpha1.Task) *config.Repository {
	return cfg.RepositoryFor(t.Spec.Repository)
}

// class is the task's repository's class name, the zero class when the repository does not declare
// it or the config does not serve the repository.
func (r *Reconciler) class(cfg *config.Config, t *v1alpha1.Task, name string) config.Class {
	cl, _ := r.repo(cfg, t).Class(name)
	return cl
}

func (r *Reconciler) narrator(ctx context.Context, cfg *config.Config) narrate.Narrator {
	return narrate.Narrator{Forge: r.gh(ctx), Login: cfg.FactoryLogin}
}

// boundKey carries the reconciled task's connections (v2 D8): every GitHub call of the pass reads
// them from its context, so none can reach another repository's.
type boundKey struct{}

type bound struct {
	forge  TaskForge
	merger forge.Merger
}

// bind puts t's repository's connections on ctx; an unknown repository is an error, so nothing
// acts for it.
func (r *Reconciler) bind(ctx context.Context, t *v1alpha1.Task) (context.Context, error) {
	if r.Clients == nil {
		return ctx, nil
	}
	f, m, ok := r.Clients(t.Spec.Repository)
	if !ok {
		return ctx, fmt.Errorf("task %s: no GitHub connection for repository %q (v2 D8)", t.Name, t.Spec.Repository)
	}
	return context.WithValue(ctx, boundKey{}, bound{forge: f, merger: m}), nil
}

// gh is the factory App on the task's repository, merger the merger App there.
func (r *Reconciler) gh(ctx context.Context) TaskForge {
	if b, ok := ctx.Value(boundKey{}).(bound); ok {
		return b.forge
	}
	return r.Forge
}

func (r *Reconciler) merger(ctx context.Context) forge.Merger {
	if b, ok := ctx.Value(boundKey{}).(bound); ok {
		return b.merger
	}
	return r.Merger
}

// onRepo: t is a task of rc's repository. One entry owns every task, as before the list.
func (r *Reconciler) onRepo(cfg *config.Config, t *v1alpha1.Task, rc *config.Repository) bool {
	return len(cfg.Repos()) == 1 || strings.EqualFold(t.Spec.Repository, rc.Name)
}

func stopReasons() map[string]string {
	return map[string]string{"true": "stopped_by_annotation", "label": "stopped_by_label", "superseded": "superseded", StopFamilyDropped: "family_dropped"}
}

func (r *Reconciler) stopRequested(ctx context.Context, t *v1alpha1.Task) (bool, string, error) {
	on, err := killswitch.Engaged(ctx, r.Client, r.Namespace)
	if err != nil {
		return false, "", err // never stop on a transient error
	}
	if on {
		return true, "kill_switch", nil
	}
	if v := t.Annotations[v1alpha1.AnnotationStop]; v != "" {
		if why, ok := stopReasons()[v]; ok {
			return true, why, nil
		}
		return true, "stopped_by_annotation", nil
	}
	return false, "", nil
}

// stop: every live run of the task is annotated revoked=manual, then every run deleted (§6.1).
// The runs are the cluster's, not status's (ruling SN): a run whose record was lost to a
// conflicting status write is still the task's, and still stopped. The claims' last usage is
// copied into the records first: once deleted, the settle can no longer read it (#55).
func (r *Reconciler) stop(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, why string) error {
	all, err := r.Runs.List(ctx)
	if err != nil {
		return err
	}
	if len(t.Status.Runs) > 0 {
		absorb(t, all)
	}
	// Every live run is revoked first, so none keeps its token while the shadow pass reads GitHub.
	for _, run := range all {
		if run.TaskID != t.Name || runs.Terminal(run.Phase) {
			continue
		}
		if err := r.Runs.Annotate(ctx, run.ID, map[string]string{runs.AnnRevoked: "manual"}); err != nil {
			return err
		}
		// Counted now, not with the status write: a replay finds the claim gone or already
		// revoked, so a deferred count would be lost. A run that carried a revoke was counted
		// by whoever wrote it, the kill switch's sweeper included (F30).
		if run.Revoked == "" {
			r.Metrics.Revoked(fmetrics.WithPrincipal(ctx, run.Payer()), "manual") // as the ledger charged it
		}
	}
	// H1: shadowed after the revoke and before the deletes, while the claims and their canaries
	// still exist; the window runs to the revoke plus runEndGrace (audit only).
	r.shadowBeforeStop(ctx, cfg, t)
	for _, run := range all {
		if run.TaskID != t.Name {
			continue
		}
		if err := r.deleteRun(ctx, run.ID); err != nil {
			return err
		}
	}
	if why != "kill_switch" {
		r.intervention(ctx, cfg, t, "stop")
	}
	exit := ""
	if why == "superseded" {
		exit = "relabel" // SC8 (a): the author edited, a maintainer re-labelled
	}
	r.leaveBounce(ctx, t, exit)
	splitExit := SplitDeclined // a maintainer's stop declines the split
	if why == "superseded" {
		splitExit = SplitSuperseded // SL4 (c): the author edited, a maintainer re-labelled
	}
	r.leaveSplit(ctx, t, splitExit)
	if sp := t.Status.Split; t.Status.Phase == v1alpha1.PhaseSplit && sp != nil { // its open parts stop with it (SL12)
		newest, _, err := r.family(ctx, t)
		if err != nil {
			return err
		}
		if err := r.stopOpenParts(ctx, sp, newest); err != nil {
			return err
		}
	}
	return r.end(ctx, cfg, t, v1alpha1.PhaseStopped, why)
}

// narrateLater queues e in the task's outbox (ruling SO), written with the transition that caused it,
// so an outage never loses it: drain posts it, now or on a later reconcile. A key already queued
// or posted is not queued again, so a timer that re-fires every poll (the reminder) writes no
// status; Post would skip it anyway.
func narrateLater(t *v1alpha1.Task, e narrate.Event) { narrateOn(t, target(t), e) }

// narrateOn is narrateLater on issue or pull request n. With no target (a schedule's task before
// its pull request) an event is dropped, except a profile announcement (R31): it is queued with no
// number and posts once the task has one, since the announcement is what a reader of the pull
// request compares the outcome with.
func narrateOn(t *v1alpha1.Task, n int, e narrate.Event) {
	if (n == 0 && !narrate.Pending(e.Key)) || slices.Contains(t.Status.Narrated, e.Key) ||
		slices.ContainsFunc(t.Status.Outbox, func(o v1alpha1.Narration) bool { return o.Key == e.Key }) {
		return
	}
	t.Status.Outbox = append(t.Status.Outbox, v1alpha1.Narration{Key: e.Key, Number: n, Body: e.Body})
}

// drain posts the outbox in order and stops at the first failure, which the next reconcile
// retries. A post that landed but failed is found by its marker then, not posted twice. A post
// GitHub refuses for good (F-3) is dropped: it would hold back every later one and the room's close.
// An entry still waiting for a target (R31) is skipped, not a failure, and dropped once the task
// has ended without one: nothing will give it a target, and the outbox must empty for the close.
func (r *Reconciler) drain(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	var kept []v1alpha1.Narration
	for i, o := range t.Status.Outbox {
		if narrate.Closing(o.Key) && r.settling(t) {
			kept = append(kept, t.Status.Outbox[i:]...) // its token total is final only once the usage settles (#42); settle requeues
			break
		}
		n := o.Number
		if n == 0 {
			if n = target(t); n == 0 {
				if !v1alpha1.TerminalPhase(t.Status.Phase) {
					kept = append(kept, o)
				} else {
					r.log().Warn("narration_dropped", "task.id", t.Name, "key", o.Key, "why", "no_target")
					narrate.Abandon(t, o.Key)
				}
				continue
			}
		}
		err := r.narrator(ctx, cfg).Post(ctx, t, n, narrate.Event{Key: o.Key, Body: o.Body})
		var pe *forge.PermanentError
		switch {
		case errors.As(err, &pe):
			// Error, not Warn: a mis-scoped App token reads as a refusal too, and must be seen.
			r.log().Error("narration_dropped", "task.id", t.Name, "key", o.Key, "number", n, "why", "refused", "status", pe.Status)
			narrate.Abandon(t, o.Key)
		case err != nil:
			t.Status.Outbox = append(kept, t.Status.Outbox[i:]...)
			return err
		}
	}
	t.Status.Outbox = kept
	return nil
}

// maxOutbox is status.outbox's CRD maxItems: a status write carrying more is refused whole.
const maxOutbox = 16

// capOutbox keeps the outbox within maxOutbox before the pass's status write (F-2): past it every
// write is refused, and drain, which posts only a written outbox, never runs, so the task wedges.
// The oldest entries go first, an ending's comment last: it carries the outcome and the token total.
func (r *Reconciler) capOutbox(t *v1alpha1.Task) {
	for len(t.Status.Outbox) > maxOutbox {
		i := max(slices.IndexFunc(t.Status.Outbox, func(o v1alpha1.Narration) bool { return !narrate.Closing(o.Key) }), 0)
		r.log().Warn("narration_dropped", "task.id", t.Name, "key", t.Status.Outbox[i].Key, "why", "outbox_full")
		t.Status.Outbox = slices.Delete(t.Status.Outbox, i, i+1)
	}
}

// target is where a task narrates: its issue, else its PR (R28), else nowhere.
func target(t *v1alpha1.Task) int {
	if t.Spec.Issue > 0 {
		return t.Spec.Issue
	}
	if t.Status.PullRequest != nil {
		return t.Status.PullRequest.Number
	}
	return 0
}

// end moves a task to a terminal or escalated phase and says why, once. The task's tokens are
// not recorded here: the settle does that, once, after the meter's late readings have had their
// window (R49). An escalation names the maintainers (§6.3); the outcome records tier fit and the
// task.final line the moment the task first ends. The reservations no claim holds any more are
// released here (#55): every ending path, and every replay of one, passes through it.
func (r *Reconciler) end(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, phase, reason string) error {
	if phase == v1alpha1.PhaseEscalated {
		if stepped, err := r.stepUp(ctx, cfg, t, reason); stepped || err != nil {
			return err
		}
	}
	r.ended(ctx, cfg, t, phase, reason)
	return nil
}

// ended is end past the escalation step.
func (r *Reconciler) ended(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, phase, reason string) {
	r.finish(ctx, cfg, t, phase, reason, narrate.Ended(t, phase, reason, r.repo(cfg, t).Maintainers...))
}

// finish is the ending every task shares: its unheld reservations released, the move, a terminal
// approved parent's parts counted (SL14), the narration and the outcome.
func (r *Reconciler) finish(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, phase, reason string, e narrate.Event) {
	r.releaseUnheld(ctx, t)
	r.to(t, phase, reason)
	if v1alpha1.TerminalPhase(phase) {
		r.countFamily(ctx, t) // an approved parent stopped or expired: its landed parts count (SL14)
	}
	narrateLater(t, e)
	r.outcome(ctx, cfg, t)
}

// settleWindow bounds how long an ended task keeps refreshing its usage before the total is
// recorded (R49): twice the meter's period — one reading can trail one tick — plus the travel of
// the annotation the reading becomes.
func (r *Reconciler) settleWindow(cfg *config.Config) time.Duration {
	return 2*cfg.Poll.Meter.Duration + 30*time.Second
}

// settling: the task has ended with runs whose usage has not settled yet (R49). A task without
// runs has nothing to settle: no claim exists for the meter to annotate.
func (r *Reconciler) settling(t *v1alpha1.Task) bool {
	return v1alpha1.TerminalPhase(t.Status.Phase) && len(t.Status.Runs) > 0 && !t.Status.UsageSettled
}

// settle refreshes a settling task's records from their claims, and once the window closes from
// the task's first end records the task's tokens, its outcome and its USD (R34) and marks the usage
// settled. Every read comes before the mark: a read that fails leaves the status as it was, and the
// next pass retries. The metrics ride the same status write as the mark, so a conflicting write
// replays both; the write that lands records them once.
func (r *Reconciler) settle(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	if _, _, err := r.observe(ctx, t); err != nil {
		return err
	}
	if at := endedAt(t); at != nil && r.Now().Sub(at.Time) < r.settleWindow(cfg) {
		return nil
	}
	outcome, micro, ready, err := r.tally(ctx, cfg, t)
	if err != nil || !ready {
		return err // not ready: the parts still settle, and settleLeft requeues on the poll
	}
	t.Status.UsageSettled = true
	blind := slices.ContainsFunc(t.Status.Runs, func(rec v1alpha1.RunRecord) bool { return rec.Tokens == 0 })
	for i := range t.Status.Outbox {
		if o := &t.Status.Outbox[i]; narrate.Closing(o.Key) { // the closing comment waited for this total (#42)
			o.Body = narrate.Retotal(o.Body, t.Status.Usage.Tokens, blind)
		}
	}
	tokens, tier, tmpl, class := t.Status.Usage.Tokens, t.Spec.Budget.Tier, t.Spec.Template, t.Spec.PredictedClass
	record(ctx, func(ctx context.Context) { r.Metrics.TaskTokens(ctx, tokens, tier, tmpl, class) })
	r.count(ctx, cfg, t, outcome, micro)
	return nil
}

// settleLeft is how much of the settle window an ended task still owes: it is requeued for its
// settle, never on the poll interval an ended task otherwise skips. Past its own window a task
// still settling waits for its family (R32), on the poll, no later than the family bound.
func (r *Reconciler) settleLeft(cfg *config.Config, t *v1alpha1.Task) time.Duration {
	at := endedAt(t)
	if at == nil {
		return cfg.Poll.Tasks.Duration
	}
	since := r.Now().Sub(at.Time)
	if left := r.settleWindow(cfg) - since; left > 0 {
		return max(left, time.Second)
	}
	return max(min(cfg.Poll.Tasks.Duration, r.familyBound(cfg)-since), time.Second)
}

// spendFinalMargin is past the meter's own settle window, so the re-price reads a figure that has
// landed: the meter prices a run from its claim's annotation, one tick behind at worst (R49).
const spendFinalMargin = time.Minute

// lastRunEnd is when a task's last run ended — its finish, else its start, else the end of the
// phase it is in — the instant CD23 measures the final spend and its metric from. false for a task
// that never ran.
func lastRunEnd(t *v1alpha1.Task) (time.Time, bool) {
	if len(t.Status.Runs) == 0 {
		return time.Time{}, false
	}
	last := t.Status.Runs[len(t.Status.Runs)-1]
	end := last.Started
	if last.Finished != nil {
		end = last.Finished
	}
	if end == nil {
		end = t.Status.PhaseSince
	}
	if end == nil {
		return time.Time{}, false
	}
	return end.Time, true
}

// spendFinalAt is when a terminal task's dollars are final (CD23): its last run's end, plus the
// settle window, plus a margin. false for a task that never ran — there is nothing to settle — or
// when facts.finalSpend is off.
func (r *Reconciler) spendFinalAt(cfg *config.Config, t *v1alpha1.Task) (time.Time, bool) {
	if !cfg.Facts.FinalSpend || !v1alpha1.TerminalPhase(t.Status.Phase) {
		return time.Time{}, false
	}
	end, ok := lastRunEnd(t)
	if !ok {
		return time.Time{}, false
	}
	return end.Add(r.settleWindow(cfg) + spendFinalMargin), true
}

// spendFinalDue: the pass that owes a terminal task's last word about its dollars (CD23) — the
// instant has passed, and the room does not carry the fact this pass would post. That fact is the
// final one when every run is priced, and the settled facts otherwise, so a task whose dollars
// cannot be claimed is at rest here rather than posting a figure it would have to change.
func (r *Reconciler) spendFinalDue(cfg *config.Config, t *v1alpha1.Task) bool {
	at, ok := r.spendFinalAt(cfg, t)
	return ok && !r.Now().Before(at) && r.factsDue(cfg, t)
}

// spendIsFinal: the task's dollars are the last figure its room will read (CD23). Past the instant,
// and only when every run of the task is priced: a final spend is a claim about money, so a run the
// meter could only say Why about — or one it has no figure for — withholds it.
func (r *Reconciler) spendIsFinal(cfg *config.Config, t *v1alpha1.Task) bool {
	at, ok := r.spendFinalAt(cfg, t)
	return ok && !r.Now().Before(at) && allPriced(t)
}

// allPriced: every run of the task carries a price. A cost entry with a Why and no dollars (the
// meter could not price the model) leaves the record unpriced, and so does a run with tokens and no
// cost: both are "no figure", never $0 (OD-6 (b)).
func allPriced(t *v1alpha1.Task) bool {
	for _, rec := range t.Status.Runs {
		if rec.Unpriced != "" || (rec.Tokens > 0 && rec.CostMicroUSD == 0) {
			return false
		}
	}
	return true
}

// soonest is the nearer of two requeues, where 0 means none.
func soonest(a, b time.Duration) time.Duration {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	}
	return min(a, b)
}

// spanDue: the task has ended and its root span is still to export (R46).
func (r *Reconciler) spanDue(t *v1alpha1.Task) bool {
	tr := t.Status.Trace
	return r.Trace != nil && v1alpha1.TerminalPhase(t.Status.Phase) && tr != nil && !tr.Exported && !tr.ExportAbandoned
}

// endTrace exports the task's root span (R46), from the label's acceptance (the Task's creation)
// to the end its status recorded, so a retry exports the same span: at least once, since a
// conflict on the write that records Exported replays it. Best effort: a lost span never holds a
// task. A failed export is retried every spanRetry and given up spanGiveUp after the end (ST2).
func (r *Reconciler) endTrace(ctx context.Context, t *v1alpha1.Task) {
	if !r.spanDue(t) {
		return
	}
	end := r.Now()
	if t.Status.PhaseSince != nil {
		end = t.Status.PhaseSince.Time
	}
	tr := t.Status.Trace
	err := r.Trace.Export(ctx, tracing.Task{TraceID: tr.TraceID, SpanID: tr.SpanID, TaskID: t.Name, Tier: t.Spec.Budget.Tier,
		Phase: t.Status.Phase, Reason: t.Status.Reason, Start: t.CreationTimestamp.Time, End: end, Coordination: r.coordinationSpan(ctx, t)})
	if err != nil {
		if r.Now().Sub(end) >= spanGiveUp {
			tr.ExportAbandoned = true
			r.log().Warn("task span given up", "task.id", t.Name, "err", err)
			return
		}
		r.log().Warn("task span not exported", "task.id", t.Name, "err", err)
		return
	}
	tr.Exported = true
}

func (r *Reconciler) countTasks(ctx context.Context, keep func(*v1alpha1.Task) bool) (int, error) {
	var l v1alpha1.TaskList
	if err := r.Client.List(ctx, &l, client.InNamespace(r.Namespace)); err != nil {
		return 0, err
	}
	n := 0
	for i := range l.Items {
		if keep(&l.Items[i]) {
			n++
		}
	}
	return n, nil
}

func sameUTCDay(a, b time.Time) bool {
	return a.UTC().Format(time.DateOnly) == b.UTC().Format(time.DateOnly)
}

// admit is R6's text cap and §6.2's daily task cap; a rejected task does not count.
func (r *Reconciler) admit(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (string, error) {
	if len(t.Spec.Text) > cfg.Caps.MaxTextBytes {
		return "text_too_long", nil
	}
	if t.Spec.Source.Kind == "adopt" {
		return "", nil // R39: Renovate's own pull request starts no run: the daily task cap is not its to spend
	}
	today, err := r.countTasks(ctx, func(o *v1alpha1.Task) bool {
		return o.Name != t.Name && v1alpha1.SpendsDailyCap(o, r.Now())
	})
	if err != nil {
		return "", err
	}
	if today >= cfg.Caps.TasksPerDay {
		return "daily_task_cap", nil
	}
	return "", nil
}

func (r *Reconciler) received(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	// A RunLore task's issue is written by the intake just after its creation (FA-8): wait for
	// it, so the first narration has a place to land. Past two minutes the intake is gone and
	// the task proceeds without one.
	waitsForIssue := t.Spec.Source.Kind == "runlore" || (t.Spec.Source.Kind == "api" && strings.HasPrefix(t.Spec.Text, "# "))
	if waitsForIssue && t.Spec.Issue == 0 && r.Now().Sub(t.CreationTimestamp.Time) < 2*time.Minute {
		return nil
	}
	if t.Spec.CoordinationParent != nil {
		// MR2: a child's admission is its confirmed plan's, not the label intake's daily cap, and
		// it comes before the splitter's Parent: the two lineages never mix.
		return r.receivedCoordinationChild(ctx, cfg, t)
	}
	reason, err := r.admit(ctx, cfg, t)
	if err != nil {
		return err
	}
	if reason != "" {
		return r.end(ctx, cfg, t, v1alpha1.PhaseRejected, reason)
	}
	if due, err := r.overlapDue(ctx, cfg, t); due || err != nil {
		return err // a new result persists before trace, triage, room, run, or budget effects
	}
	if r.Trace != nil && t.Status.Trace == nil { // R46: the task's root span, minted once at acceptance
		tr, sp := tracing.Mint()
		t.Status.Trace = &v1alpha1.TraceRef{TraceID: tr, SpanID: sp}
	}
	if t.Spec.Source.Kind == "adopt" {
		return r.receivedAdopted(ctx, cfg, t)
	}
	if t.Spec.Parent != nil {
		return r.receivedChild(ctx, cfg, t) // a part of a split: its parent's spec, no classifier (SL5)
	}
	d, err := r.Triage.Triage(ctx, cfg, t)
	if err != nil {
		return err
	}
	t.Spec.Template, t.Spec.PredictedClass, t.Spec.DataClass = d.Template, d.PredictedClass, d.DataClass
	t.Spec.Budget = triage.Ceiling(d.Budget, t.Spec.MaxMicroUSD) // F4: a task without a ceiling keeps its class budget, byte for byte
	var profile *v1alpha1.ProfileStatus
	if !triage.Investigates(cfg, t) && !cfg.Investigates(d.Template) { // a task routed to investigate never takes part in the evaluation
		profile = r.floorProfile(cfg, t, d)
	}
	var floor config.HarnessProfile
	if profile != nil {
		floor, _ = cfg.ProfileFor(r.class(cfg, t, d.PredictedClass), profile.Name)
	}
	r.profileSpec(cfg, t, d.PredictedClass, floor)
	if err := r.scopeAtTriage(ctx, t); err != nil {
		return err // a note that cannot be read is retried, never read as none (SC4)
	}
	r.splitAtTriage(cfg, t, d.Classification)
	status := t.Status // Update returns the server's status into t; keep ours
	if err := r.Client.Update(ctx, t); err != nil {
		return err
	}
	t.Status = status
	t.Status.Classification = &d.Classification
	t.Status.Profile = profile
	recordConfig(cfg, t)
	r.to(t, v1alpha1.PhaseTriaged, "")
	return nil
}

// profileSpec writes what profile p changes on t's spec: its template when set, the loop, search and
// scope of its row (the class's when it names none), and its tier only when above t's, through the
// same ceiling as triage so a sponsored task's cap holds. A zero p is the class's row: today's spec.
func (r *Reconciler) profileSpec(cfg *config.Config, t *v1alpha1.Task, class string, p config.HarnessProfile) {
	if p.Template != "" {
		t.Spec.Template = p.Template
	}
	row := cfg.RowFor(class, p)
	t.Spec.Loop = nil
	if lp := cfg.LoopFor(row); lp != nil {
		judge := lp.JudgeModel
		if t.Spec.DataClass != "public" && config.PublicOnly(judge) { // a provider judge is public tasks' only (v3 F15, F4)
			judge = config.DefaultJudge
		}
		t.Spec.Loop = &v1alpha1.Loop{StallSteps: lp.StallSteps, Continuations: lp.Continuations, JudgeModel: judge}
	}
	t.Spec.Search = cfg.SearchFor(row)
	mode := cfg.ScopeFor(row)
	if cfg.Investigates(t.Spec.Template) {
		mode = config.ScopeOff // R38's investigate path is never scoped
	}
	t.Spec.ScopeMode = string(mode)
	if slices.Index(tiers, p.Tier) > slices.Index(tiers, t.Spec.Budget.Tier) {
		t.Spec.Budget = triage.Ceiling(triage.BudgetFor(cfg, p.Tier), t.Spec.MaxMicroUSD)
	}
}

// floorProfile is the profile t is frozen under at triage: its class's floor, with the cohort's
// source; nil for a class without a harness, whose task carries no status.profile. Source label is
// a pinned profile only (P8): a class label alone leaves the task floor or control.
func (r *Reconciler) floorProfile(cfg *config.Config, t *v1alpha1.Task, d triage.Decision) *v1alpha1.ProfileStatus {
	cl := r.class(cfg, t, d.PredictedClass)
	p, ok := cfg.ProfileFor(cl, cl.Harness)
	if !ok {
		return nil
	}
	source := map[string]string{triage.CohortLabeled: "label", triage.CohortControl: "control"}[d.Cohort]
	if source == "" {
		source = "floor"
	}
	cohort := d.Cohort
	if cohort == "" {
		cohort = triage.CohortTreatment
	}
	return &v1alpha1.ProfileStatus{Name: cl.Harness, Source: source, Cohort: cohort,
		Checks: cl.Checks || p.Checks, TestFirst: cl.TestFirst || p.TestFirst}
}

// triaged gives the task its room (R2). A room of that name the factory did not make escalates:
// its runs would inherit another owner's data class (C3).
func (r *Reconciler) triaged(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	if t.Spec.CoordinationParent != nil { // MR2: a Room for another repository is never adopted
		_, err := r.checkCoordinationLineage(ctx, t)
		if reason := lineageReason(err); reason != "" {
			return r.end(ctx, cfg, t, v1alpha1.PhaseRejected, reason)
		}
		if err != nil {
			return err
		}
	}
	var members []roomv1.Member
	if p := t.Sponsored(); p != "" { // R8, R13: the principal reads its own task's room, as a watcher
		members = append(members, roomv1.Member{Principal: p, Role: "watcher"})
	}
	err := rooms.Ensure(ctx, r.Client, r.Namespace, t.Name, t.Spec.DataClass, t.Spec.Repository, members...)
	if errors.Is(err, rooms.ErrForeignRoom) {
		return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, "foreign_room")
	}
	if apierrors.IsInvalid(err) && len(members) > 0 {
		// an old Rooms CRD refuses a system: member: retrying cannot help, an operator must act
		return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, "rooms_crd_outdated")
	}
	if err != nil {
		return err
	}
	t.Status.RoomRef = t.Name
	if !scoperPlanned(t) {
		resolveUnscoped(t)
	}
	r.to(t, v1alpha1.PhaseQueued, "")
	return nil
}

// costReader is the run meter's hand-off of each run's cost (meter.Costs).
type costReader interface {
	Get(id string) (meter.Cost, bool)
}

// runFits: the task's cap still holds one whole implementer run (owner, 2026-10-07). A run may
// spend up to its run cap before the meter revokes it, so admitting on "under the cap" let a task
// end 1.5 M past it; a resume follows the same rule. In USD while usdCapped, and always in tokens:
// a flat-rate model's nominal prices left the dollars far from their cap while the task ran to
// 4 M of its 3 M tokens (#465).
func runFits(t *v1alpha1.Task) bool { return runFitsFor(t, "implementer") }

// runFitsFor sizes the fit check by the role about to run (#475). Every role but the reviewer
// needs a whole run's tokens left. A reviewer reads a diff and needs a quarter of one
// (reviewFloorDiv), and starts at whatever remains up to a whole run (runCapTokens), so a
// token-heavy implementer no longer leaves the pull request unreviewed. Both caps still bind: the
// run is capped at what is left, so the task never passes taskTokens.
func runFitsFor(t *v1alpha1.Task, role string) bool {
	b := t.Spec.Budget
	if usdCapped(t) && b.TaskMicroUSD-chargedMicroUSD(t) < b.RunMicroUSD {
		return false
	}
	return tokensFit(t, role)
}

// reviewFloorDiv: a reviewer run needs 1/reviewFloorDiv of a whole run's tokens left to start.
const reviewFloorDiv = 4

func tokensFit(t *v1alpha1.Task, role string) bool {
	b := t.Spec.Budget
	need := b.RunTokens
	if role == "reviewer" {
		need = b.RunTokens / reviewFloorDiv
	}
	return b.TaskTokens <= 0 || b.TaskTokens-chargedTokens(t) >= need
}

// runCapTokens is the token cap a run of role starts with: the tier's run cap, and for a reviewer
// no more than the task has left, so a review admitted on a partial remainder cannot take the
// task past taskTokens (the meter revokes it at this cap).
func runCapTokens(t *v1alpha1.Task, role string) int64 {
	b := t.Spec.Budget
	if role != "reviewer" || b.TaskTokens <= 0 || b.RunTokens <= 0 {
		return b.RunTokens
	}
	return max(1, min(b.RunTokens, b.TaskTokens-chargedTokens(t)))
}

// chargedTokens is the usage held against the task's caps: every run's tokens but the overlap
// evaluation's scoper, whose spend is recorded in the run's record and status.usage but belongs
// to the evaluation, not the implementation budget (#475; overlap design: "implementation budget").
func chargedTokens(t *v1alpha1.Task) int64 {
	n := t.Status.Usage.Tokens
	for _, rec := range t.Status.Runs {
		if evaluationRun(t, rec.ID) {
			n -= rec.Tokens
		}
	}
	return max(n, 0)
}

// chargedMicroUSD is chargedTokens' counterpart in dollars.
func chargedMicroUSD(t *v1alpha1.Task) int64 {
	n := t.Status.Usage.CostMicroUSD
	for _, rec := range t.Status.Runs {
		if evaluationRun(t, rec.ID) {
			n -= rec.CostMicroUSD
		}
	}
	return max(n, 0)
}

// evaluationRun: the run is the task's overlap evaluation scoper.
func evaluationRun(t *v1alpha1.Task, id string) bool {
	return t.Status.Overlap != nil && t.Status.Overlap.ScoperRun != "" && t.Status.Overlap.ScoperRun == id
}

// usdCapped: the task is held to TaskMicroUSD rather than TaskTokens (OD-6 (b)), while every run
// it holds was priced. A run the meter could not price, or one with tokens and no cost (a task
// begun before the meter priced runs), holds it to TaskTokens: never a cost counted as $0.
func usdCapped(t *v1alpha1.Task) bool {
	return t.Spec.Budget.TaskMicroUSD > 0 && allPriced(t)
}

// taskLimit names the task cap runFits checks, for the revocations metric.
func taskLimit(t *v1alpha1.Task, role string) string {
	if usdCapped(t) && tokensFit(t, role) {
		return "task-usd"
	}
	return "task-tokens"
}

// price takes each run's cost from the meter into the task's records: a record keeps its last
// cost once its claim is gone, as it keeps its tokens (R49). The first run the meter cannot
// price, on a task with a USD cap, is said once: the task falls back to TaskTokens.
func (r *Reconciler) price(ctx context.Context, t *v1alpha1.Task) {
	if r.Costs == nil {
		return
	}
	before := unpriced(t)
	var sum int64
	for i := range t.Status.Runs {
		rec := &t.Status.Runs[i]
		if c, ok := r.Costs.Get(rec.ID); ok {
			served(&rec.Provenance, c.Served)
			rec.CostMicroUSD = max(rec.CostMicroUSD, c.MicroUSD)
			if rec.Unpriced == "" {
				rec.Unpriced = c.Why
			}
		}
		sum += rec.CostMicroUSD
	}
	t.Status.Usage.CostMicroUSD = sum
	if why := unpriced(t); why != "" && before == "" && t.Spec.Budget.TaskMicroUSD > 0 {
		r.log().Warn("task capped in tokens: a run's cost cannot be priced", "task.id", t.Name, "why", why,
			"cap", t.Spec.Budget.TaskTokens)
		record(ctx, func(ctx context.Context) { r.Metrics.RunBudgetFallback(ctx, "task", why) })
	}
}

func unpriced(t *v1alpha1.Task) string {
	for _, rec := range t.Status.Runs {
		if rec.Unpriced != "" {
			return rec.Unpriced
		}
	}
	return ""
}

func (r *Reconciler) queued(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	// A claim of the next run's deterministic id is a run the status does not know: the write
	// that recorded it was lost (R48). Recorded, never duplicated, and before the caps: the run
	// already holds its slot.
	x, found, err := r.Runs.Get(ctx, runID(t.Name, len(t.Status.Runs)))
	if err != nil {
		return err
	}
	if found {
		return r.recordExisting(cfg, t, x)
	}
	// The records are refreshed here too (R49): a task waiting in Queued still holds ended runs
	// whose late readings must land.
	if _, _, err := r.observe(ctx, t); err != nil {
		return err
	}
	// #450: after a model provider refusal the next run waits out its backoff; the poll wakes the task.
	if r.modelHeld(t) {
		return nil
	}
	// MR6: a coordination child's lineage, authority and the capability gate, re-read before every
	// new run or resume and after an existing claim is adopted, before any slot or ledger admission.
	if t.Spec.CoordinationParent != nil {
		if held, err := r.coordinationHold(ctx, cfg, t); held || err != nil {
			return err
		}
	}
	ok, why, err := r.slotFree(ctx, cfg, t)
	if err != nil {
		return err
	}
	if !ok {
		t.Status.Reason = why
		if why == "waiting_room_busy" || why == "waiting_human_driver" { // the room is held, not the factory's own caps (N3)
			narrateLater(t, narrate.Held(t, why, cfg.RoomsURL))
		}
		return nil
	}
	// A pull request merged or closed meanwhile ends the wait with no run, so it is read before
	// the caps that guard a run's start: a landed task never escalates for a budget it no longer
	// needs.
	var pr forge.PR
	late := t.Status.PullRequest != nil && len(t.Status.Runs) > 0
	if late {
		var done bool
		if pr, done, err = r.lateReviews(ctx, cfg, t); err != nil || done {
			return err
		}
	}
	// A CI fix starts from the branch head: one a human pushed would ride the fix to a merge.
	if late && t.Status.NextRole == "" && nextTrigger(t) == "ci" {
		if handed, err := r.humanCommit(ctx, cfg, t, pr.HeadSHA); err != nil || handed {
			return err
		}
	}
	// MR8: a coordination child's confirmed allocation is a bound, never a shadow: its next run's
	// whole reservation fits over its own settled spend or it does not start. The daily ledger
	// below still reserves the run; neither widens the other.
	if t.Spec.CoordinationParent != nil && coordination.AllocationLeft(t) < 0 {
		return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, reasonAllocationExhausted)
	}
	if !runFitsFor(t, t.Status.NextRole) {
		limit := taskLimit(t, t.Status.NextRole)
		if cfg.Budgets.EnforceTask || t.Sponsored() != "" { // F4: an api task's ceiling is never a shadow
			record(ctx, func(ctx context.Context) { r.Metrics.RunRevoked(ctx, "budget-task", limit) })
			return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, "budget-task") // no new run past the task cap (C5)
		}
		// R3: shadow first, a week of numbers before the flag flips.
		record(ctx, func(ctx context.Context) { r.Metrics.RunRevoked(ctx, "budget-task-shadow", limit) })
		r.log().Info("task cap has no room for a whole run (shadow)", "task.id", t.Name, "limit", limit,
			"used", t.Status.Usage.Tokens, "run", t.Spec.Budget.RunTokens, "cap", t.Spec.Budget.TaskTokens,
			"used_micro_usd", t.Status.Usage.CostMicroUSD, "cap_micro_usd", t.Spec.Budget.TaskMicroUSD)
	}
	// R34: the factory's own day, checked before every run it starts, not only by the meter.
	// M-8: an api task's principal that left the config would run in shadow: refuse it first.
	if p := t.Sponsored(); p != "" && cfg.PrincipalBy(p) == nil {
		return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, "principal_unknown")
	}
	limit, sponsored := cfg.Budgets.DailyMicroUSD(runs.PrincipalFactory), r.sponsored(cfg, t)
	if limit > 0 {
		over, err := r.reserveFactoryDay(ctx, cfg, t, limit)
		switch {
		case errors.Is(err, ledger.ErrPersonOverBudget): // D4/D5: the person's day, not the fleet's: refused, not queued
			record(ctx, func(ctx context.Context) { r.Metrics.Revoked(ctx, "budget-person") })
			return r.end(ctx, cfg, t, v1alpha1.PhaseEscalated, "budget-person")
		case errors.Is(err, ledger.ErrOverBudget):
			t.Status.Reason = "waiting_daily_budget" // no new factory run until 00:00 UTC
			return nil
		case errors.Is(err, ledger.ErrMeterBlind): // an enforced cap the meter cannot feed holds (#54)
			t.Status.Reason = "waiting_meter_blind"
			return nil
		case err != nil && (cfg.Budgets.EnforcePrincipal || sponsored):
			return err
		case err != nil: // a shadow cap counts; it never holds a run
			r.log().Warn("daily cap unreserved (shadow)", "task.id", t.Name, "err", err)
		case over != "":
			record(ctx, func(ctx context.Context) { r.Metrics.PrincipalShadow(ctx, over) })
		}
	}
	if len(t.Status.Runs) == 0 {
		// The snapshot, once, in the room of record: later runs read it there, never the live
		// issue (§1, T1). Its clientSeq is the task's room ledger (ruling SK), so a retry reposts
		// the same seq, which the broker keeps once. A room the broker has no log for yet waits.
		err := narrate.Room(ctx, r.Rooms, t, "snapshot", SnapshotMessage(t, r.Nonce()),
			func(ctx context.Context) error { return r.Client.Status().Update(ctx, t) })
		switch {
		case errors.Is(err, rooms.ErrNoRoom):
			t.Status.Reason = "waiting_room_log"
			return nil
		case errors.Is(err, rooms.ErrNotPermitted): // FR-1 not enabled yet: visible, and retried with backoff
			t.Status.Reason = "waiting_broker_permission"
			return err
		case err != nil:
			return err
		}
		// SC3: the note once, after the snapshot, which stays the room's first task_state.
		if t.Spec.Steering != nil {
			if err := narrate.Room(ctx, r.Rooms, t, "steering", SteeringMessage(t),
				func(ctx context.Context) error { return r.Client.Status().Update(ctx, t) }); err != nil {
				return err
			}
		}
	}
	if overlapPending(t) {
		return r.startOverlapScoper(ctx, cfg, t)
	}
	if t.Spec.Kind == v1alpha1.TaskKindIntegration { // MR10: its own spec, never a verifier's or implementerSpec
		return r.startIntegration(ctx, cfg, t)
	}
	if late && t.Status.NextRole != "" {
		return r.startVerifier(ctx, cfg, t, pr)
	}
	// R38: a task of a [triager] template always runs the triager, retry included: no implementer
	// run ever starts from an internal-origin task. A missing template is no team, so it is not a
	// triager one either: the config's validation cannot guard a name removed after a task was
	// triaged.
	if cfg.Investigates(t.Spec.Template) {
		text, ok, err := r.resumeBrief(ctx, t)
		if err != nil {
			return err
		}
		if !ok {
			text = resumed(t, nextTrigger(t)) + TriagerBrief(t, r.Nonce())
		}
		s := r.implementerSpec(cfg, t, text)
		s.Role = "triager"
		return r.startRun(ctx, cfg, t, s, nextTrigger(t))
	}
	if scoperDue(t) {
		return r.startScoper(ctx, cfg, t)
	}
	if r.testerDue(cfg, t) { // after the scoper: its scope belongs in the tester's brief (F3-R1)
		return r.startTester(ctx, cfg, t)
	}
	s, refs, trigger, err := r.nextImplementer(ctx, cfg, t)
	if err != nil {
		return err
	}
	if err := r.startRun(ctx, cfg, t, s, trigger); err != nil {
		return err
	}
	if t.Status.Phase == v1alpha1.PhaseEscalated { // the cluster refused the loop claim: no run exists
		return nil
	}
	if late && current(t).HeadSHA == "" { // the head a revision starts on: Revised lists what it changed since
		current(t).HeadSHA = pr.HeadSHA
	}
	// Consumed once the run exists, and only what its brief quoted (F-A). A failure only means
	// the next brief repeats them.
	if err := r.Rooms.Consume(ctx, t.Status.RoomRef, refs, current(t).ID); err != nil {
		r.log().Warn("queue consume failed", "task.id", t.Name, "run.id", current(t).ID, "err", err)
	}
	return nil
}

// recordConfig stamps the config a task was triaged under: its hash and, beside it, the
// resource generations its repository was served under (D5: recorded, not pinned).
func recordConfig(cfg *config.Config, t *v1alpha1.Task) {
	t.Status.ConfigHash = cfg.Hash
	f, r, p := cfg.GenerationsFor(t.Spec.Repository)
	t.Status.ConfigGenerations = nil
	if f != 0 || r != 0 || p != 0 {
		t.Status.ConfigGenerations = &v1alpha1.ConfigGenerations{Factory: f, Repository: r, MergePolicy: p}
	}
}
