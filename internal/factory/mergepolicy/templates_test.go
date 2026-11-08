// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"encoding/json"
	"maps"
	"os"
	"slices"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/config"
)

// Every editor template passes the factory's check on the deployed profiles, started the way the
// editor starts it (web/src/settings.ts): the first profile its author may take, and docs-links and
// revert brought unarmed. The templates are the editor's own file.
func TestEveryEditorTemplatePassesCheck(t *testing.T) {
	raw, err := os.ReadFile("../../../web/src/policy-templates.json")
	if err != nil {
		t.Fatal(err)
	}
	var templates map[string]ClassPolicy
	if err := json.Unmarshal(raw, &templates); err != nil {
		t.Fatal(err)
	}
	c := central() // plain, checked, adopted-none: the deployed harnessProfiles
	first := func(author string) string {
		for _, n := range slices.Sorted(maps.Keys(c.HarnessProfiles)) {
			if (c.HarnessProfiles[n].Template == config.TemplateNone) == (author == AuthorAdopt) {
				return n
			}
		}
		return ""
	}
	if len(templates) == 0 {
		t.Fatal("no templates")
	}
	for name, tpl := range templates {
		t.Run(name, func(t *testing.T) {
			tpl.Harness = first(tpl.Author)
			off := ClassPolicy{Mode: ModeOff, Author: AuthorAgents, Harness: first(AuthorAgents)}
			out, err := yaml.Marshal(File{Factory: "aws-0", Classes: map[string]ClassPolicy{name: tpl, "docs-links": off, "revert": off}})
			if err != nil {
				t.Fatal(err)
			}
			f, err := Parse(out)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if err := Validate(f, c); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}
