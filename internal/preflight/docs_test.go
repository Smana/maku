// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"flag"
	"os"
	"strings"
	"testing"
)

var updateDocs = flag.Bool("update-docs", false, "rewrite docs/install.md's check table from the catalog")

const installDoc = "../../docs/install.md"

// TestInstallDocTable: docs/install.md's check table is DocTable of the catalog (IN2), between its
// markers. Rerun with -update-docs after a catalog change.
func TestInstallDocTable(t *testing.T) {
	raw, err := os.ReadFile(installDoc)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	const begin, end = "<!-- preflight:begin -->\n", "<!-- preflight:end -->"
	head, rest, ok := strings.Cut(doc, begin)
	if !ok {
		t.Fatalf("docs/install.md has no %q marker", begin)
	}
	got, tail, ok := strings.Cut(rest, end)
	if !ok {
		t.Fatalf("docs/install.md has no %q marker", end)
	}
	want := DocTable()
	if got == want {
		return
	}
	if !*updateDocs {
		t.Fatalf("docs/install.md's check table differs from the catalog; rerun with -update-docs:\n%s", want)
	}
	if err := os.WriteFile(installDoc, []byte(head+begin+want+end+tail), 0o600); err != nil {
		t.Fatal(err)
	}
}
