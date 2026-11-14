// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"fmt"
	"strings"
)

// DocTable is the catalog as docs/install.md's check table: one row per check, in the catalog's
// order, with the seam, where it runs, what it checks, the codes it fails or warns with, and the
// decisions it proves.
func DocTable() string {
	var b strings.Builder
	b.WriteString("| Check | Seam | Runs in | What it checks | Fails with | Warns with | Proves |\n|---|---|---|---|---|---|---|\n")
	for _, c := range Catalog {
		seams := make([]string, len(c.Seams))
		for i, s := range c.Seams {
			seams[i] = string(s)
		}
		where := make([]string, len(c.RunsIn))
		for i, w := range c.RunsIn {
			where[i] = string(w)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s | %s |\n", c.ID, strings.Join(seams, ", "), strings.Join(where, ", "),
			c.What, codes(c.Fail), codes(c.Warn), c.From)
	}
	return b.String()
}

func codes(cs []string) string {
	if len(cs) == 0 {
		return ""
	}
	return "`" + strings.Join(cs, "`, `") + "`"
}
