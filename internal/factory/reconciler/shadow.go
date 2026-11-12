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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/hardening"
)

// shadowMetrics is the shadow pass's counter: fmetrics.Set implements it. A metrics port without
// it records nothing, so the pass never needs every fake to know it.
type shadowMetrics interface {
	HardeningShadow(ctx context.Context, typ, verdict, layer, reason string)
}

// shadowRoles are the writer runs whose GitHub writes the shadow pass reads back.
var shadowRoles = []string{"implementer", "tester"}

// Bounds on the shadow pass. A failed pass is retried after a doubling wait from the run's end
// (0, 1, 3, 7, 15 minutes); at maxShadowAttempts the run is marked shadowed and reported
// incomplete, so no write that cannot be read keeps a run in the retry loop. T5 gets at most
// shadowT5Budget per pass, and none after its first failure in the pass.
const (
	maxShadowAttempts = 5
	shadowT5Budget    = time.Minute
)

// Shadow's bounded outcomes beyond hardening's verdicts.
const (
	shadowIncomplete = "incomplete"        // the metric's verdict for a run given up on
	reasonIncomplete = "shadow_incomplete" // and its reason
)

// errT5Skipped is the pass's classifier answer once T5 failed or spent its budget: Checker reads it
// as classifier_unavailable, which audit records and never acts on.
var errT5Skipped = errors.New("agent-guard: skipped for the rest of the pass")

// shadowPending runs the shadow pass on every ended writer run of t that started under audit and is
// not shadowed yet (H1, HR1). It is called once a writer run is terminal, before its outcome is
// handled, before a stop deletes the claims, and on every later pass of a live task, which retries
// a failed one after its wait. A failure is logged and never moves the task.
func (r *Reconciler) shadowPending(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) {
	if !r.auditing(cfg, t) {
		return
	}
	for i := range t.Status.Runs {
		rec := &t.Status.Runs[i]
		if rec.Shadowed || !rec.Audited || rec.Finished == nil || rec.Started == nil || !slices.Contains(shadowRoles, rec.Role) {
			continue
		}
		if wait := time.Duration(1<<rec.ShadowAttempts-1) * time.Minute; r.Now().Before(rec.Finished.Add(wait)) {
			continue
		}
		if _, open := r.answersPending(ctx, cfg, t, rec); open {
			continue // H2: the tracer reads the run's writes once the applier has answered (HR19)
		}
		err := r.shadowRun(ctx, cfg, t, rec)
		if err == nil {
			rec.Shadowed = true
			continue
		}
		rec.ShadowAttempts++
		r.log().Warn("shadow pass failed", "task.id", t.Name, "run.id", rec.ID, "attempt", rec.ShadowAttempts, "err", err)
		if rec.ShadowAttempts >= maxShadowAttempts {
			rec.Shadowed = true
			r.log().Warn("shadow pass incomplete: the run's unread writes are not shadowed", "task.id", t.Name, "run.id", rec.ID)
			if m, ok := r.Metrics.(shadowMetrics); ok {
				m.HardeningShadow(ctx, "", shadowIncomplete, "", reasonIncomplete)
			}
		}
	}
}

// shadowBeforeStop shadows a stopped task's writer runs while their claims, and so their canaries,
// still exist: a run still going is shadowed up to now.
func (r *Reconciler) shadowBeforeStop(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) {
	if !r.auditing(cfg, t) {
		return
	}
	for i := range t.Status.Runs {
		if rec := &t.Status.Runs[i]; rec.Audited && rec.Finished == nil {
			now := metav1.NewTime(r.Now())
			rec.Finished = &now
		}
	}
	r.shadowPending(ctx, cfg, t)
}

// observed is one write read back, with when it happened: the writes are checked in time order,
// so the run's caps count as they would have.
type observed struct {
	at time.Time
	w  hardening.ObservedWrite
	// title and raw are the write as GitHub holds it, footer and marker included: the tracer
	// compares them with what the applier published.
	title, raw string
	// node is the pull request's or comment's GraphQL id, which its last editor is read by, and
	// edited whether its body may have changed since it was posted (always, for a pull request).
	node   string
	edited bool
}

