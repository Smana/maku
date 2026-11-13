// SPDX-License-Identifier: Apache-2.0

package applier

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/gitbundle"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/hardening"
)

// The push fakes stand for gitbundle and the broker's push routes.
var (
	_ Pusher    = (*gitbundle.Mirror)(nil)
	_ PushRooms = (*rooms.Client)(nil)
)

const (
	oldHead  = "1111111111111111111111111111111111111111"
	baseHead = "2222222222222222222222222222222222222222"
	newHead  = "3333333333333333333333333333333333333333"
	approval = "01JC0000000000000000000001"
)

// fakePusher inspects every bundle as in, or fails with inspectErr; Push records its calls and
// answers pushErr.
type fakePusher struct {
	mu         sync.Mutex
	in         gitbundle.Inspection
	inspectErr error
	pushErr    error
	inspected  int
	pushed     []gitbundle.Inspection
	discarded  int
}

func (p *fakePusher) Discard(context.Context, gitbundle.Inspection) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discarded++
}

func (p *fakePusher) Inspect(_ context.Context, _ []byte, branch, base string) (gitbundle.Inspection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inspected++
	if branch != "agent/"+taskID || base != "main" {
		return gitbundle.Inspection{}, fmt.Errorf("inspected %s onto %s", branch, base)
	}
	return p.in, p.inspectErr
}

func (p *fakePusher) Push(_ context.Context, in gitbundle.Inspection) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pushErr != nil {
		return p.pushErr
	}
	p.pushed = append(p.pushed, in)
	return nil
}

// fakePushRooms holds the run's bundles by digest and one digest-bound approval per output.
type fakePushRooms struct {
	mu         sync.Mutex
	bundles    map[string][]byte
	approvals  map[string]*rooms.OutputApproval // by approval id
	requests   []rooms.ApprovalRequest
	superseded []string
	reads      int
}

func (f *fakePushRooms) Bundle(_ context.Context, room, run, digest string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bundles[digest]
	if !ok || room != taskID || run != runID {
		return nil, rooms.ErrNoBundle
	}
	return b, nil
}

