// SPDX-License-Identifier: Apache-2.0

package decision

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecorderKeepsTheNewest(t *testing.T) {
	var r Recorder
	t0 := time.Unix(100, 0)
	r.Record("Smana/Maku", "docs", "arm", "armed", t0.Add(time.Minute), 4)
	r.Record("smana/maku", "docs", "human", "ci_red", t0, 3) // older, arrives later
	got := r.Latest("SMANA/MAKU")["docs"]
	if got.Verdict != "arm" || got.Generation != 4 || !got.At.Equal(t0.Add(time.Minute)) {
		t.Fatalf("got %+v", got)
	}
}

func TestRecorderLatestIsACopy(t *testing.T) {
	var r Recorder
	r.Record("a/b", "c", "arm", "armed", time.Unix(1, 0), 0)
	r.Latest("a/b")["c"] = Last{Verdict: "x"}
	delete(r.Latest("a/b"), "c")
	if r.Latest("a/b")["c"].Verdict != "arm" {
		t.Fatal("Latest leaked its map")
	}
	if len((&Recorder{}).Latest("none")) != 0 {
		t.Fatal("zero value not empty")
	}
}

func TestRecorderConcurrent(_ *testing.T) {
	var r Recorder
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); r.Record("a/b", "c", "arm", "armed", time.Unix(int64(i), 0), 1) }()
		go func() { defer wg.Done(); _ = r.Latest("a/b") }()
	}
	wg.Wait()
}

func TestMarkerPolicyGeneration(t *testing.T) {
	m := Marker("abc", "docs", "arm", "armed", Rows{Reviewer: "none", Generation: 7})
	if !strings.Contains(m, " policygen=7 reviewer=none -->") {
		t.Fatal(m)
	}
	if m = Marker("abc", "docs", "arm", "armed", Rows{Reviewer: "none"}); strings.Contains(m, "policygen") {
		t.Fatal(m)
	}
}
