// SPDX-License-Identifier: Apache-2.0

package evidence

import (
	"errors"
	"strings"
	"testing"
)

const policyFile = `# the canary's policy
factory: aws-0

classes:
  docs-links:
    mode: shadow   # promoted after the drill
    author: agents
    harness: docs-standard
  revert:
    mode: shadow
    author: agents
    harness: docs-standard
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
	if got, _, _ := Propose([]byte(policyFile), "revert"); !strings.Contains(string(got), "  revert:\n    mode: live\n") ||
		!strings.Contains(string(got), "docs-links:\n    mode: shadow") {
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
		"not a policy":    {"factory: aws-0\nclasses: [\n", "docs-links", ErrInvalidPolicy},
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