// shadowRun reads back the writes run made as the agents App inside its window, checks each as the
// request it would have sent (every layer in audit), and posts one output_shadowed record per write.
// A write or a source that cannot be read, or a record the room refuses, is skipped and reported
// in the error; every other write is still posted. A replay posts the same ids, which the broker
// keeps once.
func (r *Reconciler) shadowRun(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, rec *v1alpha1.RunRecord) error {
	from, to := rec.Started.Time, rec.Finished.Add(runEndGrace)
	in := func(at time.Time) bool { return !at.Before(from) && !at.After(to) }
	ws, errs := r.observedWrites(ctx, cfg, t, rec, in)
	canary := ""
	if x, found, err := r.Runs.Get(ctx, rec.ID); err != nil {
		return errors.Join(append(errs, fmt.Errorf("read the run's claim: %w", err))...)
	} else if found {
		canary = x.Canary
	}
	c := r.Hardening.Checker
	c.Paths = r.repositoryPolicy(cfg, t).Paths
	c.LabelAllowlist = cfg.Hardening.LabelAllowlist
	c.Layers = hardening.LayersFor(hardening.ModeAudit, cfg.Hardening.EffectiveClassifier())
	if c.Classifier != nil {
		t5, cancel := context.WithTimeout(ctx, shadowT5Budget)
		defer cancel()
		c.Classifier = &passClassifier{c: c.Classifier, ctx: t5}
	}
	slices.SortStableFunc(ws, func(a, b observed) int { return a.at.Compare(b.at) })
	// H2's tracer: on an enforced repository, a write of an enforced type is exactly what the applier
	// published, and is not checked again, or it is untraced: recorded untraced_write, then checked as
	// in H1 like every push and every type not enforced. An applied write only people edited since
	// (humanEdit) is not recorded untraced, but its text is still checked.
	enforced := cfg.Hardening.EnforcedFor(t.Spec.Repository)
	var ledger outputLedger
	if len(enforced) > 0 {
		l, err := r.taskOutputs(ctx, t)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		ledger = l
	}
	var n hardening.Counts
	for _, o := range ws {
		if o.w.Type != hardening.Push && slices.Contains(enforced, o.w.Type) {
			if ledger.traces(o) {
				continue
			}
			if !r.humanEdit(ctx, cfg, t, rec.ID, ledger, o) {
				if err := r.Rooms.Output(ctx, t.Status.RoomRef, rec.ID, envelope.KindOutputShadowed, untracedRecord(rec.ID, o)); err != nil &&
					!errors.Is(err, rooms.ErrBadMessage) {
					errs = append(errs, fmt.Errorf("post an untraced %s: %w", o.w.Type, err))
					continue
				}
				r.untraced(ctx, t, rec.ID, o)
			}
		}
		res := hardening.Shadow(ctx, c, &n, o.w, t.Spec.Issue, canary)
		out := shadowRecord(rec.ID, res, len(o.w.Payload))
		err := r.Rooms.Output(ctx, t.Status.RoomRef, rec.ID, envelope.KindOutputShadowed, out)
		switch {
		case errors.Is(err, rooms.ErrBadMessage):
			// Permanent for this record (the broker does not take this run in this room, or the
			// record): retrying cannot change the answer, so it is logged and skipped, never retried.
			r.log().Warn("shadow record refused by the broker", "task.id", t.Name, "run.id", rec.ID, "type", string(res.Type), "err", err)
			continue
		case err != nil:
			errs = append(errs, fmt.Errorf("post a %s shadow: %w", res.Type, err))
			continue
		}
		r.shadowObserved(ctx, t, rec.ID, res)
	}
	return errors.Join(errs...)
}

// passClassifier bounds T5 within one pass: every call shares ctx's deadline, and after the first
// failure the rest of the pass answers errT5Skipped without calling (MaxConcurrentReconciles is 1:
// a slow classifier would hold every task).
type passClassifier struct {
	c    hardening.Classifier
	ctx  context.Context //nolint:containedctx // the pass's T5 budget, which outlives no pass
	down bool
}

func (p *passClassifier) Injection(_ context.Context, texts []string) (bool, error) {
	if p.down || p.ctx.Err() != nil {
		return false, errT5Skipped
	}
	hit, err := p.c.Injection(p.ctx, texts)
	if err != nil {
		p.down = true
	}
	return hit, err
}

