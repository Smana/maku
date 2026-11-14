// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strings"
	"testing"
)

// MR9: policyManagement.stage replaces the six switches; set switches map to the highest stage
// they cover, and nothing set is manage.
func TestEffectiveStage(t *testing.T) {
	for name, tc := range map[string]struct {
		pm                       PolicyManagement
		want                     string
		reads, simulates, manage bool
	}{
		"nothing set":         {PolicyManagement{}, StageManage, true, true, true},
		"operators only":      {PolicyManagement{Operators: []string{"Smana"}}, StageManage, true, true, true},
		"stage read":          {PolicyManagement{Stage: StageRead}, StageRead, true, false, false},
		"stage simulate":      {PolicyManagement{Stage: StageSimulate}, StageSimulate, true, true, false},
		"read switch":         {PolicyManagement{Read: true}, StageRead, true, false, false},
		"simulation switches": {PolicyManagement{Read: true, Simulation: true, DualEvaluation: true}, StageSimulate, true, true, false},
		"promotion switch":    {PolicyManagement{Read: true, Promotion: true}, StageManage, true, true, true},
		"memory switch":       {PolicyManagement{Read: true, ApprovalMemory: true}, StageManage, true, true, true},
		"authority switch":    {PolicyManagement{Read: true, RepositoryAuthority: true}, StageManage, true, true, true},
	} {
		t.Run(name, func(t *testing.T) {
			pm := tc.pm
			if got := pm.EffectiveStage(); got != tc.want {
				t.Fatalf("stage %q, want %q", got, tc.want)
			}
			if pm.Reads() != tc.reads || pm.Simulates() != tc.simulates || pm.Manages() != tc.manage {
				t.Fatalf("reads %v simulates %v manages %v", pm.Reads(), pm.Simulates(), pm.Manages())
			}
		})
	}
}

func TestStageValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		pm   PolicyManagement
		want string
	}{
		"a stage":          {PolicyManagement{Stage: StageSimulate}, ""},
		"an unknown stage": {PolicyManagement{Stage: "promote"}, `policyManagement.stage "promote" is one of read, simulate, manage`},
		"stage and switch": {PolicyManagement{Stage: StageRead, Promotion: true}, "stage and the six switches are not set together"},
	} {
		t.Run(name, func(t *testing.T) {
			var got []string
			tc.pm.Validate(func(f string, a ...any) { got = append(got, fmt.Sprintf(f, a...)) })
			joined := strings.Join(got, "; ")
			if (tc.want == "") != (joined == "") || !strings.Contains(joined, tc.want) {
				t.Fatalf("got %q, want %q", joined, tc.want)
			}
		})
	}
}
