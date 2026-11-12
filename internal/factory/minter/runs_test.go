// SPDX-License-Identifier: Apache-2.0

package minter

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/runwatch"
)

var portable = runwatch.Kind{Group: "maku.ogenki.io", Namespace: "agents", NamePrefix: "maku-run-"}

var t0 = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

// fakeMint records each mint and revoke; tokens expire an hour after the clock.
type fakeMint struct {
	mu      sync.Mutex
	now     func() time.Time
	err     error
	n       int
	mints   []string // repo role enforced
	revoked []string
}

func (f *fakeMint) Mint(_ context.Context, repo, role string, enforced []hardening.OutputType) (forge.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return forge.Token{}, f.err
	}
	f.n++
	es := make([]string, len(enforced))
	for i, e := range enforced {
		es[i] = string(e)
	}
	f.mints = append(f.mints, repo+" "+role+" "+strings.Join(es, ","))
	return forge.Token{Value: "ghs_" + string(rune('a'+f.n)), ExpiresAt: f.now().Add(time.Hour)}, nil
}

func (f *fakeMint) Revoke(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, token)
	return nil
}

type refusals struct{ got []string }

func (r *refusals) RunTokenRefusal(_ context.Context, reason string) { r.got = append(r.got, reason) }

type rig struct {
	t       *testing.T
	c       client.Client
	base    client.Client
	rt      *RunTokens
	mint    *fakeMint
	refused *refusals
	clock   time.Time
	deletes int
}

func claim(id string, mut ...func(*unstructured.Unstructured)) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(portable.GVK())
	u.SetNamespace(portable.Namespace)
	u.SetName(portable.ClaimName(id))
	u.SetUID(types.UID("uid-" + id))
	u.SetLabels(map[string]string{runs.LabelTask: "task-1"})
	u.SetAnnotations(map[string]string{runwatch.SafeOutputsAnnotation: "pr"})
	_ = unstructured.SetNestedField(u.Object, "implementer", "spec", "role")
	_ = unstructured.SetNestedField(u.Object, "Smana/demo", "spec", "repository")
	for _, m := range mut {
		m(u)
	}
	return u
}

func phase(p string) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) { _ = unstructured.SetNestedField(u.Object, p, "status", "phase") }
}

func newRig(t *testing.T, objs ...client.Object) *rig {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = factoryv1.AddToScheme(s)
	runs.Scheme(s, portable)
	task := &factoryv1.Task{ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "agent-factory"}}
	task.Spec.Repository = "smana/Demo"
	done := &factoryv1.Task{ObjectMeta: metav1.ObjectMeta{Name: "task-done", Namespace: "agent-factory"}}
	done.Spec.Repository, done.Status.Phase = "Smana/demo", factoryv1.PhaseDone
	other := &factoryv1.Task{ObjectMeta: metav1.ObjectMeta{Name: "task-other", Namespace: "agent-factory"}}
	other.Spec.Repository = "Smana/other"
	r := &rig{t: t, clock: t0, refused: &refusals{}}
	secretKind := func(o any) bool {
		_, ok := o.(*corev1.Secret)
		_, okl := o.(*corev1.SecretList)
		return ok || okl
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(append([]client.Object{task, done, other}, objs...)...).
		WithStatusSubresource(&factoryv1.Task{}).Build()
	r.base = base
	r.c = interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			if secretKind(o) {
				t.Fatalf("the minter read Secret %s", k)
			}
			return c.Get(ctx, k, o, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, l client.ObjectList, opts ...client.ListOption) error {
			if secretKind(l) {
				t.Fatal("the minter listed Secrets")
			}
			return c.List(ctx, l, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
			r.deletes++
			return c.Delete(ctx, o, opts...)
		},
	})
	r.mint = &fakeMint{now: func() time.Time { return r.clock }}
	r.rt = &RunTokens{Minter: r.mint, Client: r.c, Runs: r.c, Tasks: r.c, Kind: portable, TaskNamespace: "agent-factory",
		Reserved: func(_ context.Context, id string) (bool, error) { return id == "apiapi22", nil },
		Metrics:  r.refused, Now: func() time.Time { return r.clock }}
	return r
}

