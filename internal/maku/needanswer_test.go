// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAppendDecision(t *testing.T) {
	const plain = "## What is wrong\nx\n\n## What it should be\ny\n\n## Acceptance check\ntask check"
	t.Run("creates Decisions after Acceptance check", func(t *testing.T) {
		got, err := AppendDecision(plain, "  Use task check  ")
		want := plain + "\n\n## Decisions\n\nD1: Use task check"
		if err != nil || got != want {
			t.Fatalf("AppendDecision() = %q, %v; want %q", got, err, want)
		}
	})

	t.Run("uses the maximum interleaved non-contiguous number", func(t *testing.T) {
		body := plain + "\n\n## Decisions\n\nD1 [g1]: first choice\nA2 [g2]: assumed second\nD7: scoped earlier"
		got, err := AppendDecision(body, "Use Linux")
		want := body + "\nD8: Use Linux"
		if err != nil || got != want {
			t.Fatalf("AppendDecision() = %q, %v; want %q", got, err, want)
		}
	})

	t.Run("preserves every prior byte on repeated appends", func(t *testing.T) {
		body := plain + "\n\n## Decisions\n\nD1: first\n"
		first, err := AppendDecision(body, "second")
		if err != nil || first != body+"D2: second" {
			t.Fatalf("first AppendDecision() = %q, %v", first, err)
		}
		second, err := AppendDecision(first, "third")
		if err != nil || second != first+"\nD3: third" {
			t.Fatalf("second AppendDecision() = %q, %v", second, err)
		}
	})

	for _, answer := range []string{"", " \t "} {
		t.Run("rejects blank "+strings.ReplaceAll(answer, "\t", "tab"), func(t *testing.T) {
			if got, err := AppendDecision(plain, answer); err == nil || got != "" {
				t.Fatalf("AppendDecision() = %q, %v; want no body and an error", got, err)
			}
		})
	}
	for _, answer := range []string{"first\nsecond", "first\rsecond", "\nfirst", "first\n"} {
		t.Run("rejects multiline "+strings.NewReplacer("\n", "LF", "\r", "CR").Replace(answer), func(t *testing.T) {
			if got, err := AppendDecision(plain, answer); err == nil || got != "" {
				t.Fatalf("AppendDecision() = %q, %v; want no body and an error", got, err)
			}
		})
	}

	t.Run("rejects an invalid existing Decisions section", func(t *testing.T) {
		body := plain + "\n\n## Decisions\nD2: later\nD1: earlier"
		if got, err := AppendDecision(body, "third"); err == nil || got != "" {
			t.Fatalf("AppendDecision() = %q, %v; want no body and an error", got, err)
		}
	})
	t.Run("does not create Decisions without Acceptance check", func(t *testing.T) {
		if got, err := AppendDecision("## What is wrong\nx", "choice"); err == nil || got != "" {
			t.Fatalf("AppendDecision() = %q, %v; want no body and an error", got, err)
		}
	})
}

func TestGHIssueView(t *testing.T) {
	dir := t.TempDir()
	fixture := `{"body":"## Acceptance check\ntask check","labels":[{"name":"factory/ready"},{"name":"needs/spec"}],"updatedAt":"2026-11-09T10:11:12Z","comments":[{"author":{"login":"alice"},"body":"first","createdAt":"2026-11-09T10:12:13Z"},{"author":{"login":"bob"},"body":"second","createdAt":"2026-11-09T10:13:14Z"}]}`
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + filepath.Join(dir, "args") + "\ncat <<'EOF'\n" + fixture + "\nEOF\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := (GHIssues{}).View(t.Context(), "Smana/a", 7)
	if err != nil {
		t.Fatal(err)
	}
	want := IssueSnapshot{
		Body:      "## Acceptance check\ntask check",
		Labels:    []string{"factory/ready", "needs/spec"},
		UpdatedAt: time.Date(2026, 11, 9, 10, 11, 12, 0, time.UTC),
		Comments: []IssueComment{
			{Author: "alice", Body: "first", CreatedAt: time.Date(2026, 11, 9, 10, 12, 13, 0, time.UTC)},
			{Author: "bob", Body: "second", CreatedAt: time.Date(2026, 11, 9, 10, 13, 14, 0, time.UTC)},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("View() = %#v; want %#v", got, want)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "issue\nview\n7\n--repo=Smana/a\n--json\nbody,labels,updatedAt,comments\n" {
		t.Fatalf("gh args = %q", args)
	}
}

func TestGHIssueEditBody(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + filepath.Join(dir, "args") + "\ncat > " + filepath.Join(dir, "stdin") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	body := "## Acceptance check\ntask check\n\n## Decisions\n\nD1: use gh\n--add-label=factory/ready\n"
	if err := (GHIssues{}).EditBody(t.Context(), "Smana/a", 7, body); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "issue\nedit\n7\n--repo=Smana/a\n--body-file=-\n" {
		t.Fatalf("gh args = %q", args)
	}
	stdin, err := os.ReadFile(filepath.Join(dir, "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != body {
		t.Fatalf("gh stdin = %q; want %q", stdin, body)
	}
}

func TestGHIssueCallsTimeOut(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := ghTimeout
	ghTimeout = 100 * time.Millisecond
	t.Cleanup(func() { ghTimeout = old })
	start := time.Now()
	if _, err := (GHIssues{}).View(t.Context(), "Smana/a", 7); err == nil {
		t.Fatal("View succeeded while gh was stuck")
	}
	if err := (GHIssues{}).EditBody(t.Context(), "Smana/a", 7, "body"); err == nil {
		t.Fatal("EditBody succeeded while gh was stuck")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("gh was not cut off")
	}
}
