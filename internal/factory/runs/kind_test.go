// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/runwatch"
)

// A Client without a kind would read and write claims of no group, namespace or prefix: every
// method refuses it before reaching the API. C is nil, so a call that got past the check would panic.
func TestClientRefusesAnInvalidKind(t *testing.T) {
	ctx := t.Context()
	for kname, k := range map[string]runwatch.Kind{
		"the zero kind":           {},
		"a kind without a prefix": {Group: "maku.ogenki.io", Namespace: "agents"},
	} {
		c := Client{Kind: k}
		for name, call := range map[string]func() error{
			"create":   func() error { return c.Create(ctx, spec()) },
			"get":      func() error { _, _, err := c.Get(ctx, "7f3cq2xz"); return err },
			"list":     func() error { _, err := c.List(ctx); return err },
			"annotate": func() error { return c.Annotate(ctx, "7f3cq2xz", map[string]string{AnnUsage: "1"}) },
			"delete":   func() error { return c.Delete(ctx, "7f3cq2xz") },
		} {
			t.Run(kname+" "+name, func(t *testing.T) {
				if err := call(); err == nil || !strings.Contains(err.Error(), "runs kind") {
					t.Fatalf("err = %v, want a refusal naming the runs kind", err)
				}
			})
		}
	}
}
