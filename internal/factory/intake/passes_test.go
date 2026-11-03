// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
)

// lockedIssue is the fake forge with issue n locked: GitHub refuses every comment on it, and
// will on every poll.
type lockedIssue struct {
	*forgetest.Fake
	n int
}

func (f lockedIssue) Comment(ctx context.Context, n int, body string) error {
	if n == f.n {
		return errors.New("forge: comment: 403 Unable to create comment because issue is locked")
	}
	return f.Fake.Comment(ctx, n, body)
}

// F-6: one stop or revert item that fails, a locked issue refusing the factory's answer or a
// task that cannot be patched, is reported, and the same poll still serves every other item,
// the ready label and the orphan scan.
func TestOneFailingItemDoesNotStallThePoll(t *testing.T) {
	task := func(name string, phase string, pr *v1alpha1.PullRequestRef) client.Object {
		return &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "agent-system"},
			Status: v1alpha1.TaskStatus{Phase: phase, PullRequest: pr}}
	}
	f := forgetest.NewFake()
	// Stops: #9's is a non-maintainer's, answered on a locked issue; #12's reaches a running task.
	f.SetLabeled(LabelStop, forge.Item{Number: 9}, forge.Item{Number: 12, PullRequest: true})
	f.SetEvents(9, forge.LabelEvent{Actor: "someone", Label: LabelStop, At: t0})
	f.SetEvents(12, forge.LabelEvent{Actor: "Smana", Label: LabelStop, At: t0})
	// Reverts: #16's task cannot be patched; #14's can.
	f.SetLabeled(LabelRevert, forge.Item{Number: 16, PullRequest: true}, forge.Item{Number: 14, PullRequest: true})
	f.SetEvents(16, forge.LabelEvent{Actor: "Smana", Label: LabelRevert, At: t0})
	f.SetEvents(14, forge.LabelEvent{Actor: "Smana", Label: LabelRevert, At: t0})
	labelled(f, 13, "Smana")
	f.SetAgentPulls(forge.AgentPull{Number: 22, HeadRef: "agent/bbbbbbbb", Labels: []string{"factory/class:review"}})
	p, c := poller(t, lockedIssue{Fake: f, n: 9},
		task("3buqdlot", v1alpha1.PhaseImplementing, &v1alpha1.PullRequestRef{Number: 12}),
		task("4buqdlot", v1alpha1.PhaseDone, &v1alpha1.PullRequestRef{Number: 14, AutoMerged: true}),
		task("5buqdlot", v1alpha1.PhaseDone, &v1alpha1.PullRequestRef{Number: 16, AutoMerged: true}))
	p.Client = interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
		Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, o ...client.PatchOption) error {
			if obj.GetName() == "5buqdlot" {
				return errors.New("apiserver unavailable")
			}
			return cl.Patch(ctx, obj, patch, o...)
		}})
	err := p.Poll(t.Context())
	if err == nil || !strings.Contains(err.Error(), "locked") || !strings.Contains(err.Error(), "5buqdlot") {
		t.Fatalf("both failures are reported: %v", err)
	}
	annotation := func(name, key string) string {
		var got v1alpha1.Task
		_ = c.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: name}, &got)
		return got.Annotations[key]
	}
	if annotation("3buqdlot", v1alpha1.AnnotationStop) != "label" || len(f.Removed(12)) != 1 {
		t.Errorf("the stop after the locked issue's: %q removed %v", annotation("3buqdlot", v1alpha1.AnnotationStop), f.Removed(12))
	}
	if len(f.Removed(9)) != 0 {
		t.Errorf("an unanswered stop label stays for the next poll: %v", f.Removed(9))
	}
	if annotation("4buqdlot", v1alpha1.AnnotationRevert) != "label" || len(f.Removed(14)) != 1 || len(f.Removed(16)) != 0 {
		t.Errorf("the revert after the failed one: %q removed #14 %v #16 %v", annotation("4buqdlot", v1alpha1.AnnotationRevert),
			f.Removed(14), f.Removed(16))
	}
	if n := len(tasks(t, c)); n != 4 || !slices.Equal(f.Removed(13), []string{"factory/ready"}) {
		t.Errorf("the ready label: %d tasks, removed %v", n, f.Removed(13))
	}
	if !slices.Equal(f.Added(22), []string{LabelOrphaned}) {
		t.Errorf("the orphan scan: %v", f.Added(22))
	}
}