func (f *fakePushRooms) RequestApproval(_ context.Context, room string, req rooms.ApprovalRequest) (rooms.OutputApproval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	for _, a := range f.approvals {
		if a.Output == req.Output {
			return *a, nil
		}
	}
	id := fmt.Sprintf("01JC00000000000000000000%02d", len(f.approvals)+1)
	a := &rooms.OutputApproval{ApprovalID: id, RoomID: room, RunID: req.RunID, State: "pending", Branch: req.Branch,
		Digest: req.Digest, Output: req.Output, ExpiresAt: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	f.approvals[id] = a
	return *a, nil
}

func (f *fakePushRooms) SupersedeApproval(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.approvals[id]
	if !ok {
		return "", rooms.ErrNoApproval
	}
	if a.State == "pending" {
		a.State = "superseded"
		f.superseded = append(f.superseded, id)
	}
	return a.State, nil
}

func (f *fakePushRooms) Approval(_ context.Context, id string) (rooms.OutputApproval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	a, ok := f.approvals[id]
	if !ok {
		return rooms.OutputApproval{}, rooms.ErrNoApproval
	}
	return *a, nil
}

// decide closes approval id as state, and appends the store's approval_decided to the room.
func (r *rig) decide(t *testing.T, pr *fakePushRooms, id, state string) {
	t.Helper()
	pr.mu.Lock()
	pr.approvals[id].State = state
	pr.mu.Unlock()
	raw, _ := json.Marshal(envelope.ApprovalDecidedPayload{ApprovalID: id, Decision: state})
	r.rooms.add(envelope.Event{RunID: runID, Type: envelope.ApprovalDecided, Origin: envelope.OriginBroker,
		Actor: envelope.Actor{Kind: envelope.ActorSystem, ID: "system:broker"}, TS: r.now, Payload: raw})
}

type approvalWaits struct {
	latency
	decisions []string
}

func (a *approvalWaits) HardeningApprovalWait(_ context.Context, decision string, _ time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.decisions = append(a.decisions, decision)
}

// pushRig is newRig with push enforced, a mirror that inspects a clean fast-forward of one file
// with one added line, the floor's protected paths plus a blocked secrets/**, and class caps of 10
// files and 100 lines.
func pushRig(t *testing.T) (*rig, *fakePusher, *fakePushRooms, *approvalWaits) {
	t.Helper()
	p := &fakePusher{in: gitbundle.Inspection{Ref: "refs/heads/agent/" + taskID, Head: newHead, OldHead: oldHead, BaseHead: baseHead,
		Kind: gitbundle.FastForward, Files: []gitbundle.File{{Path: "docs/a.md", Status: "M", Mode: "100644"}},
		Added: []string{"a new line", "docs: fix a link"}, Cumulative: gitbundle.Stats{Files: 1, Lines: 2}}}
	pr := &fakePushRooms{bundles: map[string][]byte{}, approvals: map[string]*rooms.OutputApproval{}}
	m := &approvalWaits{}
	paths, err := hardening.NewProtectedPaths([]hardening.PathRule{{Pattern: "secrets/**", On: hardening.OnBlocked}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, 7, func(a *Applier) {
		a.Enforced = func(string) []hardening.OutputType { return hardening.Types() }
		a.Checker = func(string) hardening.Checker {
			return hardening.Checker{Secrets: scanner{}, Paths: paths, LabelAllowlist: []string{"documentation"}}
		}
		a.Pushers = func(context.Context, string) (Pusher, error) { return p, nil }
		a.PushRooms = pr
		a.Caps = func(*v1alpha1.Task) (int, int) { return 10, 100 }
		a.Metrics = m
	})
	return r, p, pr, m
}

// pushRequest seeds an output_push of bundle, stored in the broker under its digest.
func (r *rig) pushRequest(t *testing.T, pr *fakePushRooms, bundle string) string {
	t.Helper()
	digest := hardening.Digest([]byte(bundle))
	pr.mu.Lock()
	pr.bundles[digest] = []byte(bundle)
	pr.mu.Unlock()
	outputSeq++
	id := fmt.Sprintf("%016x", outputSeq)
	rec := envelope.OutputRecord{ID: id, Type: string(hardening.Push), Digest: digest, Bytes: len(bundle)}
	raw, _ := json.Marshal(envelope.MessagePayload{Kind: envelope.KindOutputRequested, Output: &rec})
	r.rooms.add(envelope.Event{RunID: runID, Type: envelope.Message, Origin: envelope.OriginClient,
		Actor: envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:" + runID}, TS: r.now.Add(-time.Second), Payload: raw})
	return id
}

// answers is every answer and waiting record to id, in order, as "kind reason".
func (r *rig) answers(id string) []string {
	var out []string
	for _, a := range r.rooms.got() {
		if a.rec.ID == id {
			out = append(out, strings.TrimSpace(string(a.kind)+" "+a.rec.Reason))
		}
	}
	return out
}

// A clean bundle is inspected, checked (T1 to T4 enforced), pushed with the lease on the head it
// read, and answered once applied, its object the commit; its latency is observed under push.
func TestApplyPush(t *testing.T) {
	r, p, pr, m := pushRig(t)
	id := r.pushRequest(t, pr, "bundle-1")
	r.tick(t)
	a := r.only(t, id)
	if a.kind != envelope.KindOutputApplied || a.rec.Object != "https://github.com/owner/repo/commit/"+newHead ||
		a.rec.Digest != hardening.Digest([]byte("bundle-1")) {
		t.Fatalf("answer %+v", a)
	}
	if len(p.pushed) != 1 || p.pushed[0].OldHead != oldHead || p.pushed[0].Head != newHead {
		t.Fatalf("pushed %+v", p.pushed)
	}
	if len(pr.requests) != 0 {
		t.Errorf("a clean push requested an approval: %+v", pr.requests)
	}
	if len(m.seen["push"]) != 1 {
		t.Errorf("push latency %v", m.seen)
	}
	r.tick(t)
	if len(p.pushed) != 1 || len(r.answers(id)) != 1 {
		t.Errorf("a second tick pushed again: %d pushes, answers %q", len(p.pushed), r.answers(id))
	}
}

// A push touching go.mod waits: one output_waiting naming the approval, and one approval request
// binding the branch, the digest and the output, its action the heads and the protected paths; no
// push. The approval's close answers it once: approved is re-inspected, checked with Approved and
// pushed; denied and expired are refused.
func TestPushNeedsApproval(t *testing.T) {
	for _, tc := range []struct {
		decision string
		want     []string
		pushed   int
	}{
		{"approved", []string{"output_waiting protected_path_needs_approval", "output_applied"}, 1},
		{"denied", []string{"output_waiting protected_path_needs_approval", "output_refused approval_denied"}, 0},
		{"expired", []string{"output_waiting protected_path_needs_approval", "output_refused approval_expired"}, 0},
	} {
		t.Run(tc.decision, func(t *testing.T) {
			r, p, pr, m := pushRig(t)
			p.in.Files = append(p.in.Files, gitbundle.File{Path: "go.mod", Status: "M", Mode: "100644"})
			id := r.pushRequest(t, pr, "bundle-gomod")
			r.tick(t)
			r.tick(t)
			if got := r.answers(id); len(got) != 1 || len(p.pushed) != 0 {
				t.Fatalf("answers %q, pushed %d", got, len(p.pushed))
			}
			if len(pr.requests) != 1 {
				t.Fatalf("approval requests %+v", pr.requests)
			}
			req := pr.requests[0]
			act, _ := json.Marshal(req.Action)
			if req.RunID != runID || req.Output != id || req.Digest != hardening.Digest([]byte("bundle-gomod")) ||
				req.Branch != "agent/"+taskID || req.TTL != DefaultApprovalTTL ||
				string(act) != `{"output":"`+id+`","digest":"`+req.Digest+`","base":"`+baseHead+`","head":"`+newHead+`","protected":["go.mod"]}` {
				t.Fatalf("approval request %+v action %s", req, act)
			}
			waiting := r.rooms.got()[0].rec
			if waiting.ApprovalID == "" || pr.approvals[waiting.ApprovalID] == nil {
				t.Fatalf("waiting record %+v", waiting)
			}
			inspected := p.inspected
			r.decide(t, pr, waiting.ApprovalID, tc.decision)
			r.tick(t)
			r.tick(t)
			if got := r.answers(id); !slices.Equal(got, tc.want) || len(p.pushed) != tc.pushed {
				t.Fatalf("answers %q, pushed %d", got, len(p.pushed))
			}
			if tc.decision == "approved" && p.inspected != inspected+1 {
				t.Errorf("an approved push was not re-inspected: %d inspections", p.inspected)
			}
			if !slices.Equal(m.decisions, []string{tc.decision}) {
				t.Errorf("approval wait observed %q", m.decisions)
			}
		})
	}
}

// The approval is read back from the broker before acting: one that binds another digest is not
// the push's approval, and the push is refused.
func TestPushApprovalBindingChecked(t *testing.T) {
	r, p, pr, _ := pushRig(t)
	p.in.Files = append(p.in.Files, gitbundle.File{Path: "go.mod", Status: "M", Mode: "100644"})
	id := r.pushRequest(t, pr, "bundle-gomod")
	r.tick(t)
	waiting := r.rooms.got()[0].rec
	pr.approvals[waiting.ApprovalID].Digest = strings.Repeat("f", 64)
	r.decide(t, pr, waiting.ApprovalID, "approved")
	r.tick(t)
	if got := r.answers(id); len(got) != 2 || got[1] != "output_refused approval_denied" || len(p.pushed) != 0 {
		t.Fatalf("answers %q, pushed %d", got, len(p.pushed))
	}
}

// A second push of the run while the first waits supersedes it: the first is refused
// output_superseded and its approval closed, then the second is checked.
func TestPushSuperseded(t *testing.T) {
	r, p, pr, _ := pushRig(t)
	p.in.Files = append(p.in.Files, gitbundle.File{Path: "go.mod", Status: "M", Mode: "100644"})
	first := r.pushRequest(t, pr, "bundle-1")
	r.tick(t)
	p.in.Files = p.in.Files[:1]
	second := r.pushRequest(t, pr, "bundle-2")
	r.tick(t)
	if got := r.answers(first); !slices.Equal(got, []string{"output_waiting protected_path_needs_approval", "output_refused output_superseded"}) {
		t.Fatalf("first %q", got)
	}
	if len(pr.superseded) != 1 {
		t.Errorf("superseded approvals %q", pr.superseded)
	}
	if got := r.answers(second); !slices.Equal(got, []string{"output_applied"}) || len(p.pushed) != 1 {
		t.Fatalf("second %q, pushed %d", got, len(p.pushed))
	}
}

// The run's later requests wait behind a waiting push, in order.
func TestPushBehindWaiting(t *testing.T) {
	r, p, pr, _ := pushRig(t)
	p.in.Files = append(p.in.Files, gitbundle.File{Path: "go.mod", Status: "M", Mode: "100644"})
	push := r.pushRequest(t, pr, "bundle-1")
	r.tick(t)
	pull := r.request(t, runID, hardening.PR, prPayload("docs: fix a link"))
	r.tick(t)
	if got := r.answers(pull); len(got) != 0 {
		t.Fatalf("the pr behind a waiting push was answered: %q", got)
	}
	r.decide(t, pr, r.rooms.got()[0].rec.ApprovalID, "approved")
	r.tick(t)
	if got := r.answers(push); len(got) != 2 || got[1] != "output_applied" {
		t.Fatalf("push %q", got)
	}
	if got := r.answers(pull); !slices.Equal(got, []string{"output_applied"}) {
		t.Fatalf("pr %q", got)
	}
}

// A run that ended long ago is still read while its push waits; once the approval closes its
// window counts from there, as H2's does from the run's end. A restarted applier finds the wait in
// the log of a task the reconciler holds.
func TestPushWindow(t *testing.T) {
	r, p, pr, _ := pushRig(t)
	p.in.Files = append(p.in.Files, gitbundle.File{Path: "go.mod", Status: "M", Mode: "100644"})
	id := r.pushRequest(t, pr, "bundle-1")
	r.tick(t)
	r.finish(t, 3*DefaultAnswerWait)
	r.tick(t)
	approvalID := r.rooms.got()[0].rec.ApprovalID

	// A restart: a new applier, its state empty, on a task the reconciler holds.
	fresh := *r.a
	fresh.rooms = nil
	r.a = &fresh
	var task v1alpha1.Task
	if err := r.c.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: taskID}, &task); err != nil {
		t.Fatal(err)
	}
	task.Status.Reason = reasonHeld
	if err := r.c.Status().Update(t.Context(), &task); err != nil {
		t.Fatal(err)
	}
	r.decide(t, pr, approvalID, "approved")
	r.tick(t)
	if got := r.answers(id); len(got) != 2 || got[1] != "output_applied" {
		t.Fatalf("answers %q", got)
	}
	// A request behind it is answered within the window that starts at the approval's close.
	pull := r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "done"})
	r.tick(t)
	if got := r.answers(pull); !slices.Equal(got, []string{"output_applied"}) {
		t.Fatalf("after the wait %q", got)
	}
	r.now = r.now.Add(3 * DefaultAnswerWait)
	late := r.request(t, runID, hardening.Comment, hardening.CommentPayload{Target: hardening.TargetIssue, Body: "late"})
	r.tick(t)
	if got := r.answers(late); len(got) != 0 {
		t.Fatalf("a run past its window was still answered: %q", got)
	}
}

