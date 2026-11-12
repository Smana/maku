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

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/hardening"
)

// shadowMetrics is the shadow pass's counter: fmetrics.Set implements it. A metrics port without
// it records nothing, so the pass never needs every fake to know it.
type shadowMetrics interface {
	HardeningShadow(ctx context.Context, typ, verdict, layer, reason string)
}

// shadowRoles are the writer runs whose GitHub writes the shadow pass reads back.
var shadowRoles = []string{"implementer", "tester"}

// shadowPending runs the shadow pass on every ended writer run of t not shadowed yet (H1, HR1).
// It is called once a writer run is terminal, before its outcome is handled, and on every later
// pass of a live task, which retries a failed one. A failure is logged and never moves the task.
func (r *Reconciler) shadowPending(ctx context.Context, cfg *config.Config, t *v1alpha1.Task) {
	if !r.auditing(cfg, t) {
		return
	}
	for i := range t.Status.Runs {
		rec := &t.Status.Runs[i]
		if rec.Shadowed || rec.Finished == nil || rec.Started == nil || !slices.Contains(shadowRoles, rec.Role) {
			continue
		}
		if err := r.shadowRun(ctx, cfg, t, rec); err != nil {
			r.log().Warn("shadow pass failed", "task.id", t.Name, "run.id", rec.ID, "err", err)
			continue
		}
		rec.Shadowed = true
	}
}

// observed is one write read back, with when it happened: the writes are checked in time order,
// so the run's caps count as they would have.
type observed struct {
	at time.Time
	w  hardening.ObservedWrite
}

// shadowRun reads back the writes run made as the agents App inside its window, checks each as the
// request it would have sent (every layer in audit), and posts one output_shadowed record per write.
// A replay posts the same ids, which the broker keeps once.
func (r *Reconciler) shadowRun(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, rec *v1alpha1.RunRecord) error {
	from, to := rec.Started.Time, rec.Finished.Add(runEndGrace)
	in := func(at time.Time) bool { return !at.Before(from) && !at.After(to) }
	ws, err := r.observedWrites(ctx, cfg, t, rec, in)
	if err != nil {
		return err
	}
	canary := ""
	if x, found, err := r.Runs.Get(ctx, rec.ID); err != nil {
		return fmt.Errorf("read the run's claim: %w", err)
	} else if found {
		canary = x.Canary
	}
	c := r.Hardening.Checker
	c.Paths = r.repositoryPolicy(cfg, t).Paths
	c.LabelAllowlist = cfg.Hardening.LabelAllowlist
	c.Layers = hardening.LayersFor(hardening.ModeAudit, cfg.Hardening.EffectiveClassifier())
	slices.SortStableFunc(ws, func(a, b observed) int { return a.at.Compare(b.at) })
	var n hardening.Counts
	for _, o := range ws {
		res := hardening.Shadow(ctx, c, &n, o.w, t.Spec.Issue, canary)
		out := shadowRecord(rec.ID, res, len(o.w.Payload))
		if err := r.Rooms.Output(ctx, t.Status.RoomRef, rec.ID, envelope.KindOutputShadowed, out); err != nil {
			return fmt.Errorf("post a %s shadow: %w", res.Type, err)
		}
		r.shadowObserved(ctx, t, rec.ID, res)
	}
	return nil
}

