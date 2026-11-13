// SPDX-License-Identifier: Apache-2.0

package applier

import (
	"context"
	"errors"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/gitbundle"
	"github.com/Smana/maku/internal/hardening"
)

// landingPusher is a fakePusher whose push lands on the remote but whose reply is lost: Push
// answers lost, and records the task's push intent as it stood when the push began.
type landingPusher struct {
	*fakePusher
	r      *rig
	lost   error
	intent []string
}

func (p *landingPusher) Push(ctx context.Context, in gitbundle.Inspection) error {
	var task v1alpha1.Task
	if err := p.r.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: taskID}, &task); err != nil {
		return err
	}
	p.intent = append(p.intent, task.Annotations[AnnotationPushIntent])
	if err := p.fakePusher.Push(ctx, in); err != nil {
		return err
	}
	return p.lost
}

// #505's review: the head is recorded on the task before git push, so a push whose reply is lost
// (a timeout, a crash) is answered applied on the next inspection, which reads the branch at that
// head, instead of refused push_ref and reported untraced_write. A fresh applier, as after a
// restart, reads the record from the task.
func TestPushLostReply(t *testing.T) {
	for _, restart := range []bool{false, true} {
		r, fp, pr, _ := pushRig(t)
		p := &landingPusher{fakePusher: fp, r: r, lost: errors.New("push: connection reset")}
		r.a.Pushers = func(context.Context, string) (Pusher, error) { return p, nil }
		r.a.Tasks = r.c
		id := r.pushRequest(t, pr, "bundle-1")
		if err := r.a.Tick(t.Context()); err == nil {
			t.Fatal("a lost reply is no error")
		}
		if got := r.answers(id); len(got) != 0 {
			t.Fatalf("a push whose reply was lost was answered: %q", got)
		}
		if len(p.intent) != 1 || p.intent[0] != id+" "+newHead {
			t.Fatalf("the intent when the push began: %q", p.intent)
		}
		if restart {
			old := r.a
			r.a = &Applier{}
			*r.a = *old
			r.a.rooms = nil
		}
		fp.in.OldHead = newHead // the push landed
		p.lost = nil
		r.tick(t)
		if a := r.only(t, id); a.kind != envelope.KindOutputApplied || a.rec.Object != "https://github.com/"+repo+"/commit/"+newHead ||
			len(fp.pushed) != 1 {
			t.Fatalf("restart %v: answer %+v, pushed %d", restart, a, len(fp.pushed))
		}
	}

	// Another output's intent, or another head, launders nothing: still push_ref (HR26).
	r, p, pr, _ := pushRig(t)
	r.a.Tasks = r.c
	var task v1alpha1.Task
	if err := r.c.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: taskID}, &task); err != nil {
		t.Fatal(err)
	}
	task.Annotations = map[string]string{AnnotationPushIntent: "00000000000000ff " + newHead}
	if err := r.c.Update(t.Context(), &task); err != nil {
		t.Fatal(err)
	}
	p.in.OldHead = newHead
	id := r.pushRequest(t, pr, "bundle-1")
	r.tick(t)
	if a := r.only(t, id); a.kind != envelope.KindOutputRefused || a.rec.Reason != string(hardening.RefusePushRef) {
		t.Fatalf("another output's intent: %+v", a)
	}
}

// An intent that cannot be recorded leaves the request pending, and nothing is pushed.
func TestPushIntentUnrecorded(t *testing.T) {
	r, p, pr, _ := pushRig(t)
	r.a.Tasks = failingWriter{r.c}
	id := r.pushRequest(t, pr, "bundle-1")
	if err := r.a.Tick(t.Context()); err == nil {
		t.Fatal("an unrecorded intent is no error")
	}
	if got := r.answers(id); len(got) != 0 || len(p.pushed) != 0 {
		t.Fatalf("answers %q, pushed %d", got, len(p.pushed))
	}
}

