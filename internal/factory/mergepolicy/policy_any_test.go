// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"strings"
	"testing"
)

// any is a list of branches, each a list of leaves: the class holds when all holds and one branch
// does (MP4). One level only, so the vocabulary stays closed.
func TestParseAny(t *testing.T) {
	f, err := Parse([]byte("factory: aws-0\nclasses:\n  c:\n    mode: shadow\n    author: agents\n    harness: p\n" +
		"    conditions:\n      all: [{maxPatchFiles: 3}]\n      any:\n        - [{diffShape: links-only}]\n        - [{allowedFiles: ['^docs/']}, {maxModifiedLines: 5}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if b := f.Classes["c"].Conditions.Any; len(b) != 2 || len(b[0]) != 1 || len(b[1]) != 2 {
		t.Fatalf("%+v", b)
	}
}

// An empty predicate reads as absent, so it would pass as equivalent and check nothing: refused
// before any gate reads the parser's output (plan 1, carried to plan 2).
func TestParseRefusesWhatChecksNothing(t *testing.T) {
	cl := func(cond string) string {
		return "factory: aws-0\nclasses:\n  c: {mode: shadow, author: agents, harness: p, " + cond + "}\n"
	}
	many := strings.Repeat("[{maxPatchFiles: 1}], ", MaxBranches+1)
	for name, tc := range map[string]struct{ in, want string }{
		"allowedFiles empty":        {cl("conditions: {all: [{allowedFiles: []}]}"), "allowedFiles is empty"},
		"holdoutChecks empty":       {cl("conditions: {all: [{holdoutChecks: []}]}"), "holdoutChecks is empty"},
		"companionFiles empty":      {cl("conditions: {all: [{companionFiles: []}]}"), "companionFiles is empty"},
		"maxPatchFiles zero":        {cl("conditions: {all: [{maxPatchFiles: 0}]}"), "maxPatchFiles is at least 1"},
		"maxModifiedLines neg":      {cl("conditions: {all: [{maxModifiedLines: -1}]}"), "maxModifiedLines is at least 1"},
		"diffShape empty":           {cl("conditions: {all: [{diffShape: ''}]}"), "diffShape is empty"},
		"releaseAgeCheck empty":     {cl("conditions: {all: [{releaseAgeCheck: ''}]}"), "releaseAgeCheck is empty"},
		"requiredLabel empty":       {cl("conditions: {all: [{requiredLabel: ''}]}"), "requiredLabel is empty"},
		"an empty pattern":          {cl("conditions: {all: [{allowedFiles: ['']}]}"), "an empty pattern"},
		"an empty deny":             {cl("conditions: {deny: [{}]}"), "protectedFiles is empty"},
		"an empty approval":         {cl("requiresApproval: [{}]"), "changedFiles is empty"},
		"a negative day cap":        {cl("limits: {maxPerDay: -1}"), "maxPerDay is 0 (the guardrail's) or more"},
		"an empty any":              {cl("conditions: {any: []}"), "any holds at least two branches"},
		"one branch":                {cl("conditions: {any: [[{maxPatchFiles: 1}]]}"), "any holds at least two branches"},
		"an empty branch":           {cl("conditions: {any: [[], [{maxPatchFiles: 1}]]}"), "a branch of any is empty"},
		"too many branches":         {cl("conditions: {any: [" + many + "]}"), "branches"},
		"a key in all and a branch": {cl("conditions: {all: [{maxPatchFiles: 1}], any: [[{maxPatchFiles: 2}], [{modifyOnly: true}]]}"), "maxPatchFiles is in all and in a branch"},
		"a leaf twice in a branch":  {cl("conditions: {any: [[{maxPatchFiles: 1}, {maxPatchFiles: 2}], [{modifyOnly: true}]]}"), "twice"},
		"two keys in a branch leaf": {cl("conditions: {any: [[{maxPatchFiles: 1, modifyOnly: true}], [{modifyOnly: true}]]}"), "exactly one"},
		"nested any":                {cl("conditions: {any: [[{any: []}], [{modifyOnly: true}]]}"), "any"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// The per-list bound let 32 classes carry thousands of patterns, each compiled on every read (a
// crafted file cost 2.8 to 8 s): the file's total is bounded too.
func TestParseBoundsThePatternsOfTheWholeFile(t *testing.T) {
	var b strings.Builder
	b.WriteString("factory: aws-0\nclasses:\n")
	for i := range 5 {
		b.WriteString("  c" + string(rune('a'+i)) + ":\n    mode: off\n    author: agents\n    harness: p\n    conditions:\n      all:\n        - allowedFiles: [")
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
