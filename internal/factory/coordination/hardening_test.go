// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"
	"testing"

	"github.com/Smana/maku/internal/hardening"
)

type fakeClasses struct {
	ok  bool
	err error
}

func (f fakeClasses) Authoritative(context.Context, string) (bool, error) { return f.ok, f.err }

type fakeReadiness struct {
	r     hardening.Report
	err   error
	calls int
}

func (f *fakeReadiness) Hardened(context.Context, string) (hardening.Report, error) {
	f.calls++
	return f.r, f.err
}

// The adapter adds nothing to ReadyForExecute's fail-closed contract (execution prerequisites):
// each half false or failing refuses, and readiness is asked at every admission.
func TestPrerequisitesComposeHardening(t *testing.T) {
	down := errors.New("github: 502")
	yes := hardening.Report{Hardened: true, Drill: "drill-1"}
	no := hardening.Report{Missing: []hardening.Missing{hardening.MissingDrill}}
	for name, tc := range map[string]struct {
		classes fakeClasses
		h       fakeReadiness
		ok      bool
	}{
		"both hold":            {fakeClasses{ok: true}, fakeReadiness{r: yes}, true},
		"not hardened":         {fakeClasses{ok: true}, fakeReadiness{r: no}, false},
		"readiness unreadable": {fakeClasses{ok: true}, fakeReadiness{r: yes, err: down}, false},
		"classes not declared": {fakeClasses{}, fakeReadiness{r: yes}, false},
		"classes unreadable":   {fakeClasses{ok: true, err: down}, fakeReadiness{r: yes}, false},
	} {
		t.Run(name, func(t *testing.T) {
			p := Prerequisites{Classes: tc.classes, Hardening: &tc.h}
			err := ReadyForExecute(t.Context(), p, []string{"a/b"})
			if (err == nil) != tc.ok || (err != nil && !errors.Is(err, ErrPrerequisites)) {
				t.Fatalf("%v, want ok=%v", err, tc.ok)
			}
		})
	}
	h := &fakeReadiness{r: yes}
	p := Prerequisites{Classes: fakeClasses{ok: true}, Hardening: h}
	for range 2 {
		if err := ReadyForExecute(t.Context(), p, []string{"a/b", "c/d"}); err != nil {
			t.Fatal(err)
		}
	}
	if h.calls != 4 {
		t.Fatalf("readiness asked %d times, want once per repository per admission", h.calls)
	}
	// A regression refuses the next admission.
	h.r = no
	if err := ReadyForExecute(t.Context(), p, []string{"a/b"}); !errors.Is(err, ErrPrerequisites) {
		t.Fatalf("%v", err)
	}
	// Stage H0's evaluator, with nothing to read, refuses.
	h0 := Prerequisites{Classes: fakeClasses{ok: true}, Hardening: hardening.Evaluator{Settings: func(string) hardening.Settings {
		return hardening.Settings{Mode: hardening.ModeOff}
	}}}
	if err := ReadyForExecute(t.Context(), h0, []string{"a/b"}); !errors.Is(err, ErrPrerequisites) {
		t.Fatalf("H0: %v", err)
	}
}
