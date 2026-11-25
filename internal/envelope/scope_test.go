// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"errors"
	"strings"
	"testing"
)

func readyScope() Scope {
	return Scope{State: ScopeReady, Task: "Fix the link.", Touches: []string{"docs/guide.md"}, Plan: []string{"Fix it."},
		Assumptions: []string{"Runbook 08 replaces 07."}, Acceptance: []Acceptance{{Check: "task docs", Proves: "the site builds"}}}
}

func TestScopeProfileFields(t *testing.T) {
	with := func(f func(*Scope)) Scope { s := readyScope(); f(&s); return s }
	for name, c := range map[string]struct {
		s  Scope
		ok bool
	}{
		"no profile":                   {readyScope(), true},
		"ready with both":              {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = "A behaviour change." }), true},
		"profile alone":                {with(func(s *Scope) { s.Profile = "test-first" }), true},
		"profileWhy of 200 bytes":      {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = strings.Repeat("w", MaxProfileWhy) }), true},
		"profileWhy of 201 bytes":      {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = strings.Repeat("w", MaxProfileWhy+1) }), false},
		"two-byte runes at 200 bytes":  {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = strings.Repeat("é", MaxProfileWhy/2) }), true},
		"three-byte runes past 200":    {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = strings.Repeat("€", MaxProfileWhy/3+1) }), false},
		"a two-byte rune past 200":     {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = strings.Repeat("w", MaxProfileWhy-1) + "é" }), false},
		"profile with state question":  {with(func(s *Scope) { s.State = ScopeQuestion; s.Question = "Which?"; s.Profile = "test-first" }), false},
		"profileWhy on a question":     {with(func(s *Scope) { s.State = ScopeQuestion; s.Question = "Which?"; s.ProfileWhy = "Because." }), false},
		"profile not a profile name":   {with(func(s *Scope) { s.Profile = "Test_First" }), false},
		"profile too long":             {with(func(s *Scope) { s.Profile = strings.Repeat("a", 41) }), false},
		"profileWhy control character": {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = "a\u0000b" }), false},
		"profileWhy format character":  {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = "a\u202eb" }), false},
		"profileWhy invalid UTF-8":     {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = "a\xffb" }), false},
		"profileWhy blank":             {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = "  " }), false},
	} {
		t.Run(name, func(t *testing.T) {
			if err := c.s.Validate(); (err == nil) != c.ok || (err != nil && !errors.Is(err, ErrScope)) {
				t.Fatalf("Validate() = %v, want ok %v", err, c.ok)
			}
		})
	}
}

func TestSplitParallelFieldsValidation(t *testing.T) {
	part1 := readyScope()
	part1.Files = []string{"docs/a.md"}
	part1.Checks = []string{"Pre-commit checks"}
	part2 := readyScope()
	part2.Files = []string{"docs/b.md"}
	part2.Checks = []string{"Pre-commit checks"}
	part2.DependsOn = []int{1}

	sp := Split{Why: "Two parts.", Children: []Scope{part1, part2}}
	if err := sp.Validate(); err != nil {
		t.Fatalf("valid split with files/checks/dependsOn failed: %v", err)
	}

	// Self or forward dependency must be rejected.
	badDep := readyScope()
	badDep.DependsOn = []int{2}
	if err := (Split{Why: "Bad dep.", Children: []Scope{badDep, part2}}).Validate(); err == nil {
		t.Fatal("forward dependsOn must fail Validate")
	}

	// Blank file or check must be rejected.
	blankFile := readyScope()
	blankFile.Files = []string{"  "}
	if err := blankFile.Validate(); err == nil {
		t.Fatal("blank files entry must fail Validate")
	}
	blankCheck := readyScope()
	blankCheck.Checks = []string{""}
	if err := blankCheck.Validate(); err == nil {
		t.Fatal("blank checks entry must fail Validate")
	}
}
