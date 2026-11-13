// SPDX-License-Identifier: Apache-2.0

package applier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/gitbundle"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/hardening"
)

// Approval defaults (HR27): how long a protected-path push waits for a maintainer, and the most it
// may.
const (
	DefaultApprovalTTL = 24 * time.Hour
	MaxApprovalTTL     = 7 * 24 * time.Hour
)

// approvalPoll is how often a waiting push's approval is re-read when no approval_decided event
// named it, and approvalSlack how long past its deadline a pending one still is: the broker's
// sweep closes it expired, and a decision past the deadline is refused (store.Decide).
const (
	approvalPoll  = 5 * time.Minute
	approvalSlack = time.Minute
)

// reasonHeld is the reconciler's task reason while a push waits for a maintainer (HR29): a task
// ended past the run window still has its room read while it holds it, a restart included.
const reasonHeld = "push_awaiting_approval"

// errLater leaves a request pending for the next tick without an error: a push whose lease was
// lost, or a push still waiting for its approval.
var errLater = errors.New("applier: answered on a later tick")

// Pusher inspects a run's bundle and pushes it as the publisher; *gitbundle.Mirror implements it.
// Discard drops what an inspection fetched, for a push the applier refuses or holds after it.
type Pusher interface {
	Inspect(ctx context.Context, bundle []byte, branch, base string) (gitbundle.Inspection, error)
	Push(ctx context.Context, in gitbundle.Inspection) error
	Discard(ctx context.Context, in gitbundle.Inspection)
}

// AnnotationPushIntent records, on the task, the push the applier is about to make: "<output id>
// <head>", written before git push (#505's review). A push whose reply is lost (a timeout, a
// crash, a restart) lands with no answer; the next inspection reads the branch at that head, and
// this record, not the room's applied pushes, says the factory put it there, so it is answered
// applied rather than refused push_ref and reported untraced_write. Only the latest push is kept:
// a run's requests are answered one at a time, in order. A push the remote definitively refused
// (a lost lease, a rejection) clears it, so another hand's push of the same head is never read as
// the factory's (#513's review); only an ambiguous failure (a timeout, a reset) keeps it.
const AnnotationPushIntent = "agents.ogenki.io/push-intent"

// intended reports whether the task records the factory's own push of head for output id, and
// this applier has not cleared that record since.
func (a *Applier) intended(t *v1alpha1.Task, rs *runState, id, head string) bool {
	return t.Annotations[AnnotationPushIntent] == id+" "+head && !rs.room.voided[id+" "+head]
}

// clearIntent forgets the push's intent after the remote definitively refused it: in memory at
// once, and on the task. A task that cannot be written keeps a record this applier still ignores;
// the error leaves the request pending, so the next tick writes it again.
func (a *Applier) clearIntent(ctx context.Context, t *v1alpha1.Task, rs *runState, id, head string) error {
	rs.room.voided[id+" "+head] = true
	if t.Annotations[AnnotationPushIntent] != id+" "+head {
		return nil
	}
	orig := t.DeepCopy()
	delete(t.Annotations, AnnotationPushIntent)
	if a.Tasks == nil {
		return nil
	}
	if err := a.Tasks.Patch(ctx, t, client.MergeFrom(orig)); err != nil {
		*t = *orig
		return fmt.Errorf("clear the push's intent: %w", err)
	}
	return nil
}

// recordIntent writes the push's intent on the task before the push. Without a writer it is set
// on this tick's copy of the task alone, which the next tick's read replaces: the factory sets one.
func (a *Applier) recordIntent(ctx context.Context, t *v1alpha1.Task, rs *runState, id, head string) error {
	delete(rs.room.voided, id+" "+head) // this push begins again
	if t.Annotations[AnnotationPushIntent] == id+" "+head {
		return nil
	}
	orig := t.DeepCopy()
	if t.Annotations == nil {
		t.Annotations = map[string]string{}
	}
	t.Annotations[AnnotationPushIntent] = id + " " + head
	if a.Tasks == nil {
		return nil
	}
	if err := a.Tasks.Patch(ctx, t, client.MergeFrom(orig)); err != nil {
		*t = *orig
		return fmt.Errorf("record the push's intent: %w", err)
	}
	return nil
}

