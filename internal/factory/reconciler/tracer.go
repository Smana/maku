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

// outputEntry is one output record of a run, with when the broker stored it.
type outputEntry struct {
	rec envelope.OutputRecord
	at  time.Time
}

// outputLedger is one run's safe outputs as its room holds them, each read through
// envelope.ReadOutput: the requests the broker stamped as the run's own, in seq order, and the
// factory's answers.
type outputLedger struct {
	requests []outputEntry
	applied  map[string]outputEntry
	answered map[string]bool
}

// runOutputs reads rec's requests and answers from its room, from the seq it started at.
func (r *Reconciler) runOutputs(ctx context.Context, t *v1alpha1.Task, rec *v1alpha1.RunRecord) (outputLedger, error) {
	l := outputLedger{applied: map[string]outputEntry{}, answered: map[string]bool{}}
	evs, _, err := r.roomTail(ctx, t.Status.RoomRef, rec.StartSeq, func(e envelope.Event) bool {
		return e.Type == envelope.Message && e.RunID == rec.ID
	})
	if err != nil {
		return l, fmt.Errorf("read the run's outputs: %w", err)
	}
	for _, e := range evs {
		kind, o, ok := envelope.ReadOutput(e, rec.ID)
		if !ok {
			continue
		}
		switch kind {
		case envelope.KindOutputRequested:
			l.requests = append(l.requests, outputEntry{rec: o, at: e.TS})
		case envelope.KindOutputApplied:
			l.applied[o.ID] = outputEntry{rec: o, at: e.TS}
			l.answered[o.ID] = true
		case envelope.KindOutputRefused:
			l.answered[o.ID] = true
		}
	}
	return l, nil
}

// unanswered counts the run's requests with neither answer yet.
func (l outputLedger) unanswered() int {
	n := 0
	for _, q := range l.requests {
		if !l.answered[q.rec.ID] {
			n++
		}
	}
	return n
}

// traces reports whether an observed agents-App write is one the factory applied: a pull request
// by its URL, a comment by its URL and the marker it ends with, labels by name and time (each label
// one of an applied request's, added between that request and its answer). An issue never is: the
// factory App files the issue output, so an agents-App issue is always a direct write.
func (l outputLedger) traces(o observed) bool {
	switch o.w.Type {
	case hardening.PR:
		for _, a := range l.applied {
			if a.rec.Type == string(hardening.PR) && strings.EqualFold(a.rec.Object, o.w.Object) {
				return true
			}
		}
	case hardening.Comment:
		var c hardening.CommentPayload
		if json.Unmarshal(o.w.Payload, &c) != nil {
			return false
		}
		id, ok := hardening.FindMarker(c.Body)
		a, applied := l.applied[id]
		return ok && applied && a.rec.Type == string(hardening.Comment) && strings.EqualFold(a.rec.Object, o.w.Object)
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
	for _, q := range l.requests {
		a, ok := l.applied[q.rec.ID]
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

// awaitOutputs holds an ended implementer run of an enforced repository while any of its requests
// is unanswered, for up to hardening.applier.answerWait from its end (HR19): its pull request may
// still be on its way. true is wait (the task stays where it is and is polled again). Past the wait
// it goes on and logs outputs_unanswered; the applier still answers late requests. Once nothing is
// waited on, a pull request the applier opened is detected before the run's outcome is read.
func (r *Reconciler) awaitOutputs(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, run runs.Run) (bool, error) {
	cur := current(t)
	if cur.Role != "implementer" {
		return false, nil
	}
	n, open, err := r.answersPending(ctx, cfg, t, cur)
	switch {
	case err != nil:
		return false, err
	case open:
		r.log().Info("the run's outputs are not all answered yet: waiting", "task.id", t.Name, "run.id", cur.ID, "unanswered", n)
		return true, nil
	case n > 0:
		r.log().Warn("outputs_unanswered: the run's end goes on without the factory's answers", "task.id", t.Name, "run.id", cur.ID,
			"unanswered", n, "reason", "outputs_unanswered")
	}
	if t.Status.PullRequest == nil && len(cfg.Hardening.EnforcedFor(t.Spec.Repository)) > 0 {
		return false, r.detectPR(ctx, t, run)
	}
	return false, nil
}

// answersPending is how many of an ended run's requests are unanswered on an enforced repository,
// and whether its answer wait is still open: while it is, neither its end nor its shadow pass (the
// tracer) reads its writes, so a write still being applied is never reported untraced.
func (r *Reconciler) answersPending(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, rec *v1alpha1.RunRecord) (int, bool, error) {
	if len(cfg.Hardening.EnforcedFor(t.Spec.Repository)) == 0 || rec.Finished == nil {
		return 0, false, nil
	}
	l, err := r.runOutputs(ctx, t, rec)
	if err != nil {
		return 0, false, err
	}
	n := l.unanswered()
	return n, n > 0 && r.Now().Sub(rec.Finished.Time) < cfg.Hardening.Applier.AnswerWait.Duration, nil
}
