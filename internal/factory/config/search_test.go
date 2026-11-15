// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// v3 feature 5 (ST9, offline proof 5): loops.<class>.search parses, absent is off, a row without
// search inherits default's (WF10) and its own search: false is kept, and it never switches the
// loop on, as scope does not.
func TestSearchFor(t *testing.T) {
	cfg, err := Parse([]byte(good + `loops:
  default: {search: true}
  standard: {search: true}
  careful: {stallSteps: 40}
  quick: {search: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	for class, want := range map[string]bool{"standard": true, "careful": true, "review": true, "quick": false, "docs-links": true} {
		if got := cfg.SearchFor(class); got != want {
			t.Errorf("SearchFor(%s) = %v, want %v", class, got, want)
		}
	}
	if cfg.LoopFor("standard") != nil || cfg.LoopFor("review") != nil {
		t.Error("a search row switched the loop on")
	}
	none, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if none.SearchFor("standard") {
		t.Error("no loops: search on, want off (D4)")
	}
}

func TestSearchIsRefused(t *testing.T) {
	for name, c := range map[string]struct{ loops, want string }{
		"a string":            {`{standard: {search: "true"}}`, "cannot unmarshal string"},
		"a number":            {"{standard: {search: 1}}", "cannot unmarshal number"},
		"an undeclared class": {"{scripts: {search: true}}", "loops.scripts: a row is named default or a workflow"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(good + "loops: " + c.loops + "\n"))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}
