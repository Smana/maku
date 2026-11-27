// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"slices"
	"strings"
	"testing"
)

func TestRoutinesUIWriteCatalogEntry(t *testing.T) {
	c, ok := lookup("routines-ui-write")
	if !ok {
		t.Fatal("routines-ui-write missing from preflight.Catalog")
	}
	if len(c.Seams) != 1 || c.Seams[0] != SeamCluster {
		t.Fatalf("Seams = %v, want [cluster]", c.Seams)
	}
	if !slices.Equal(c.RunsIn, []Where{InFactory}) {
		t.Fatalf("RunsIn = %v, want [factory]", c.RunsIn)
	}
	if !slices.Equal(c.Fail, []string{"ui_write_check_failed"}) {
		t.Fatalf("Fail = %v, want [ui_write_check_failed]", c.Fail)
	}
	if !slices.Equal(c.Warn, []string{"ui_write_gitops_managed"}) {
		t.Fatalf("Warn = %v, want [ui_write_gitops_managed]", c.Warn)
	}

	warnRes := normalize(Result{Check: "routines-ui-write", Outcome: Warn, Code: "ui_write_gitops_managed", Message: "flux managed"})
	if warnRes.Outcome != Warn || warnRes.Code != "ui_write_gitops_managed" {
		t.Fatalf("normalize warn = %+v", warnRes)
	}
	failRes := normalize(Result{Check: "routines-ui-write", Outcome: Fail, Code: "ui_write_check_failed", Message: "list failed"})
	if failRes.Outcome != Fail || failRes.Code != "ui_write_check_failed" {
		t.Fatalf("normalize fail = %+v", failRes)
	}
	if !strings.Contains(DocTable(), "`routines-ui-write`") {
		t.Fatalf("DocTable missing routines-ui-write:\n%s", DocTable())
	}
}
