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

// maxCatchUp bounds one tick's read of a room: rooms.Client reads at most 10,000 events a call, so
// a room is read in calls until its last seq. A room not caught up within these calls answers
// nothing this tick and goes on from its cursor on the next.
const maxCatchUp = 10

// writerRoles are the run roles whose outputs the applier answers (hardening.Roles).
var writerRoles = []string{"implementer", "tester"}

// errStopped leaves a request pending: the kill switch engaged before its write.
var errStopped = errors.New("applier: the kill switch is engaged")

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
	IssuesByAuthorSince(ctx context.Context, login string, since time.Time) ([]forge.Issue, error)
}

// Rooms is the broker's system API as the applier uses it; rooms.Client implements it.
type Rooms interface {
	EventsSince(ctx context.Context, room string, afterSeq int64, keep func(envelope.Event) bool) ([]envelope.Event, int64, error)
	LastSeq(ctx context.Context, room string) (int64, error)
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
	// Pushers is the repository's mirror, which inspects a run's bundle and pushes it as the
	// publisher (H3, HR24); PushRooms reads the bundle and the push's approval from the broker. With
	// either nil, a push is refused output_type. ConnectPushers builds Pushers.
	Pushers   func(ctx context.Context, repo string) (Pusher, error)
	PushRooms PushRooms
	// Caps is the task's class caps on a push's cumulative change, changed files and lines; 0 is
	// no cap, a negative value refuses every push push_size, and nil caps nothing (HR25).
	Caps func(t *v1alpha1.Task) (files, lines int)
	// ApprovalTTL is how long a protected-path push waits for a maintainer (DefaultApprovalTTL).
	ApprovalTTL time.Duration
	// Tasks persists a push's intent on its task before the push (AnnotationPushIntent); with nil
	// a push whose reply is lost is refused push_ref on the next tick (HR26's residual).
	Tasks client.Writer
	// AgentsLogin and FactoryLogin are the publisher's and the factory App's logins: the
	// applier recognises its own writes only by them.
	AgentsLogin, FactoryLogin string
	// Interval is the poll (DefaultInterval). AnswerWait is how long after a run ends its requests
	// are still applied (DefaultAnswerWait); for as long again, what is still unanswered is
	// refused output_expired, and then the run is no longer read.
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
	// prs are the room's applied pr outputs, every run's: the ids a pull request the applier may
	// edit names in its footer.
	prs map[string]bool
	// pushed are the room's applied push objects, every run's, lower-cased: the heads the factory
	// itself put on the branch (HR26).
	pushed map[string]bool
	// voided are the push intents ("<output id> <head>") this applier cleared after a definitive
	// refusal: never read as the factory's push, even before the task's record is gone.
	voided map[string]bool
}

type runState struct {
	pending  []request
	answered map[string]bool
	counts   hardening.Counts
	claim    *runs.Run // read once; nil until read
	// waiting is the push held for a maintainer's approval (HR27), lastHeld the one held before,
	// and heldUntil when the last wait ended: the run's answer window counts from there.
	waiting, lastHeld *held
	heldUntil         time.Time
	room              *roomState
}

type request struct {
	rec      envelope.OutputRecord
	at       time.Time
	redacted bool
}

