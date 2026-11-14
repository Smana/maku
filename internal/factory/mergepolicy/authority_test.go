// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
)

func (r *rec) PolicyChange(_ context.Context, _, class, change string) {
	r.changes = append(r.changes, class+" "+change)
}

// authorityRig is a Watch over one repository cut over to its own file (stage 4), at a clock the
// test moves.
func authorityRig() (*Watch, *rec, *time.Time) {
	c := central()
	c.PolicyManagement = config.PolicyManagement{Read: true, RepositoryAuthority: true}
	c.Repositories = []config.Repository{{Name: "o/r", Merge: config.Merge{PolicyBot: new(true)}, ClassAuthority: config.ClassAuthorityRepository}}
	r := &rec{}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	w := &Watch{Cfg: c, Metrics: r, Stale: 15 * time.Minute, Now: func() time.Time { return now }}
	c.SetClassSource(w.Classes)
	return w, r, &now
}

const (
	liveDocs    = "factory: aws-0\nmergePolicies:\n  - {name: docs-links, mode: live, maxModifiedLines: 10}\n  - {name: revert, mode: shadow}\n"
	tighterDocs = "factory: aws-0\nmergePolicies:\n  - {name: docs-links, mode: live, maxModifiedLines: 5}\n  - {name: revert, mode: shadow}\n"
	looserDocs  = "factory: aws-0\nmergePolicies:\n  - {name: docs-links, mode: live, maxModifiedLines: 50}\n  - {name: revert, mode: shadow}\n"
)

// Stage 4's fail-closed rule: no read yet, a stale read, an invalid file or one with no classes
// gives no authority; a fresh valid file does.
func TestAuthority(t *testing.T) {
	for name, tc := range map[string]struct {
		body string // "" reads nothing
		age  time.Duration
		want error
	}{
		"never read":       {"", 0, ErrUnreadable},
		"a valid file":     {liveDocs, 0, nil},
		"a stale read":     {liveDocs, 15 * time.Minute, ErrStale},
		"an invalid file":  {"factory: aws-0\nclasses: [no]\n", 0, ErrInvalid},
		"a fence-only one": {"factory: aws-0\n", 0, ErrInvalid},
		"without revert":   {"factory: aws-0\nmergePolicies: [{name: docs-links, mode: live}]\n", 0, ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			w, _, now := authorityRig()
			if tc.body != "" {
				w.Observe(t.Context(), "o/r", []byte(tc.body))
			}
			*now = now.Add(tc.age)
			s, err := w.Authority("O/R")
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if err == nil && !s.Classes["docs-links"].Class.Live {
				t.Fatalf("%+v", s.Classes)
			}
		})
	}
}

// A cut-over repository is compared with nothing: its central classes are gone by design.
func TestACutOverRepositoryRecordsNoDivergence(t *testing.T) {
	w, r, _ := authorityRig()
	w.Observe(t.Context(), "o/r", []byte(liveDocs))
	if s, _ := w.Snapshot("o/r"); len(s.Divergences) != 0 || len(r.gauge) != 0 {
		t.Fatalf("%v %v", s.Divergences, r.gauge)
	}
}

// Every other reader gets the last valid classes; while the file gives no authority, each live
// class reads as shadow, so nothing merges on it (MP13), and the monitor and triage keep working.
func TestClassesPauseWhileThePolicyGivesNoAuthority(t *testing.T) {
	w, _, _ := authorityRig()
	if _, ok := w.Classes("o/r"); ok {
		t.Fatal("never read: no classes")
	}
	w.Observe(t.Context(), "o/r", []byte(liveDocs))
	if cl, _ := w.Classes("o/r"); !cl["docs-links"].Live || !w.Cfg.Current().RepositoryFor("o/r").Classes["docs-links"].Live {
		t.Fatalf("%+v", cl)
	}
	w.Observe(t.Context(), "o/r", []byte("factory: aws-0\nclasses: [no]\n"))
	cl, ok := w.Classes("o/r")
	if !ok || cl["docs-links"].Live || !cl["docs-links"].Shadow || cl["docs-links"].MaxModifiedLines != 10 {
		t.Fatalf("paused, as last valid: %+v", cl)
	}
}

// MP8: a valid file relaxing a live class runs that class in shadow, a tightening keeps it live,
// and both are counted. The demotion holds until the process restarts.
func TestARelaxationReturnsTheClassToShadow(t *testing.T) {
	w, r, _ := authorityRig()
	w.Observe(t.Context(), "o/r", []byte(liveDocs))
	w.Observe(t.Context(), "o/r", []byte(tighterDocs))
	if s, err := w.Authority("o/r"); err != nil || !s.Classes["docs-links"].Class.Live {
		t.Fatalf("a tightening stays live: %v %+v", err, s.Classes["docs-links"])
	}
	w.Observe(t.Context(), "o/r", []byte(looserDocs))
	s, err := w.Authority("o/r")
	if err != nil {
		t.Fatal(err)
	}
	if d := s.Classes["docs-links"]; d.Class.Live || !d.Class.Shadow {
		t.Fatalf("relaxed: %+v", d)
	}
	if cl, _ := w.Classes("o/r"); cl["docs-links"].Live {
		t.Fatal("every reader sees the demotion")
	}
	w.Observe(t.Context(), "o/r", []byte(liveDocs)) // tighter than the relaxed file, still demoted
	if s, _ := w.Authority("o/r"); s.Classes["docs-links"].Class.Live {
		t.Fatal("a demotion is not undone by the next file")
	}
	if want := []string{"docs-links tightening", "docs-links relaxation", "docs-links tightening"}; !slices.Equal(r.changes, want) {
		t.Fatalf("%q", r.changes)
	}
}
