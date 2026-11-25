// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
)

// catalogPolicies are the chart's catalog (charts/agent-factory/tests/golden/catalog.yaml, the specs
// the merge policies review states), labelled as the chart labels them.
func catalogPolicies(t *testing.T) []Object {
	t.Helper()
	raw, err := os.ReadFile("../../../charts/agent-factory/tests/golden/catalog.yaml")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := Decode(raw)
	if err != nil || len(objs) != 3 {
		t.Fatalf("%v: %d policies, want 3", err, len(objs))
	}
	for i := range objs {
		objs[i].Generation, objs[i].Catalog = 1, true
	}
	return objs
}

// MR2, MR6, MR4: the three catalog policies are accepted alone, each bounded and carrying its own
// evidence level, and a Repository with no mergePolicies at all evaluates every one in shadow.
func TestCatalogPolicyIsShadowWithoutABinding(t *testing.T) {
	r := repo("ai-native-ref", "Smana/ai-native-ref")
	r.Spec["merge"] = mergeBlock(t)
	res, err := Assemble(file(t), append(catalogPolicies(t), r))
	if err != nil {
		t.Fatal(err, res.Refused)
	}
	if len(res.Refused) != 0 {
		t.Fatalf("refused: %+v", res.Refused)
	}
	rc := res.Config.RepositoryFor("Smana/ai-native-ref")
	for name, minimum := range map[string]int{"docs-links": 5, "deps-patch": 5, "docs": 20} {
		cl, ok := rc.Classes[name]
		switch {
		case !ok:
			t.Errorf("%s: not resolved", name)
		case !cl.Shadow || cl.Live:
			t.Errorf("%s: live %v shadow %v, want shadow", name, cl.Live, cl.Shadow)
		case !cl.Bounded(name):
			t.Errorf("%s: unbounded", name)
		case cl.MinClean(name) != minimum:
			t.Errorf("%s: %d clean decisions to promote, want %d", name, cl.MinClean(name), minimum)
		}
		if b := binding(res, "ai-native-ref", name); b == nil || !b.Accepted || b.Mode != config.ModeShadow {
			t.Errorf("%s: outcome %+v", name, b)
		}
	}
	if got := strings.Join(rc.Bound, " "); got != "deps-patch docs docs-links" {
		t.Fatalf("bound %q: the catalog's names, so the rules that read Bound see them", got)
	}
}

// A binding wins over the catalog: mode disabled switches a catalog policy off, live keeps it live,
// and a policy without the catalog label is evaluated only where it is bound.
func TestCatalogPolicyBindingWins(t *testing.T) {
	r := boundRepo(t, "ai-native-ref", "Smana/ai-native-ref", 1, `
- {name: docs-links, mode: live, holdoutChecks: [validate]}
- {name: docs, mode: disabled}
`)
	own := classPolicy(t, "alert-rules", 1, "allowedFiles: ['^observability/.+\\.ya?ml$']\n")
	res, err := Assemble(file(t), append(catalogPolicies(t), r, own))
	if err != nil {
		t.Fatal(err, res.Refused)
	}
	rc := res.Config.RepositoryFor("Smana/ai-native-ref")
	if cl := rc.Classes["docs"]; cl.Live || cl.Shadow {
		t.Fatalf("docs bound disabled: live %v shadow %v, want neither", cl.Live, cl.Shadow)
	}
	if cl := rc.Classes["docs-links"]; !cl.Live || !slices.Contains(cl.HoldoutChecks, "validate") {
		t.Fatalf("docs-links bound live: %+v", cl)
	}
	if !rc.Classes["deps-patch"].Shadow {
		t.Fatalf("the unbound catalog policy is shadow: %+v", rc.Classes)
	}
	if _, ok := rc.Classes["alert-rules"]; ok {
		t.Fatal("an unlabelled policy no Repository binds is not evaluated")
	}
	if got := strings.Join(rc.Bound, " "); got != "docs-links docs deps-patch" {
		t.Fatalf("bound %q: the Repository's bindings in order, then the catalog's", got)
	}
	if n := len(res.Bindings); n != 3 {
		t.Fatalf("%d binding outcomes, want 3 (two bound, one from the catalog)", n)
	}
}

func TestDecodeReadsTheCatalogLabel(t *testing.T) {
	objs, err := Decode([]byte(`apiVersion: maku.ogenki.io/v1alpha1
kind: MergePolicy
metadata: {name: docs, labels: {maku.ogenki.io/catalog: "true"}}
spec: {allowedFiles: ['^.+\.md$']}
---
apiVersion: maku.ogenki.io/v1alpha1
kind: MergePolicy
metadata: {name: own, labels: {maku.ogenki.io/catalog: "false"}}
spec: {allowedFiles: ['^.+\.md$']}
`))
	if err != nil || len(objs) != 2 || !objs[0].Catalog || objs[1].Catalog {
		t.Fatalf("%v %+v", err, objs)
	}
}
