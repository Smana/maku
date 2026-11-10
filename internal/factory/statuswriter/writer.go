// SPDX-License-Identifier: Apache-2.0

package statuswriter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/factory/v1alpha1"
)

// defaultEvery is the pass interval; absentEvery the interval while none of the three CRDs is
// installed, a kick still running a pass at once.
const (
	defaultEvery = 30 * time.Second
	absentEvery  = 5 * time.Minute
)

// Writer publishes the status of the factory resources. Only the leader runs it
// (NeedLeaderElection), on a ticker and whenever it is kicked, and it writes an object only when
// its computed status differs from the stored one.
type Writer struct {
	Client client.Client // the manager's: writes status
	// Reader lists the objects: the manager's uncached reader, so a kind whose CRD is missing
	// starts no informer. Client when nil.
	Reader    client.Reader
	Namespace string
	Inputs    func(ctx context.Context) Inputs
	Every     time.Duration // 30s when zero
	Log       *slog.Logger

	kick chan struct{}
	once sync.Once
	// absent is true when the last pass found none of the three CRDs installed.
	absent atomic.Bool
}

func (w *Writer) init() { w.once.Do(func() { w.kick = make(chan struct{}, 1) }) }

// Kick asks for a pass soon; kicks coalesce.
func (w *Writer) Kick() {
	w.init()
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// NeedLeaderElection makes only the leader write status.
func (w *Writer) NeedLeaderElection() bool { return true }

// Start writes on Every and on each Kick until ctx ends.
func (w *Writer) Start(ctx context.Context) error {
	w.init()
	every := w.Every
	if every <= 0 {
		every = defaultEvery
	}
	t := time.NewTimer(every)
	defer t.Stop()
	for {
		if err := w.WriteOnce(ctx); err != nil && ctx.Err() == nil && w.Log != nil {
			w.Log.Warn("factory resources status not written", "error", err)
		}
		next := every
		if w.absent.Load() {
			next = absentEvery
		}
		t.Reset(next)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		case <-w.kick:
		}
	}
}

// WriteOnce lists the three kinds, computes Desired and updates each object whose status differs.
// With no object of any kind it reads none of the factory's inputs.
func (w *Writer) WriteOnce(ctx context.Context) error {
	var (
		factories v1alpha1.FactoryList
		repos     v1alpha1.RepositoryList
		policies  v1alpha1.MergePolicyList
	)
	reader := w.Reader
	if reader == nil {
		reader = w.Client
	}
	var errs []error
	missing := 0
	for _, l := range []client.ObjectList{&factories, &repos, &policies} {
		absent, err := list(ctx, reader, l, w.Namespace)
		if err != nil {
			errs = append(errs, err)
		}
		if absent {
			missing++
		}
	}
	w.absent.Store(missing == 3)
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if len(factories.Items)+len(repos.Items)+len(policies.Items) == 0 {
		return nil
	}
	d := Desired(w.Inputs(ctx), factories.Items, repos.Items, policies.Items, metav1.Now())
	for i := range factories.Items {
		o := &factories.Items[i]
		errs = append(errs, update(ctx, w.Client, reader, o, func() bool {
			want := d.Factories[o.Name]
			want.Conditions = keepTimes(o.Status.Conditions, want.Conditions)
			if apiequality.Semantic.DeepEqual(o.Status, want) {
				return false
			}
			o.Status = want
			return true
		}))
	}
	for i := range repos.Items {
		o := &repos.Items[i]
		errs = append(errs, update(ctx, w.Client, reader, o, func() bool {
			want := d.Repositories[o.Name]
			want.Conditions = keepTimes(o.Status.Conditions, want.Conditions)
			if apiequality.Semantic.DeepEqual(o.Status, want) {
				return false
			}
			o.Status = want
			return true
		}))
	}
	for i := range policies.Items {
		o := &policies.Items[i]
		errs = append(errs, update(ctx, w.Client, reader, o, func() bool {
			want := d.MergePolicies[o.Name]
			want.Conditions = keepTimes(o.Status.Conditions, want.Conditions)
			if apiequality.Semantic.DeepEqual(o.Status, want) {
				return false
			}
			o.Status = want
			return true
		}))
	}
	return errors.Join(errs...)
}

// list fills l; a kind whose CRD is not installed is an empty list, absent, so the others still
// go on.
func list(ctx context.Context, r client.Reader, l client.ObjectList, ns string) (absent bool, err error) {
	err = r.List(ctx, l, client.InNamespace(ns))
	switch {
	case err == nil:
		return false, nil
	case apimeta.IsNoMatchError(err) || apierrors.IsNotFound(err):
		return true, nil
	}
	return false, fmt.Errorf("listing %T: %w", l, err)
}

// update re-gets o through r on each attempt after the first (a conflict means it moved; r is
// the uncached reader, so the copy is the API server's, not a cache's that may be the stale one),
// lets set recompute its status against the fresh copy, and writes through c only when set says
// it changed. A vanished object is skipped. The first attempt uses the listed copy, so a quiet
// pass costs no extra read.
func update(ctx context.Context, c client.Client, r client.Reader, o client.Object, set func() bool) error {
	first := true
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if !first {
			if err := r.Get(ctx, client.ObjectKeyFromObject(o), o); err != nil {
				return err
			}
		}
		first = false
		if !set() {
			return nil
		}
		return c.Status().Update(ctx, o)
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// keepTimes gives each desired condition the stored lastTransitionTime when its type and status
// did not change, as apimeta.SetStatusCondition does: a new reason or message is no transition.
func keepTimes(stored, desired []metav1.Condition) []metav1.Condition {
	for i := range desired {
		for _, s := range stored {
			if s.Type == desired[i].Type && s.Status == desired[i].Status {
				desired[i].LastTransitionTime = s.LastTransitionTime
			}
		}
	}
	return desired
}
