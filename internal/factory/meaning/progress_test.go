// SPDX-License-Identifier: Apache-2.0

package meaning_test

import (
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/meaning"
)

func TestProgress(t *testing.T) {
	links := meaning.Build(meaning.Input{Name: "docs-links", Mode: "live"})
	prose := meaning.Build(meaning.Input{Name: "docs", Mode: "live", Class: config.Class{AllowedFiles: []string{`^.+\.md$`}}})
	for _, c := range []struct{ got, want string }{
		{meaning.Progress("shadow", prose, meaning.Counts{Agreed: 7, Minimum: 20, Changed: 1, Drill: "none"}),
			"7 of 20 agreed: you merged 7 unchanged, changed 1. 0 wrong calls. Rollback drill: not done."},
		{meaning.Progress("live", links, meaning.Counts{Merges: 34, Quiet: 34}),
			"Live. 34 merged alone, all signal-checked by `links-only`. 34 quiet. 0 wrong calls."},
		{meaning.Progress("live", prose, meaning.Counts{Merges: 20, Quiet: 19, WrongCalls: 1}),
			"Live. 20 merged alone, unchecked: nothing checks the meaning of prose. 19 quiet. 1 wrong call."},
	} {
		if c.got != c.want {
			t.Errorf("\n got %s\nwant %s", c.got, c.want)
		}
	}
}

func TestBlocker(t *testing.T) {
	c := meaning.Counts{Agreed: 7, Minimum: 20, WrongCalls: 1}
	for code, want := range map[string]string{
		"below_minimum": "Needs 13 more decisions you agree with.",
		"bad_outcome":   "1 wrong call among its recent merges.",
		"no_drill":      "No rollback drill on this repository yet.",
		"Unchecked":     "Unchecked.", // plan 1: the factory never emits it, and no word promises the rule
		"x_y":           "x y.",
	} {
		if says, _ := meaning.Blocker(code, c, meaning.Rules{}); says != want {
			t.Errorf("%s: %q, want %q", code, says, want)
		}
	}
	if says, next := meaning.Blocker("Unchecked", c, meaning.Rules{UncheckedStaysShadow: true}); says != "Nothing checks what these changes mean, so this policy cannot merge alone." || next == "" {
		t.Errorf("%q %q", says, next)
	}
}
