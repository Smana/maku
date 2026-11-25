// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"context"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
)

type rec struct {
	reasons []string
	gauge   []int
	changes []string
}

func (r *rec) PolicyLoadError(_ context.Context, _, reason string) {
	r.reasons = append(r.reasons, reason)
}
func (r *rec) PolicyDivergence(_ context.Context, _ string, n int) { r.gauge = append(r.gauge, n) }

func watchRig() (*Watch, *rec) {
	c := central()
	c.Repositories = []config.Repository{{Name: "o/r", Merge: config.Merge{PolicyBot: new(true)}, Classes: map[string]config.Class{
		"docs-links": {Live: true}, "revert": {Live: true},
	}}}
	r := &rec{}
	return &Watch{Cfg: c, Metrics: r}, r
}

const same = "factory: aws-0\nmergePolicies:\n  - {name: docs-links, mode: live}\n  - {name: revert, mode: live}\n"

func TestObserve(t *testing.T) {
	for name, tc := range map[string]struct {
		body    string
		reasons []string
		gauge   []int
		classes bool
	}{
		"no policy":         {"factory: aws-0\n", nil, nil, false},
		"the classes shape": {"factory: aws-0\nclasses:\n  docs-links: {mode: live}\n", []string{ReasonInvalid}, nil, false},
		"equivalent":        {same, nil, []int{0}, true},
		"divergent":         {"factory: aws-0\nmergePolicies:\n  - {name: docs-links, mode: shadow}\n  - {name: revert, mode: live}\n", nil, []int{1}, true},
		"invalid":           {"factory: aws-0\nclasses: [no]\n", []string{ReasonInvalid}, nil, false},
		"a single policy":   {"factory: aws-0\nmergePolicies: [{name: docs-links, mode: live}]\n", nil, []int{1}, true},
	} {
		t.Run(name, func(t *testing.T) {
			w, r := watchRig()
			w.Observe(t.Context(), "o/r", []byte(tc.body))
			if len(r.reasons) != len(tc.reasons) || (len(tc.reasons) == 1 && r.reasons[0] != tc.reasons[0]) {
				t.Fatalf("reasons %v", r.reasons)
			}
			if len(r.gauge) != len(tc.gauge) || (len(tc.gauge) == 1 && r.gauge[0] != tc.gauge[0]) {
				t.Fatalf("gauge %v", r.gauge)
			}
			s, ok := w.Snapshot("o/r")
			if !ok || s.Hash == "" || s.HasClasses != tc.classes || (s.Err != nil) != (len(tc.reasons) > 0) {
				t.Fatalf("%+v", s)
			}
		})
	}
}

// A repository the config does not list has nothing to compare with, and its name must not reach
// a metric through this path.
func TestObserveIgnoresAnUnlistedRepository(t *testing.T) {
	w, r := watchRig()
	w.Observe(t.Context(), "o/other", []byte(same))
	if len(r.reasons)+len(r.gauge) != 0 {
		t.Fatalf("%+v", r)
	}
	if _, ok := w.Snapshot("o/other"); ok {
		t.Fatal("no snapshot")
	}
}

// REL-M1: an older snapshot must not overwrite a newer concurrent Observe or run demote backwards.
func TestObserveRejectsStaleCommit(t *testing.T) {
	w, _ := watchRig()
	cfg := w.Cfg.(*config.Config)
	cfg.Repositories[0].ClassAuthority = config.ClassAuthorityRepository

	t1 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)

	// Older policy had a looser maxPatchFiles (10), newer policy tightened it to 3.
	older := "factory: aws-0\nmergePolicies:\n  - {name: docs-links, mode: live, maxPatchFiles: 10}\n  - {name: revert, mode: live}\n"
	newer := "factory: aws-0\nmergePolicies:\n  - {name: docs-links, mode: live, maxPatchFiles: 3}\n  - {name: revert, mode: live}\n"

	// Commit the newer snapshot first (t2), then an older snapshot arrives late (t1 < t2).
	w.observe(t.Context(), "o/r", []byte(newer), t2)
	sBefore, ok := w.Snapshot("o/r")
	if !ok {
		t.Fatal("expected newer snapshot")
	}

	w.observe(t.Context(), "o/r", []byte(older), t1)
	sAfter, ok := w.Snapshot("o/r")
	if !ok {
		t.Fatal("expected snapshot after stale observe")
	}
	if sAfter.Hash != sBefore.Hash || !sAfter.Read.Equal(t2) {
		t.Fatalf("stale observe overwrote state: got hash=%s read=%v, want hash=%s read=%v",
			sAfter.Hash, sAfter.Read, sBefore.Hash, t2)
	}
	w.mu.RLock()
	demoted := w.demoted["o/r"]
	w.mu.RUnlock()
	if len(demoted) != 0 {
		t.Fatalf("stale observe ran demote backwards: %v", demoted)
	}
}
