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
		"no profile":                                   {readyScope(), true},
		"ready with both":                              {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = "A behaviour change." }), true},
		"profile alone":                                {with(func(s *Scope) { s.Profile = "test-first" }), true},
		"profileWhy of 200 bytes":                      {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = strings.Repeat("w", MaxProfileWhy) }), true},
		"profileWhy of 201 bytes":                      {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = strings.Repeat("w", MaxProfileWhy+1) }), false},
		"profileWhy of 100 two-byte runes, 200 bytes":  {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = strings.Repeat("é", MaxProfileWhy/2) }), true},
		"profileWhy of 67 three-byte runes, 201 bytes": {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = strings.Repeat("€", MaxProfileWhy/3+1) }), false},
		"profileWhy of 199 bytes and a two-byte rune":  {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = strings.Repeat("w", MaxProfileWhy-1) + "é" }), false},
		"profile with state question":                  {with(func(s *Scope) { s.State = ScopeQuestion; s.Question = "Which?"; s.Profile = "test-first" }), false},
		"profileWhy on a question":                     {with(func(s *Scope) { s.State = ScopeQuestion; s.Question = "Which?"; s.ProfileWhy = "Because." }), false},
		"profile not a profile name":                   {with(func(s *Scope) { s.Profile = "Test_First" }), false},
		"profile too long":                             {with(func(s *Scope) { s.Profile = strings.Repeat("a", 41) }), false},
		"profileWhy control character":                 {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = "a\u0000b" }), false},
		"profileWhy format character":                  {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = "a\u202eb" }), false},
		"profileWhy invalid UTF-8":                     {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = "a\xffb" }), false},
		"profileWhy blank":                             {with(func(s *Scope) { s.Profile = "test-first"; s.ProfileWhy = "  " }), false},
	} {
		t.Run(name, func(t *testing.T) {
			if err := c.s.Validate(); (err == nil) != c.ok || (err != nil && !errors.Is(err, ErrScope)) {
				t.Fatalf("Validate() = %v, want ok %v", err, c.ok)
			}
		})
	}
}
