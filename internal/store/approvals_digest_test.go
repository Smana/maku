// SPDX-License-Identifier: Apache-2.0

package store

import (
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/store/migrations"
)

const (
	pushDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	pushOutput = "0123456789abcdef"
)

// outputApproval is the factory's request for a protected-path push of approvalRun (HR28).
func outputApproval(id, output string) Approval {
	return Approval{ID: id, RoomID: room, RunID: approvalRun, Class: "forge.push", BindingBranch: "agent/t-1",
		BindingDigest: pushDigest, BindingOutput: output, Prompters: []string{"human:own"},
		Action:    json.RawMessage(`{"output":"` + output + `","digest":"` + pushDigest + `","protected":["go.mod"]}`),
		ExpiresAt: time.Now().Add(24 * time.Hour)}
}

// factoryDraft keys the request on its output id, as the broker's route does.
func factoryDraft(output string) envelope.Draft {
	n, _ := strconv.ParseUint(output, 16, 64)
	return envelope.Draft{Actor: envelope.Actor{Kind: envelope.ActorSystem, ID: "system:factory"}, Origin: envelope.OriginClient,
		OriginClient: "system:factory:approvals", OriginSeq: int64(n>>1) + 1}
}

// HR28: the factory requests a digest-bound approval with no bridge lease; the row is its
// approval_requested event, and a second request for the same output returns the first row.
func TestRequestOutputApproval(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	a, ev, err := s.RequestOutputApproval(ctx, outputApproval("01JC0000000000000000000001", pushOutput), factoryDraft(pushOutput))
	if err != nil {
		t.Fatal(err)
	}
	if a.State != ApprovalPending || a.BindingDigest != pushDigest || a.BindingOutput != pushOutput || a.BindingBranch != "agent/t-1" ||
		a.Class != "forge.push" || a.CallID != "output:"+pushOutput || a.EventID != "output:"+pushOutput {
		t.Fatalf("%+v", a)
	}
	var p envelope.ApprovalRequestedPayload
	if json.Unmarshal(ev.Payload, &p) != nil || p.Digest != pushDigest || p.Output != pushOutput || p.Branch != "agent/t-1" ||
		!slices.Equal(p.Prompters, []string{"human:own"}) || ev.Actor.ID != "system:factory" || ev.Type != envelope.ApprovalRequested {
		t.Fatalf("event %+v %s", ev, ev.Payload)
	}
	again, ev2, err := s.RequestOutputApproval(ctx, outputApproval("01JC0000000000000000000002", pushOutput), factoryDraft(pushOutput))
	if err != nil || again.ID != a.ID || ev2.Seq != 0 {
		t.Fatalf("a retried request: %+v %v", again, err)
	}
	read, err := s.Approval(ctx, a.ID)
	if err != nil || read.BindingDigest != pushDigest || read.BindingOutput != pushOutput || read.DecidedBy != "" {
		t.Fatalf("read back: %+v %v", read, err)
	}
	for name, mut := range map[string]func(*Approval){
		"no digest":       func(a *Approval) { a.BindingDigest = "" },
		"a short digest":  func(a *Approval) { a.BindingDigest = "abc" },
		"a bad output":    func(a *Approval) { a.BindingOutput = "x" },
		"no branch":       func(a *Approval) { a.BindingBranch = "" },
		"another class":   func(a *Approval) { a.Class = "forge.pr" },
		"a bad run":       func(a *Approval) { a.RunID = "Run" },
		"no id":           func(a *Approval) { a.ID = "" },
		"an action array": func(a *Approval) { a.Action = json.RawMessage(`[]`) },
	} {
		t.Run(name, func(t *testing.T) {
			x := outputApproval("01JC0000000000000000000003", "1111111111111111")
			mut(&x)
			if _, _, err := s.RequestOutputApproval(ctx, x, factoryDraft("1111111111111111")); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	// The broker's role cannot rewrite a binding once recorded.
	if err := inTx(ctx, s, `UPDATE approvals SET binding_digest = '' WHERE approval_id = '`+a.ID+`'`); err == nil {
		t.Fatal("the broker rewrote a binding")
	}
}

// HR28: a digest-bound approval survives its run's end and a tool_result of its call id, which
// close a plain forge.push approval as superseded, as today.
func TestDigestApprovalsSurviveRunEnd(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	holdBridge(t, s, room, approvalRun)
	plain := approval("ap-plain", "e1", "c1", time.Hour)
	plain.Class, plain.BindingBranch = "forge.push", "agent/t-1"
	if _, _, err := s.RequestApproval(ctx, plain, approvalDraft()); err != nil {
		t.Fatal(err)
	}
	bound, _, err := s.RequestOutputApproval(ctx, outputApproval("01JC0000000000000000000004", pushOutput), factoryDraft(pushOutput))
	if err != nil {
		t.Fatal(err)
	}
	toolResult(t, s, 1, "output:"+pushOutput)
	leave(t, s, approvalRun)
	evs, err := s.SupersedeAnswered(ctx)
	if err != nil || len(evs) != 1 || !strings.Contains(string(evs[0].Payload), `"approvalId":"ap-plain"`) {
		t.Fatalf("superseded %v, %v", evs, err)
	}
	if a, err := s.Approval(ctx, bound.ID); err != nil || a.State != ApprovalPending {
		t.Fatalf("the digest-bound row: %+v %v", a, err)
	}
	// A maintainer still decides it after the run ended.
	if _, a, err := s.Decide(ctx, bound.ID, ApprovalApproved, "human:maint", "", deciderDraft("human:maint", 1)); err != nil ||
		a.State != ApprovalApproved || a.DecidedBy != "human:maint" {
		t.Fatalf("decide: %+v %v", a, err)
	}
}

// HR27: the factory closes a waiting push's approval superseded when a later push replaces it; a
// decided row is left as it is, and a plain row is not the factory's to close.
func TestSupersedeOutputApproval(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	a, _, err := s.RequestOutputApproval(ctx, outputApproval("01JC0000000000000000000005", pushOutput), factoryDraft(pushOutput))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.CloseOutputApproval(ctx, a.ID, ApprovalSuperseded)
	if err != nil || got.State != ApprovalSuperseded {
		t.Fatalf("%+v %v", got, err)
	}
	var reason string
	if err := s.pool.QueryRow(ctx, `SELECT reason FROM approvals WHERE approval_id = $1`, a.ID).Scan(&reason); err != nil || reason != reasonPushSuperseded {
		t.Fatalf("reason %q %v", reason, err)
	}
	if again, err := s.CloseOutputApproval(ctx, a.ID, ApprovalSuperseded); err != nil || again.State != ApprovalSuperseded {
		t.Fatalf("a no-op on a closed row: %+v %v", again, err)
	}
	b, _, err := s.RequestOutputApproval(ctx, outputApproval("01JC0000000000000000000006", "2222222222222222"), factoryDraft("2222222222222222"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Decide(ctx, b.ID, ApprovalDenied, "human:maint", "", deciderDraft("human:maint", 2)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CloseOutputApproval(ctx, b.ID, ApprovalSuperseded); err != nil || got.State != ApprovalDenied {
		t.Fatalf("a decided row: %+v %v", got, err)
	}
	if _, err := s.CloseOutputApproval(ctx, a.ID, ApprovalApproved); err == nil {
		t.Fatal("the factory approved")
	}
	holdBridge(t, s, room, approvalRun)
	if _, _, err := s.RequestApproval(ctx, approval("ap-plain", "e1", "c1", time.Hour), approvalDraft()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseOutputApproval(ctx, "ap-plain", ApprovalSuperseded); !errors.Is(err, ErrNoApproval) {
		t.Fatalf("a plain row: %v", err)
	}
	if _, err := s.CloseOutputApproval(ctx, "nope", ApprovalSuperseded); !errors.Is(err, ErrNoApproval) {
		t.Fatalf("no row: %v", err)
	}
}

// The bundle sweep keeps a bundle a pending approval binds, whatever its age.
func TestSweepKeepsABoundBundle(t *testing.T) {
	ctx := t.Context()
	s, _, retention, super := open(t)
	bound, free := []byte("bound bundle"), []byte("free bundle")
	for _, b := range [][]byte{bound, free} {
		if err := s.PutBundle(ctx, room, approvalRun, digestOf(b), b); err != nil {
			t.Fatal(err)
		}
	}
	x := outputApproval("01JC0000000000000000000007", pushOutput)
	x.BindingDigest = digestOf(bound)
	if _, _, err := s.RequestOutputApproval(ctx, x, factoryDraft(pushOutput)); err != nil {
		t.Fatal(err)
	}
	forge(t, super, `UPDATE output_bundles SET created_at = now() - interval '30 days'`)
	r, err := Open(ctx, retention, RoleRetention)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if n, err := r.SweepBundles(ctx, time.Now()); err != nil || n != 1 {
		t.Fatalf("swept %d (%v), want the free one", n, err)
	}
	if _, err := s.Bundle(ctx, room, approvalRun, digestOf(bound)); err != nil {
		t.Fatalf("the bound bundle: %v", err)
	}
}

// An approved push is applied from its bundle after the decision: the sweep keeps the bundle of an
// approved approval until the factory answers its output, then lets it go.
func TestSweepKeepsAnApprovedUnappliedBundle(t *testing.T) {
	ctx := t.Context()
	s, _, retention, super := open(t)
	b := []byte("approved bundle")
	if err := s.PutBundle(ctx, room, approvalRun, digestOf(b), b); err != nil {
		t.Fatal(err)
	}
	x := outputApproval("01JC0000000000000000000008", pushOutput)
	x.BindingDigest = digestOf(b)
	a, _, err := s.RequestOutputApproval(ctx, x, factoryDraft(pushOutput))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Decide(ctx, a.ID, ApprovalApproved, "human:maint", "", deciderDraft("human:maint", 1)); err != nil {
		t.Fatal(err)
	}
	forge(t, super, `UPDATE output_bundles SET created_at = now() - interval '30 days'`)
	r, err := Open(ctx, retention, RoleRetention)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if n, err := r.SweepBundles(ctx, time.Now()); err != nil || n != 0 {
		t.Fatalf("swept %d (%v), want the approved bundle kept", n, err)
	}
	// The factory's answer: an output_applied for the output, stamped as the factory.
	if _, _, err := s.Append(ctx, envelope.Draft{RoomID: room, RunID: approvalRun, Type: envelope.Message,
		Actor: envelope.Actor{Kind: envelope.ActorSystem, ID: "system:factory"}, Origin: envelope.OriginClient,
		OriginClient: "system:factory:output:output_applied", OriginSeq: 1,
		Payload: envelope.Must(envelope.MessagePayload{Kind: envelope.KindOutputApplied, Delivery: envelope.DeliveryNone,
			Output: &envelope.OutputRecord{ID: pushOutput, Type: "push", Digest: digestOf(b), Object: "https://github.com/o/r/commit/a"}})}); err != nil {
		t.Fatal(err)
	}
	if n, err := r.SweepBundles(ctx, time.Now()); err != nil || n != 1 {
		t.Fatalf("swept %d (%v), want the applied bundle", n, err)
	}
}

// The sweep is a definer function: a caller's cutoff never reaches past the retention floor.
func TestSweepFloorsTheCutoff(t *testing.T) {
	ctx := t.Context()
	s, _, retention, _ := open(t)
	b := []byte("fresh bundle")
	if err := s.PutBundle(ctx, room, approvalRun, digestOf(b), b); err != nil {
		t.Fatal(err)
	}
	r, err := Open(ctx, retention, RoleRetention)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if n, err := r.SweepBundles(ctx, time.Now().Add(365*24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("a future cutoff swept %d (%v)", n, err)
	}
	if err := run(ctx, retention, `SELECT sweep_output_bundles(now() + interval '1 year')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bundle(ctx, room, approvalRun, digestOf(b)); err != nil {
		t.Fatalf("the fresh bundle: %v", err)
	}
}

// A retried request for an output must name the binding the first recorded: another digest or
// branch for the same output is a conflict, never the stored row.
func TestRequestOutputApprovalBindingConflict(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := open(t)
	if _, _, err := s.RequestOutputApproval(ctx, outputApproval("01JC0000000000000000000009", pushOutput), factoryDraft(pushOutput)); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Approval){
		"another digest": func(a *Approval) { a.BindingDigest = strings.Repeat("f", 64) },
		"another branch": func(a *Approval) { a.BindingBranch = "agent/t-2" },
	} {
		t.Run(name, func(t *testing.T) {
			x := outputApproval("01JC000000000000000000000A", pushOutput)
			mut(&x)
			if _, _, err := s.RequestOutputApproval(ctx, x, factoryDraft(pushOutput)); !errors.Is(err, ErrBindingConflict) {
				t.Fatalf("err = %v, want ErrBindingConflict", err)
			}
		})
	}
}

// The upgrade from the schema before 20261113130000 keeps every row: an older approval reads with
// no digest, and is decided as before.
func TestApprovalDigestUpgrade(t *testing.T) {
	ctx, log := t.Context(), slog.New(slog.DiscardHandler)
	dsn := emptyOwnedDB(t)
	files, err := migrations.Files()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(files, func(f migrations.File) bool { return strings.HasPrefix(f.Name, "20261113130000_") })
	if i < 0 {
		t.Fatal("the migration is not embedded")
	}
	if _, err := migrate(ctx, dsn, files[:i], log); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, dsn, RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.EnsureRoom(ctx, NewRoom{ID: room, Driver: "system:factory", Retention: 90 * 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	exec(t, dsn, `UPDATE rooms SET last_seq = last_seq + 1 WHERE room_id = '3kq7x2ma';
		INSERT INTO events (room_id, seq, id, run_id, actor_kind, actor_id, type, origin, origin_client, origin_seq, ts, payload)
		SELECT room_id, last_seq, 'e1', '7f3cq2xz', 'agent', 'agent:7f3cq2xz', 'approval_requested', 'harness',
			'test:upgrade', 1, now(),
			'{"approvalId":"apOld","callId":"c1","class":"forge.push","action":{"command":"git push"},"branch":"agent/t-1"}'
			FROM rooms WHERE room_id = '3kq7x2ma';
		INSERT INTO approvals (approval_id, room_id, run_id, event_id, call_id, class, action, binding_branch, state,
			requested_seq, expires_at)
		SELECT 'apOld', room_id, '7f3cq2xz', 'e1', 'c1', 'forge.push', '{"command":"git push"}', 'agent/t-1',
			'pending', last_seq, now() + interval '1 hour' FROM rooms WHERE room_id = '3kq7x2ma'`)
	if n, err := migrate(ctx, dsn, files, log); err != nil || n != len(files)-i {
		t.Fatalf("applied %d, %v", n, err)
	}
	a, err := s.Approval(ctx, "apOld")
	if err != nil || a.BindingDigest != "" || a.BindingOutput != "" || a.BindingBranch != "agent/t-1" || a.State != ApprovalPending {
		t.Fatalf("%+v %v", a, err)
	}
	if _, a, err := s.Decide(ctx, "apOld", ApprovalApproved, "human:x", "", deciderDraft("human:x", 1)); err != nil || a.State != ApprovalApproved {
		t.Fatalf("decide: %+v %v", a, err)
	}
}
