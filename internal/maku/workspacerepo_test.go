// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/Smana/maku/internal/mcpwire"
)

// checkout makes dir look like a git checkout whose origin is remote.
func checkout(t *testing.T, dir, remote string) {
	t.Helper()
	writeGitConfig(t, filepath.Join(dir, ".git"), remote)
}

func writeGitConfig(t *testing.T, gitDir, remote string) {
	t.Helper()
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[core]\n\tbare = false\n[remote \"upstream\"]\n\turl = https://github.com/other/fork.git\n" +
		"[remote \"origin\"]\n\turl = " + remote + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOriginRepository(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/Smana/ai-native-ref.git": "Smana/ai-native-ref",
		"https://github.com/Smana/ai-native-ref":     "Smana/ai-native-ref",
		"https://x:tok@github.com/Smana/maku.git/":   "Smana/maku",
		"git@github.com:Smana/cloud-native-ref.git":  "Smana/cloud-native-ref",
		"ssh://git@github.com/Smana/maku.git":        "Smana/maku",
		"/srv/git/maku":                              "",
		"https://github.com/only":                    "",
	} {
		dir := t.TempDir()
		checkout(t, dir, remote)
		if got := originRepository(dir); got != want {
			t.Errorf("%s: %q, want %q", remote, got, want)
		}
	}
	t.Run("a worktree", func(t *testing.T) {
		main, wt := t.TempDir(), t.TempDir()
		checkout(t, main, "git@github.com:Smana/maku.git")
		gitdir := filepath.Join(main, ".git", "worktrees", "wt")
		if err := os.MkdirAll(gitdir, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("../..\n"), 0o644)
		_ = os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644)
		if got := originRepository(wt); got != "Smana/maku" {
			t.Fatalf("%q", got)
		}
	})
	t.Run("no git", func(t *testing.T) {
		if got := originRepository(t.TempDir()); got != "" {
			t.Fatalf("%q", got)
		}
	})
}

// handoffWithRoots sends a handoff with args from a client whose roots are dirs, and returns the
// arguments the broker received.
func handoffWithRoots(t *testing.T, args map[string]any, dirs ...string) map[string]any {
	t.Helper()
	b := newMCPBroker(t)
	s, _ := locals(t, b)
	s.request(t, 1, "initialize", map[string]any{"capabilities": map[string]any{"roots": map[string]any{}}})
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": mcpwire.Handoff, "arguments": args}})
	for {
		m := s.next(t)
		if m["method"] == "roots/list" {
			var roots []any
			for _, d := range dirs {
				roots = append(roots, map[string]any{"uri": (&url.URL{Scheme: "file", Path: d}).String()})
			}
			s.send(t, map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": map[string]any{"roots": roots}})
			continue
		}
		if m["id"] == 2.0 {
			break
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.calls) != 1 {
		t.Fatalf("calls %v", b.calls)
	}
	return b.calls[0].Args
}

// #639: a handoff without a repository takes the one of the workspace's git checkout, as if the
// agent had passed it, so the plan does not depend on the client's model following the skill.
func TestHandoffTakesTheRepositoryFromTheWorkspace(t *testing.T) {
	plain, repo := t.TempDir(), t.TempDir()
	checkout(t, repo, "git@github.com:Smana/ai-native-ref.git")
	t.Run("filled from the first checkout", func(t *testing.T) {
		got := handoffWithRoots(t, map[string]any{"text": "fix the budget"}, plain, repo)
		if got["repository"] != "Smana/ai-native-ref" || got["context"] != "Repository: Smana/ai-native-ref (from the workspace)" {
			t.Fatalf("%v", got)
		}
	})
	t.Run("the context keeps the agent's", func(t *testing.T) {
		got := handoffWithRoots(t, map[string]any{"text": "fix", "context": "read docs/a.md"}, repo)
		if got["context"] != "Repository: Smana/ai-native-ref (from the workspace)\nread docs/a.md" {
			t.Fatalf("%v", got)
		}
	})
	t.Run("the agent's repository is kept", func(t *testing.T) {
		got := handoffWithRoots(t, map[string]any{"text": "fix", "repository": "Smana/maku"}, repo)
		if got["repository"] != "Smana/maku" || got["context"] != nil {
			t.Fatalf("%v", got)
		}
	})
	t.Run("no git leaves it unchanged", func(t *testing.T) {
		got := handoffWithRoots(t, map[string]any{"text": "fix", "repository": ""}, plain)
		if got["repository"] != "" || got["context"] != nil {
			t.Fatalf("%v", got)
		}
	})
}

// A continuation, or a request that names candidates, is the agent's: never filled.
func TestHandoffContinuationIsNotFilled(t *testing.T) {
	repo := t.TempDir()
	checkout(t, repo, "https://github.com/Smana/maku.git")
	for name, args := range map[string]map[string]any{
		"continuation": {"id": "h1", "acceptRest": true},
		"candidates":   {"text": "fix", "candidates": []any{"Smana/a", "Smana/b"}},
	} {
		t.Run(name, func(t *testing.T) {
			b := newMCPBroker(t)
			s, _ := locals(t, b)
			s.request(t, 1, "initialize", map[string]any{"capabilities": map[string]any{"roots": map[string]any{}}})
			if res := s.request(t, 2, "tools/call", map[string]any{"name": mcpwire.Handoff, "arguments": args}); res["id"] != 2.0 {
				t.Fatalf("roots must not be asked: %v", res)
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			if _, ok := b.calls[0].Args["repository"]; ok {
				t.Fatalf("%v", b.calls[0].Args)
			}
		})
	}
}