// PushRooms is the broker's half of a push (HR22, HR28); rooms.Client implements it.
type PushRooms interface {
	Bundle(ctx context.Context, room, runID, digest string) ([]byte, error)
	RequestApproval(ctx context.Context, room string, req rooms.ApprovalRequest) (rooms.OutputApproval, error)
	SupersedeApproval(ctx context.Context, id string) (string, error)
	Approval(ctx context.Context, id string) (rooms.OutputApproval, error)
}

// approvalWaitMetrics is the histogram of how long a protected-path push waited; fmetrics.Set
// implements it. A metrics port without it records nothing.
type approvalWaitMetrics interface {
	HardeningApprovalWait(ctx context.Context, decision string, d time.Duration)
}

// held is a push request the applier answered output_waiting (HR27): its approval, when it began
// waiting, whether an approval_decided event named its approval since the last read, and when the
// approval is read again regardless.
type held struct {
	id, approval string
	since        time.Time
	decided      bool
	checkAt      time.Time
}

// pushAction is what an approval card and the room page show of a push: never commit text.
type pushAction struct {
	Output    string   `json:"output"`
	Digest    string   `json:"digest"`
	Base      string   `json:"base"`
	Head      string   `json:"head"`
	Protected []string `json:"protected"`
}

// approvalTTL is ApprovalTTL within (0, MaxApprovalTTL], DefaultApprovalTTL when unset.
func (a *Applier) approvalTTL() time.Duration {
	if a.ApprovalTTL <= 0 || a.ApprovalTTL > MaxApprovalTTL {
		return DefaultApprovalTTL
	}
	return a.ApprovalTTL
}

// commitURL is a pushed head's object.
func commitURL(t *v1alpha1.Task, head string) string {
	return fmt.Sprintf("https://github.com/%s/commit/%s", t.Spec.Repository, head)
}

// answerPush answers the run's head push request. A push waiting for its approval answers only
// once the approval closes, or a later push of the run supersedes it; until then the run's later
// requests wait behind it (errLater).
func (a *Applier) answerPush(ctx context.Context, t *v1alpha1.Task, rec *v1alpha1.RunRecord, rs *runState, req request) error {
	if a.Pushers == nil || a.PushRooms == nil {
		return a.refuse(ctx, t, rec.ID, rs, req, hardening.RefuseType, rs.counts) // push without a pusher
	}
	if w := rs.waiting; w != nil && w.id == req.rec.ID {
		return a.waitingPush(ctx, t, rec, rs, req)
	}
	return a.checkPush(ctx, t, rec, rs, req, false)
}

// waitingPush settles a waiting push: superseded by a later push of the run, or its approval read
// again when an event named it or its check is due, and answered once it is closed.
func (a *Applier) waitingPush(ctx context.Context, t *v1alpha1.Task, rec *v1alpha1.RunRecord, rs *runState, req request) error {
	w := rs.waiting
	later := slices.ContainsFunc(rs.pending[1:], func(r request) bool { return r.rec.Type == string(hardening.Push) })
	var (
		ap  rooms.OutputApproval
		err error
	)
	switch {
	case later:
		var state string
		if state, err = a.PushRooms.SupersedeApproval(ctx, w.approval); err != nil {
			return fmt.Errorf("supersede the approval: %w", err)
		}
		if state == "superseded" {
			a.approvalClosed(ctx, state, w)
			n := maps.Clone(rs.counts)
			n.Take(hardening.Push)
			return a.refusePush(ctx, t, rec.ID, rs, req, hardening.RefuseSuperseded, n)
		}
		ap, err = a.PushRooms.Approval(ctx, w.approval) // a decision came first: it stands
	case w.decided || !a.Now().Before(w.checkAt):
		ap, err = a.PushRooms.Approval(ctx, w.approval)
	default:
		return errLater
	}
	if err != nil {
		return fmt.Errorf("read the approval: %w", err)
	}
	w.decided = false
	if ap.ApprovalID != w.approval || ap.RoomID != t.Status.RoomRef || ap.RunID != rec.ID || ap.Output != req.rec.ID ||
		ap.Digest != req.rec.Digest || ap.Branch != branch(t) {
		a.Log.Error("output applier: an approval does not bind the push it holds: refused", "task.id", t.Name, "run.id", rec.ID,
			"output.id", req.rec.ID, "approval.id", w.approval)
		return a.closePush(ctx, t, rec, rs, req, "denied", hardening.RefuseApprovalDenied)
	}
	switch ap.State {
	case "approved":
		err := a.checkPush(ctx, t, rec, rs, req, true)
		if err == nil {
			a.approvalClosed(ctx, ap.State, w)
		}
		return err
	case "denied":
		return a.closePush(ctx, t, rec, rs, req, ap.State, hardening.RefuseApprovalDenied)
	case "expired":
		return a.closePush(ctx, t, rec, rs, req, ap.State, hardening.RefuseApprovalExpired)
	case "superseded":
		return a.closePush(ctx, t, rec, rs, req, ap.State, hardening.RefuseSuperseded)
	}
	if !ap.ExpiresAt.IsZero() && a.Now().After(ap.ExpiresAt.Add(approvalSlack)) {
		return a.closePush(ctx, t, rec, rs, req, "expired", hardening.RefuseApprovalExpired)
	}
	w.checkAt = a.Now().Add(approvalPoll)
	if !ap.ExpiresAt.IsZero() {
		w.checkAt = minTime(w.checkAt, ap.ExpiresAt.Add(approvalSlack))
	}
	return errLater
}

