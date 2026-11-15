// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// GH files with the developer's gh: each value joined to its flag, the body on stdin, no label.
func TestGHFilesWithoutALabel(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + filepath.Join(dir, "args") + "\ncat > " + filepath.Join(dir, "stdin") +
		"\necho https://github.com/Smana/a/issues/7\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	url, err := GH(t.Context(), "Smana/a", "docs: a dead link", "## What is wrong\nx")
	if err != nil || url != "https://github.com/Smana/a/issues/7" {
		t.Fatalf("%q %v", url, err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if string(args) != "issue\ncreate\n--repo=Smana/a\n--title=docs: a dead link\n--body-file=-\n" || string(stdin) != "## What is wrong\nx" {
		t.Fatalf("args %q stdin %q", args, stdin)
	}
}

func TestGHLabelArgs(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + filepath.Join(dir, "args") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := GHLabel(t.Context(), "Smana/a", 7, "factory/ready"); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if string(args) != "issue\nedit\n7\n--repo=Smana/a\n--add-label=factory/ready\n" {
		t.Fatalf("%q", args)
	}
}

func TestIssueNumber(t *testing.T) {
	for url, want := range map[string]int{
		"https://github.com/Smana/a/issues/7":   7,
		"https://github.com/Smana/b/issues/7":   0,
		"https://github.com/Smana/a/pull/7":     0,
		"https://github.com/Smana/a/issues/7/x": 0,
		"https://github.com/Smana/a/issues/0":   0,
		"http://github.com/Smana/a/issues/7":    0,
		"":                                      0,
		"https://github.com/smana/A/issues/7":   7, // gh prints the canonical case
	} {
		n, err := issueNumber(url, "Smana/a")
		if (err == nil) != (want != 0) || n != want {
			t.Errorf("%q: %d %v", url, n, err)
		}
	}
}

func TestGHCallsTimeOut(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := ghTimeout
	ghTimeout = 100 * time.Millisecond
	t.Cleanup(func() { ghTimeout = old })
	start := time.Now()
	if err := GHLabel(t.Context(), "Smana/a", 7, "factory/ready"); err == nil {
		t.Fatal("label")
	}
	if _, err := GH(t.Context(), "Smana/a", "t", "b"); err == nil {
		t.Fatal("file")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("gh was not cut off")
	}
}

// The command quotes what the draft holds: a title with a quote, a body that holds the heredoc's end.
func TestFallbackCommandQuoting(t *testing.T) {
	cmd := fallbackCommand(draftShape{Repo: "Smana/a", Title: "fix: it's $(bad)", Body: "x\nBODY_EOF\ny"})
	if !strings.Contains(cmd, `--title='fix: it'\''s $(bad)'`) || strings.Count(cmd, "\nBODY_EOF\n") != 1 && !strings.Contains(cmd, "BODY_EOF_1") {
		t.Fatalf("%s", cmd)
	}
	if strings.Contains(cmd, "--add-label") {
		t.Fatalf("filed only: %s", cmd)
	}
}

// fakeBrokerServer is the broker's /mcp as issue_file calls it: the plan_check answers are the

func TestLabelArgsNeverAShell(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + filepath.Join(dir, "args") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := GHAddLabel(t.Context(), "Smana/a", 7, "class:docs-links; rm -rf /"); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if string(args) != "api\n-X\nPOST\nrepos/Smana/a/issues/7/labels\n-f\nlabels[]=class:docs-links; rm -rf /\n" {
		t.Fatalf("%q", args)
	}
}

// O1: the form shows the class, the factory's choice of profile and the merge words, then the

// withFile files through f.
func withFile(f IssueFiler) func(*Relay) { return func(r *Relay) { r.File = f } }

// filing records what the relay asked gh to file.
type filing struct {
	mu    sync.Mutex
	calls []string
	url   string // what gh prints; empty is an issue of Smana/a
}

func (f *filing) file(_ context.Context, repo, title, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, repo+"|"+title+"|"+body)
	if f.url != "" {
		return f.url, nil
	}
	return "https://github.com/Smana/a/issues/7", nil
}

// labeling records what the relay asked gh to label.
type labeling struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (l *labeling) label(_ context.Context, repo string, issue int, label string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, fmt.Sprintf("%s#%d+%s", repo, issue, label))
	return l.err
}

// startSession opens a relay whose client can ask, with the fake gh runners and a short room wait.
func startSession(t *testing.T, b *mcpBroker, f *filing, l *labeling, wait time.Duration) session {
	t.Helper()
	s := relayTo(t, b, tok, withFile(f.file), func(r *Relay) {
		r.Label, r.RoomWait = l.label, wait
		r.After = func(time.Duration) <-chan time.Time { return time.After(5 * time.Millisecond) }
	})
	s.request(t, 1, "initialize", map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}})
	return s
}
