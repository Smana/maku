// SPDX-License-Identifier: Apache-2.0

package grant

import (
	"strings"
	"testing"
)

// A grant covers a decision only when every caught path matches one of its whole-path patterns
// (MP10): a partial match, an empty list or a broken pattern covers nothing.
func TestCovers(t *testing.T) {
	g := Grant{Files: []string{`docs/security/[a-z-]+\.md`, Exact("README.md")}}
	for name, tc := range map[string]struct {
		paths []string
		want  bool
	}{
		"every path matched":        {[]string{"docs/security/threat-model.md", "README.md"}, true},
		"one path outside":          {[]string{"docs/security/a.md", "docs/security/sub/b.md"}, false},
		"anchored at both ends":     {[]string{"x/docs/security/a.md"}, false},
		"an exact path is literal":  {[]string{"READMEXmd"}, false},
		"no path is no cover":       {nil, false},
		"the alternation anchored":  {[]string{"docs/security/a.mdx"}, false},
		"a rename's old name, also": {[]string{"docs/security/a.md", "docs/old.md"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := g.Covers(tc.paths); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
	if (Grant{Files: []string{"("}}).Covers([]string{"("}) {
		t.Fatal("a pattern that does not compile covered a path")
	}
}

// A grant is bound to the policy it was made under: a prefix of 12 hex digits at least.
func TestBoundTo(t *testing.T) {
	full := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for hash, want := range map[string]bool{full: true, full[:12]: true, full[:11]: false, "ffffffffffff": false, "": false} {
		if got := (Grant{PolicyHash: hash}).BoundTo(full); got != want {
			t.Fatalf("%q: got %v", hash, got)
		}
	}
}

func TestPredicateValidate(t *testing.T) {
	ok := Predicate{Repository: "smana/cloud-native-ref", Class: "docs-broad", Files: []string{`docs/security/a\.md`}, PolicyHash: "0123456789ab"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Predicate){
		"an uppercase repository": func(p *Predicate) { p.Repository = "Smana/x" },
		"a bad class":             func(p *Predicate) { p.Class = "Docs" },
		"a short hash":            func(p *Predicate) { p.PolicyHash = "0123" },
		"no file":                 func(p *Predicate) { p.Files = nil },
		"too many files":          func(p *Predicate) { p.Files = make([]string, MaxFiles+1) },
		"a broken pattern":        func(p *Predicate) { p.Files = []string{"("} },
		"a long pattern":          func(p *Predicate) { p.Files = []string{strings.Repeat("a", MaxPatternLen+1)} },
		"an empty pattern":        func(p *Predicate) { p.Files = []string{""} },
		"added-entry":             func(p *Predicate) { p.DiffShape = "added-entry" },
	} {
		t.Run(name, func(t *testing.T) {
			p := ok
			mutate(&p)
			if p.Validate() == nil {
				t.Fatalf("%+v accepted", p)
			}
		})
	}
}

func TestRef(t *testing.T) {
	if got := (Grant{ID: "abc", Version: 3}).Ref(); got != "abc:3" {
		t.Fatal(got)
	}
}