// shadowRecord is res as the room keeps it: no payload, the deciding refusal as the reason, or
// would_wait for a protected path awaiting approval (HR3), and every other finding as audit. Its id
// is derived from the run, the type, the object and the digest, so a replay posts the same one.
func shadowRecord(runID string, res hardening.ShadowResult, size int) envelope.OutputRecord {
	sum := sha256.Sum256([]byte(strings.Join([]string{runID, string(res.Type), res.Object, res.Digest}, "\x00")))
	out := envelope.OutputRecord{ID: hex.EncodeToString(sum[:8]), Type: string(res.Type), Digest: res.Digest,
		Bytes: min(size, envelope.MaxOutputBytes), Object: res.Object}
	switch res.Verdict {
	case hardening.WouldRefuse:
		out.Reason = string(res.Reason)
	case hardening.WouldWait:
		out.Reason = string(hardening.WouldWait)
	}
	for _, f := range res.Audit {
		if len(out.Audit) == envelope.MaxOutputFindings {
			break
		}
		out.Audit = append(out.Audit, envelope.OutputFinding{Layer: string(f.Layer), Reason: string(f.Refusal)})
	}
	return out
}

// shadowObserved counts a record and logs a would-refuse or would-wait: bounded facts and the
// object's URL, never the payload.
func (r *Reconciler) shadowObserved(ctx context.Context, t *v1alpha1.Task, runID string, res hardening.ShadowResult) {
	if m, ok := r.Metrics.(shadowMetrics); ok {
		m.HardeningShadow(ctx, string(res.Type), string(res.Verdict), string(res.Layer), string(res.Reason))
	}
	if res.Verdict != hardening.WouldApply {
		r.log().Info("shadow: an agent write the threat check would stop", "task.id", t.Name, "run.id", runID,
			"type", string(res.Type), "verdict", string(res.Verdict), "layer", string(res.Layer), "reason", string(res.Reason),
			"object", res.Object)
	}
}

// pushTypes are the branch activities that put commits on it.
var pushTypes = []string{"push", "force_push", "branch_creation"}

// observedWrites are run's writes as the agents App inside its window: its pushes to the task's
// branch, the task's pull request (an implementer run's), its comments and labels on the task's
// issue and pull request, and the issues it opened. A source or a write that cannot be read is
// skipped with its error: the rest are still returned.
func (r *Reconciler) observedWrites(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, rec *v1alpha1.RunRecord,
	in func(time.Time) bool) ([]observed, []error) {
	login := cfg.AgentsLogin
	if login == "" {
		return nil, []error{errors.New("no agentsLogin: the agents App's writes cannot be told apart")}
	}
	agent := func(who string) bool { return strings.EqualFold(who, login) }
	repoURL := "https://github.com/" + t.Spec.Repository
	gh := r.gh(ctx)
	var (
		out  []observed
		errs []error
	)
	add := func(at time.Time, w hardening.ObservedWrite, payload any) {
		w.Payload, _ = json.Marshal(payload) // structs of strings: Marshal cannot fail
		out = append(out, observed{at: at, w: w})
	}
	// Pushes: the branch's activity, each read as its diff and its head commit's message.
	branch := "agent/" + t.Name
	acts, err := gh.BranchActivity(ctx, branch)
	if err != nil && !errors.Is(err, forge.ErrActivityTruncated) {
		errs = append(errs, fmt.Errorf("read the branch's activity: %w", err))
		acts = nil // only a truncation's partial list is a list
	}
	for _, p := range acts {
		if !agent(p.Actor) || !in(p.At) || !slices.Contains(pushTypes, p.Type) {
			continue
		}
		w, err := r.pushed(ctx, p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		w.Type, w.Object = hardening.Push, repoURL+"/commit/"+p.After
		add(p.At, w, map[string]string{"ref": branch, "before": p.Before, "after": p.After, "type": p.Type})
	}
	// The task's pull request, as it stands at the run's end, when the agents App opened it.
	if pr := t.Status.PullRequest; pr != nil && pr.Number > 0 && rec.Role == "implementer" {
		p, err := gh.PullRequest(ctx, pr.Number)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("read the pull request: %w", err))
		case agent(p.Author):
			add(rec.Finished.Time, hardening.ObservedWrite{Type: hardening.PR, Object: fmt.Sprintf("%s/pull/%d", repoURL, pr.Number)},
				hardening.PRPayload{Title: p.Title, Body: hardening.StripHarnessFooter(p.Body)})
			o := &out[len(out)-1]
			o.title, o.raw, o.node, o.edited = p.Title, p.Body, p.NodeID, true
		}
	}
	// Comments and labels on the task's issue and pull request.
	for _, th := range r.shadowThreads(t, repoURL) {
		cs, err := gh.AllComments(ctx, th.number)
		if err != nil {
			errs = append(errs, fmt.Errorf("read comments on %d: %w", th.number, err))
		}
		for _, c := range cs {
			if agent(c.Author) && in(c.At) {
				add(c.At, hardening.ObservedWrite{Type: hardening.Comment, Object: fmt.Sprintf("%s#issuecomment-%d", th.url, c.ID)},
					hardening.CommentPayload{Target: th.target, Body: c.Body})
				o := &out[len(out)-1]
				o.raw, o.node, o.edited = c.Body, c.NodeID, c.Edited
			}
		}
		evs, err := gh.LabelEventsBy(ctx, th.number, login, rec.Started.Time)
		if err != nil && !errors.Is(err, forge.ErrEventsTruncated) {
			errs = append(errs, fmt.Errorf("read label events on %d: %w", th.number, err))
		}
		// One labels call adds its labels in the same second: events at one instant are one request.
		var calls []time.Time
		byAt := map[time.Time][]string{}
		for _, e := range evs {
			if !agent(e.Actor) || !in(e.At) {
				continue
			}
			if _, ok := byAt[e.At]; !ok {
				calls = append(calls, e.At)
			}
			byAt[e.At] = append(byAt[e.At], e.Label)
		}
		for _, at := range calls {
			add(at, hardening.ObservedWrite{Type: hardening.Labels, Object: th.url}, hardening.LabelsPayload{Add: byAt[at]})
		}
	}
	// Issues the agents App opened inside the window.
	iss, err := gh.IssuesByAuthorSince(ctx, login, rec.Started.Time)
	if err != nil && !errors.Is(err, forge.ErrIssuesTruncated) {
		errs = append(errs, fmt.Errorf("read the agents' issues: %w", err))
	}
	for _, i := range iss {
		if in(i.CreatedAt) {
			add(i.CreatedAt, hardening.ObservedWrite{Type: hardening.Issue, Object: fmt.Sprintf("%s/issues/%d", repoURL, i.Number)},
				hardening.IssuePayload{Title: i.Title, Body: i.Body})
		}
	}
	return out, errs
}

