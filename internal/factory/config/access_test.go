// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strings"
	"testing"
)

// Developer review gap 1, owner decision D1: write access starts a task only where per-person daily
// caps are enforced. A maintainer always may.
func TestMayStart(t *testing.T) {
	enforced := Budgets{EnforcePrincipal: true, PerPerson: PerPerson{Enabled: true, DailyUSD: 20}}
	shadow := Budgets{PerPerson: PerPerson{Enabled: true, DailyUSD: 20}}
	write := &Repository{Name: "acme/api", Maintainers: []string{"lead"}, Access: RepositoryAccess{Start: AccessWrite}}
	maintainers := &Repository{Name: "acme/api", Maintainers: []string{"lead"}}
	for name, c := range map[string]struct {
		r     *Repository
		login string
		b     Budgets
		want  bool
	}{
		"a maintainer":                          {maintainers, "Lead", Budgets{}, true},
		"a writer, write, caps enforced":        {write, "dev", enforced, true},
		"a writer, write, caps in shadow":       {write, "dev", shadow, false},
		"a writer, write, per-person off":       {write, "dev", Budgets{EnforcePrincipal: true}, false},
		"a writer, maintainers only":            {maintainers, "dev", enforced, false},
		"a bot never starts by write access":    {write, "renovate[bot]", enforced, false},
		"an unserved repository starts nothing": {nil, "dev", enforced, false},
	} {
		if got := c.r.MayStart(c.login, c.b); got != c.want {
			t.Errorf("%s: got %v", name, got)
		}
	}
	if !write.WriteStartsConfigured() || write.WriteStarts(shadow) || !write.WriteStarts(enforced) {
		t.Error("configured but not in effect in shadow")
	}
}

func TestAccessStartIsValidated(t *testing.T) {
	r := Repository{Access: RepositoryAccess{Start: "everyone"}}
	var errs []string
	r.Access.validate(func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) })
	if len(errs) != 1 || !strings.Contains(errs[0], "access.start") {
		t.Fatalf("%q", errs)
	}
}
