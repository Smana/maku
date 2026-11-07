// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"strings"
	"testing"
	"time"
)

// The fence reads every five minutes and the file rarely moves: an unchanged file is not parsed
// or compiled again, and its metrics are recorded as if it were, so a rate still shows a bad file.
func TestObserveSkipsAnUnchangedFile(t *testing.T) {
	w, r := watchRig()
	bad := "factory: aws-0\nclasses: [no]\n"
	for range 3 {
		w.Observe(t.Context(), "o/r", []byte(same))
		w.Observe(t.Context(), "o/q", []byte(same)) // unlisted: never evaluated
	}
	w.Observe(t.Context(), "o/r", []byte(bad))
	w.Observe(t.Context(), "o/r", []byte(bad))
	if w.evaluations != 2 {
		t.Fatalf("%d evaluations, want one per distinct file", w.evaluations)
	}
	if len(r.gauge) != 3 || len(r.reasons) != 2 {
		t.Fatalf("every read records: gauge %v reasons %v", r.gauge, r.reasons)
	}
}

// The snapshot holds the resolved classes the evaluator reads, and when the file was read.
func TestTheSnapshotHoldsTheResolvedClasses(t *testing.T) {
	w, _ := watchRig()
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	w.Now = func() time.Time { return at }
	w.Observe(t.Context(), "O/R", []byte(same))
	s, ok := w.Snapshot("o/r")
	if !ok || len(s.Classes) != 2 || !s.Classes["docs-links"].Class.Live || !s.Read.Equal(at) {
		t.Fatalf("%+v", s)
	}
	w.Observe(t.Context(), "o/r", []byte("factory: aws-0\nclasses: [no]\n"))
	if s, _ := w.Snapshot("o/r"); s.Classes != nil || s.Err == nil {
		t.Fatalf("an invalid file resolves nothing: %+v", s)
	}
}

// A file mixing a guardrail fault with an invalid class is invalid.
func TestAMixedFileIsInvalid(t *testing.T) {
	w, r := watchRig()
	mixed := "factory: aws-0\nclasses:\n  docs-links: {mode: live, author: agents, harness: nope}\n  revert: {mode: live, author: agents, harness: plain}\n" +
		"  bad:\n    mode: shadow\n    author: agents\n    harness: plain\n    conditions: {all: [{allowedFiles: ['docs/.+']}]}\n"
	w.Observe(t.Context(), "o/r", []byte(mixed))
	if strings.Join(r.reasons, ",") != ReasonInvalid {
		t.Fatalf("%v", r.reasons)
	}
}
