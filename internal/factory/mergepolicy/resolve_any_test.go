// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
)

const anyClass = "factory: aws-0\nclasses:\n  docs-broad:\n    mode: live\n    author: agents\n    harness: plain\n" +
	"    conditions:\n      all: [{maxPatchFiles: 3}]\n      any:\n        - [{diffShape: links-only}]\n        - [{allowedFiles: ['^docs/']}, {maxModifiedLines: 5}]\n" +
	"      deny: [{protectedFiles: ['^docs/specs/']}]\n"

// Each branch of any is a class of its own as the gate reads it: all's leaves, the branch's, the
// deny and the guardrails. The gate decides a variant at a time (the evaluator).
func TestResolveExpandsAnyIntoVariants(t *testing.T) {
	c := central()
	c.MergeGuardrails = config.MergeGuardrails{MandatoryChecks: []string{"Leak scan"}, MaxModifiedLines: 100}
	r := resolved(t, anyClass, c)
	v := r.Variants()
	if len(v) != 2 {
		t.Fatalf("%d variants", len(v))
	}
	for i, cl := range v {
		if cl.MaxPatchFiles != 3 || !slices.Equal(cl.ProtectedFiles, []string{"^docs/specs/"}) || !slices.Contains(cl.HoldoutChecks, "Leak scan") || !cl.Live {
			t.Fatalf("variant %d lost all, deny or a guardrail: %+v", i, cl)
		}
	}
	switch {
	case v[0].DiffShape != "links-only" || v[0].AllowedFiles != nil || v[0].MaxModifiedLines != 100:
		t.Fatalf("variant 0: %+v", v[0])
	case v[1].DiffShape != "" || !slices.Equal(v[1].AllowedFiles, []string{"^docs/"}) || v[1].MaxModifiedLines != 5:
		t.Fatalf("variant 1: %+v", v[1])
	}
	if got := resolved(t, example, central()).Variants(); len(got) != 1 {
		t.Fatalf("a class without any is one variant: %d", len(got))
	}
}

// A branch's cap is held to the guardrail like all's.
func TestABranchCapOverTheGuardrailIsRefused(t *testing.T) {
	c := central()
	c.MergeGuardrails.MaxModifiedLines = 4
	f, err := Parse([]byte(anyClass))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("docs-broad", f.Classes["docs-broad"], c); !errors.Is(err, ErrGuardrail) || !strings.Contains(err.Error(), "maxModifiedLines 5") {
		t.Fatalf("got %v", err)
	}
}

// Compare has no central form for any: a class using it diverges, as requiresApproval does.
func TestCompareNamesAny(t *testing.T) {
	rc := &config.Repository{Classes: map[string]config.Class{"docs-broad": {Live: true, MaxPatchFiles: 3}}}
	f, err := Parse([]byte(anyClass))
	if err != nil {
		t.Fatal(err)
	}
	if d := Compare(rc, f, central()); !slices.ContainsFunc(d, func(s string) bool { return strings.Contains(s, "any has no central form") }) {
		t.Fatalf("%v", d)
	}
}

// A file both invalid and over a guardrail is invalid: guardrail_mismatch names a file whose every
// fault is the central config's (plan 1, carried to plan 2).
func TestOnlyGuardrail(t *testing.T) {
	g := fmt.Errorf("class a: %w: harness", ErrGuardrail)
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"nil":                {nil, false},
		"a guardrail":        {g, true},
		"two guardrails":     {errors.Join(g, fmt.Errorf("class b: %w: cap", ErrGuardrail)), true},
		"mixed":              {errors.Join(g, errors.New("class b: must start with ^")), false},
		"nested mixed":       {errors.Join(errors.Join(g), errors.Join(errors.New("x"))), false},
		"wrapped guardrails": {fmt.Errorf("mergepolicy: %w", errors.Join(g, g)), true},
		"invalid":            {errors.New("not YAML"), false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := OnlyGuardrail(tc.err); got != tc.want {
				t.Fatalf("got %t", got)
			}
		})
	}
}

// Export picks a profile that runs the class as today: one pinning a tier, a loop or a template
// would change how it runs (plan 1, carried to plan 2).
func TestExportSkipsAPinnedProfile(t *testing.T) {
	c := &config.Config{Factory: "aws-0", HarnessProfiles: map[string]config.HarnessProfile{
		"a-pinned": {Tier: "light"}, "b-looped": {Loop: config.LoopOff}, "c-solo": {Template: "solo"}, "z-plain": {},
	}}
	rc := &config.Repository{Classes: map[string]config.Class{"docs-links": {Live: true}}}
	f, err := Export(rc, c)
	if err != nil {
		t.Fatal(err)
	}
	if h := f.Classes["docs-links"].Harness; h != "z-plain" {
		t.Fatalf("got %q", h)
	}
}
