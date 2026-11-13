// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"slices"
	"testing"
)

// IN2: the catalog's ids are unique, each check has a seam the summary orders, a place it runs and
// at least one fail code, and no code is shared between checks or with the universal two.
func TestCatalogIsWellFormed(t *testing.T) {
	ids, codes := map[string]bool{}, map[string]string{}
	for _, c := range Catalog {
		if c.ID == "" || ids[c.ID] {
			t.Errorf("check id %q is empty or used twice", c.ID)
		}
		ids[c.ID] = true
		if len(c.Seams) == 0 || !slices.Contains(SeamOrder, c.Seams[0]) {
			t.Errorf("%s: seam %v is not in SeamOrder", c.ID, c.Seams)
		}
		if len(c.RunsIn) == 0 || len(c.Fail) == 0 {
			t.Errorf("%s: no place to run or no fail code", c.ID)
		}
		for _, code := range append(slices.Clone(c.Fail), c.Warn...) {
			if code == Undecided || code == ComponentTooOld {
				t.Errorf("%s: %s is universal, not a check's", c.ID, code)
			}
			if other, ok := codes[code]; ok {
				t.Errorf("code %s is %s's and %s's", code, other, c.ID)
			}
			codes[code] = c.ID
		}
	}
	for _, id := range []string{"kubernetes", "crds", "runtimeclass", "secrets", "run-path", "database", "oidc", "github-link",
		"apps", "merge-gate", "private-code", "models", "knowledge", "exposure", "tracing"} {
		if !ids[id] {
			t.Errorf("IN2's check %s is missing", id)
		}
	}
}

// A result's code must be its check's: in tests an unknown one panics, so a new code is never
// shipped without its catalog entry.
func TestNormalizePanicsOnAnUnknownCodeInTests(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("an unknown code did not panic")
		}
	}()
	normalize(Result{Check: "kubernetes", Outcome: Fail, Code: "made_up"})
}

func TestNormalize(t *testing.T) {
	for name, c := range map[string]struct {
		in   Result
		want Result
	}{
		"a pass with no code":        {Result{Check: "kubernetes", Outcome: Pass}, Result{Check: "kubernetes", Outcome: Pass}},
		"a fail with its code":       {Result{Check: "crds", Outcome: Fail, Code: "crd_missing"}, Result{Check: "crds", Outcome: Fail, Code: "crd_missing"}},
		"undecided is every check's": {Result{Check: "secrets", Outcome: Fail, Code: Undecided}, Result{Check: "secrets", Outcome: Fail, Code: Undecided}},
		"a warn with its warn code": {Result{Check: "runtimeclass", Outcome: Warn, Code: "unsafe_allow_runc"},
			Result{Check: "runtimeclass", Outcome: Warn, Code: "unsafe_allow_runc"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := normalize(c.in); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
	for name, r := range map[string]Result{
		"a fail code as a warn": {Check: "runtimeclass", Outcome: Warn, Code: "runtimeclass_missing"},
		"a warn code as a fail": {Check: "runtimeclass", Outcome: Fail, Code: "unsafe_allow_runc"},
		"an unknown check":      {Check: "nope", Outcome: Pass},
		"an unknown outcome":    {Check: "kubernetes", Outcome: "maybe"},
		"a fail with no code":   {Check: "kubernetes", Outcome: Fail},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("accepted")
				}
			}()
			normalize(r)
		})
	}
}

// Outside tests an invalid result is never a pass: it becomes a fail undecided.
func TestNormalizeInProduction(t *testing.T) {
	panicOnInvalid = false
	defer func() { panicOnInvalid = true }()
	got := normalize(Result{Check: "kubernetes", Outcome: Pass, Code: ""})
	if got.Outcome != Pass {
		t.Fatalf("a valid result changed: %+v", got)
	}
	got = normalize(Result{Check: "kubernetes", Outcome: Fail, Code: "made_up"})
	if got.Outcome != Fail || got.Code != Undecided {
		t.Fatalf("got %+v", got)
	}
}
