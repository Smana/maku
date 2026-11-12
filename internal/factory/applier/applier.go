// SPDX-License-Identifier: Apache-2.0

// Package applier is the safe-output applier (hardening H2, UH3 to UH5): a factory runnable that
// tails the rooms of tasks with a writer run, checks each output_requested the broker stamped as
// the run's own, applies it through the publisher (the agents App, through octo-sts) or the
// factory App, and answers it with exactly one output_applied or output_refused. Only the leader
// runs it. Nothing in it is reachable until PR 3's enforceOutputs config starts it.
package applier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/fmetrics"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/killswitch"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/hardening"
)

// Defaults (HR15, HR19).
const (
	DefaultInterval   = 2 * time.Second
	DefaultAnswerWait = 5 * time.Minute
)

// writerRoles are the run roles whose outputs the applier answers (hardening.Roles).
var writerRoles = []string{"implementer", "tester"}

// Publisher is the agents App's connection to one repository, its token from PublisherTokens
// (HR11): it opens and edits the task's pull request, comments, and labels. *forge.GitHub
// implements it.
type Publisher interface {
	PullRequestForBranch(ctx context.Context, branch string) (int, error)
	PullRequest(ctx context.Context, number int) (forge.PR, error)
	CreatePR(ctx context.Context, base, head, title, body string, draft bool) (forge.PR, error)
	EditPR(ctx context.Context, number int, title, body string) (forge.PR, error)
	RecentComments(ctx context.Context, number int) ([]forge.Comment, error)
	CommentWithID(ctx context.Context, number int, body string) (int64, error)
	AddLabels(ctx context.Context, number int, labels ...string) error
}

// Filer is the factory App's connection to one repository: the issue output is filed in its voice,
// factory/proposed (HR11, v4 D12). The reconciler's TaskForge implements it.
type Filer interface {
	CreateIssue(ctx context.Context, title, body string, labels []string) (forge.Issue, error)
	IssuesLabelledSince(ctx context.Context, label string, since time.Time) ([]forge.Issue, error)
}

// Rooms is the broker's system API as the applier uses it; rooms.Client implements it.
type Rooms interface {
	EventsSince(ctx context.Context, room string, afterSeq int64, keep func(envelope.Event) bool) ([]envelope.Event, int64, error)
	Output(ctx context.Context, room, runID string, kind envelope.MessageKind, rec envelope.OutputRecord) error
}

// Runs reads a run's claim: its canary (HR7) and model; runs.Client implements it.
type Runs interface {
	Get(ctx context.Context, id string) (runs.Run, bool, error)
}

// Metrics is the applier's latency histogram; fmetrics.Set implements it.
type Metrics interface {
	HardeningOutput(ctx context.Context, typ string, d time.Duration)
}

// Applier is the runnable. Every function field takes the task's repository.
type Applier struct {
	Reader    client.Reader // Tasks and the kill switch, in Namespace
	Namespace string
	Rooms     Rooms
	Runs      Runs
	// Publisher is the repository's agents-App connection; ConnectPublishers builds it.
	Publisher func(ctx context.Context, repo string) (Publisher, error)
	// Factory is the repository's factory-App connection, nil when it has none.
	Factory func(repo string) Filer
	// Checker is the repository's threat check: its paths, secrets, classifier and label
	// allowlist. Its layers are replaced: T1 to T4 enforce, T5 at most audit (HR10).
	Checker func(repo string) hardening.Checker
	// Enforced is the repository's enforced output types; an empty list answers nothing.
	Enforced func(repo string) []hardening.OutputType
	// AgentsLogin and FactoryLogin are the publisher's and the factory App's logins: the
	// applier recognises its own writes only by them.
	AgentsLogin, FactoryLogin string
	// Interval is the poll (DefaultInterval); AnswerWait how long after a run ends its requests
	// are still answered (DefaultAnswerWait).
	Interval, AnswerWait time.Duration
	Now                  func() time.Time
	Metrics              Metrics
	Log                  *slog.Logger

	rooms map[string]*roomState
}

// roomState is what the applier has read of one room: its cursor and, per run, the requests in
// seq order, the answered ids and the counts the answers spent.
type roomState struct {
	cursor int64
	runs   map[string]*runState
}

type runState struct {
	pending  []request
	answered map[string]bool
	counts   hardening.Counts
	claim    *runs.Run // read once; nil until read
}

type request struct {
	rec      envelope.OutputRecord
	at       time.Time
	redacted bool
}

// NeedLeaderElection is true: two replicas would apply the same request twice.
func (a *Applier) NeedLeaderElection() bool { return true }