func minTime(x, y time.Time) time.Time {
	if y.Before(x) {
		return y
	}
	return x
}

// closePush refuses a waiting push whose approval closed without approving it; the refusal spends
// the push's cap, as a refusal past T1 does.
func (a *Applier) closePush(ctx context.Context, t *v1alpha1.Task, rec *v1alpha1.RunRecord, rs *runState, req request,
	decision string, r hardening.Refusal) error {
	n := maps.Clone(rs.counts)
	n.Take(hardening.Push)
	if err := a.refusePush(ctx, t, rec.ID, rs, req, r, n); err != nil {
		return err
	}
	a.approvalClosed(ctx, decision, rs.lastHeld)
	return nil
}

// refusePush is refuse for a push, which also ends its wait.
func (a *Applier) refusePush(ctx context.Context, t *v1alpha1.Task, runID string, rs *runState, req request, r hardening.Refusal,
	n hardening.Counts, audit ...hardening.Finding) error {
	if err := a.refuse(ctx, t, runID, rs, req, r, n, audit...); err != nil {
		return err
	}
	rs.unhold(req.rec.ID, a.Now())
	return nil
}

func (a *Applier) approvalClosed(ctx context.Context, decision string, w *held) {
	if m, ok := a.Metrics.(approvalWaitMetrics); ok && w != nil {
		m.HardeningApprovalWait(ctx, decision, a.Now().Sub(w.since))
	}
}

