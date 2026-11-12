// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/hardening"
)

// reasonUntraced is the tracer's output_shadowed reason: an agents-App write of an enforced type
// that no output_applied explains (H2, AC2). Its counter is agent_factory_hardening_untraced_total.
const reasonUntraced = "untraced_write"

// traceSlack is how far a label event's GitHub timestamp, kept to the second, may fall outside its
// request and answer: GitHub's clock and the broker's are not the same clock.
const traceSlack = 2 * time.Second

// untracedMetrics is the tracer's counter: fmetrics.Set implements it.
type untracedMetrics interface {
	HardeningUntraced(ctx context.Context, typ string)
}

// outputEntry is one output record, with the run it belongs to and when the broker stored it.
type outputEntry struct {
	run string
	rec envelope.OutputRecord
	at  time.Time
}

// outputLedger is a task's safe outputs as its room holds them, every run's, each read through
// envelope.ReadOutput: the requests the broker stamped as their run's own, in seq order, and the
// factory's answers. An answer is posted for its request's run, so it counts there whenever it
// lands, and a write applied after its run's window is still traced in the next run's.
type outputLedger struct {
	requests map[string]outputEntry
	order    []string // request ids in seq order
	applied  map[string]outputEntry
	answered map[string]bool
	ownIssue int
}

// taskOutputs reads every run's requests and answers from t's room, from its first run's start.
func (r *Reconciler) taskOutputs(ctx context.Context, t *v1alpha1.Task) (outputLedger, error) {
	l := outputLedger{requests: map[string]outputEntry{}, applied: map[string]outputEntry{}, answered: map[string]bool{},
		ownIssue: t.Spec.Issue}
	var from int64
	if len(t.Status.Runs) > 0 {
		from = t.Status.Runs[0].StartSeq
	}
	evs, _, err := r.roomTail(ctx, t.Status.RoomRef, from, func(e envelope.Event) bool {
		return e.Type == envelope.Message && e.RunID != ""
	})
	if err != nil {
		return l, fmt.Errorf("read the task's outputs: %w", err)
	}
	for _, e := range evs {
		kind, o, ok := envelope.ReadOutput(e, e.RunID)
		if !ok {
			continue
		}
		switch kind {
		case envelope.KindOutputRequested:
			if _, seen := l.requests[o.ID]; !seen {
				l.requests[o.ID] = outputEntry{run: e.RunID, rec: o, at: e.TS}
				l.order = append(l.order, o.ID)
			}
		case envelope.KindOutputApplied:
			l.applied[o.ID] = outputEntry{run: e.RunID, rec: o, at: e.TS}
			l.answered[o.ID] = true
		case envelope.KindOutputRefused:
			l.answered[o.ID] = true
		}
	}
	return l, nil
}

// unanswered counts run's requests with neither answer yet.
func (l outputLedger) unanswered(run string) int {
	n := 0
	for _, id := range l.order {
		if l.requests[id].run == run && !l.answered[id] {
			n++
		}
	}
	return n
}

// published is what applied request id published, before the footer or marker (T1 and T2 as the
// applier ran them), when it is of type typ on object.
func (l outputLedger) published(id string, typ hardening.OutputType, object string) (any, bool) {
	a, ok := l.applied[id]
	q, asked := l.requests[id]
	if !ok || !asked || a.rec.Type != string(typ) || !strings.EqualFold(a.rec.Object, object) {
		return nil, false
	}
	return hardening.Published(typ, q.rec.Payload, l.ownIssue)
}

// lastApplied is the id of the newest applied output of typ on object, "" when there is none.
func (l outputLedger) lastApplied(typ hardening.OutputType, object string) string {
	last := ""
	for _, id := range l.order {
		if a, ok := l.applied[id]; ok && a.rec.Type == string(typ) && strings.EqualFold(a.rec.Object, object) {
			last = id
		}
	}
	return last
}

// normal is text as compared: line breaks as \n, trailing blanks trimmed.
func normal(s string) string { return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), " \t\n") }

// traces reports whether an observed agents-App write is exactly what the factory applied, so a
// direct edit of an applied object is never laundered as traced:
//   - a pull request: its footer names the newest applied pr output on it, and its title and body
//     above the footer are that output's, as published;
//   - a comment: it ends with an applied comment output's marker, on its URL, and its body is that
//     output's, as published;
//   - labels: each label one of an applied request's on the same pull request, added between that
//     request and its answer.
//
// An issue never is: the factory App files the issue output, so an agents-App issue is always a
// direct write.
func (l outputLedger) traces(o observed) bool {
	switch o.w.Type {
	case hardening.PR:
		text, id := hardening.SplitFooter(o.raw)
		if id == "" || id != l.lastApplied(hardening.PR, o.w.Object) {
			return false
		}
		p, ok := l.published(id, hardening.PR, o.w.Object)
		pr, isPR := p.(hardening.PRPayload)
		return ok && isPR && o.title == pr.Title && text == normal(hardening.Neutralise(pr.Body))
	case hardening.Comment:
		id, ok := hardening.FindMarker(o.raw)
		if !ok {
			return false
		}
		p, ok := l.published(id, hardening.Comment, o.w.Object)
		c, isComment := p.(hardening.CommentPayload)
		return ok && isComment && normal(o.raw) == normal(hardening.WithMarker(c.Body, id))
	case hardening.Labels:
		var p hardening.LabelsPayload
		if json.Unmarshal(o.w.Payload, &p) != nil || len(p.Add) == 0 {
			return false
		}
		for _, label := range p.Add {
			if !l.labelApplied(label, o.w.Object, o.at) {
				return false
			}
		}
		return true
	}
	return false
}

