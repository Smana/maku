// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"strings"
	"testing"
)

// A file naming only the factory has no policy: not an error, and no bindings.
func TestParseAFenceOnlyFile(t *testing.T) {
	f, err := Parse([]byte("factory: aws-0\n"))
	if err != nil || f.Declares() {
		t.Fatalf("%+v %v", f, err)
	}
}

// An unknown key names where it is and, for a near miss or another tool's name, the key meant.
func TestAnUnknownKeySaysWhereAndWhichKeyWasMeant(t *testing.T) {
	b := func(body string) string { return "factory: aws-0\nmergePolicies:\n  - " + body + "\n" }
	for name, tc := range map[string]struct {
		in   string
		want []string
	}{
		"another tool's name": {b("{name: docs, mode: shadow, files: ['^a$']}"), []string{"mergePolicies[0]", `"files"`, `did you mean "allowedFiles"`}},
		"a typo":              {b("{name: docs, mode: shadow, allowedFile: ['^a$']}"), []string{"mergePolicies[0]", `did you mean "allowedFiles"`}},
		"a cased key":         {b("{name: docs, Mode: shadow}"), []string{"mergePolicies[0]", `did you mean "mode"`}},
		"a top key":           {"factory: aws-0\nmergePolicie: []\n", []string{`"mergePolicie"`, `did you mean "mergePolicies"`}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.in))
			for _, w := range tc.want {
				if err == nil || !strings.Contains(err.Error(), w) {
					t.Fatalf("got %v, want %q", err, w)
				}
			}
		})
	}
}

func TestParseRefuses(t *testing.T) {
	b := func(body string) string { return "factory: aws-0\nmergePolicies:\n  - " + body + "\n" }
	long := strings.Repeat("a", MaxPatternBytes+1)
	for name, tc := range map[string]struct{ in, want string }{
		"not yaml":          {"factory: [\n", "not YAML"},
		"too big":           {"factory: aws-0\n#" + strings.Repeat("x", MaxFileBytes), "bytes"},
		"bad factory":       {"factory: AWS 0\n", "factory"},
		"unknown top key":   {"factory: aws-0\nalso: x\n", "also"},
		"a duplicate key":   {b("{name: docs, mode: shadow, mode: live}"), "mode"},
		"an invalid mode":   {b("{name: docs, mode: armed}"), "mode"},
		"a bad class name":  {b("{name: Bad_Name, mode: shadow}"), "class name"},
		"a cased top key":   {"Factory: aws-0\n", "Factory"},
		"a long pattern":    {b("{name: docs, mode: shadow, allowedFiles: [" + long + "]}"), "bytes"},
		"an empty pattern":  {b("{name: docs, mode: shadow, allowedFiles: ['']}"), "an empty pattern"},
		"a negative cap":    {b("{name: docs, mode: shadow, maxPerDay: -1}"), "maxPerDay is 0"},
		"an empty approval": {b("{name: docs, mode: shadow, requiresApproval: [{changedFiles: []}]}"), "changedFiles is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// The per-list bound let 32 bindings carry thousands of patterns, each compiled on every read: the
// file's total is bounded too.
func TestParseBoundsThePatternsOfTheWholeFile(t *testing.T) {
	var b strings.Builder
	b.WriteString("factory: aws-0\nmergePolicies:\n")
	for i := range 5 {
		b.WriteString("  - name: c" + string(rune('a'+i)) + "\n    mode: \"off\"\n    allowedFiles: [")
		for j := range MaxPatterns {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString("'^d/'")
		}
		b.WriteString("]\n")
	}
	if _, err := Parse([]byte(b.String())); err == nil || !strings.Contains(err.Error(), "patterns in the file") {
		t.Fatalf("got %v", err)
	}
}