// shadowThread is one GitHub thread the run could write on.
type shadowThread struct {
	number int
	target string
	url    string
}

func (r *Reconciler) shadowThreads(t *v1alpha1.Task, repoURL string) []shadowThread {
	var ths []shadowThread
	if t.Spec.Issue > 0 {
		ths = append(ths, shadowThread{t.Spec.Issue, hardening.TargetIssue, fmt.Sprintf("%s/issues/%d", repoURL, t.Spec.Issue)})
	}
	if pr := t.Status.PullRequest; pr != nil && pr.Number > 0 && pr.Number != t.Spec.Issue {
		ths = append(ths, shadowThread{pr.Number, hardening.TargetPR, fmt.Sprintf("%s/pull/%d", repoURL, pr.Number)})
	}
	return ths
}

// zeroSHA is the before of a branch's creation.
var zeroSHA = strings.Repeat("0", 40)

// pushed is a push's changed paths and its added text: the lines the diff adds and the head
// commit's message. A branch's creation is read against main, where the factory's branches start.
// A diff GitHub cannot list whole (past its file cap, or not a descendant) is DiffUnavailable,
// which T4 records as diff_unavailable: the push is still checked on its commit message.
func (r *Reconciler) pushed(ctx context.Context, p forge.Push) (hardening.ObservedWrite, error) {
	var w hardening.ObservedWrite
	c, err := r.gh(ctx).Commit(ctx, p.After)
	if err != nil {
		return w, fmt.Errorf("read a push's commit: %w", err)
	}
	base := p.Before
	if base == "" || base == zeroSHA {
		base = "main"
	}
	d, err := r.merger(ctx).Diff(ctx, base, p.After)
	switch {
	case errors.Is(err, forge.ErrDiffTooLarge), errors.Is(err, forge.ErrNotDescendant):
		w.DiffUnavailable = true
	case err != nil:
		return w, fmt.Errorf("read a push's diff: %w", err)
	}
	for _, f := range d.Files {
		w.Files = append(w.Files, f.Path)
		if f.Previous != "" {
			w.Files = append(w.Files, f.Previous)
		}
		w.Added = append(w.Added, addedLines(d.Base[f.Path], d.Head[f.Path])...)
	}
	w.Added = append(w.Added, c.Message)
	return w, nil
}

// addedLines are head's lines that base does not have, counted as a multiset: what a diff adds,
// near enough for the secret, canary and classifier checks, which read text, not positions.
func addedLines(base, head string) []string {
	have := map[string]int{}
	for l := range strings.SplitSeq(base, "\n") {
		have[l]++
	}
	var out []string
	for l := range strings.SplitSeq(head, "\n") {
		if have[l] > 0 {
			have[l]--
			continue
		}
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
