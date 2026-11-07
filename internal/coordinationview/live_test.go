// SPDX-License-Identifier: Apache-2.0

package coordinationview

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/ledger"
)

const ns = "agent-system"

func liveWorld(t *testing.T) (v1alpha1.Task, v1alpha1.Task, v1alpha1.Task, []client.Object) {
	t.Helper()
	plan := v1alpha1.CoordinationPlan{ID: "pppppppp", Version: 1, Children: []v1alpha1.CoordinationChild{{ID: "contract", Repository: repoA}}}
	parent := v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "pppppppp", Namespace: ns}, Spec: v1alpha1.TaskSpec{Kind: v1alpha1.TaskKindCoordination}}
	parent.Status.Coordination = &v1alpha1.CoordinationStatus{Plan: &plan, PlanHash: "h",
		Children:            []v1alpha1.CoordinationChildStatus{{ID: "contract", Repository: repoA, Task: "aaaaaaaa", Issue: 1}},
		IntegrationAttempts: []v1alpha1.IntegrationAttempt{{Task: "iiiiiiii", Purpose: "final", PlanHash: "h", State: "pending"}}}
	label := map[string]string{v1alpha1.LabelCoordinationParent: "pppppppp"}
	child := v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "aaaaaaaa", Namespace: ns, Labels: label}}
	child.Status.Runs = []v1alpha1.RunRecord{{ID: "runaaaaa"}}
	integ := v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "iiiiiiii", Namespace: ns, Labels: label}, Spec: v1alpha1.TaskSpec{Kind: v1alpha1.TaskKindIntegration}}
	integ.Status.Runs = []v1alpha1.RunRecord{{ID: "runiiiii"}}
	other := v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "zzzzzzzz", Namespace: ns}}
	day := func(d string, data map[string]string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: ledger.Prefix + d, Namespace: ns}, Data: data}
	}
	objs := []client.Object{parent.DeepCopy(), child.DeepCopy(), integ.DeepCopy(), other.DeepCopy(),
		day("20261106", map[string]string{ledger.Settled + "runaaaaa": "700", ledger.Reserved + "runaaaaa": `{"principal":"p","maxMicroUSD":100}`}),
		day("20261107", map[string]string{ledger.Settled + "runiiiii": "30", ledger.Reserved + "runaaaaa": `{"principal":"p","maxMicroUSD":50}`})}
	return parent, child, integ, objs
}

func liveScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// failing is a reader whose List fails: a ledger or a child list that cannot be read.
type failing struct{ client.Reader }

func (failing) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("apiserver down")
}

func TestSourceUsesTheCoreViewAndLiveReads(t *testing.T) {
	parent, child, integ, objs := liveWorld(t)
	r := fake.NewClientBuilder().WithScheme(liveScheme(t)).WithObjects(objs...).WithStatusSubresource(&v1alpha1.Task{}).Build()
	got, err := Source{Reader: r, Namespace: ns}.View(t.Context(), "pppppppp")
	if err != nil {
		t.Fatal(err)
	}
	want := coordination.View(parent, []v1alpha1.Task{child}, []v1alpha1.Task{integ}, coordination.LedgerUsage{ByRun: map[string]coordination.RunUsage{
		"runaaaaa": {ReservedMicroUSD: 150, SettledMicroUSD: 700}, "runiiiii": {SettledMicroUSD: 30}}})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
	if got.Children[0].SettledMicroUSD != 700 || got.Children[0].ReservedMicroUSD != 150 || got.Budget.IntegrationSettledMicroUSD != 30 {
		t.Fatalf("ledger not read across days: %+v", got)
	}

	for name, task := range map[string]string{"missing": "nnnnnnnn", "not a parent": "zzzzzzzz"} {
		if _, err := (Source{Reader: r, Namespace: ns}).View(t.Context(), task); !errors.Is(err, ErrNoCoordination) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := (Source{Reader: failing{r}, Namespace: ns}).View(t.Context(), "pppppppp"); !errors.Is(err, ErrCoordinationUnavailable) {
		t.Errorf("a failing list: %v", err)
	}
}