// shadowRecord is res as the room keeps it: no payload, the deciding refusal as the reason, or
// would_wait for a protected path awaiting approval (HR3), and every other finding as audit. Its id
// is derived from the run, the type, the object and the digest, so a replay posts the same one.
func shadowRecord(runID string, res hardening.ShadowResult, size int) envelope.OutputRecord {
	sum := sha256.Sum256([]byte(strings.Join([]string{runID, string(res.Type), res.Object, res.Digest}, "\x00")))
	out := envelope.OutputRecord{ID: hex.EncodeToString(sum[:8]), Type: string(res.Type), Digest: res.Digest, Bytes: size,
		Object: res.Object}
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
// issue and pull request, and the issues it opened.
func (r *Reconciler) observedWrites(ctx context.Context, cfg *config.Config, t *v1alpha1.Task, rec *v1alpha1.RunRecord,
	in func(time.Time) bool) ([]observed, error) {
	login := cfg.AgentsLogin
	if login == "" {
		return nil, errors.New("no agentsLogin: the agents App's writes cannot be told apart")
	}
	repoURL := "https://github.com/" + t.Spec.Repository
	gh := r.gh(ctx)
	var out []observed
	add := func(at time.Time, typ hardening.OutputType, payload any, object string, files, added []string) {
		b, _ := json.Marshal(payload) // structs of strings: Marshal cannot fail
		out = append(out, observed{at: at, w: hardening.ObservedWrite{Type: typ, Payload: b, Object: object, Files: files, Added: added}})
	}
	// Pushes: the branch's activity, each read as its diff and its head commit's message.
	branch := "agent/" + t.Name
	acts, err := gh.BranchActivity(ctx, branch)
	if err != nil && !errors.Is(err, forge.ErrActivityTruncated) {
		return nil, fmt.Errorf("read the branch's activity: %w", err)
	}
	for _, p := range acts {
		if p.Actor != login || !in(p.At) || !slices.Contains(pushTypes, p.Type) {
			continue
		}
		files, added, err := r.pushed(ctx, p)
		if err != nil {
			return nil, err
		}
		add(p.At, hardening.Push, map[string]string{"ref": branch, "before": p.Before, "after": p.After, "type": p.Type},
			repoURL+"/commit/"+p.After, files, added)
	}
	// The task's pull request, as it stands at the run's end, when the agents App opened it.
	if pr := t.Status.PullRequest; pr != nil && pr.Number > 0 && rec.Role == "implementer" {
		p, err := gh.PullRequest(ctx, pr.Number)
		if err != nil {
			return nil, fmt.Errorf("read the pull request: %w", err)
		}
		if p.Author == login {
			add(rec.Finished.Time, hardening.PR, hardening.PRPayload{Title: p.Title, Body: hardening.StripHarnessFooter(p.Body)},
				fmt.Sprintf("%s/pull/%d", repoURL, pr.Number), nil, nil)
		}
	}
	// Comments and labels on the task's issue and pull request.
	for _, th := range r.shadowThreads(t, repoURL) {
		cs, err := gh.AllComments(ctx, th.number)
		if err != nil {
			return nil, fmt.Errorf("read comments on %d: %w", th.number, err)
		}
		for _, c := range cs {
			if c.Author == login && in(c.At) {
				add(c.At, hardening.Comment, hardening.CommentPayload{Target: th.target, Body: c.Body},
					fmt.Sprintf("%s#issuecomment-%d", th.url, c.ID), nil, nil)
			}
		}
		evs, err := gh.LabelEventsBy(ctx, th.number, login, rec.Started.Time)
		if err != nil && !errors.Is(err, forge.ErrEventsTruncated) {
			return nil, fmt.Errorf("read label events on %d: %w", th.number, err)
		}
		// One labels call adds its labels in the same second: events at one instant are one request.
		var calls []time.Time
		byAt := map[time.Time][]string{}
		for _, e := range evs {
			if !in(e.At) {
				continue
			}
			if _, ok := byAt[e.At]; !ok {
				calls = append(calls, e.At)
			}
			byAt[e.At] = append(byAt[e.At], e.Label)
		}
		for _, at := range calls {
			add(at, hardening.Labels, hardening.LabelsPayload{Add: byAt[at]}, th.url, nil, nil)
		}
	}
	// Issues the agents App opened inside the window.
	iss, err := gh.IssuesByAuthorSince(ctx, login, rec.Started.Time)
	if err != nil && !errors.Is(err, forge.ErrIssuesTruncated) {
		return nil, fmt.Errorf("read the agents' issues: %w", err)
	}
	for _, i := range iss {
		if in(i.CreatedAt) {
			add(i.CreatedAt, hardening.Issue, hardening.IssuePayload{Title: i.Title, Body: i.Body}, fmt.Sprintf("%s/issues/%d", repoURL, i.Number), nil, nil)
		}
	}
	return out, nil
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
func (r *Reconciler) pushed(ctx context.Context, p forge.Push) (files, added []string, err error) {
	base := p.Before
	if base == "" || base == zeroSHA {
		base = "main"
	}
	d, err := r.merger(ctx).Diff(ctx, base, p.After)
	if err != nil {
		return nil, nil, fmt.Errorf("read a push's diff: %w", err)
	}
	for _, f := range d.Files {
		files = append(files, f.Path)
		if f.Previous != "" {
			files = append(files, f.Previous)
		}
		added = append(added, addedLines(d.Base[f.Path], d.Head[f.Path])...)
	}
	c, err := r.gh(ctx).Commit(ctx, p.After)
	if err != nil {
		return nil, nil, fmt.Errorf("read a push's commit: %w", err)
	}
	return files, append(added, c.Message), nil
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
