// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
)

type flakyForge struct {
	*forgetest.Fake
	down bool
}

func (f *flakyForge) Comment(ctx context.Context, n int, body string) error {
	if f.down {
		return errors.New("github down")
	}
	return f.Fake.Comment(ctx, n, body)
}

// Probe 2: GitHub is down when the task ends; the end is never narrated afterwards.
func TestProbeTheEndIsNarratedAfterAnOutage(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	ff := &flakyForge{Fake: g.f}
	g.r.Forge = ff
	tk := g.reconcile(t, "3buqdlot", 3)
	tk.Annotations = map[string]string{v1alpha1.AnnotationStop: "true"}
	_ = g.c.Update(t.Context(), tk)
	ff.down = true
	_, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot"))
	t.Logf("reconcile during outage: %v", err)
	ff.down = false
	g.r.Now = func() time.Time { return now.Add(g.r.settleWindow()) } // the end waits for the usage to settle (#42)
	for range 3 {
		_, _ = g.r.Reconcile(t.Context(), reqFor("3buqdlot"))
	}
	var got v1alpha1.Task
	_ = g.c.Get(t.Context(), keyFor("3buqdlot"), &got)
	t.Logf("phase=%s narrated=%v comments=%d", got.Status.Phase, got.Status.Narrated, len(eventCommentBodies(g.f, 7)))
	if len(eventCommentBodies(g.f, 7)) < 2 {
		t.Errorf("DEFECT: the Stopped end was never narrated after GitHub came back")
	}
}

// landedForge posts the comment and then reports an error, once: the double failure SO names.
type landedForge struct {
	*forgetest.Fake
	lie bool
}

func (f *landedForge) Comment(ctx context.Context, n int, body string) error {
	if err := f.Fake.Comment(ctx, n, body); err != nil {
		return err
	}
	if f.lie {
		f.lie = false
		return errors.New("502 after the write")
	}
	return nil
}

// Ruling SO: the end is in the outbox with the phase; GitHub down, then back, gives exactly one
// end comment, and the drained task is left alone.
func TestTheEndIsPostedOnceAfterAnOutage(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	ff := &flakyForge{Fake: g.f}
	g.r.Forge = ff
	tk := g.reconcile(t, "3buqdlot", 3)
	tk.Annotations = map[string]string{v1alpha1.AnnotationStop: "true"}
	_ = g.c.Update(t.Context(), tk)
	// The end waits for the usage to settle (#42); the outage is at the post, past the window.
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil {
		t.Fatal(err)
	}
	tk = g.reconcile(t, "3buqdlot", 0)
	if tk.Status.Phase != v1alpha1.PhaseStopped || len(tk.Status.Outbox) != 1 || !strings.HasPrefix(tk.Status.Outbox[0].Key, "end-stopped") {
		t.Fatalf("the phase and its narration are written together: %s %+v", tk.Status.Phase, tk.Status.Outbox)
	}
	// Past the settle window the usage settles and TaskTokens is recorded once; after that the
	// ended task is left alone again.
	g.r.Now = func() time.Time { return now.Add(g.r.settleWindow()) }
	ff.down = true
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err == nil {
		t.Fatal("an undrained outbox retries")
	}
	ff.down = false
	for range 2 {
		if res, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil || res.RequeueAfter != 0 {
			t.Fatalf("%v %v", err, res)
		}
	}
	tk = g.reconcile(t, "3buqdlot", 0)
	if n := strings.Count(strings.Join(eventCommentBodies(g.f, 7), "\n"), "was stopped"); n != 1 || len(tk.Status.Outbox) != 0 {
		t.Fatalf("%d end comments, outbox %+v", n, tk.Status.Outbox)
	}
	// The stop revoked the task's one run, counted once through the outage (F30).
	if strings.Join(g.metrics.recorded, "|") != "revoked manual|intervention stop none none none|task_tokens 0 standard solo review" {
		t.Fatalf("an ended task is drained, then settled once: %q", g.metrics.recorded)
	}
}

// A post that landed but answered an error is found by its marker on the retry, not repeated.
func TestALandedPostIsNotRepeated(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	lf := &landedForge{Fake: g.f, lie: true}
	g.r.Forge = lf
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, _ = g.r.Reconcile(t.Context(), reqFor("3buqdlot"))
	}
	if tk := g.reconcile(t, "3buqdlot", 2); tk.Status.Phase != v1alpha1.PhaseImplementing || len(tk.Status.Outbox) != 0 {
		t.Fatalf("%s %+v", tk.Status.Phase, tk.Status.Outbox)
	}
	if c := eventCommentBodies(g.f, 7); len(c) != 1 || !strings.Contains(c[0], "started run") {
		t.Fatalf("%q", c)
	}
}

// The started narration survives an outage at the run's start, and the run is recorded meanwhile.
func TestTheStartIsPostedAfterAnOutage(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "x"))
	ff := &flakyForge{Fake: g.f, down: true}
	g.r.Forge = ff
	g.reconcile(t, "3buqdlot", 2)
	_, _ = g.r.Reconcile(t.Context(), reqFor("3buqdlot"))
	tk := g.reconcile(t, "3buqdlot", 0)
	if tk.Status.Phase != v1alpha1.PhaseImplementing || len(tk.Status.Runs) != 1 || len(tk.Status.Outbox) != 1 {
		t.Fatalf("%s %+v", tk.Status.Phase, tk.Status.Outbox)
	}
	ff.down = false
	if tk := g.reconcile(t, "3buqdlot", 1); len(tk.Status.Outbox) != 0 || len(eventCommentBodies(g.f, 7)) != 1 || len(g.runs.specs) != 1 {
		t.Fatalf("%+v %q", tk.Status.Outbox, eventCommentBodies(g.f, 7))
	}
}