// window is one writer run the applier still reads, and whether its answer wait ran out. late is a
// run past its window that is read only while one of its pushes waits for an approval.
type window struct {
	rec     *v1alpha1.RunRecord
	expired bool
	late    bool
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

// Tick reads every live task's room up to its last seq, then answers what is unanswered. While the
// kill switch is engaged, or cannot be read, nothing is written; a run past its answer wait still
// gets output_expired, which writes nothing.
func (a *Applier) Tick(ctx context.Context) error {
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
		live := a.liveRuns(t, a.rooms[t.Status.RoomRef])
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

// liveRuns are t's writer runs still running or ended at most twice AnswerWait ago, those past
// AnswerWait marked expired. A run's end counts from when its last push wait ended, if later, and
// a run with a push waiting for an approval is live however long ago it ended (HR27). Before its
// room is read, a task the reconciler holds for an approval (push_awaiting_approval) has its
// older runs read late: a restart finds its waiting pushes in the log.
func (a *Applier) liveRuns(t *v1alpha1.Task, st *roomState) map[string]window {
	wait := a.AnswerWait
	if wait <= 0 {
		wait = DefaultAnswerWait
	}
	out := map[string]window{}
	for i := range t.Status.Runs {
		rec := &t.Status.Runs[i]
		if rec.Started == nil || !slices.Contains(writerRoles, rec.Role) {
			continue
		}
		var rs *runState
		if st != nil {
			rs = st.runs[rec.ID]
		}
		if rec.Finished == nil || (rs != nil && rs.waiting != nil) {
			out[rec.ID] = window{rec: rec}
			continue
		}
		end := rec.Finished.Time
		if rs != nil && rs.heldUntil.After(end) {
			end = rs.heldUntil
		}
		switch ago := a.Now().Sub(end); {
		case ago <= wait:
			out[rec.ID] = window{rec: rec}
		case ago <= 2*wait:
			out[rec.ID] = window{rec: rec, expired: true}
		case st == nil && t.Status.Reason == reasonHeld:
			out[rec.ID] = window{rec: rec, late: true}
		}
	}
	return out
}

// stopped is the kill switch, read now: an unreadable one is engaged.
func (a *Applier) stopped(ctx context.Context) bool {
	on, err := killswitch.Engaged(ctx, a.Reader, a.Namespace)
	return err != nil || on
}

// room reads the room to its last seq, then answers each live run's requests in seq order. Nothing
// is answered before the read has caught up: a request's answer may lie anywhere after it, and a
// request answered before its answer is read would be applied again (HR16). A run whose request
// cannot be answered now stops there for this tick, so its caps still count in order.
func (a *Applier) room(ctx context.Context, t *v1alpha1.Task, live map[string]window) error {
	st := a.rooms[t.Status.RoomRef]
	if st == nil {
		st = &roomState{runs: map[string]*runState{}, prs: map[string]bool{}, pushed: map[string]bool{}, voided: map[string]bool{}}
		a.rooms[t.Status.RoomRef] = st
	}
	last, err := a.Rooms.LastSeq(ctx, t.Status.RoomRef)
	if err != nil {
		return fmt.Errorf("read the room's last seq: %w", err)
	}
	for range maxCatchUp {
		if st.cursor >= last {
			break
		}
		evs, cursor, err := a.Rooms.EventsSince(ctx, t.Status.RoomRef, st.cursor,
			func(e envelope.Event) bool {
				return (e.Type == envelope.Message || e.Type == envelope.ApprovalDecided) && e.RunID != ""
			})
		if err != nil {
			return fmt.Errorf("read the room: %w", err)
		}
		st.read(evs)
		if cursor <= st.cursor {
			break // nothing past the cursor, whatever lastSeq said
		}
		st.cursor = cursor
	}
	if st.cursor < last {
		a.Log.Info("output applier: a room is still being read, nothing answered yet", "task.id", t.Name, "cursor", st.cursor, "last", last)
		return nil
	}
	stopped := a.stopped(ctx)
	var errs []error
	for id, rs := range st.runs {
		w, ok := live[id]
		if !ok || (w.late && rs.waiting == nil) {
			continue
		}
		for len(rs.pending) > 0 {
			var err error
			switch {
			case w.expired && rs.waiting == nil:
				err = a.refuse(ctx, t, id, rs, rs.pending[0], hardening.RefuseExpired, rs.counts)
			case stopped:
				err = errStopped
			default:
				err = a.answer(ctx, t, w.rec, rs, rs.pending[0], st.prs)
			}
			if errors.Is(err, errStopped) || errors.Is(err, errLater) {
				break
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("run %s: %w", id, err))
				break
			}
			rs.pending = rs.pending[1:]
		}
	}
	return errors.Join(errs...)
}

// read takes a page of events in: requests join their run's queue, answers close them, a waiting
// record holds its push (HR27), and the store's approval_decided names a held push's approval.
func (st *roomState) read(evs []envelope.Event) {
	for _, e := range evs {
		if e.Type == envelope.ApprovalDecided {
			st.decided(e)
			continue
		}
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
		case envelope.KindOutputWaiting:
			if !rs.answered[rec.ID] && (rs.waiting == nil || rs.waiting.id != rec.ID) {
				rs.hold(rec.ID, rec.ApprovalID, e.TS)
			}
		case envelope.KindOutputApplied, envelope.KindOutputRefused:
			if rs.waiting != nil && rs.waiting.id == rec.ID {
				rs.unhold(rec.ID, e.TS)
			}
			rs.answer(kind, rec)
			if kind == envelope.KindOutputApplied && rec.Type == string(hardening.PR) {
				st.prs[rec.ID] = true
			}
			if kind == envelope.KindOutputApplied && rec.Type == string(hardening.Push) {
				st.pushed[strings.ToLower(rec.Object)] = true
			}
		}
	}
}

