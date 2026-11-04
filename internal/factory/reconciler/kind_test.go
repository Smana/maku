// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"
)

// D9: the reconciler watches the runs kind it is given; without one it refuses at startup rather
// than watch an AgentRun of no group.
func TestSetupRefusesAMissingRunsKind(t *testing.T) {
	if err := (&Reconciler{}).SetupWithManager(nil); err == nil || !strings.Contains(err.Error(), "runs kind") {
		t.Fatalf("err = %v, want one naming the runs kind", err)
	}
}
