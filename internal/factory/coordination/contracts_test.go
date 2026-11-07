// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"encoding/json"
	"slices"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// The neutral contracts keep one representation of the CRD records (an alias, not a copy) and the
// wire names the experience plan imports.
func TestContractsWireNames(t *testing.T) {
	for name, v := range map[string]any{"ParentConfirmation": ParentConfirmation{}, "ChildAuthorization": ChildAuthorization{}, "CoordinationIncident": CoordinationIncident{}} {
		switch v.(type) {
		case v1alpha1.ParentConfirmation, v1alpha1.ChildAuthorization, v1alpha1.CoordinationIncident:
		default:
			t.Errorf("%s is a copy, not the CRD record", name)
		}
	}
	for name, c := range map[string]struct {
		v    any
		want []string
	}{
		"confirmation": {CoordinationConfirmation{}, []string{"plan", "planHash", "planning"}},
		"planning":     {PlanningReconciliation{}, []string{"attemptID", "effectiveRequester", "reservedMicroUSD", "settledMicroUSD"}},
		"child view": {CoordinationChildView{}, []string{"id", "repository", "task", "room", "issueURL", "phase", "reason", "fingerprint",
			"dependsOn", "consumes", "allocationMicroUSD", "settledMicroUSD", "reservedMicroUSD"}},
		"view": {CoordinationView{}, []string{"parentTask", "phase", "reason", "plan", "planHash", "childAuthorizations", "children",
			"artifacts", "integrationAttempts", "mergeOrder", "budget"}},
		"checkout": {CheckoutManifest{}, []string{"parentTask", "planVersion", "planHash", "children"}},
	} {
		b, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		var got []string
		for k := range m {
			got = append(got, k)
		}
		slices.Sort(got)
		want := slices.Sorted(slices.Values(c.want))
		if !slices.Equal(got, want) {
			t.Errorf("%s: keys %v, want %v", name, got, want)
		}
	}
}