// checkPush reads the request's bundle from the broker, inspects it in the repository's mirror,
// checks it (T1 to T4 enforced, T5 at most audit), and pushes it with the lease. A push touching a
// path that needs approval, not approved yet, is answered output_waiting and requests a
// maintainer's approval of its digest. approved is a maintainer's approval read back for it.
func (a *Applier) checkPush(ctx context.Context, t *v1alpha1.Task, rec *v1alpha1.RunRecord, rs *runState, req request, approved bool) error {
	n := maps.Clone(rs.counts) // spent only once the answer is posted
	refuse := func(r hardening.Refusal) error {
		if !n.Take(hardening.Push) && r != hardening.RefuseCap {
			r = hardening.RefuseCap
		}
		return a.refusePush(ctx, t, rec.ID, rs, req, r, n)
	}
	if req.rec.Bytes > hardening.MaxBundle {
		return refuse(hardening.RefusePushSize)
	}
	bundle, err := a.PushRooms.Bundle(ctx, t.Status.RoomRef, rec.ID, req.rec.Digest)
	switch {
	case errors.Is(err, rooms.ErrNoBundle), errors.Is(err, rooms.ErrBundleDigest):
		a.Log.Warn("output applier: the push's bundle cannot be read from the broker", "task.id", t.Name, "run.id", rec.ID,
			"output.id", req.rec.ID, "err", err)
		return refuse(hardening.RefuseUnavailable)
	case err != nil:
		return fmt.Errorf("read the push's bundle: %w", err)
	}
	if len(bundle) > hardening.MaxBundle {
		return refuse(hardening.RefusePushSize)
	}
	p, err := a.Pushers(ctx, t.Spec.Repository)
	if err != nil {
		return fmt.Errorf("connect the pusher: %w", err)
	}
	in, err := p.Inspect(ctx, bundle, branch(t), baseRef)
	var refused *gitbundle.Refused
	switch {
	case errors.As(err, &refused):
		a.Log.Info("output applier: a push's bundle refused", "task.id", t.Name, "run.id", rec.ID, "output.id", req.rec.ID,
			"reason", string(refused.Reason))
		return refuse(refused.Reason)
	case err != nil:
		return fmt.Errorf("inspect the push's bundle: %w", err)
	}
	pushed := false
	defer func() {
		if !pushed { // a refusal, a hold or a retry after the inspection: its fetched ref goes
			p.Discard(ctx, in)
		}
	}()
	if in.OldHead != "" && in.OldHead == in.Head {
		// HR26: the branch is at the bundle's head already. Answered applied with no push only when
		// an applied push of the task put that head there, or the task records this very push as
		// begun and no definitive refusal cleared it (its reply lost); otherwise another hand pushed
		// it, and an answer would launder that write for the tracer.
		if !rs.room.pushed[strings.ToLower(commitURL(t, in.Head))] && !a.intended(t, rs, req.rec.ID, in.Head) {
			a.Log.Warn("output applier: the branch is at the push's head already, and no applied push put it there",
				"task.id", t.Name, "run.id", rec.ID, "output.id", req.rec.ID)
			return refuse(hardening.RefusePushRef)
		}
		if !n.Take(hardening.Push) {
			return refuse(hardening.RefuseCap)
		}
		return a.pushApplied(ctx, t, rec.ID, rs, req, n, in, nil)
	}
	if files, lines := a.caps(t); files < 0 || lines < 0 || (files > 0 && in.Cumulative.Files > files) ||
		(lines > 0 && in.Cumulative.Lines > lines) {
		return refuse(hardening.RefusePushSize)
	}
	c := a.Checker(t.Spec.Repository)
	t5 := hardening.ModeOff
	if c.Layers.Classifier.AtLeast(hardening.ModeAudit) {
		t5 = hardening.ModeAudit
	}
	c.Layers = hardening.LayersFor(hardening.ModeEnforce, t5)
	payload, err := json.Marshal(hardening.PushPayload{Bundle: bundle})
	if err != nil {
		return fmt.Errorf("encode the push: %w", err)
	}
	paths := make([]string, 0, len(in.Files))
	for _, f := range in.Files {
		paths = append(paths, f.Path)
	}
	hreq := hardening.Request{Type: hardening.Push, Payload: payload, Files: paths, Added: in.Added,
		Approved: approved, Canary: rs.claim.Canary}
	v := c.Check(ctx, &n, hreq)
	if v.Refusal != "" {
		v = c.Measure(ctx, hreq, v) // HR39: T5 reads a T4 refusal's added lines, already past T3, in audit
	}
	a.countT5(ctx, t, hardening.Push, v.T5)
	switch v.Refusal {
	case "":
	case hardening.RefuseCanary:
		a.Log.Error("canary leaked: an output request repeated the run's canary", "task.id", t.Name, "run.id", rec.ID,
			"output.id", req.rec.ID, "type", req.rec.Type)
		return a.refusePush(ctx, t, rec.ID, rs, req, v.Refusal, n, v.Audit...)
	case hardening.RefuseNeedsApproval:
		return a.holdPush(ctx, t, rec, rs, req, in, c.Paths)
	default:
		return a.refusePush(ctx, t, rec.ID, rs, req, v.Refusal, n, v.Audit...)
	}
	if a.stopped(ctx) {
		return errStopped
	}
	if err := a.recordIntent(ctx, t, rs, req.rec.ID, in.Head); err != nil {
		return err
	}
	err = p.Push(ctx, in)
	pushed = err == nil
	if errors.Is(err, gitbundle.ErrLeaseLost) || errors.Is(err, gitbundle.ErrPushRejected) {
		// Definitive: the push did not land, so its intent goes (#513's review).
		if cerr := a.clearIntent(ctx, t, rs, req.rec.ID, in.Head); cerr != nil {
			return cerr
		}
	}
	switch {
	case errors.Is(err, gitbundle.ErrLeaseLost):
		// The next tick inspects again: a branch moved to this very head by another hand is then
		// refused, never answered applied (HR26).
		a.Log.Info("output applier: the branch moved since it was read: the push is retried", "task.id", t.Name,
			"run.id", rec.ID, "output.id", req.rec.ID)
		return errLater
	case errors.Is(err, gitbundle.ErrPushRejected):
		a.Log.Warn("output applier: GitHub refused a push for good", "task.id", t.Name, "run.id", rec.ID,
			"output.id", req.rec.ID, "err", err)
		return a.refusePush(ctx, t, rec.ID, rs, req, hardening.RefuseUnavailable, n)
	case err != nil:
		return fmt.Errorf("push: %w", err)
	}
	return a.pushApplied(ctx, t, rec.ID, rs, req, n, in, v.Audit)
}

