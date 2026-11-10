// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"
)

// A printer column reads its status field's JSON path: a false or 0 value must be present, or
// kubectl prints an empty cell.
func TestPrinterColumnFieldsAreAlwaysPresent(t *testing.T) {
	for name, tc := range map[string]struct {
		status any
		keys   []string
	}{
		"factory":     {FactoryStatus{}, []string{`"stopped":false`, `"repositories":0`}},
		"mergepolicy": {MergePolicyStatus{}, []string{`"classCount":0`, `"liveCount":0`}},
	} {
		b, err := json.Marshal(tc.status)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range tc.keys {
			if !strings.Contains(string(b), k) {
				t.Errorf("%s status %s lacks %s", name, b, k)
			}
		}
	}
}
