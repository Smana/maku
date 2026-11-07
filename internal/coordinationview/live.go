// SPDX-License-Identifier: Apache-2.0

package coordinationview

import (
	"context"
	"fmt"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/ledger"
)

// Source reads a parent's unfiltered view live: the parent Task, the Tasks labelled with its
// coordination lineage (implementation children and integration attempts), and every retained day
// ledger's settled and outstanding reserved spend, handed unchanged to the core's View. Reader must
// be uncached (the manager's API reader): a stale informer must never decide what a viewer sees.
// It only reads Tasks and ConfigMaps (get, list).
type Source struct {
	Reader    client.Reader
	Namespace string
}

// View is parentTask's core view. A missing Task, or one that is no coordination parent, is
// ErrNoCoordination; any read failure is ErrCoordinationUnavailable.
func (s Source) View(ctx context.Context, parentTask string) (coordination.CoordinationView, error) {
	var parent v1alpha1.Task
	err := s.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: parentTask}, &parent)
	switch {
	case apierrors.IsNotFound(err):
		return coordination.CoordinationView{}, fmt.Errorf("task %s: %w", parentTask, ErrNoCoordination)
	case err != nil:
		return coordination.CoordinationView{}, fmt.Errorf("read parent: %w", ErrCoordinationUnavailable)
	case parent.Spec.Kind != v1alpha1.TaskKindCoordination:
		return coordination.CoordinationView{}, fmt.Errorf("task %s: %w", parentTask, ErrNoCoordination)
	}
	var lineage v1alpha1.TaskList
	if err := s.Reader.List(ctx, &lineage, client.InNamespace(s.Namespace),
		client.MatchingLabels{v1alpha1.LabelCoordinationParent: parentTask}); err != nil {
		return coordination.CoordinationView{}, fmt.Errorf("list lineage: %w", ErrCoordinationUnavailable)
	}
	var attempts []string
	if cs := parent.Status.Coordination; cs != nil {
		for _, a := range cs.IntegrationAttempts {
			attempts = append(attempts, a.Task)
		}
	}
	var children, integrations []v1alpha1.Task
	for _, t := range lineage.Items {
		switch {
		case t.Spec.Kind == v1alpha1.TaskKindIntegration && slices.Contains(attempts, t.Name):
			integrations = append(integrations, t)
		case t.Spec.Kind == "":
			children = append(children, t)
		}
	}
	settled, err := ledger.SettledByRun(ctx, s.Reader, s.Namespace)
	if err != nil {
		return coordination.CoordinationView{}, fmt.Errorf("read settled: %w", ErrCoordinationUnavailable)
	}
	reserved, err := ledger.ReservedByRun(ctx, s.Reader, s.Namespace)
	if err != nil {
		return coordination.CoordinationView{}, fmt.Errorf("read reserved: %w", ErrCoordinationUnavailable)
	}
	usage := coordination.LedgerUsage{ByRun: map[string]coordination.RunUsage{}}
	for id, n := range settled {
		u := usage.ByRun[id]
		u.SettledMicroUSD = n
		usage.ByRun[id] = u
	}
	for id, n := range reserved {
		u := usage.ByRun[id]
		u.ReservedMicroUSD = n
		usage.ByRun[id] = u
	}
	return coordination.View(parent, children, integrations, usage), nil
}