func (a *Applier) caps(t *v1alpha1.Task) (files, lines int) {
	if a.Caps == nil {
		return 0, 0
	}
	return a.Caps(t)
}

func (a *Applier) pushApplied(ctx context.Context, t *v1alpha1.Task, runID string, rs *runState, req request, n hardening.Counts,
	in gitbundle.Inspection, audit []hardening.Finding) error {
	out := envelope.OutputRecord{ID: req.rec.ID, Type: req.rec.Type, Digest: req.rec.Digest, Bytes: req.rec.Bytes,
		Object: commitURL(t, in.Head)}
	for _, f := range audit {
		if len(out.Audit) == envelope.MaxOutputFindings {
			break
		}
		out.Audit = append(out.Audit, envelope.OutputFinding{Layer: string(f.Layer), Reason: string(f.Refusal)})
	}
	if err := a.applied(ctx, t, runID, rs, req, n, nil, out); err != nil {
		return err
	}
	rs.room.pushed[strings.ToLower(out.Object)] = true
	rs.unhold(req.rec.ID, a.Now())
	return nil
}

// holdPush asks the room's approvers to approve the bundle's digest, then answers output_waiting
// with the approval's id (HR27). The push's cap is spent only by its answer, applied or refused.
func (a *Applier) holdPush(ctx context.Context, t *v1alpha1.Task, rec *v1alpha1.RunRecord, rs *runState, req request,
	in gitbundle.Inspection, paths hardening.ProtectedPaths) error {
	var protected []string
	for _, f := range in.Files {
		if on, ok := paths.Match(f.Path); ok && on == hardening.OnApproval {
			protected = append(protected, f.Path)
		}
	}
	ap, err := a.PushRooms.RequestApproval(ctx, t.Status.RoomRef, rooms.ApprovalRequest{RunID: rec.ID, Output: req.rec.ID,
		Digest: req.rec.Digest, Branch: branch(t), TTL: a.approvalTTL(),
		Action: pushAction{Output: req.rec.ID, Digest: req.rec.Digest, Base: in.BaseHead, Head: in.Head, Protected: protected}})
	if err != nil {
		return fmt.Errorf("request the push's approval: %w", err)
	}
	out := envelope.OutputRecord{ID: req.rec.ID, Type: req.rec.Type, Digest: req.rec.Digest, Bytes: req.rec.Bytes,
		Reason: envelope.WaitingReason, ApprovalID: ap.ApprovalID}
	if err := a.Rooms.Output(ctx, t.Status.RoomRef, rec.ID, envelope.KindOutputWaiting, out); err != nil {
		return fmt.Errorf("post the output_waiting answer: %w", err)
	}
	a.Log.Info("output applier: a push touches a protected path and waits for a maintainer", "task.id", t.Name,
		"run.id", rec.ID, "output.id", req.rec.ID, "approval.id", ap.ApprovalID, "protected", len(protected))
	rs.hold(req.rec.ID, ap.ApprovalID, a.Now())
	if ap.State != "pending" { // a retried request answers an approval already decided
		rs.waiting.decided = true
	}
	return errLater
}

// hold records a push waiting for approval; unhold ends the wait of the push id, if it is the one
// waiting, keeping when it ended: the run's answer window starts again from there.
func (rs *runState) hold(id, approval string, since time.Time) {
	rs.waiting = &held{id: id, approval: approval, since: since}
}

func (rs *runState) unhold(id string, at time.Time) {
	if rs.waiting == nil || rs.waiting.id != id {
		return
	}
	rs.lastHeld = rs.waiting
	rs.waiting = nil
	if at.After(rs.heldUntil) {
		rs.heldUntil = at
	}
}
