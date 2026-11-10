// SPDX-License-Identifier: Apache-2.0

package statuswriter

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/Smana/maku/api/factory/v1alpha1"
)

// keepTimes is apimeta's rule: the stored lastTransitionTime stays while Type and Status do,
// whatever the reason and message say; a new status takes the desired time.
func TestKeepTimesFollowsTypeAndStatus(t *testing.T) {
	then := metav1.NewTime(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	stored := []metav1.Condition{
		{Type: "Accepted", Status: metav1.ConditionFalse, Reason: "Invalid", Message: "one", LastTransitionTime: then},
		{Type: "Connected", Status: metav1.ConditionTrue, Reason: "Healthy", LastTransitionTime: then},
	}
	desired := []metav1.Condition{
		{Type: "Accepted", Status: metav1.ConditionFalse, Reason: "DefinedTwice", Message: "two", LastTransitionTime: now},
		{Type: "Connected", Status: metav1.ConditionFalse, Reason: "Unhealthy", LastTransitionTime: now},
	}
	got := keepTimes(stored, desired)
	if !got[0].LastTransitionTime.Equal(&then) {
		t.Errorf("Accepted stayed False: time %v, want the stored %v", got[0].LastTransitionTime, then)
	}
	if !got[1].LastTransitionTime.Equal(&now) {
		t.Errorf("Connected turned False: time %v, want now", got[1].LastTransitionTime)
	}
}

func fakeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// A conflict re-reads the object through the uncached Reader, never the manager's cached client,
// whose copy may be the one that just conflicted.
func TestConflictRereadsThroughTheReader(t *testing.T) {
	r := repoObj("r", repo, 1)
	r.Namespace = "ns"
	conflicts := 0
	base := fake.NewClientBuilder().WithScheme(fakeScheme(t)).WithObjects(&r).WithStatusSubresource(&r).Build()
	c := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			t.Error("re-read through the cached client")
			return errors.New("cached read")
		},
		SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
			if conflicts++; conflicts == 1 {
				return apierrors.NewConflict(schema.GroupResource{Resource: "repositories"}, o.GetName(), errors.New("moved"))
			}
			return cl.SubResource(sub).Update(ctx, o, opts...)
		},
	})
	gets := 0
	reader := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			gets++
			return cl.Get(ctx, k, o, opts...)
		},
	})
	w := &Writer{Client: c, Reader: reader, Namespace: "ns", Inputs: func(context.Context) Inputs { return Inputs{Config: cfg()} }}
	if err := w.WriteOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if gets != 1 || conflicts != 2 {
		t.Fatalf("reader gets %d, updates %d: want one re-read and a second update", gets, conflicts)
	}
}

// noCRDs is a cluster without the three CRDs: every list is a NoMatch. It counts the lists.
type noCRDs struct {
	client.Reader
	lists atomic.Int32
}

func (n *noCRDs) List(context.Context, client.ObjectList, ...client.ListOption) error {
	n.lists.Add(1)
	return &apimeta.NoKindMatchError{GroupKind: schema.GroupKind{Group: v1alpha1.GroupVersion.Group, Kind: "Factory"}}
}

// Without the CRDs, a pass reads none of the factory's inputs, and the writer backs off to five
// minutes between passes; a kick still runs one.
func TestNoCRDsNoInputsAndBackOff(t *testing.T) {
	r := &noCRDs{}
	var inputs atomic.Int32
	w := &Writer{Reader: r, Every: time.Millisecond, Inputs: func(context.Context) Inputs { inputs.Add(1); return Inputs{} }}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()
	time.Sleep(100 * time.Millisecond) // a hundred ticks at Every
	if n := r.lists.Load(); n != 3 {
		t.Errorf("%d lists in 100 ticks, want one pass (3): no backoff", n)
	}
	w.Kick()
	deadline := time.Now().Add(5 * time.Second)
	for r.lists.Load() < 6 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := r.lists.Load(); n != 6 {
		t.Errorf("%d lists after a kick, want 6", n)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := inputs.Load(); n != 0 {
		t.Fatalf("Inputs read %d times with no object to write", n)
	}
}

// Installed CRDs with no objects: still no inputs read, and no backoff.
func TestNoObjectsNoInputs(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(fakeScheme(t)).Build()
	w := &Writer{Client: c, Inputs: func(context.Context) Inputs { t.Fatal("Inputs read with no object"); return Inputs{} }}
	if err := w.WriteOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
}
