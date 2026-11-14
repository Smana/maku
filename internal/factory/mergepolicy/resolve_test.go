// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
)

// central is a factory with three profiles, serving docs-links and revert, each with no condition
// of its own.
func central() *config.Config {
	return &config.Config{HarnessProfiles: map[string]config.HarnessProfile{
		"plain":        {},
		"checked":      {Checks: true},
		"adopted-none": {Template: config.TemplateNone},
	}, Policies: map[string]config.ServedPolicy{"docs-links": {Generation: 1}, "revert": {Generation: 1}}}
}

// OD-8 on a binding file, and the unarmed line the error gives; a binding that does not resolve
// never invalidates the file (B9).
func TestValidate(t *testing.T) {
	head := "factory: aws-0\nmergePolicies:\n"
	required := "  - {name: docs-links, mode: live}\n  - {name: revert, mode: live}\n"
	for name, tc := range map[string]struct{ raw, want string }{
		"fine":           {head + required, ""},
		"fence only":     {"factory: aws-0\n", ""},
		"no revert":      {head + "  - {name: docs-links, mode: live}\n", "revert"},
		"no revert, how": {head + "  - {name: docs-links, mode: live}\n", `"- {name: revert, mode: 'off'}"`},
		"unserved":       {head + required + "  - {name: docs, mode: shadow}\n", ""},
	} {
		t.Run(name, func(t *testing.T) {
			f, err := Parse([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			err = Validate(f, central())
			if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