// Start polls at once, then every Interval, until ctx ends. A failed tick is logged and the next
// one retries.
func (a *Applier) Start(ctx context.Context) error {
	every := a.Interval
	if every <= 0 {
		every = DefaultInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := a.Tick(ctx); err != nil {
			a.Log.Warn("output applier tick failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick reads every live task's room once and answers what is unanswered. Nothing is applied while
// the kill switch is engaged, or when it cannot be read.
func (a *Applier) Tick(ctx context.Context) error {
	if on, err := killswitch.Engaged(ctx, a.Reader, a.Namespace); err != nil || on {
		return err
	}
	var l v1alpha1.TaskList
	if err := a.Reader.List(ctx, &l, client.InNamespace(a.Namespace)); err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}
	if a.rooms == nil {
		a.rooms = map[string]*roomState{}
	}
	seen := map[string]bool{}
	var errs []error
	for i := range l.Items {
		t := &l.Items[i]
		live := a.liveRuns(t)
		if len(live) == 0 || t.Status.RoomRef == "" || len(a.Enforced(t.Spec.Repository)) == 0 {
			continue
		}
		seen[t.Status.RoomRef] = true
		if err := a.room(fmetrics.WithRepository(ctx, t.Spec.Repository), t, live); err != nil {
			errs = append(errs, fmt.Errorf("task %s: %w", t.Name, err))
		}
	}
	for room := range a.rooms {
		if !seen[room] {
			delete(a.rooms, room) // no live run: a later one re-reads the room from its start
		}
	}
	return errors.Join(errs...)
}

// liveRuns are t's writer runs still running, or ended at most AnswerWait ago.
func (a *Applier) liveRuns(t *v1alpha1.Task) map[string]*v1alpha1.RunRecord {
	wait := a.AnswerWait
	if wait <= 0 {
		wait = DefaultAnswerWait
	}
	out := map[string]*v1alpha1.RunRecord{}
	for i := range t.Status.Runs {
		rec := &t.Status.Runs[i]
		if rec.Started == nil || !slices.Contains(writerRoles, rec.Role) {
			continue
		}
		if rec.Finished == nil || a.Now().Sub(rec.Finished.Time) <= wait {
			out[rec.ID] = rec
		}
	}
	return out
}

// room reads the room past its cursor, then answers each live run's requests in seq order. A run
// whose request cannot be answered now stops there for this tick, so its caps still count in
// order.
func (a *Applier) room(ctx context.Context, t *v1alpha1.Task, live map[string]*v1alpha1.RunRecord) error {
	st := a.rooms[t.Status.RoomRef]
	if st == nil {
		st = &roomState{runs: map[string]*runState{}}
		a.rooms[t.Status.RoomRef] = st
	}
	evs, cursor, err := a.Rooms.EventsSince(ctx, t.Status.RoomRef, st.cursor,
		func(e envelope.Event) bool { return e.Type == envelope.Message && e.RunID != "" })
	if err != nil {
		return fmt.Errorf("read the room: %w", err)
	}
	for _, e := range evs {
		kind, rec, ok := envelope.ReadOutput(e, e.RunID)
		if !ok {
			continue
		}
		rs := st.run(e.RunID)
		switch kind {
		case envelope.KindOutputRequested:
			if !rs.answered[rec.ID] && !slices.ContainsFunc(rs.pending, func(r request) bool { return r.rec.ID == rec.ID }) {
				rs.pending = append(rs.pending, request{rec: rec, at: e.TS, redacted: len(e.Redactions) > 0})
			}
		case envelope.KindOutputApplied, envelope.KindOutputRefused:
			rs.answer(kind, rec)
		}
	}
	st.cursor = cursor
	var errs []error
	for id, rs := range st.runs {
		rec, ok := live[id]
		if !ok {
			continue
		}
		for len(rs.pending) > 0 {
			if err := a.answer(ctx, t, rec, rs, rs.pending[0]); err != nil {
				errs = append(errs, fmt.Errorf("run %s: %w", id, err))
				break
			}
			rs.pending = rs.pending[1:]
		}
	}
	return errors.Join(errs...)
}

func (st *roomState) run(id string) *runState {
	rs := st.runs[id]
	if rs == nil {
		rs = &runState{answered: map[string]bool{}}
		st.runs[id] = rs
	}
	return rs
}

// answer records an answer read from the log: the request is done, and it spent its type's cap
// unless T1 refused it before counting (type, schema, cap), as Checker.Check counts.
func (rs *runState) answer(kind envelope.MessageKind, rec envelope.OutputRecord) {
	if rs.answered[rec.ID] {
		return
	}
	rs.answered[rec.ID] = true
	rs.pending = slices.DeleteFunc(rs.pending, func(r request) bool { return r.rec.ID == rec.ID })
	switch hardening.Refusal(rec.Reason) {
	case hardening.RefuseType, hardening.RefuseSchema, hardening.RefuseCap:
		if kind == envelope.KindOutputRefused {
			return
		}
	}
	rs.counts.Take(hardening.OutputType(rec.Type))
}

// done is req answered by the applier itself: its counts are n, and its answer, read back on a
// later tick, counts nothing again.
func (rs *runState) done(req request, n hardening.Counts) {
	rs.counts = n
	rs.answered[req.rec.ID] = true
}

// answer checks one request and applies or refuses it, then posts the answer. An error leaves it
// pending: the next tick finds its own write by its marker, so nothing is written twice (HR16).
func (a *Applier) answer(ctx context.Context, t *v1alpha1.Task, rec *v1alpha1.RunRecord, rs *runState, req request) error {
	if rs.claim == nil {
		x, found, err := a.Runs.Get(ctx, rec.ID)
		if err != nil {
			return fmt.Errorf("read the run's claim: %w", err)
		}
		if !found {
			a.Log.Warn("output applier: the run's claim is gone, its canary unknown", "task.id", t.Name, "run.id", rec.ID)
		}
		rs.claim = &x
	}
	typ := hardening.OutputType(req.rec.Type)
	refuse := func(r hardening.Refusal) error {
		return a.post(ctx, t, rec.ID, envelope.KindOutputRefused, envelope.OutputRecord{ID: req.rec.ID, Type: req.rec.Type,
			Digest: req.rec.Digest, Bytes: req.rec.Bytes, Reason: string(r)}, req)
	}
	if !slices.Contains(a.Enforced(t.Spec.Repository), typ) || typ == hardening.Push {
		if err := refuse(hardening.RefuseType); err != nil { // the gate serves only enforced types: this guards it
			return err
		}
		rs.done(req, rs.counts)
		return nil
	}
	c := a.Checker(t.Spec.Repository)
	t5 := hardening.ModeOff
	if c.Layers.Classifier.AtLeast(hardening.ModeAudit) {
		t5 = hardening.ModeAudit
	}
	c.Layers = hardening.LayersFor(hardening.ModeEnforce, t5)
	n := maps.Clone(rs.counts) // spent only once the answer is posted
	v := c.Check(ctx, &n, hardening.Request{Type: typ, Payload: req.rec.Payload, OwnIssue: t.Spec.Issue, Canary: rs.claim.Canary})
	switch {
	case v.Refusal == hardening.RefuseCanary:
		a.Log.Error("canary leaked: an output request repeated the run's canary", "task.id", t.Name, "run.id", rec.ID,
			"output.id", req.rec.ID, "type", req.rec.Type)
	case v.Refusal == "" && req.redacted:
		// The broker's gitleaks pass rewrote the payload before it was stored: a secret was in it.
		v.Refusal = hardening.RefuseSecret
	}
	if v.Refusal != "" {
		if err := refuse(v.Refusal); err != nil {
			return err
		}
		rs.done(req, n)
		return nil
	}
	prov := hardening.Provenance{Room: t.Status.RoomRef, Run: rec.ID, Role: rec.Role, Task: t.Name, TaskURL: taskURL(t),
		Model: rs.claim.Model, Output: req.rec.ID}
	obj, refusal, err := a.apply(ctx, t, rec, v.Payload, prov)
	if err != nil {
		return err
	}
	if refusal != "" {
		if err := refuse(refusal); err != nil {
			return err
		}
		rs.done(req, n)
		return nil
	}
	sanitised, err := json.Marshal(v.Payload)
	if err != nil {
		return fmt.Errorf("encode the sanitised payload: %w", err)
	}
	out := envelope.OutputRecord{ID: req.rec.ID, Type: req.rec.Type, Digest: hardening.Digest(sanitised),
		Bytes: min(len(sanitised), envelope.MaxOutputBytes), Object: obj}
	for _, f := range v.Audit {
		if len(out.Audit) == envelope.MaxOutputFindings {
			break
		}
		out.Audit = append(out.Audit, envelope.OutputFinding{Layer: string(f.Layer), Reason: string(f.Refusal)})
	}
	if err := a.post(ctx, t, rec.ID, envelope.KindOutputApplied, out, req); err != nil {
		return err
	}
	rs.done(req, n)
	return nil
}

// post answers in the room and records the wait. The broker keeps one answer per output id, so a
// retried post never writes a second.
func (a *Applier) post(ctx context.Context, t *v1alpha1.Task, runID string, kind envelope.MessageKind, out envelope.OutputRecord, req request) error {
	if err := a.Rooms.Output(ctx, t.Status.RoomRef, runID, kind, out); err != nil {
		return fmt.Errorf("post the %s answer: %w", kind, err)
	}
	if a.Metrics != nil {
		a.Metrics.HardeningOutput(ctx, out.Type, a.Now().Sub(req.at))
	}
	return nil
}

// taskURL is the task's issue on GitHub, "" for a text task.
func taskURL(t *v1alpha1.Task) string {
	if t.Spec.Issue < 1 {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/issues/%d", t.Spec.Repository, t.Spec.Issue)
}

// branch is the task's one branch (C3).
func branch(t *v1alpha1.Task) string { return "agent/" + t.Name }

// sameLogin is forge.SameLogin with an empty want matching nothing.
func sameLogin(got, want string) bool {
	return want != "" && forge.SameLogin(strings.TrimSpace(got), want)
}
