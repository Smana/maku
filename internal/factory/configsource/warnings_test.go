// SPDX-License-Identifier: Apache-2.0

package configsource

import (
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/resources"
)

// Assemble's warnings are logged when the set changes, not on every Sync: a Factory beside a file
// that still holds the catalogue warns once, until the set moves.
func TestWarningsAreLoggedWhenTheyChange(t *testing.T) {
	manifests, _, err := resources.Migrate(file(t), ns)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := resources.Decode(manifests)
	if err != nil {
		t.Fatal(err)
	}
	var f v1alpha1.Factory
	for _, o := range objs {
		if o.Kind == resources.KindFactory {
			f.Name, f.Namespace, f.Generation = o.Name, ns, 1
			if err := specInto(o.Spec, &f.Spec); err != nil {
				t.Fatal(err)
			}
		}
	}
	var mu sync.Mutex
	var sb strings.Builder
	s, c := source(t, &f) // the whole file: its catalogue keys are ignored, with a warning each
	s.Log = slog.New(slog.NewTextHandler(lockedWriter{&mu, &sb}, nil))
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return strings.Count(sb.String(), "msg=config warning=")
	}
	for range 3 {
		if err := s.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	n := count()
	if n == 0 {
		t.Fatal("the fixture must warn")
	}
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if count() != n {
		t.Fatalf("an unchanged warning set is logged again: %d then %d", n, count())
	}
	if err := c.Delete(t.Context(), &f); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(t.Context()); err != nil { // no Factory: no warning, a changed (empty) set
		t.Fatal(err)
	}
	if err := c.Create(t.Context(), func() *v1alpha1.Factory { g := f.DeepCopy(); g.ResourceVersion = ""; return g }()); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if count() != 2*n {
		t.Fatalf("a set that came back is logged again: %d, want %d", count(), 2*n)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// A switch block written twice is refused through the resources path too: Assemble re-marshals
// the file, where YAML's last key would win silently.
func TestADuplicatedSwitchBlockIsRefused(t *testing.T) {
	for _, block := range []string{"coordination: {mode: off}\n", "hardening: {}\n"} {
		s, _ := source(t)
		s.File = append(append(append([]byte{}, file(t)...), block...), block...)
		if err := s.Sync(t.Context()); err == nil || !strings.Contains(err.Error(), "written twice") {
			t.Errorf("%q twice: %v", strings.TrimSpace(block), err)
		}
	}
}
