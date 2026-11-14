// SPDX-License-Identifier: Apache-2.0

package evidence

import (
	"errors"
	"strings"
	"testing"
)

const policyFile = `# the canary's policy
factory: aws-0

mergePolicies:
  - name: docs-links
    mode: shadow   # promoted after the drill
  - name: revert
    mode: shadow
`

// Promotion changes one token, the class's mode, and nothing else of the file: comments, order
// and the other classes stay, so the maintainer's pull request reviews as a one-line diff.
func TestProposeChangesOnlyTheClassMode(t *testing.T) {
	got, line, err := Propose([]byte(policyFile), "docs-links")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(policyFile, "    mode: shadow   # promoted", "    mode: live   # promoted", 1)
	if string(got) != want || line != 6 {
		t.Fatalf("line %d:\n%s", line, got)
	}
	if got, _, _ := Propose([]byte(policyFile), "revert"); !strings.Contains(string(got), "  - name: revert\n    mode: live\n") ||
		!strings.Contains(string(got), "docs-links\n    mode: shadow") {
		t.Fatalf("%s", got)
	}
}

func TestProposeRefusesWhatIsNotAShadowClass(t *testing.T) {
	live := strings.Replace(policyFile, "mode: shadow   #", "mode: live   #", 1)
	quoted := strings.Replace(policyFile, "mode: shadow   #", `mode: "shadow"   #`, 1)
	for name, tc := range map[string]struct {
		file, class string
		want        error
	}{
		"absent":          {policyFile, "deps-patch", ErrNoClass},
		"already live":    {live, "docs-links", ErrNotShadow},
		"not a policy":    {"factory: aws-0\nmergePolicies: [\n", "docs-links", ErrInvalidPolicy},
		"a quoted shadow": {quoted, "docs-links", nil},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := Propose([]byte(tc.file), tc.class)
			if !errors.Is(err, tc.want) {
				t.Fatalf("%v, want %v", err, tc.want)
			}
		})
	}
}

const boundFile = `# the canary's policy, in the binding shape
factory: aws-0
mergePolicies:
  - {name: docs-links, mode: shadow, holdoutChecks: ['Check the documentation links 🔗']}
  - name: revert
    mode: shadow   # with docs-links
    holdoutChecks: ['Check the documentation links 🔗']
  - {name: docs, mode: "off"}
`

// B5, follow-up of #538: in the binding shape, promotion edits the binding's mode, one token.
func TestProposeEditsABindingsMode(t *testing.T) {
	got, line, err := Propose([]byte(boundFile), "revert")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(boundFile, "    mode: shadow   # with", "    mode: live   # with", 1); string(got) != want || line != 6 {
		t.Fatalf("line %d:\n%s", line, got)
	}
	got, line, err = Propose([]byte(boundFile), "docs-links")
	if err != nil || line != 4 || !strings.Contains(string(got), "{name: docs-links, mode: live, holdoutChecks") {
		t.Fatalf("flow binding: line %d, %v:\n%s", line, err, got)
	}
	for class, want := range map[string]error{"docs": ErrNotShadow, "dead-links": ErrNoClass} {
		if _, _, err := Propose([]byte(boundFile), class); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", class, err, want)
		}
	}
}
