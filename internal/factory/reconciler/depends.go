// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// Dependency wait and drop reasons (phase H2).
const (
	ReasonWaitingDependency = "waiting_dependency"
	ReasonDependencyDropped = "dependency_dropped"
)

type depState int

const (
	depSatisfied depState = iota
	depWaiting
	depDropped
)

// dependencyState evaluates t.Spec.DependsOn against factory Tasks and GitHub issues in the same
// repository (phase H2):
//   - depDropped when any dependency issue was closed without a merged pull request;
//   - depWaiting when no dependency was dropped and at least one has no merged pull request yet;
//   - depSatisfied when every dependency has a merged pull request.
func (r *Reconciler) dependencyState(ctx context.Context, t *v1alpha1.Task) (depState, error) {
	if len(t.Spec.DependsOn) == 0 {
		return depSatisfied, nil
	}
	var list v1alpha1.TaskList
	if err := r.Client.List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
		return depWaiting, err
	}
	byIssue := make(map[int][]*v1alpha1.Task)
	for i := range list.Items {
		o := &list.Items[i]
		if o.Spec.Issue > 0 && strings.EqualFold(o.Spec.Repository, t.Spec.Repository) {
			byIssue[o.Spec.Issue] = append(byIssue[o.Spec.Issue], o)
		}
	}
	waiting := false
	for _, dep := range t.Spec.DependsOn {
		st, err := r.oneDependencyState(ctx, int(dep), byIssue[int(dep)])
		if err != nil {
			return depWaiting, err
		}
		switch st {
		case depDropped:
			return depDropped, nil
		case depWaiting:
			waiting = true
		}
	}
	if waiting {
		return depWaiting, nil
	}
	return depSatisfied, nil
}

func (r *Reconciler) oneDependencyState(ctx context.Context, depIssue int, tasks []*v1alpha1.Task) (depState, error) {
	for _, dt := range tasks {
		if taskMergedPR(dt) {
			return depSatisfied, nil
		}
	}
	for _, dt := range tasks {
		if dt.Status.PullRequest != nil && dt.Status.PullRequest.Number > 0 {
			pr, err := r.gh(ctx).PullRequest(ctx, dt.Status.PullRequest.Number)
			if err == nil && pr.State == "MERGED" {
				return depSatisfied, nil
			}
		}
	}
	iss, err := r.gh(ctx).Issue(ctx, depIssue)
	if err != nil {
		return depWaiting, err
	}
	if strings.EqualFold(iss.State, "CLOSED") {
		return depDropped, nil
	}
	return depWaiting, nil
}

func taskMergedPR(dt *v1alpha1.Task) bool {
	if dt.Status.Phase == v1alpha1.PhaseReverted {
		return false
	}
	if dt.Status.PullRequest != nil && dt.Status.PullRequest.MergedAt != nil {
		return true
	}
	if dt.Status.Phase == v1alpha1.PhaseMerged || dt.Status.Phase == v1alpha1.PhaseVerifying {
		return true
	}
	return dt.Status.Phase == v1alpha1.PhaseDone &&
		(dt.Status.PullRequest != nil || dt.Status.Reason == ReasonChildrenMerged)
}