// secret reads the run's Secret behind the minter's back: the test may, the minter may not.
func (r *rig) secret(id string) (*corev1.Secret, bool) {
	r.t.Helper()
	s := &corev1.Secret{}
	err := r.base.Get(r.t.Context(), types.NamespacedName{Namespace: "agents", Name: portable.ClaimName(id) + "-git"}, s)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return s, true
}

func (r *rig) tick() {
	r.t.Helper()
	if err := r.rt.Tick(r.t.Context()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) setPhase(id, p string) {
	r.t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(portable.GVK())
	if err := r.c.Get(r.t.Context(), types.NamespacedName{Namespace: "agents", Name: portable.ClaimName(id)}, u); err != nil {
		r.t.Fatal(err)
	}
	_ = unstructured.SetNestedField(u.Object, p, "status", "phase")
	if err := r.c.Update(r.t.Context(), u); err != nil {
		r.t.Fatal(err)
	}
}

// Plan PR3 to PR5: a Task-bound implementer run with pr enforced gets its own Secret, from the table.
func TestRunTokensTaskBoundRun(t *testing.T) {
	r := newRig(t, claim("abcdefgh"))
	r.tick()
	if !slices.Equal(r.mint.mints, []string{"Smana/demo implementer pr"}) {
		t.Fatalf("mints %q", r.mint.mints)
	}
	s, ok := r.secret("abcdefgh")
	if !ok {
		t.Fatal("no Secret")
	}
	if string(s.Data["token"]) != "ghs_b" || s.Labels[runwatch.RunTokenRunLabel] != "maku-run-abcdefgh" ||
		s.Labels[runwatch.RunTokenManagedByLabel] != "maku-factory" ||
		s.Annotations[runwatch.RunTokenExpiresAnnotation] != t0.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("Secret %+v", s)
	}
	if len(s.OwnerReferences) != 1 || s.OwnerReferences[0].UID != "uid-abcdefgh" || s.OwnerReferences[0].Kind != "AgentRun" ||
		s.OwnerReferences[0].BlockOwnerDeletion == nil || *s.OwnerReferences[0].BlockOwnerDeletion {
		t.Fatalf("ownerReferences %+v", s.OwnerReferences)
	}
	// Fresh: the next tick mints nothing.
	r.tick()
	if len(r.mint.mints) != 1 {
		t.Fatalf("a fresh token was re-minted: %q", r.mint.mints)
	}
}

func TestRunTokensBinding(t *testing.T) {
	api := func(u *unstructured.Unstructured) {
		u.SetLabels(nil)
		u.SetAnnotations(map[string]string{runs.AnnRequestHash: "h"})
	}
	task := func(name string) func(*unstructured.Unstructured) {
		return func(u *unstructured.Unstructured) { u.SetLabels(map[string]string{runs.LabelTask: name}) }
	}
	for _, tc := range []struct {
		name    string
		claim   *unstructured.Unstructured
		refused string
	}{
		{"an API run with a reservation", claim("apiapi22", api), ""},
		{"an API run without one", claim("apiapi33", api), RefusedUnboundAPI},
		{"an API run without its request hash", claim("apiapi22", api, func(u *unstructured.Unstructured) { u.SetAnnotations(nil) }), RefusedUnboundAPI},
		{"a Task on another repository", claim("abcdefgh", task("task-other")), RefusedRepositoryMismatch},
		{"a terminal Task", claim("abcdefgh", task("task-done")), RefusedUnboundTask},
		{"a missing Task", claim("abcdefgh", task("task-gone")), RefusedUnboundTask},
		{"a role outside the table", claim("abcdefgh", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, "scoper", "spec", "role")
		}), RefusedRoleUnknown},
		{"a run claiming the publisher's role", claim("abcdefgh", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, "publisher", "spec", "role")
		}), RefusedRoleUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, tc.claim)
			r.tick()
			r.tick() // a refusal is counted once per run
			_, has := r.secret(tc.claim.GetName()[len(portable.NamePrefix):])
			if tc.refused == "" {
				if len(r.mint.mints) != 1 || !has || len(r.refused.got) != 0 {
					t.Fatalf("mints %q Secret %v refusals %q", r.mint.mints, has, r.refused.got)
				}
				return
			}
			if len(r.mint.mints) != 0 || has || !slices.Equal(r.refused.got, []string{tc.refused}) {
				t.Fatalf("mints %q Secret %v refusals %q, want one %s", r.mint.mints, has, r.refused.got, tc.refused)
			}
		})
	}
}

