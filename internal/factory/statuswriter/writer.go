// SPDX-License-Identifier: Apache-2.0

package statuswriter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/factory/v1alpha1"
)

const defaultEvery = 30 * time.Second

// Writer publishes the status of the factory resources. Only the leader runs it
// (NeedLeaderElection), on a ticker and whenever it is kicked, and it writes an object only when
// its computed status differs from the stored one.
type Writer struct {
	Client    client.Client // the manager's
	Namespace string
	Inputs    func(ctx context.Context) Inputs
	Every     time.Duration // 30s when zero
	Log       *slog.Logger

	kick chan struct{}
	once sync.Once
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
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := w.WriteOnce(ctx); err != nil && ctx.Err() == nil && w.Log != nil {
			w.Log.Warn("factory resources status not written", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		case <-w.kick:
		}
	}
}

// WriteOnce lists the three kinds, computes Desired and updates each object whose status differs.
func (w *Writer) WriteOnce(ctx context.Context) error {
	var (
		factories v1alpha1.FactoryList
		repos     v1alpha1.RepositoryList
		policies  v1alpha1.MergePolicyList
	)
	in := client.InNamespace(w.Namespace)
	if err := w.Client.List(ctx, &factories, in); err != nil {
		return fmt.Errorf("listing Factories: %w", err)
	}
	if err := w.Client.List(ctx, &repos, in); err != nil {
		return fmt.Errorf("listing Repositories: %w", err)
	}
	if err := w.Client.List(ctx, &policies, in); err != nil {
		return fmt.Errorf("listing MergePolicies: %w", err)
	}
	d := Desired(w.Inputs(ctx), factories.Items, repos.Items, policies.Items, metav1.Now())
	var errs []error
	for i := range factories.Items {
		o := &factories.Items[i]
		errs = append(errs, update(ctx, w.Client, o, func() bool {
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
		errs = append(errs, update(ctx, w.Client, o, func() bool {
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
		errs = append(errs, update(ctx, w.Client, o, func() bool {
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

// update re-gets o on each attempt (a conflict means it moved), lets set recompute its status
// against the fresh copy, and writes only when set says it changed. A vanished object is skipped.
// The first attempt uses the listed copy, so a quiet pass costs no extra read.
func update(ctx context.Context, c client.Client, o client.Object, set func() bool) error {
	first := true
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if !first {
			if err := c.Get(ctx, client.ObjectKeyFromObject(o), o); err != nil {
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

// keepTimes gives each desired condition the stored lastTransitionTime when its status, reason
// and message did not change: the time moves only when the condition does.
func keepTimes(stored, desired []metav1.Condition) []metav1.Condition {
	for i := range desired {
		for _, s := range stored {
			if s.Type == desired[i].Type && s.Status == desired[i].Status && s.Reason == desired[i].Reason && s.Message == desired[i].Message {
				desired[i].LastTransitionTime = s.LastTransitionTime
			}
		}
	}
	return desired
}