// The push refusals, each answered once with no push and no approval requested.
func TestPushRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(p *fakePusher)
		want  hardening.Refusal
		alert bool
	}{
		"push_ref":       {func(p *fakePusher) { p.inspectErr = &gitbundle.Refused{Reason: hardening.RefusePushRef} }, hardening.RefusePushRef, false},
		"push_history":   {func(p *fakePusher) { p.inspectErr = &gitbundle.Refused{Reason: hardening.RefusePushHistory} }, hardening.RefusePushHistory, false},
		"push_tree":      {func(p *fakePusher) { p.inspectErr = &gitbundle.Refused{Reason: hardening.RefusePushTree} }, hardening.RefusePushTree, false},
		"too many files": {func(p *fakePusher) { p.in.Cumulative.Files = 11 }, hardening.RefusePushSize, false},
		"too many lines": {func(p *fakePusher) { p.in.Cumulative.Lines = 101 }, hardening.RefusePushSize, false},
		"the canary in an added line": {func(p *fakePusher) { p.in.Added = append(p.in.Added, "token "+canary) },
			hardening.RefuseCanary, true},
		"an AWS key in a commit message": {func(p *fakePusher) { p.in.Added = append(p.in.Added, "AKIAIOSFODNN7EXAMPLE") },
			hardening.RefuseSecret, false},
		"a blocked path": {func(p *fakePusher) {
			p.in.Files = append(p.in.Files, gitbundle.File{Path: "secrets/prod.env", Status: "A", Mode: "100644"},
				gitbundle.File{Path: "go.mod", Status: "M", Mode: "100644"})
		}, hardening.RefuseBlockedPath, false},
		"GitHub refuses for good": {func(p *fakePusher) { p.pushErr = fmt.Errorf("push: %w", gitbundle.ErrPushRejected) },
			hardening.RefuseUnavailable, false},
	} {
		t.Run(name, func(t *testing.T) {
			r, p, pr, _ := pushRig(t)
			tc.setup(p)
			id := r.pushRequest(t, pr, "bundle-1")
			r.tick(t)
			a := r.only(t, id)
			if a.kind != envelope.KindOutputRefused || a.rec.Reason != string(tc.want) {
				t.Fatalf("answer %+v, want refused %s", a, tc.want)
			}
			if len(p.pushed) != 0 || len(pr.requests) != 0 {
				t.Errorf("pushed %d, approvals %d", len(p.pushed), len(pr.requests))
			}
			if alert := strings.Contains(r.log.String(), "canary leaked"); alert != tc.alert {
				t.Errorf("canary alert %t, want %t", alert, tc.alert)
			}
			if strings.Contains(r.log.String(), canary) || strings.Contains(r.log.String(), "AKIA") {
				t.Errorf("pushed text reached the log:\n%s", r.log.String())
			}
		})
	}
	t.Run("a bundle the broker no longer holds", func(t *testing.T) {
		r, _, pr, _ := pushRig(t)
		id := r.pushRequest(t, pr, "bundle-1")
		pr.bundles = map[string][]byte{}
		r.tick(t)
		if a := r.only(t, id); a.rec.Reason != string(hardening.RefuseUnavailable) {
			t.Fatalf("answer %+v", a)
		}
	})
	t.Run("no pusher", func(t *testing.T) {
		r, _, pr, _ := pushRig(t)
		r.a.Pushers = nil
		id := r.pushRequest(t, pr, "bundle-1")
		r.tick(t)
		if a := r.only(t, id); a.rec.Reason != string(hardening.RefuseType) {
			t.Fatalf("answer %+v", a)
		}
	})
}

