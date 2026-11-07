// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/runwatch"
)

// The demo's runs are the portable AgentRun, maku.ogenki.io/v1alpha1, whose Go type the factory's
// scheme already holds (it registers api/v1alpha1 for Room). Registering it again as unstructured
// panicked at start-up ("Double registration of different types"); the factory still reads and
// writes it as unstructured, which controller-runtime serves whatever the scheme holds.
func TestSchemeAcceptsAKindTheSchemeAlreadyTypes(t *testing.T) {
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	k := runwatch.Kind{Group: v1alpha1.GroupVersion.Group, Namespace: "agents", NamePrefix: "run-"}
	Scheme(s, k)
	c := Client{C: fake.NewClientBuilder().WithScheme(s).Build(), Kind: k}
	if err := c.Create(t.Context(), spec()); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok, err := c.Get(t.Context(), spec().RunID); err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v, want the run back", ok, err)
	}
}
