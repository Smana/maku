// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/rooms"
)

// quotesNote fails t unless run's brief quotes note in the QUEUED-DATA fence and the run
// consumed it, and only it.
func quotesNote(t *testing.T, g *rig, run string, ref int64, note string) {
	t.Helper()
	text := g.runs.specs[run].TaskText
	fence := "QUEUED-DATA-n0nce234"
	open, end := strings.Index(text, "\n"+fence+"\n"), strings.LastIndex(text, "\n"+fence+"\n")
	at := strings.Index(text, "> "+note)
	if open < 0 || end <= open || at < open || at > end || !strings.Contains(text, "Queued message seq") {
		t.Fatalf("run %s's brief does not quote the queued note in its fence:\n%s", run, text)
	}
	if got := g.log.consumed[ref]; got != run {
		t.Fatalf("note %d consumed by %q, want %q", ref, got, run)
	}
}

// A note queued before a task's first run is that run's: its brief quotes it, fenced, and the run
// consumes it.
func TestTheFirstRunQuotesWhatWasQueuedBeforeIt(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "# Fix the link"))
	g.log.queue = []rooms.Queued{{Ref: 3, Author: "human:alice", Text: "Also fix the footer link."}}
	tk := g.reconcile(t, "3buqdlot", 3) // Received → Triaged → Queued → Implementing
	if tk.Status.Phase != v1alpha1.PhaseImplementing {
		t.Fatalf("phase %s", tk.Status.Phase)
	}
	quotesNote(t, g, rid(0), 3, "Also fix the footer link.")
	if q, _ := g.log.Queue(t.Context(), "3buqdlot"); len(q) != 0 {
		t.Fatalf("still queued: %+v", q)
	}
}

// A retry before any pull request exists quotes the notes queued since the last run.
func TestARetryWithoutAPullRequestQuotesTheQueue(t *testing.T) {
	tk := awaiting()
	tk.Status.PullRequest = nil
	tk.Status.Phase, tk.Status.NextTrigger = v1alpha1.PhaseQueued, "retry"
	tk.Status.Runs[0].Phase = "Failed"
	g := newRig(t, tk, roomOf("3buqdlot"))
	g.log.queue = []rooms.Queued{{Ref: 40, Author: "human:alice", Text: "Use the v2 client this time."}}
	tk = g.reconcile(t, "3buqdlot", 1)
	if tk.Status.Phase != v1alpha1.PhaseImplementing || len(tk.Status.Runs) != 2 {
		t.Fatalf("%s %+v", tk.Status.Phase, tk.Status.Runs)
	}
	quotesNote(t, g, rid(1), 40, "Use the v2 client this time.")
}

// A scoper's first run, and a plan revision's, quote the notes queued for them: a plan written
// without them would be approved without them.
func TestAPlanAndItsRevisionQuoteTheQueue(t *testing.T) {
	g, tk := planGateRig(t, "alice")
	g.log.queue = []rooms.Queued{{Ref: 5, Author: "human:alice", Text: "Keep the public API as is."}}
	tk = g.reconcile(t, tk.Name, 3)
	if len(tk.Status.Runs) != 1 || tk.Status.Runs[0].Role != "triager" {
		t.Fatalf("no scoper run: %+v", tk.Status.Runs)
	}
	quotesNote(t, g, rid(0), 5, "Keep the public API as is.")
	text := g.runs.specs[rid(0)].TaskText
	if !strings.HasPrefix(text, "<!--") {
		t.Fatalf("the scoper's marker is no longer its brief's first line:\n%s", text[:min(len(text), 120)])
	}
	tk = endScoperReady(g, t, tk, rid(0), nil)

	g.log.queue = append(g.log.queue, rooms.Queued{Ref: 9, Author: "human:alice", Text: "And drop the cache rewrite."})
	appendComment(g, 7, forge.Comment{ID: 102, Author: "alice", Body: "/factory plan revise smaller", At: now.Add(5 * time.Minute)})
	tk = g.reconcile(t, tk.Name, 2)
	if len(tk.Status.Runs) != 2 || tk.Status.Runs[1].Role != "triager" {
		t.Fatalf("no revision run: %+v", tk.Status.Runs)
	}
	quotesNote(t, g, rid(1), 9, "And drop the cache rewrite.")
	if strings.Contains(g.runs.specs[rid(1)].TaskText, "Keep the public API as is.") {
		t.Fatal("a consumed note is quoted twice")
	}
}

// What does not fit beside a first brief waits, unconsumed, for a later run: the same bound as a
// revision's.
func TestAFirstBriefQuotesTheQueueWithinItsBound(t *testing.T) {
	g := newRig(t, issueTask("3buqdlot", 7, "# Fix the link"))
	for i := range 6 {
		g.log.queue = append(g.log.queue, rooms.Queued{Ref: int64(10 + i), Author: "human:alice", Text: strings.Repeat("x", 3<<10)})
	}
	g.reconcile(t, "3buqdlot", 3)
	text := g.runs.specs[rid(0)].TaskText
	if len(text) > reviseCap {
		t.Fatalf("brief %d bytes, over reviseCap %d", len(text), reviseCap)
	}
	if g.log.consumed[10] != rid(0) || len(g.log.consumed) == 6 || !strings.Contains(text, "more queued messages are not quoted") {
		t.Fatalf("consumed %v", g.log.consumed)
	}
}