// HR26: a branch already at the bundle's head is answered applied with no push only when an applied
// push of the task put that head there (a retried request). A head another hand pushed is refused
// push_ref, never answered applied: the tracer would read that answer as tracing the direct push.
// A lost lease leaves the request pending for the next tick, which pushes it.
func TestPushIdempotent(t *testing.T) {
	r, p, pr, _ := pushRig(t)
	first := r.pushRequest(t, pr, "bundle-1")
	r.tick(t)
	p.in.OldHead = newHead
	id := r.pushRequest(t, pr, "bundle-1")
	r.tick(t)
	if a := r.only(t, first); a.kind != envelope.KindOutputApplied {
		t.Fatalf("first %+v", a)
	}
	if a := r.only(t, id); a.kind != envelope.KindOutputApplied || len(p.pushed) != 1 {
		t.Fatalf("answer %+v, pushed %d", a, len(p.pushed))
	}

	r, p, pr, _ = pushRig(t)
	p.in.OldHead = newHead
	id = r.pushRequest(t, pr, "bundle-1")
	r.tick(t)
	if a := r.only(t, id); a.kind != envelope.KindOutputRefused || a.rec.Reason != string(hardening.RefusePushRef) || len(p.pushed) != 0 {
		t.Fatalf("a head another hand pushed: answer %+v, pushed %d", a, len(p.pushed))
	}

	r, p, pr, _ = pushRig(t)
	p.pushErr = gitbundle.ErrLeaseLost
	id = r.pushRequest(t, pr, "bundle-1")
	r.tick(t)
	if got := r.answers(id); len(got) != 0 {
		t.Fatalf("a lost lease was answered: %q", got)
	}
	p.pushErr = nil
	r.tick(t)
	if a := r.only(t, id); a.kind != envelope.KindOutputApplied || len(p.pushed) != 1 {
		t.Fatalf("answer %+v, pushed %d", a, len(p.pushed))
	}
}

// The approval wait is observed when the approval closes; output latency only with the answer.
func TestPushLatency(t *testing.T) {
	r, p, pr, m := pushRig(t)
	p.in.Files = append(p.in.Files, gitbundle.File{Path: "go.mod", Status: "M", Mode: "100644"})
	r.pushRequest(t, pr, "bundle-1")
	r.tick(t)
	if len(m.seen["push"]) != 0 {
		t.Fatalf("a waiting record observed latency: %v", m.seen)
	}
	r.now = r.now.Add(time.Hour)
	r.decide(t, pr, r.rooms.got()[0].rec.ApprovalID, "approved")
	r.tick(t)
	if len(m.seen["push"]) != 1 || !slices.Equal(m.decisions, []string{"approved"}) {
		t.Fatalf("latency %v, approval waits %q", m.seen, m.decisions)
	}
}
