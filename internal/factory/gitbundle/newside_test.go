// SPDX-License-Identifier: Apache-2.0

package gitbundle

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

// calls splits fakeGit's log into its calls: each call's ARG and ENV lines.
func calls(t *testing.T, log string) [][]string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, line := range strings.Split(string(data), "\n") {
		if line == "CALL" {
			out = append(out, nil)
			continue
		}
		if len(out) > 0 {
			out[len(out)-1] = append(out[len(out)-1], line)
		}
	}
	return out
}

// #505's review: the blob sizing reads only the new side's blobs, all of them in the bundle, with
// lazy fetching off, so it never fetches one old blob at a time from the remote. A new-side blob
// the bundle leaves out (a file reverted to content the remote holds already) is the remote's own,
// as an old-side blob is: core.bigFileThreshold and the binary refusal bound it.
func TestCheckBlobsNewSideOnly(t *testing.T) {
	f := newFixture(t)
	bin, log := fakeGit(t)
	f.cache.Git = bin
	m := f.cache.Mirror("owner/repo")
	ctx := context.Background()

	// agent/t-1 changes a.txt, which the remote then holds; the bundle reverts it to main's content.
	f.commit(map[string]string{"a.txt": "2\n"}, "change a")
	git(t, f.work, "", "push", "--quiet", f.remote, "agent/t-1")
	old := f.remoteHead("refs/heads/agent/t-1")
	f.commit(map[string]string{"a.txt": "1\n", "c.txt": "c\n"}, "revert a, add c")
	in, err := m.Inspect(ctx, f.bundle(old+"..agent/t-1"), "agent/t-1", "main")
	if err != nil {
		t.Fatalf("Inspect of a revert: %v", err)
	}
	if in.Kind != FastForward || len(in.Files) != 2 {
		t.Fatalf("Inspect = %+v", in)
	}

	sized := 0
	for _, c := range calls(t, log) {
		isBatch := false
		for _, l := range c {
			if strings.HasPrefix(l, "ARG --batch-check") {
				isBatch = true
			}
		}
		if !isBatch {
			continue
		}
		sized++
		if !slices.Contains(c, "ENV GIT_NO_LAZY_FETCH=1") {
			t.Errorf("the blob sizing may fetch lazily: %q", c)
		}
	}
	if sized == 0 {
		t.Fatal("no blob was sized")
	}
}

// #505's review: the applier's own refusals (caps, T1 to T4, a hold) drop the ref an inspection
// fetched the bundle into, as Inspect's refusals do.
func TestDiscard(t *testing.T) {
	f := newFixture(t)
	m := f.cache.Mirror("owner/repo")
	old := f.remoteHead("refs/heads/agent/t-1")
	f.commit(map[string]string{"c.txt": "c\n"}, "two")
	in, err := m.Inspect(context.Background(), f.bundle(old+"..agent/t-1"), "agent/t-1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if outRefs(t, m) == "" {
		t.Fatal("the inspection fetched into no ref")
	}
	m.Discard(context.Background(), in)
	if got := outRefs(t, m); got != "" {
		t.Fatalf("refs after Discard: %q", got)
	}
	m.Discard(context.Background(), in) // twice is harmless
	m.Discard(context.Background(), Inspection{})
}
