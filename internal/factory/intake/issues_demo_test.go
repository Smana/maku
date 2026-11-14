// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
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

// v4 D3, step 8 R9: outside the demo, private code without spec.privateCode.acknowledged is refused
// once with private_code_unacknowledged, its label consumed, and no Task exists.
func TestRefusesUnacknowledgedPrivateCode(t *testing.T) {
	f := forgetest.NewFake()
	labelled(f, 7, "Smana")
	f.SetPublic(false)
	p, c := poller(t, f)
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	cs := f.Comments(7)
	if n := len(tasks(t, c)); n != 0 || len(cs) != 1 || len(f.Removed(7)) != 1 {
		t.Fatalf("tasks %d comments %q removed %q", n, cs, f.Removed(7))
	}
	if !strings.Contains(cs[0], "spec.privateCode.acknowledged: true") {
		t.Errorf("comment %q does not carry D3's sentence", cs[0])
	}
}

// An acknowledged private repository is served, and so is a public one.
func TestServesAcknowledgedPrivateCodeAndPublicCode(t *testing.T) {
	f := forgetest.NewFake()
	labelled(f, 7, "Smana")
	f.SetPublic(false)
	p, c := poller(t, f)
	cfg := p.Cfg.Current()
	cfg.Repositories[0].PrivateCode = &config.PrivateCode{Acknowledged: true}
	if err := p.Poll(t.Context()); err != nil || len(tasks(t, c)) != 1 {
		t.Fatalf("acknowledged: %v tasks %d", err, len(tasks(t, c)))
	}
	f2 := forgetest.NewFake()
	labelled(f2, 7, "Smana")
	p2, c2 := poller(t, f2)
	if err := p2.Poll(t.Context()); err != nil || len(tasks(t, c2)) != 1 {
		t.Fatalf("public: %v tasks %d", err, len(tasks(t, c2)))
	}
}

// A task started before the refusal finishes: the refusal is the door's, and an open Task of an
// unacknowledged private repository is never stopped by it.
func TestPrivateCodeRefusalLeavesAnOpenTask(t *testing.T) {
	f := forgetest.NewFake()
	f.SetPublic(false)
	open := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "t-open", Namespace: "agent-system",
		Labels: map[string]string{v1alpha1.LabelIssue: "7"}}, Spec: v1alpha1.TaskSpec{Issue: 7}}
	p, c := poller(t, f, open)
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	var got v1alpha1.Task
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(open), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Annotations) != 0 {
		t.Errorf("the open task was annotated %v", got.Annotations)
	}
}

// A failed visibility read fails closed, in the demo and outside it: no Task, no refusal comment, the
// label stays for the next poll, which starts the task once the answer is public.
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
