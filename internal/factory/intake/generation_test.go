// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/factory/taskid"
)

// F-5: a maintainer removed from the config no longer lowers the count. bob's label made
// generation 1 and Smana's generation 2, both done; with bob gone, Smana's fresh label is
// generation 3, never a second generation 2 that AlreadyExists would swallow as the dedup.
func TestARemovedMaintainerDoesNotRewindTheGeneration(t *testing.T) {
	done := func(gen int) client.Object {
		return &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: taskid.Name(taskid.IssueKey("Smana/cloud-native-ref", 7, gen)),
			Namespace: "agent-system", Labels: map[string]string{v1alpha1.LabelIssue: "7"}},
			Spec:   v1alpha1.TaskSpec{Issue: 7, Repository: "Smana/cloud-native-ref"},
			Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone}}
	}
	f := forgetest.NewFake()
	labelled(f, 7, "bob", "Smana", "Smana") // the poller's maintainers are [Smana]: bob was removed
	p, c := poller(t, f, done(1), done(2))
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	name := taskid.Name(taskid.IssueKey("Smana/cloud-native-ref", 7, 3))
	var got v1alpha1.Task
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: "agent-system", Name: name}, &got); err != nil {
		t.Fatalf("generation 3 was not created: %v (tasks: %d)", err, len(tasks(t, c)))
	}
	if got.Spec.Source.RequestedBy != "github:Smana" || len(tasks(t, c)) != 3 {
		t.Fatalf("%+v, %d tasks", got.Spec.Source, len(tasks(t, c)))
	}
	if r := f.Removed(7); len(r) != 1 || r[0] != "factory/ready" || len(f.Comments(7)) != 0 {
		t.Fatalf("the label is consumed, with nothing to refuse: removed %q comments %q", r, f.Comments(7))
	}
}
