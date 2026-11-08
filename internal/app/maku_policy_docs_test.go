// SPDX-License-Identifier: Apache-2.0

package app

import (
	"maps"
	"os"
	"regexp"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

const guide = "../../docs/merge-policies.md"

// A YAML block the guide marks with <!-- check: central|repository --> on the line before its fence.
var guideBlock = regexp.MustCompile("(?s)<!-- check: (central|repository) -->\n```yaml\n(.*?)```")

// centralOver is policyConfig with fragment's top-level keys in place of its own. A repositories
// list replaces the legacy single-repository keys, and an entry without merge takes the base's.
func centralOver(t *testing.T, fragment string) *config.Config {
	t.Helper()
	raw, err := os.ReadFile(policyConfig)
	if err != nil {
		t.Fatal(err)
	}
	var base, over map[string]any
	if err := yaml.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte(fragment), &over); err != nil {
		t.Fatal(err)
	}
	if rs, ok := over["repositories"].([]any); ok {
		for _, r := range rs {
			if e, ok := r.(map[string]any); ok && e["merge"] == nil {
				e["merge"] = base["merge"]
			}
		}
		for _, k := range []string{"repository", "maintainers", "triggerLabel", "classes", "merge"} {
			delete(base, k)
		}
		if api, ok := base["api"].(map[string]any); ok {
			delete(api, "repositories") // derived from repositories
		}
	}
	maps.Copy(base, over)
	out, err := yaml.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Parse(out)
	if err != nil {
		t.Fatalf("central block:\n%s\n%v", fragment, err)
	}
	return c
}

// Every YAML example docs/merge-policies.md marks is valid: a central block loads over the test
// config, a repository file validates against the guide's first central block.
func TestTheGuidesExamplesValidate(t *testing.T) {
	raw, err := os.ReadFile(guide)
	if err != nil {
		t.Fatal(err)
	}
	blocks := guideBlock.FindAllStringSubmatch(string(raw), -1)
	var reference *config.Config
	repositories := 0
	for _, b := range blocks {
		if b[1] == "central" {
			c := centralOver(t, b[2])
			if reference == nil {
				reference = c
			}
			continue
		}
		repositories++
		f, err := mergepolicy.Parse([]byte(b[2]))
		if err != nil {
			t.Fatalf("repository block:\n%s\n%v", b[2], err)
		}
		if reference == nil || f.Factory != reference.Factory {
			t.Fatalf("a repository block before the first central block, or naming another factory:\n%s", b[2])
		}
		if err := mergepolicy.Validate(f, reference); err != nil {
			t.Fatalf("repository block:\n%s\n%v", b[2], err)
		}
	}
	if repositories < 4 {
		t.Fatalf("found %d repository blocks: the markers moved", repositories)
	}
}