// Plan PR5: refreshed with under 10 minutes left; a Suspended run is left alone and re-minted
// when it leaves Suspended.
func TestRunTokensRefresh(t *testing.T) {
	r := newRig(t, claim("abcdefgh"))
	r.tick()
	r.clock = t0.Add(49 * time.Minute) // 11 minutes left
	r.tick()
	if len(r.mint.mints) != 1 {
		t.Fatalf("re-minted with 11 minutes left: %q", r.mint.mints)
	}
	r.clock = t0.Add(51 * time.Minute) // 9 minutes left
	r.tick()
	if len(r.mint.mints) != 2 {
		t.Fatalf("not re-minted with 9 minutes left: %q", r.mint.mints)
	}
	if s, _ := r.secret("abcdefgh"); string(s.Data["token"]) != "ghs_c" ||
		s.Annotations[runwatch.RunTokenExpiresAnnotation] != r.clock.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("the Secret was not re-applied: %+v", s)
	}

	r.setPhase("abcdefgh", "Suspended")
	r.clock = r.clock.Add(55 * time.Minute)
	r.tick()
	if len(r.mint.mints) != 2 {
		t.Fatalf("a Suspended run was refreshed: %q", r.mint.mints)
	}
	r.setPhase("abcdefgh", "Running")
	r.tick()
	if len(r.mint.mints) != 3 {
		t.Fatalf("not re-minted on leaving Suspended: %q", r.mint.mints)
	}
}

// Plan PR5: at a terminal phase the token held is revoked and the Secret deleted; NotFound is
// success. After a restart (an empty memory) a live run is minted afresh, a terminal one only deleted.
func TestRunTokensTerminal(t *testing.T) {
	r := newRig(t, claim("abcdefgh"), claim("bbbbbbbb", phase("Succeeded")))
	r.tick()
	if !slices.Equal(r.mint.mints, []string{"Smana/demo implementer pr"}) {
		t.Fatalf("mints %q: only the live run", r.mint.mints)
	}
	if r.deletes != 1 {
		t.Fatalf("deletes %d: the terminal run's (absent) Secret, NotFound being success", r.deletes)
	}
	r.tick()
	if r.deletes != 1 {
		t.Fatalf("deletes %d: a deleted Secret is deleted once", r.deletes)
	}
	r.setPhase("abcdefgh", "Failed")
	r.tick()
	if !slices.Equal(r.mint.revoked, []string{"ghs_b"}) {
		t.Fatalf("revoked %q", r.mint.revoked)
	}
	if _, ok := r.secret("abcdefgh"); ok {
		t.Fatal("the ended run's Secret is still there")
	}

	// A restart: a fresh RunTokens, nothing in memory.
	r2 := newRig(t, claim("cccccccc"), claim("dddddddd", phase("Succeeded")))
	r2.tick()
	if len(r2.mint.mints) != 1 || len(r2.mint.revoked) != 0 || r2.deletes != 1 {
		t.Fatalf("mints %q revoked %q deletes %d", r2.mint.mints, r2.mint.revoked, r2.deletes)
	}
}

// A mint failure changes no Secret, logs the result, and is retried at the next tick.
func TestRunTokensMintFailure(t *testing.T) {
	r := newRig(t, claim("abcdefgh"))
	r.mint.err = errors.New("minter: mint implementer's token on Smana/demo: " + forge.ErrNotInstalled.Error())
	r.tick()
	if _, ok := r.secret("abcdefgh"); ok {
		t.Fatal("a failed mint wrote a Secret")
	}
	r.mint.err = nil
	r.tick()
	if _, ok := r.secret("abcdefgh"); !ok || len(r.mint.mints) != 1 {
		t.Fatalf("not retried: %q", r.mint.mints)
	}
}

// Ensure, the Created hook's target, mints the first token at once.
func TestRunTokensEnsure(t *testing.T) {
	c := claim("abcdefgh")
	r := newRig(t, c)
	r.rt.Ensure(t.Context(), c)
	if _, ok := r.secret("abcdefgh"); !ok {
		t.Fatal("Ensure wrote no Secret")
	}
	r.tick()
	if len(r.mint.mints) != 1 {
		t.Fatalf("the tick re-minted what Ensure applied: %q", r.mint.mints)
	}
}
