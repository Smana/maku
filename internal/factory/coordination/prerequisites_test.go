// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"
	"testing"
)

// fakePrerequisites answers per repository and counts its calls.
type fakePrerequisites struct {
	classes, hardened map[string]bool
	err               error
	calls             int
}

func (f *fakePrerequisites) Ready(_ context.Context, repository string) (bool, bool, error) {
	f.calls++
	return f.classes[repository], f.hardened[repository], f.err
}

func ready(repos ...string) *fakePrerequisites {
	f := &fakePrerequisites{classes: map[string]bool{}, hardened: map[string]bool{}}
	for _, r := range repos {
		f.classes[r], f.hardened[r] = true, true
	}
	return f
}

var family = []string{"Smana/public-contracts", "Smana/public-consumer"}

// MR16: execute needs both v4 capabilities, repository-declared class authority and hardened safe
// outputs, on every repository, at every admission. A false, an error or a nil port refuses with
// coordination_prerequisites; a later regression refuses again.
func TestExecuteNeedsBothPrerequisites(t *testing.T) {
	var nilPort ExecutionPrerequisites
	for name, c := range map[string]struct {
		p     ExecutionPrerequisites
		repos []string
		want  bool
	}{
		"both capabilities on both repositories": {ready(family...), family, true},
		"classes not authoritative":              {&fakePrerequisites{classes: map[string]bool{family[0]: false, family[1]: true}, hardened: ready(family...).hardened}, family, false},
		"outputs not hardened":                   {&fakePrerequisites{classes: ready(family...).classes, hardened: map[string]bool{family[0]: true}}, family, false},
		"a port error":                           {&fakePrerequisites{classes: ready(family...).classes, hardened: ready(family...).hardened, err: errors.New("registry down")}, family, false},
		"a nil port":                             {nilPort, family, false},
		"no repository to prove":                 {ready(family...), nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			err := ReadyForExecute(t.Context(), c.p, c.repos)
			if c.want != (err == nil) {
				t.Fatalf("err = %v, want ready %v", err, c.want)
			}
			if !c.want && (!errors.Is(err, ErrPrerequisites) || err.Error() == "" || ReasonPrerequisites != "coordination_prerequisites") {
				t.Fatalf("err = %v, want coordination_prerequisites", err)
			}
		})
	}
	// A capability that regresses after an admission refuses the next one: nothing is cached.
	p := ready(family...)
	if err := ReadyForExecute(t.Context(), p, family); err != nil {
		t.Fatal(err)
	}
	p.hardened[family[1]] = false
	if err := ReadyForExecute(t.Context(), p, family); !errors.Is(err, ErrPrerequisites) {
		t.Fatalf("a regression admitted: %v", err)
	}
	if p.calls != 4 {
		t.Fatalf("%d calls, want one per repository per admission", p.calls)
	}
}

// MR16: only execute consults the port; off and plan never start anything and never call it.
func TestExecuteGateByMode(t *testing.T) {
	for mode, want := range map[string]error{"off": ErrNotExecuting, "plan": ErrNotExecuting, "": ErrNotExecuting, "execute": nil} {
		p := ready(family...)
		err := GateExecute(t.Context(), mode, p, family)
		if !errors.Is(err, want) && (want != nil || err != nil) {
			t.Errorf("mode %q: %v, want %v", mode, err, want)
		}
		if mode != "execute" && p.calls != 0 {
			t.Errorf("mode %q called the port %d times", mode, p.calls)
		}
	}
	if err := GateExecute(t.Context(), "execute", nil, family); !errors.Is(err, ErrPrerequisites) {
		t.Errorf("execute with no port: %v", err)
	}
}
