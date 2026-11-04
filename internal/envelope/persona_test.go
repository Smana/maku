// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"strings"
	"testing"
)

func TestValidPersona(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"scoper", "scoper", true},
		{"hyphenated", "security-reviewer", true},
		{"one letter", "a", true},
		{"32 characters", "a" + strings.Repeat("b", 31), true},
		{"empty", "", false},
		{"uppercase", "Scoper", false},
		{"leading digit", "1st", false},
		{"leading hyphen", "-x", false},
		{"33 characters", "a" + strings.Repeat("b", 32), false},
		{"bidi override", "scoper\u202e", false},
		{"space", "rev iewer", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidPersona(tc.in); got != tc.want {
				t.Errorf("ValidPersona(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