// Review M-a: a narration is posted only once the status write that queued it has succeeded.
// Here the write recording "ended, no PR" conflicts; the replay finds the PR the agent opened
// meanwhile, and the issue never hears "no pull request".
func TestNothingIsPostedBeforeItsTransitionIsWritten(t *testing.T) {
	conflict := false
	c := fake.NewClientBuilder().WithScheme(scheme()).WithStatusSubresource(&v1alpha1.Task{}, &roomv1.Room{}).
		WithObjects(issueTask("3buqdlot", 7, "x")).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
			if conflict {
				conflict = false
				return apierrors.NewConflict(schema.GroupResource{Resource: "tasks"}, o.GetName(), errors.New("stale"))
			}
			return cl.SubResource(sub).Update(ctx, o, opts...)
		}}).Build()
	g := newRig(t)
	g.c, g.r.Client = c, c
	g.reconcile(t, "3buqdlot", 3)
	g.runs.set(rid(0), "Succeeded")
	g.log.end(rid(0), "Succeeded", "agent_finished")
	conflict = true
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err == nil {
		t.Fatal("the conflict is returned")
	}
	if c := eventCommentBodies(g.f, 7); len(c) != 1 {
		t.Fatalf("posted before its transition was written: %q", c)
	}
	g.f.SetBranch("agent/3buqdlot", 12)
	g.f.SetPR(forge.PR{Number: 12, URL: "https://github.com/Smana/cloud-native-ref/pull/12", State: "OPEN"})
	tk := g.reconcile(t, "3buqdlot", 1)
	if all := strings.Join(eventCommentBodies(g.f, 7), "\n"); tk.Status.Phase != v1alpha1.PhaseAwaitingCI ||
		strings.Contains(all, "no pull request") || !strings.Contains(all, "#12") {
		t.Fatalf("%s %q", tk.Status.Phase, eventCommentBodies(g.f, 7))
	}
}

// F-2: a monitor pass that finds 9 follow-ups queues 18 comments (the merge's and the control
// issue's copy of each), over the CRD's 16. The status write must still be accepted, the outbox at
// 16, or the task wedges: every write refused, so the outbox is never drained.
func TestAMonitorPassWithManyFollowUpsStillWrites(t *testing.T) {
	g := monitorRig(t)
	// The API server enforces status.outbox's maxItems; the fake client does not, so this does.
	g.r.Client = interceptor.NewClient(g.c.(client.WithWatch), interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
		if tk, ok := o.(*v1alpha1.Task); ok && len(tk.Status.Outbox) > 16 {
			return apierrors.NewInvalid(schema.GroupKind{Group: v1alpha1.GroupVersion.Group, Kind: "Task"}, tk.Name,
				field.ErrorList{field.TooMany(field.NewPath("status", "outbox"), len(tk.Status.Outbox), 16)})
		}
		return cl.SubResource(sub).Update(ctx, o, opts...)
	}})
	g.r.Forge = &flakyForge{Fake: g.f, down: true} // nothing posts: the outbox stays as the write left it
	var ps []forge.MergedPull
	for n := 20; n < 29; n++ {
		ps = append(ps, later(n, "Smana", "fix", FollowUpOfLabel(12)))
	}
	g.f.SetMerged(ps...)
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); apierrors.IsInvalid(err) {
		t.Fatalf("the status write is refused: %v", err)
	}
	tk := g.reconcile(t, "3buqdlot", 0)
	if tk.Status.Monitor == nil || len(tk.Status.Monitor.FollowUps) != 9 || len(tk.Status.Outbox) != 16 {
		t.Fatalf("monitor %+v, outbox of %d", tk.Status.Monitor, len(tk.Status.Outbox))
	}
}

// goneForge is GitHub with issue gone deleted: every read or write of it is refused for good (F-3).
type goneForge struct {
	*forgetest.Fake
	gone int
}

func (f *goneForge) RecentComments(ctx context.Context, n int) ([]forge.Comment, error) {
	if n == f.gone {
		return nil, &forge.PermanentError{Status: 404, Err: errors.New("forge: list comments: 404 Not Found")}
	}
	return f.Fake.RecentComments(ctx, n)
}

func (f *goneForge) Comment(ctx context.Context, n int, body string) error {
	if n == f.gone {
		return &forge.PermanentError{Status: 404, Err: errors.New("forge: comment: 404 Not Found")}
	}
	return f.Fake.Comment(ctx, n, body)
}

// F-3: a comment GitHub refuses for good (here its issue was deleted) is dropped and recorded; the
// comments after it still post, the outbox empties and the ended task's room closes.
func TestANarrationRefusedForGoodIsDropped(t *testing.T) {
	tk := endedTask(v1alpha1.PhaseDone)
	tk.Status.Outbox = []v1alpha1.Narration{{Key: "to-the-deleted-issue", Number: 9, Body: "lost"}, {Key: "to-the-issue", Number: 7, Body: "kept"}}
	g := newRig(t, tk)
	withRoom(t, g)
	g.r.Forge = &goneForge{Fake: g.f, gone: 9}
	if _, err := g.r.Reconcile(t.Context(), reqFor("3buqdlot")); err != nil {
		t.Fatalf("a permanent refusal is not retried: %v", err)
	}
	got := g.reconcile(t, "3buqdlot", 0)
	if len(got.Status.Outbox) != 0 || !contains(eventCommentBodies(g.f, 7), "kept") || !slices.Contains(got.Status.Narrated, "to-the-deleted-issue") {
		t.Fatalf("outbox %+v, narrated %v, comments %q", got.Status.Outbox, got.Status.Narrated, eventCommentBodies(g.f, 7))
	}
	if !roomGone(t, g) {
		t.Fatal("the room waits on a comment GitHub will never take")
	}
}
