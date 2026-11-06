// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"errors"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/forge/forgetest"
)

// Demo mode serves public repositories only: a private repository's label is refused once with
// private_repository, consumed, and no Task exists.
func TestDemoRefusesAPrivateRepository(t *testing.T) {
	f := forgetest.NewFake()
	labelled(f, 7, "Smana")
	f.SetPublic(false)
	p, c := poller(t, f)
	p.Demo = true
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	cs := f.Comments(7)
	if n := len(tasks(t, c)); n != 0 || len(cs) != 1 || len(f.Removed(7)) != 1 {
		t.Fatalf("tasks %d comments %q removed %q", n, cs, f.Removed(7))
	}
	if !strings.Contains(cs[0], "serves public repositories only") {
		t.Errorf("comment %q", cs[0])
	}
}

// A public repository proceeds in demo mode.
func TestDemoServesAPublicRepository(t *testing.T) {
	f := forgetest.NewFake()
	labelled(f, 7, "Smana")
	p, c := poller(t, f)
	p.Demo = true
	if err := p.Poll(t.Context()); err != nil || len(tasks(t, c)) != 1 {
		t.Fatalf("%v tasks %d", err, len(tasks(t, c)))
	}
	if f.PublicCalls() != 1 {
		t.Errorf("Public called %d times", f.PublicCalls())
	}
}

// Without demo mode the same issue proceeds and the visibility is never read.
func TestNoDemoNeverReadsVisibility(t *testing.T) {
	f := forgetest.NewFake()
	labelled(f, 7, "Smana")
	f.SetPublic(false)
	p, c := poller(t, f)
	if err := p.Poll(t.Context()); err != nil || len(tasks(t, c)) != 1 {
		t.Fatalf("%v tasks %d", err, len(tasks(t, c)))
	}
	if f.PublicCalls() != 0 {
		t.Errorf("Public called %d times with demo off", f.PublicCalls())
	}
}

// A failed visibility read fails closed: no Task, no refusal comment, the label stays for the
// next poll, which starts the task once the answer is public.
func TestDemoVisibilityErrorFailsClosedAndRetries(t *testing.T) {
	f := forgetest.NewFake()
	labelled(f, 7, "Smana")
	f.FailPublic(errors.New("boom"))
	p, c := poller(t, f)
	p.Demo = true
	if err := p.Poll(t.Context()); err == nil {
		t.Fatal("a failed read must surface as a poll error")
	}
	if n := len(tasks(t, c)); n != 0 || len(f.Comments(7)) != 0 || len(f.Removed(7)) != 0 {
		t.Fatalf("tasks %d comments %q removed %q", n, f.Comments(7), f.Removed(7))
	}
	f.FailPublic(nil)
	if err := p.Poll(t.Context()); err != nil || len(tasks(t, c)) != 1 {
		t.Fatalf("retry: %v tasks %d", err, len(tasks(t, c)))
	}
}