// #513's review: a definitive refusal (a lost lease, a rejection) clears the intent, so another
// hand's later push of the inspected head is refused push_ref, never answered applied; a restarted
// applier reads no record either.
func TestPushIntentClearedOnRefusal(t *testing.T) {
	for _, restart := range []bool{false, true} {
		r, fp, pr, _ := pushRig(t)
		r.a.Tasks = r.c
		fp.pushErr = gitbundle.ErrLeaseLost
		id := r.pushRequest(t, pr, "bundle-1")
		r.tick(t)
		var task v1alpha1.Task
		if err := r.c.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: taskID}, &task); err != nil {
			t.Fatal(err)
		}
		if v, ok := task.Annotations[AnnotationPushIntent]; ok {
			t.Fatalf("a lost lease kept the intent %q", v)
		}
		if restart {
			old := r.a
			r.a = &Applier{}
			*r.a = *old
			r.a.rooms = nil
		}
		fp.pushErr = nil
		fp.in.OldHead = newHead // another hand pushed the inspected head
		r.tick(t)
		if a := r.only(t, id); a.kind != envelope.KindOutputRefused || a.rec.Reason != string(hardening.RefusePushRef) {
			t.Fatalf("restart %v: %+v", restart, a)
		}
	}

	r, fp, pr, _ := pushRig(t)
	r.a.Tasks = r.c
	fp.pushErr = gitbundle.ErrPushRejected
	id := r.pushRequest(t, pr, "bundle-1")
	r.tick(t)
	var task v1alpha1.Task
	if err := r.c.Get(t.Context(), client.ObjectKey{Namespace: ns, Name: taskID}, &task); err != nil {
		t.Fatal(err)
	}
	if _, ok := task.Annotations[AnnotationPushIntent]; ok {
		t.Fatal("a rejection kept the intent")
	}
	if a := r.only(t, id); a.kind != envelope.KindOutputRefused || a.rec.Reason != string(hardening.RefuseUnavailable) {
		t.Fatalf("%+v", a)
	}
}

// #513's review: caps of -1 (a task whose class the repository does not declare, and no class
// declares caps) refuse the push push_size.
func TestPushNegativeCaps(t *testing.T) {
	r, p, pr, _ := pushRig(t)
	r.a.Caps = func(*v1alpha1.Task) (int, int) { return -1, -1 }
	id := r.pushRequest(t, pr, "bundle-1")
	r.tick(t)
	if a := r.only(t, id); a.kind != envelope.KindOutputRefused || a.rec.Reason != string(hardening.RefusePushSize) || len(p.pushed) != 0 {
		t.Fatalf("%+v, pushed %d", a, len(p.pushed))
	}
}

type failingWriter struct{ client.Client }

func (failingWriter) Patch(context.Context, client.Object, client.Patch, ...client.PatchOption) error {
	return errors.New("apiserver unavailable")
}

// #505's review: a push the applier refuses or holds after its inspection has its fetched ref
// discarded; an applied one is left to Push.
func TestPushDiscardsOnRefusal(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate  func(p *fakePusher)
		discard int
	}{
		"applied":                 {func(*fakePusher) {}, 0},
		"over the caps":           {func(p *fakePusher) { p.in.Cumulative.Files = 11 }, 1},
		"a blocked path":          {func(p *fakePusher) { p.in.Files = append(p.in.Files, gitbundle.File{Path: "secrets/k", Status: "A"}) }, 1},
		"held for a maintainer":   {func(p *fakePusher) { p.in.Files = append(p.in.Files, gitbundle.File{Path: "go.mod", Status: "M"}) }, 1},
		"refused by GitHub":       {func(p *fakePusher) { p.pushErr = gitbundle.ErrPushRejected }, 1},
		"a lease lost":            {func(p *fakePusher) { p.pushErr = gitbundle.ErrLeaseLost }, 1},
		"a head another hand put": {func(p *fakePusher) { p.in.OldHead = newHead }, 1},
	} {
		t.Run(name, func(t *testing.T) {
			r, p, pr, _ := pushRig(t)
			tc.mutate(p)
			r.pushRequest(t, pr, "bundle-1")
			r.tick(t)
			if p.discarded != tc.discard {
				t.Fatalf("discarded %d, want %d", p.discarded, tc.discard)
			}
		})
	}
}
