// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/factory/config"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/narrate"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/runs"
)

// maxHandled is the CRD's cap on status.handled.
const maxHandled = 512

// maxReviewers is the CRD's cap on status.pullRequest.reviewers.
const maxReviewers = 16

// nextTrigger is why the next implementer run exists: what sent the task back to Queued, else
// initial for its first run and retry for any later one.
func nextTrigger(t *v1alpha1.Task) string {
	switch {
	case t.Status.NextTrigger != "":
		return t.Status.NextTrigger
	case len(t.Status.Runs) > 0:
		return "retry"
	}
	return "initial"
}

// nextImplementer builds the next implementer run. Before a pull request exists it is the fenced
// snapshot; after, the revise brief with the room's log. Every one of them, a first run, a retry, a
// deeper retry and a resumed run included, quotes the room's queued messages, and refs are the
// queued messages that brief quoted: the only ones the run may consume (F-A).
func (r *Reconciler) nextImplementer(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (runs.Spec, []int64, string, error) {
	trigger := nextTrigger(t)
	text, ok, err := r.resumeBrief(ctx, t)
	if err != nil {
		return runs.Spec{}, nil, "", err
	}
	if ok {
		q, err := r.Rooms.Queue(ctx, t.Status.RoomRef)
		if err != nil {
			return runs.Spec{}, nil, "", err
		}
		text, refs := withQueued(text, q, r.Nonce())
		return r.implementerSpec(cfg, t, text), refs, trigger, nil
	}
	// The scope is read only for a brief that carries it: a resumed replay quotes its lost run's.
	sc, err := r.taskScope(ctx, t)
	if err != nil {
		return runs.Spec{}, nil, "", err
	}
	if t.Status.PullRequest == nil && trigger != TriggerEscalation { // a step continues the run before it, PR or not
		q, err := r.Rooms.Queue(ctx, t.Status.RoomRef)
		if err != nil {
			return runs.Spec{}, nil, "", err
		}
		nonce := r.Nonce()
		text, refs := forThisRun(resumed(t, trigger)+firstBrief(t, nonce, sc), q, nonce)
		return r.implementerSpec(cfg, t, text), refs, trigger, nil
	}
	// The finished run's handoff and verdict are after its start; brief.Build reads only those.
	evs, _, err := r.roomTail(ctx, t.Status.RoomRef, briefSince(t), briefEvent)
	if err != nil {
		return runs.Spec{}, nil, "", err
	}
	q, err := r.Rooms.Queue(ctx, t.Status.RoomRef)
	if err != nil {
		return runs.Spec{}, nil, "", err
	}
	var judge string
	if trigger == TriggerEscalation {
		j, err := r.lastJudge(ctx, t)
		if err != nil {
			return runs.Spec{}, nil, "", err
		}
		judge = judgeBlock(j, r.Nonce(), reviseCap)
	}
	text, refs := r.revisionBrief(ctx, cfg, t, evs, q, sc, judge)
	return r.implementerSpec(cfg, t, resumed(t, trigger)+text), refs, trigger, nil
}

// briefSince is the room seq a revision brief reads after: the start of the run before it. A
// resumed run whose lost run's claim is gone reads after the start of the run before its resumed
// chain, as the lost run's brief did: the room after the lost run's own start holds no verdict.
func briefSince(t *v1alpha1.Task) int64 {
	i := len(t.Status.Runs) - 1
	if nextTrigger(t) == "resume" {
		for i > 0 && t.Status.Runs[i].Trigger == "resume" {
			i--
		}
		i--
	}
	if i < 0 {
		return 0
	}
	return t.Status.Runs[i].StartSeq
}

// maxTailReads bounds roomTail: each EventsSince reads at most 10,000 events.
const maxTailReads = 10

// roomTail pages room's log after afterSeq to its end, keeping only the events keep selects. A
// single EventsSince stops at 10,000 events, so it would miss the newest of a long room. complete
// is false when it stopped at maxTailReads before reading the end: whatever it returns may not
// hold the newest events.
func (r *Reconciler) roomTail(ctx context.Context, room string, after int64, keep func(envelope.Event) bool) ([]envelope.Event, bool, error) {
	var out []envelope.Event
	for range maxTailReads {
		evs, cursor, err := r.Rooms.EventsSince(ctx, room, after, keep)
		if err != nil {
			return nil, false, err
		}
		out = append(out, evs...)
		if cursor <= after {
			return out, true, nil
		}
		after = cursor
	}
	// The factory's rooms are named for their task, so room is the task id here.
	r.log().Warn("room log longer than the factory reads", "task.id", room, "after", after)
	return out, false, nil
}

// briefEvent is what brief.Build quotes from a room's log: handoffs and review verdicts that a run
// recorded itself with its room tools, as the broker stamped them (ruling TB): the run's own
// agent:<run> actor, by the client origin.
func briefEvent(e envelope.Event) bool {
	if e.Origin != envelope.OriginClient || e.Actor.Kind != envelope.ActorAgent || e.Actor.ID != "agent:"+e.RunID {
		return false
	}
	if e.Type == envelope.Handoff {
		return true
	}
	var p struct {
		Kind envelope.MessageKind `json:"kind"`
	}
	return e.Type == envelope.Message && json.Unmarshal(e.Payload, &p) == nil && p.Kind == envelope.KindReviewVerdict
}

// readPR reads the task's pull request, and keeps who wrote and who reviewed it for the room's facts.
func (r *Reconciler) readPR(ctx context.Context, t *v1alpha1.Task) (forge.PR, error) {
	pr, err := r.gh(ctx).PullRequest(ctx, t.Status.PullRequest.Number)
	if err != nil {
		return pr, err
	}
	notePR(t.Status.PullRequest, pr)
	return pr, nil
}

// notePR copies pr's author and reviewers into ref: each reviewer once, oldest first, the first
// maxReviewers.
func notePR(ref *v1alpha1.PullRequestRef, pr forge.PR) {
	var rvs []string
	for _, rv := range pr.Reviews {
		if rv.Author != "" && len(rvs) < maxReviewers && !slices.Contains(rvs, rv.Author) {
			rvs = append(rvs, rv.Author)
		}
	}
	ref.Author, ref.Reviewers = pr.Author, rvs
}

// lateReviews re-reads the pull request just before a run starts. A maintainer's review submitted
// while the task waited in Queued joins this run: after it, the run's start would put the review
// before "since" for good. A dismissed review is skipped like any non-CHANGES_REQUESTED one. Such a
// review wins over a queued verifier: the revision runs instead, and goes back to the maintainer
// (Δ5), so the review is never marked handled behind a verifier that cannot act on it. A pull
// request merged or closed meanwhile is not run on: the task goes back to AwaitingHuman, which
// ends it (done is true). pr is the pull request as read, for the run to start on.
func (r *Reconciler) lateReviews(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (forge.PR, bool, error) {
	pr, err := r.readPR(ctx, t)
	if err != nil {
		return pr, false, err
	}
	if pr.State != "OPEN" {
		t.Status.NextTrigger, t.Status.NextRole = "", ""
		r.to(t, v1alpha1.PhaseAwaitingHuman, "")
		return pr, true, nil
	}
	rvs := r.changesRequested(cfg, t, pr)
	if len(rvs) > 0 && t.Status.NextRole != "" {
		t.Status.NextRole = ""
		return pr, false, r.revise(ctx, cfg, t, pr, rvs)
	}
	return pr, false, r.queueReviews(ctx, t, pr, rvs)
}

// changesRequested are the maintainers' "Request changes" reviews on the task's own pull request
// since its last run started, not yet acted on, oldest id first (Δ5). Anyone else's review is
// advice to a maintainer, not input; so is a review its author later approved over. Every one is
// returned, not the newest: two maintainers' reviews in one round are both the next run's input.
func (r *Reconciler) changesRequested(cfg *config.Config, t *v1alpha1.Task, pr forge.PR) []forge.Review {
	if pr.HeadRef != "agent/"+t.Name { // only the task's branch is the factory's to revise
		return nil
	}
	since := current(t).Started
	var out []forge.Review
	for _, rv := range openChangeRequests(pr.Reviews, false) {
		if slices.Contains(t.Status.Handled, rv.ID) || (since != nil && !rv.At.After(since.Time)) {
			continue
		}
		if !r.repo(cfg, t).IsMaintainer(rv.Author) { // advice to a maintainer, and told so
			r.refuseInput(cfg, t, rv.ID, rv.Author, pr.Number, "your Request changes review")
			continue
		}
		out = append(out, rv)
	}
	slices.SortFunc(out, func(a, b forge.Review) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// answerCommentReviews tells a maintainer once per task that a Comment review starts nothing
// (developer review gap 7): a maintainer whose newest review since the last run started is a comment
// with text, or with inline comments. A review followed by an approval or a request for changes
// needs no answer, and anyone else's review is advice to a maintainer.
func (r *Reconciler) answerCommentReviews(cfg *config.Config, t *v1alpha1.Task, pr forge.PR) {
	if pr.HeadRef != "agent/"+t.Name || len(t.Status.Runs) == 0 {
		return
	}
	since := current(t).Started
	newest := map[string]forge.Review{}
	for _, rv := range pr.Reviews {
		if !r.repo(cfg, t).IsMaintainer(rv.Author) || rv.State == "PENDING" || (since != nil && !rv.At.After(since.Time)) {
			continue
		}
		login := strings.ToLower(rv.Author)
		if cur, ok := newest[login]; !ok || rv.At.After(cur.At) || (rv.At.Equal(cur.At) && rv.ID > cur.ID) {
			newest[login] = rv
		}
	}
	for _, rv := range newest {
		if rv.State == "COMMENTED" && (strings.TrimSpace(rv.Body) != "" || len(rv.Comments) > 0) {
			narrateOn(t, pr.Number, narrate.CommentReviewIgnored(t))
			return
		}
	}
}

// openChangeRequests are the CHANGES_REQUESTED reviews their author has not approved over since,
// oldest first as GitHub lists them: a login's later APPROVED review lifts its earlier requests.
// A ghost's (login "") are never lifted: two deleted accounts are not one reviewer. With
// dismissed, a DISMISSED review counts as a request too: GitHub does not say what it was before,
// and anyone with write access can dismiss, so only the reviewer's own approval lifts it.
func openChangeRequests(reviews []forge.Review, dismissed bool) []forge.Review {
	approvedAt := map[string]int{} // a login's latest approval, by index; logins fold case
	for i, rv := range reviews {
		if rv.State == "APPROVED" && rv.Author != "" {
			approvedAt[strings.ToLower(rv.Author)] = i
		}
	}
	var out []forge.Review
	for i, rv := range reviews {
		last, approved := approvedAt[strings.ToLower(rv.Author)]
		if (rv.State == "CHANGES_REQUESTED" || dismissed && rv.State == "DISMISSED") && (!approved || last < i) {
			out = append(out, rv)
		}
	}
	return out
}

// revise queues the reviews in the room (queueReviews) and sends the task back to Queued for a
// new implementer run: the caps and the human-driver rule (C4) stay in one place.
func (r *Reconciler) revise(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, pr forge.PR, rvs []forge.Review) error {
	if err := r.queueReviews(ctx, t, pr, rvs); err != nil {
		return err
	}
	r.intervention(ctx, cfg, t, "request_changes")
	t.Status.NextTrigger = "human"
	r.to(t, v1alpha1.PhaseQueued, "")
	// A revision the task cap will refuse is not announced: Queued escalates it as budget-task,
	// and the review stays queued in the room for whoever picks the task up.
	if runFits(t) || !cfg.Budgets.EnforceTask {
		narrateOn(t, reviewTarget(t), narrate.Revising(t, rvs[len(rvs)-1].Author))
	}
	return nil
}

// queueReviews enqueues each review in the room, as a sanitised ReviewMessage on the review
// stream keyed by its GitHub id, and records it handled. The broker stores an id once, whatever
// the order, so a replay is safe. A review dismissed after this still reaches the run its queue
// row feeds: the system API cannot remove a queued message.
func (r *Reconciler) queueReviews(ctx context.Context, t *v1alpha1.Task, pr forge.PR, rvs []forge.Review) error {
	for _, rv := range rvs {
		if err := r.Rooms.Enqueue(ctx, t.Status.RoomRef, "review", ReviewMessage(pr, rv), rv.ID); err != nil {
			return err
		}
	}
	for _, rv := range rvs {
		markHandled(t, rv.ID)
	}
	return nil
}

// markHandled records a review or comment id as acted on, keeping the newest maxHandled.
func markHandled(t *v1alpha1.Task, id int64) {
	t.Status.Handled = append(t.Status.Handled, id)
	if over := len(t.Status.Handled) - maxHandled; over > 0 {
		t.Status.Handled = slices.Delete(t.Status.Handled, 0, over)
	}
}

// retrySkew widens "posted since the phase began": c.At is GitHub's clock and PhaseSince the
// factory's, and the two can disagree by seconds. A comment just before the escalation by the
// factory's reckoning still counts; one already acted on is in Status.Handled and never counts twice.
const retrySkew = time.Minute

// cmdRetry asks for a fresh run of an escalated task (§6.3); cmdRetryDeeper steps it up once (WR4);
// cmdRetryRescue explicitly rescues an escalated OpenCode task onto OpenHands in the rescue cohort.
const (
	cmdRetry       = "/factory retry"
	cmdRetryDeeper = "/factory retry deeper"
	cmdRetryRescue = "/factory retry harness=openhands"
)

// command is the newest maintainer comment carrying verb alone on a line, on the task's issue or
// on pr (the zero PR when the task has none), posted since the task entered its phase, or held
// from before it (held.go), and not yet acted on, whose author holds write on the task's
// repository (asked last, of the newest candidate only). Anyone else's command is not acted on
// (T1) and answered so, at most once per person an hour (refuseInput); so is a maintainer's
// without write. An edited comment never counts: its text may not be its author's. A failing
// write check is an error: the command waits for the next reconcile.
func (r *Reconciler) command(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, pr forge.PR, verb string) (forge.Comment, bool, error) {
	all := slices.Clone(pr.Comments)
	prComments := len(all)
	if t.Spec.Issue > 0 {
		cs, err := r.gh(ctx).RecentComments(ctx, t.Spec.Issue)
		if err != nil {
			return forge.Comment{}, false, err
		}
		all = append(all, cs...)
	}
	for _, h := range heldComments(t, verb) { // a held comment counts even once it is past the 50 newest
		if !slices.ContainsFunc(all, func(c forge.Comment) bool { return c.ID == h.ID }) {
			all = append(all, h)
		}
	}
	var found forge.Comment
	ok := false
	for i, c := range all {
		if c.Edited || slices.Contains(t.Status.Handled, c.ID) || !commandLine(c.Body, verb) ||
			(t.Status.PhaseSince != nil && !c.At.After(t.Status.PhaseSince.Add(-retrySkew)) && !isHeld(t, c.ID)) ||
			forge.SameLogin(c.Author, cfg.FactoryLogin) {
			continue
		}
		if !r.repo(cfg, t).IsMaintainer(c.Author) {
			on := target(t)
			if i < prComments {
				on = pr.Number
			}
			r.refuseInput(cfg, t, c.ID, c.Author, on, "`"+verb+"`")
			continue
		}
		if !ok || c.At.After(found.At) {
			found, ok = c, true
		}
	}
	if !ok || r.CanWrite == nil {
		return forge.Comment{}, false, nil
	}
	w, err := r.CanWrite(ctx, t.Spec.Repository, found.Author)
	if err != nil {
		return forge.Comment{}, false, fmt.Errorf("write check of %s: %w", found.Author, err)
	}
	if !w {
		r.refuseInput(cfg, t, found.ID, found.Author, target(t), "`"+verb+"`")
	}
	return found, w, nil
}

// retryCommand reads a sponsored or human retry annotation (SI23, WR4) first, then a
// `/factory retry` or `/factory retry deeper` comment, holding all to the same maintainer and
// GitHub write checks.
func (r *Reconciler) retryCommand(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, pr forge.PR) (forge.Comment, bool, bool, error) {
	if c, deeper, ok, err := r.retryAnnotation(ctx, cfg, t); err != nil || ok {
		return c, deeper, ok, err
	}
	cd, okD, err := r.command(ctx, cfg, t, pr, cmdRetryDeeper)
	if err != nil {
		return forge.Comment{}, false, false, err
	}
	cp, okP, err := r.command(ctx, cfg, t, pr, cmdRetry)
	if err != nil {
		return forge.Comment{}, false, false, err
	}
	switch {
	case okD && okP:
		if cd.At.After(cp.At) {
			return cd, true, true, nil
		}
		return cp, false, true, nil
	case okD:
		return cd, true, true, nil
	case okP:
		return cp, false, true, nil
	}
	return forge.Comment{}, false, false, nil
}

func (r *Reconciler) retryAnnotation(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) (forge.Comment, bool, bool, error) {
	raw := t.Annotations[v1alpha1.AnnotationRetry]
	if raw == "" {
		return forge.Comment{}, false, false, nil
	}
	genStr, rest, ok := strings.Cut(raw, ":")
	gen, err := strconv.ParseInt(genStr, 10, 32)
	if !ok || err != nil || int32(gen) != t.Status.Retries || rest == "" {
		return forge.Comment{}, false, false, nil
	}
	login, extra, hasExtra := strings.Cut(rest, ":")
	deeper := hasExtra && extra == "deeper"
	if login == "" || (hasExtra && !deeper) {
		return forge.Comment{}, false, false, nil
	}
	id := retryAnnotationID(raw)
	if slices.Contains(t.Status.Handled, id) || !r.repo(cfg, t).IsMaintainer(login) || r.CanWrite == nil {
		return forge.Comment{}, false, false, nil
	}
	w, err := r.CanWrite(ctx, t.Spec.Repository, login)
	if err != nil {
		return forge.Comment{}, false, false, fmt.Errorf("write check of %s: %w", login, err)
	}
	return forge.Comment{ID: id, Author: login}, deeper, w, nil
}

func retryAnnotationID(raw string) int64 {
	sum := sha256.Sum256([]byte(raw))
	return -int64(binary.BigEndian.Uint64(sum[:8])&0x7fffffffffffffff) - 1
}

// commandLine: some line of body outside a code fence is exactly verb, from its first character;
// trailing blanks aside. A quote ("> /factory retry"), a longer word ("/factory retrying") or a
// fenced example of the command is not the command.
func commandLine(body, verb string) bool {
	fenced := false
	for _, line := range strings.Split(body, "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			fenced = !fenced
			continue
		}
		if !fenced && strings.TrimRight(line, " \t\r") == verb {
			return true
		}
	}
	return false
}

// escalated waits for a maintainer (§6.3): a pull request merged or closed meanwhile ends the
// task; a /factory retry sends it back for a fresh run, through Queued and its caps. The command
// is marked handled in the same status write as the move, and no run is created here, so a replay
// after a lost write repeats the move; the next run's deterministic id (R48) keeps a replay from
// ever starting a second one.
func (r *Reconciler) escalated(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	var pr forge.PR
	if t.Status.PullRequest != nil {
		var err error
		if pr, err = r.readPR(ctx, t); err != nil {
			return err
		}
		if ended, err := r.prEnded(ctx, cfg, t, pr); ended || err != nil {
			return err
		}
	}
	if done, err := r.escalatedSplit(ctx, cfg, t, pr); done || err != nil {
		return err
	}
	// #628: a return the factory cannot read is no return: it never blocks a retry or the expiry.
	if back, err := r.escalatedReturn(ctx, cfg, t, pr); err != nil {
		r.log().Warn("merge gate return unreadable", "task.id", t.Name, "reason", t.Status.Reason, "err", err)
	} else if back {
		return nil
	}
	// Explicit maintainer OpenHands rescue on an escalated OpenCode task: record the failed
	// original attempt under its original cohort/harness, then move to the rescue cohort on
	// OpenHands while preserving workflow, branch, and room.
	if p := t.Status.Profile; p != nil && p.Harness == config.HarnessOpenCode {
		if rc, okR, err := r.command(ctx, cfg, t, pr, cmdRetryRescue); err != nil {
			return err
		} else if okR {
			if err := r.resolveRetried(ctx, cfg, t); err != nil {
				return err
			}
			r.countRescueOriginalFailure(ctx, cfg, t)
			p.Harness = config.HarnessOpenHands
			p.HarnessSource = "rescue"
			p.Cohort = "rescue"
			p.Counted = false
			markHandled(t, rc.ID)
			r.leaveSplit(ctx, t, SplitDeclined)
			if t.Status.Reason == ReasonNeedsAuthor && t.Status.Scope != nil {
				r.leaveBounce(ctx, t, "retry")
				t.Status.Scope.State = "assumed"
			}
			after := t.Status.Reason
			t.Status.Retries++
			t.Status.RetriedBy = v1alpha1.Person(rc.Author)
			t.Status.NextTrigger = "retry"
			r.intervention(ctx, cfg, t, "retry")
			r.to(t, retryPhase(t), "")
			narrateLater(t, narrate.Retrying(t, rc.Author, after))
			return nil
		}
	}
	c, deeper, ok, err := r.retryCommand(ctx, cfg, t, pr)
	if err != nil {
		return err
	}
	if !ok {
		return r.expireEscalation(ctx, cfg, t)
	}
	if deeper {
		return r.stepUpBy(ctx, cfg, t, c.Author, c.ID)
	}
	if RetryHeld(cfg, t) {
		// F-4: Queued would only escalate it again. The command is answered and handled; the
		// escalation's clock keeps running, and Retries counts only retries that can run. No run
		// is next, so no trigger is either: a stale one would read as a retry under way.
		markHandled(t, c.ID)
		t.Status.NextTrigger = ""
		narrateLater(t, narrate.TaskCapHolds(t, c.ID))
		return nil
	}
	if err := r.resolveRetried(ctx, cfg, t); err != nil {
		return err
	}
	markHandled(t, c.ID)
	r.leaveSplit(ctx, t, SplitDeclined) // SL4 (b): the task runs whole on its scope
	if t.Status.Reason == ReasonNeedsAuthor {
		r.leaveBounce(ctx, t, "retry")
		if t.Status.Scope != nil {
			t.Status.Scope.State = "assumed" // SC8 (b): no second scoper run
		}
	}
	after := t.Status.Reason
	t.Status.Retries++
	t.Status.RetriedBy = v1alpha1.Person(c.Author) // D2: the retrier pays for the runs the retry causes
	t.Status.NextTrigger = "retry"
	r.intervention(ctx, cfg, t, "retry")
	r.to(t, retryPhase(t), "") // escalated before it had a room (foreign_room), or at intake: triage it again
	narrateLater(t, narrate.Retrying(t, c.Author, after))
	return nil
}

// noop handles a maintainer's `/factory retry deeper` (or deeper retry annotation) on a NoOp task,
// stepping it up once and queueing a fresh implementer run on the same room and branch (WR4).
func (r *Reconciler) noop(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	c, deeper, ok, err := r.retryCommand(ctx, cfg, t, forge.PR{})
	if err != nil || !ok || !deeper {
		return err
	}
	return r.stepUpBy(ctx, cfg, t, c.Author, c.ID)
}

// expireEscalation stops a task that has waited Escalated past caps.escalatedHours (#61), unless
// a person drives its room (C4): the factory never acts on a task a human holds, so such a task
// never expires while they drive. The PR is left as it is (no stale label, no close). The room
// closes by the rooms' own close rules once the task has ended, not here.
func (r *Reconciler) expireEscalation(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) error {
	limit := cfg.EscalatedLimit()
	// No PhaseSince (a task from before the field) never expires: there is nothing to count from.
	if limit <= 0 || t.Status.PhaseSince == nil || r.Now().Sub(t.Status.PhaseSince.Time) < limit {
		return nil
	}
	if t.Status.RoomRef != "" {
		var room roomv1.Room
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: t.Status.RoomRef}, &room); client.IgnoreNotFound(err) != nil {
			return err
		} else if err == nil && rooms.HumanDriver(&room) {
			return nil
		}
	}
	r.leaveBounce(ctx, t, "expired")
	r.leaveSplit(ctx, t, SplitExpired)
	return r.end(ctx, cfg, t, v1alpha1.PhaseStopped, "escalation_expired")
}

// labelStale marks a pull request the factory closed for want of a review (§6.3).
const labelStale = "factory/stale"

// quietSince is when maintainer activity on the pull request last happened: the task's entry into
// AwaitingHuman; a later review or comment of a maintainer on the pull request, or in thread (the
// comments where the task narrates, read only once the wait is long); a head commit without an
// Agent-Run trailer, so not an agent's (a takeover). It never goes back before a spell a reminder
// was already keyed on: a comment that leaves GitHub's read window must not restart an older one.
func (r *Reconciler) quietSince(cfg *config.Config, t *v1alpha1.Task, pr forge.PR, thread []forge.Comment) time.Time {
	since := t.Status.PhaseSince.Time
	if at, ok := narrate.RemindedSince(t); ok && at.After(since) {
		since = at
	}
	for _, rv := range pr.Reviews {
		if r.repo(cfg, t).IsMaintainer(rv.Author) && rv.At.After(since) {
			since = rv.At
		}
	}
	for _, c := range slices.Concat(pr.Comments, thread) {
		if r.repo(cfg, t).IsMaintainer(c.Author) && c.At.After(since) {
			since = c.At
		}
	}
	if pr.Trailer("Agent-Run") == "" && pr.HeadCommittedAt.After(since) {
		since = pr.HeadCommittedAt
	}
	return since
}

// approved: the latest maintainer review that decides (approve, request changes or a dismissal)
// approves. Such a pull request waits for a merge, which until the wave only a human makes.
func (r *Reconciler) approved(cfg *config.Config, t *v1alpha1.Task, pr forge.PR) bool {
	last := ""
	for _, rv := range pr.Reviews {
		if r.repo(cfg, t).IsMaintainer(rv.Author) && rv.State != "COMMENTED" && rv.State != "PENDING" {
			last = rv.State
		}
	}
	return last == "APPROVED"
}

// thread is the comments where the task narrates: its issue's newest, else its pull request's.
func (r *Reconciler) thread(ctx context.Context, t *v1alpha1.Task, pr forge.PR) ([]forge.Comment, error) {
	if t.Spec.Issue > 0 {
		return r.gh(ctx).RecentComments(ctx, t.Spec.Issue)
	}
	return pr.Comments, nil
}

// postedAt is when the factory posted the comment carrying key's marker, if thread still has it.
func (r *Reconciler) postedAt(cfg *config.Config, t *v1alpha1.Task, thread []forge.Comment, key string) (time.Time, bool) {
	marker := narrate.Marker(t.Name, key)
	for _, c := range thread {
		if forge.SameLogin(c.Author, cfg.FactoryLogin) && strings.Contains(c.Body, marker) {
			return c.At, true
		}
	}
	return time.Time{}, false
}

// remind nudges the maintainers once a pull request has had no maintainer activity for
// RemindAfter, and closes it with factory/stale after StaleAfter (§6.3). Every close is:
//   - on the task's own branch, and never of a pull request a maintainer approved;
//   - after StaleAfter of maintainer silence (quietSince), the reply to a reminder included;
//   - at least reminderNotice after that spell's reminder was posted, as found in the thread.
//
// The reminder goes through the outbox, written before it is posted, so a replay posts it once.
// The label goes on before the close, so a replay after a lost status write still reads the
// closed pull request as stale (prEnded); a refused close takes the label off again.
func (r *Reconciler) remind(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, pr forge.PR) error {
	if t.Status.PhaseSince == nil || pr.HeadRef != "agent/"+t.Name {
		return nil
	}
	if r.Now().Sub(r.quietSince(cfg, t, pr, nil)) < RemindAfter { // spares the thread read
		return nil
	}
	thread, err := r.thread(ctx, t, pr)
	if err != nil {
		return err
	}
	since := r.quietSince(cfg, t, pr, thread)
	waited := r.Now().Sub(since)
	if waited < RemindAfter {
		return nil
	}
	approved := r.approved(cfg, t, pr)
	reminder := narrate.Reminder(t, r.repo(cfg, t).Maintainers, since, approved)
	posted, ok := r.postedAt(cfg, t, thread, reminder.Key)
	if approved || waited < StaleAfter || !ok || r.Now().Sub(posted) < reminderNotice {
		narrateLater(t, reminder)
		return nil
	}
	if err := r.gh(ctx).AddLabels(ctx, pr.Number, labelStale); err != nil {
		return err
	}
	if err := r.gh(ctx).ClosePR(ctx, pr.Number); err != nil {
		if rerr := r.gh(ctx).RemoveLabel(ctx, pr.Number, labelStale); rerr != nil {
			r.log().Warn("stale label left on an open pull request", "task.id", t.Name, "pr", pr.Number, "err", rerr)
		}
		return err
	}
	class := t.Spec.PredictedClass
	record(ctx, func(ctx context.Context) { r.Metrics.PROutcome(ctx, class, "closed") })
	return r.end(ctx, cfg, t, v1alpha1.PhaseClosed, "stale")
}