// decided marks the held push whose approval an approval_decided event names: the applier reads
// the approval back from the broker before it acts (the event only says when to read). The broker
// accepts approval_decided from the store alone, so a run cannot forge one; a forged one would
// only cause a read.
func (st *roomState) decided(e envelope.Event) {
	var p envelope.ApprovalDecidedPayload
	if json.Unmarshal(e.Payload, &p) != nil {
		return
	}
	if rs := st.runs[e.RunID]; rs != nil && rs.waiting != nil && rs.waiting.approval == p.ApprovalID {
		rs.waiting.decided = true
	}
}

func (st *roomState) run(id string) *runState {
	rs := st.runs[id]
	if rs == nil {
		rs = &runState{answered: map[string]bool{}, room: st}
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

// refuse answers req refused with r and spends n.
func (a *Applier) refuse(ctx context.Context, t *v1alpha1.Task, runID string, rs *runState, req request, r hardening.Refusal, n hardening.Counts,
	audit ...hardening.Finding) error {
	if err := a.post(ctx, t, runID, envelope.KindOutputRefused, envelope.OutputRecord{ID: req.rec.ID, Type: req.rec.Type,
		Digest: req.rec.Digest, Bytes: req.rec.Bytes, Reason: string(r), Audit: findings(audit)}, req); err != nil {
		return err
	}
	rs.done(req, n)
	return nil
}

// answer answers one request. Its own earlier write, found by the output id it carries, is answered
// applied without a second check, so a request never gets both answers. Otherwise it is checked,
// and applied or refused; the kill switch is read again just before the write. A refusal GitHub
// makes for good is answered output_unavailable; any other error leaves it pending for the next
// tick, where the lookup finds a write whose answer was lost (HR16).
func (a *Applier) answer(ctx context.Context, t *v1alpha1.Task, rec *v1alpha1.RunRecord, rs *runState, req request, prs map[string]bool) error {
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
	if !slices.Contains(a.Enforced(t.Spec.Repository), typ) {
		return a.refuse(ctx, t, rec.ID, rs, req, hardening.RefuseType, rs.counts) // the gate serves only enforced types: this guards it
	}
	if typ == hardening.Push {
		return a.answerPush(ctx, t, rec, rs, req)
	}
	n := maps.Clone(rs.counts) // spent only once the answer is posted
	obj, found, err := a.find(ctx, t, rec, typ, req.rec.ID)
	if err != nil {
		return err
	}
	if found {
		n.Take(typ)
		return a.applied(ctx, t, rec.ID, rs, req, n, prs, envelope.OutputRecord{ID: req.rec.ID, Type: req.rec.Type,
			Digest: req.rec.Digest, Bytes: req.rec.Bytes, Object: obj})
	}
	c := a.Checker(t.Spec.Repository)
	t5 := hardening.ModeOff
	if c.Layers.Classifier.AtLeast(hardening.ModeAudit) {
		t5 = hardening.ModeAudit
	}
	c.Layers = hardening.LayersFor(hardening.ModeEnforce, t5)
	hreq := hardening.Request{Type: typ, Payload: req.rec.Payload, OwnIssue: t.Spec.Issue, Canary: rs.claim.Canary}
	v := c.Check(ctx, &n, hreq)
	switch {
	case v.Refusal == hardening.RefuseCanary:
		a.Log.Error("canary leaked: an output request repeated the run's canary", "task.id", t.Name, "run.id", rec.ID,
			"output.id", req.rec.ID, "type", req.rec.Type)
	case v.Refusal == "" && req.redacted:
		// The broker's gitleaks pass rewrote the payload before it was stored: a secret was in it.
		v.Refusal = hardening.RefuseSecret
	}
	if v.Refusal != "" {
		v = c.Measure(ctx, hreq, v) // HR39: T5 reads what another layer refused, in audit
	}
	a.countT5(ctx, t, typ, v.T5)
	if v.Refusal != "" {
		return a.refuse(ctx, t, rec.ID, rs, req, v.Refusal, n, v.Audit...)
	}
	if a.stopped(ctx) {
		return errStopped
	}
	prov := hardening.Provenance{Room: t.Status.RoomRef, Run: rec.ID, Role: rec.Role, Task: t.Name, TaskURL: taskURL(t),
		Model: rs.claim.Model, Output: req.rec.ID}
	obj, refusal, err := a.apply(ctx, t, v.Payload, prov, prs)
	switch {
	case forge.Unappliable(err):
		a.Log.Warn("output applier: GitHub refused an output for good", "task.id", t.Name, "run.id", rec.ID,
			"output.id", req.rec.ID, "type", req.rec.Type, "err", err)
		return a.refuse(ctx, t, rec.ID, rs, req, hardening.RefuseUnavailable, n)
	case err != nil:
		return err
	case refusal != "":
		return a.refuse(ctx, t, rec.ID, rs, req, refusal, n)
	}
	sanitised, err := json.Marshal(v.Payload)
	if err != nil {
		return fmt.Errorf("encode the sanitised payload: %w", err)
	}
	out := envelope.OutputRecord{ID: req.rec.ID, Type: req.rec.Type, Digest: hardening.Digest(sanitised),
		Bytes: min(len(sanitised), envelope.MaxOutputBytes), Object: obj}
	out.Audit = findings(v.Audit)
	return a.applied(ctx, t, rec.ID, rs, req, n, prs, out)
}

// findings is the record's audit list: at most MaxOutputFindings, nil when there is none.
func findings(audit []hardening.Finding) []envelope.OutputFinding {
	var out []envelope.OutputFinding
	for _, f := range audit {
		if len(out) == envelope.MaxOutputFindings {
			break
		}
		out = append(out, envelope.OutputFinding{Layer: string(f.Layer), Reason: string(f.Refusal)})
	}
	return out
}

// t5Metrics counts T5's calls (HR39); fmetrics.Set implements it. A metrics port without it
// records nothing.
type t5Metrics interface {
	HardeningT5(ctx context.Context, typ, result string, drill bool)
}

// countT5 counts one T5 answer on an output of t, labelled by whether t is a drill task: false
// positives are hits on applied outputs of non-drill tasks.
func (a *Applier) countT5(ctx context.Context, t *v1alpha1.Task, typ hardening.OutputType, result string) {
	if m, ok := a.Metrics.(t5Metrics); ok && result != "" {
		m.HardeningT5(ctx, string(typ), result, t.Labels[v1alpha1.LabelDrillInjection] == "true")
	}
}

func (a *Applier) applied(ctx context.Context, t *v1alpha1.Task, runID string, rs *runState, req request, n hardening.Counts,
	prs map[string]bool, out envelope.OutputRecord) error {
	if err := a.post(ctx, t, runID, envelope.KindOutputApplied, out, req); err != nil {
		return err
	}
	rs.done(req, n)
	if out.Type == string(hardening.PR) {
		prs[out.ID] = true
	}
	return nil
}

// post answers in the room and records the wait. The broker keys an answer's replay on its kind
// and output id, so a retried post of the same answer is stored once; one id never gets two kinds
// because a found write is answered applied before any check.
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