// labelApplied is whether an applied labels request on object asked for label, and at falls between
// that request and its answer.
func (l outputLedger) labelApplied(label, object string, at time.Time) bool {
	for _, id := range l.order {
		q := l.requests[id]
		a, ok := l.applied[id]
		if !ok || q.rec.Type != string(hardening.Labels) || !strings.EqualFold(a.rec.Object, object) {
			continue
		}
		var p hardening.LabelsPayload
		if json.Unmarshal(q.rec.Payload, &p) != nil || !slices.ContainsFunc(p.Add, func(x string) bool { return strings.EqualFold(x, label) }) {
			continue
		}
		if !at.Before(q.at.Add(-traceSlack)) && !at.After(a.at.Add(traceSlack)) {
			return true
		}
	}
	return false
}

// untracedRecord is an untraced write as the room keeps it: no payload, the write's digest and
// object, reason untraced_write. Its id is derived as a shadow record's, so a replay posts it once.
func untracedRecord(runID string, o observed) envelope.OutputRecord {
	digest := hardening.Digest(o.w.Payload)
	sum := sha256.Sum256([]byte(strings.Join([]string{runID, string(o.w.Type), o.w.Object, digest, reasonUntraced}, "\x00")))
	return envelope.OutputRecord{ID: hex.EncodeToString(sum[:8]), Type: string(o.w.Type), Digest: digest,
		Bytes: min(len(o.w.Payload), envelope.MaxOutputBytes), Object: o.w.Object, Reason: reasonUntraced}
}

// untraced counts and logs one untraced write: bounded facts and the object, never the payload.
func (r *Reconciler) untraced(ctx context.Context, t *v1alpha1.Task, runID string, o observed) {
	if m, ok := r.Metrics.(untracedMetrics); ok {
		m.HardeningUntraced(ctx, string(o.w.Type))
	}
	r.log().Warn("tracer: an agent write of an enforced output type that the factory did not apply", "task.id", t.Name,
		"run.id", runID, "type", string(o.w.Type), "object", o.w.Object)
}

// awaitOutputs holds an ended writer run (implementer or tester) of an enforced repository while
// any of its requests is unanswered, for up to hardening.applier.answerWait from its end (HR19): its
// pull request or comment may still be on its way. true is wait (the task stays where it is and is
// polled again). Past the wait it goes on and logs outputs_unanswered; the applier still answers
// late requests. Once nothing is waited on, a pull request the applier opened for an implementer
// is detected before the run's outcome is read.
func (r *Reconciler) awaitOutputs(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, run runs.Run) (bool, error) {
	cur := current(t)
	if !slices.Contains(shadowRoles, cur.Role) {
		return false, nil
	}
	n, open := r.answersPending(ctx, cfg, t, cur)
	switch {
	case open:
		r.log().Info("the run's outputs are not all answered yet: waiting", "task.id", t.Name, "run.id", cur.ID, "unanswered", n)
		return true, nil
	case n != 0: // -1: the room could not be read within answerWait
		r.log().Warn("outputs_unanswered: the run's end goes on without the factory's answers", "task.id", t.Name, "run.id", cur.ID,
			"unanswered", n, "reason", "outputs_unanswered")
	}
	if cur.Role == "implementer" && t.Status.PullRequest == nil && len(cfg.Hardening.EnforcedFor(t.Spec.Repository)) > 0 {
		return false, r.detectPR(ctx, t, run)
	}
	return false, nil
}

// answersPending is how many of an ended run's requests are unanswered on an enforced repository,
// and whether its answer wait is still open: while it is, neither its end nor its shadow pass (the
// tracer) reads its writes, so a write still being applied is never reported untraced. A room that
// cannot be read is waited on the same way, and never past answerWait: the run's end then goes on
// (n is -1, unknown).
func (r *Reconciler) answersPending(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, rec *v1alpha1.RunRecord) (int, bool) {
	if len(cfg.Hardening.EnforcedFor(t.Spec.Repository)) == 0 || rec.Finished == nil {
		return 0, false
	}
	within := r.Now().Sub(rec.Finished.Time) < cfg.Hardening.Applier.AnswerWait.Duration
	l, err := r.taskOutputs(ctx, t)
	if err != nil {
		r.log().Warn("the run's outputs cannot be read: waiting up to answerWait", "task.id", t.Name, "run.id", rec.ID,
			"within", within, "err", err)
		return -1, within
	}
	n := l.unanswered(rec.ID)
	return n, n > 0 && within
}
