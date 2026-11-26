// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

func TestScreenshotsFor(t *testing.T) {
	var none *Screenshots
	if _, ok := none.For("implementer"); ok {
		t.Fatal("nil screenshots is off")
	}
	if _, ok := (&Screenshots{MaxScreenshots: 5}).For("implementer"); ok {
		t.Fatal("not enabled is off")
	}
	s := &Screenshots{Enabled: true}
	for role, want := range map[string]bool{"implementer": true, "tester": true, "reviewer": false, "triager": false, "scoper": false} {
		if _, ok := s.For(role); ok != want {
			t.Errorf("%s: got %v", role, ok)
		}
	}
	if m, _ := s.For("tester"); m != DefaultMaxScreenshots {
		t.Errorf("default max %d", m)
	}
	s = &Screenshots{Enabled: true, Roles: []string{"tester"}, MaxScreenshots: 99}
	if _, ok := s.For("implementer"); ok {
		t.Error("roles narrows the set")
	}
	if m, _ := s.For("tester"); m != MaxScreenshotsCeiling {
		t.Errorf("ceiling: %d", m)
	}
}

func TestScreenshotsValidated(t *testing.T) {
	for name, tc := range map[string]struct {
		s    Screenshots
		want string
	}{
		"a reviewer role is refused":  {Screenshots{Enabled: true, Roles: []string{"reviewer"}}, "screenshots.roles"},
		"a cap above 25 is refused":   {Screenshots{Enabled: true, MaxScreenshots: 26}, "screenshots.maxScreenshots"},
		"a negative cap is refused":   {Screenshots{Enabled: true, MaxScreenshots: -1}, "screenshots.maxScreenshots"},
		"implementer and tester pass": {Screenshots{Enabled: true, Roles: []string{"implementer", "tester"}, MaxScreenshots: 25}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			var got []string
			tc.s.validate(func(f string, _ ...any) { got = append(got, f) })
			if tc.want == "" && len(got) > 0 || tc.want != "" && (len(got) == 0 || !strings.Contains(got[0], tc.want)) {
				t.Errorf("got %v, want %q", got, tc.want)
			}
		})
	}
}
