// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/coordinationview"
	"github.com/Smana/maku/internal/factory/coordination"
)

// family is two repositories under one work directory: Smana/a, whose main carries the merged
// contract, and Smana/b, whose agent/bbbbcccc carries the consumer's open head. Each clone records
// a GitHub URL that insteadOf sends to its bare origin.
type family struct {
	parent, wsA, wsB, seedA, seedB string
	merge, head                    string
}

func repoFixture(t *testing.T, root, name string) (seed, ws string) {
	t.Helper()
	origin, seed := filepath.Join(root, name+".git"), filepath.Join(root, "seed-"+name)
	for _, d := range []string{origin, seed, filepath.Join(root, "work")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, origin, "init", "-q", "--bare", "-b", "main")
	gitIn(t, seed, "init", "-q", "-b", "main")
	put(t, seed, "README.md", name+"\n", 0o644)
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-q", "-m", "init")
	gitIn(t, seed, "remote", "add", "origin", origin)
	gitIn(t, seed, "push", "-q", "origin", "main")
	ws = filepath.Join(root, "work", name)
	gitIn(t, filepath.Join(root, "work"), "clone", "-q", origin, ws)
	url := "git@github.com:Smana/" + name + ".git"
	gitIn(t, ws, "remote", "set-url", "origin", url)
	gitIn(t, ws, "config", "url."+origin+".insteadOf", url)
	return seed, ws
}

func newFamily(t *testing.T, consumerFiles map[string]string) family {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	f := family{parent: filepath.Join(root, "work")}
	f.seedA, f.wsA = repoFixture(t, root, "a")
	f.seedB, f.wsB = repoFixture(t, root, "b")
	put(t, f.seedA, "schema.json", "{}\n", 0o644)
	gitIn(t, f.seedA, "add", "-A")
	gitIn(t, f.seedA, "commit", "-q", "-m", "contract")
	gitIn(t, f.seedA, "push", "-q", "origin", "main")
	f.merge = gitIn(t, f.seedA, "rev-parse", "HEAD")
	gitIn(t, f.seedB, "checkout", "-q", "-b", "agent/bbbbcccc")
	put(t, f.seedB, "consumer.go", "package b\n", 0o644)
	for rel, body := range consumerFiles {
		put(t, f.seedB, rel, body, 0o755)
	}
	gitIn(t, f.seedB, "add", "-A")
	gitIn(t, f.seedB, "commit", "-q", "-m", "consumer")
	gitIn(t, f.seedB, "push", "-q", "origin", "agent/bbbbcccc")
	f.head = gitIn(t, f.seedB, "rev-parse", "HEAD")
	return f
}

func (f family) document() coordinationview.CheckoutDocument {
	art := v1alpha1.LandedArtifact{ChildID: "contract", Name: "schema", Repository: "Smana/a", Path: "schema.json", MergeCommitSHA: f.merge}
	return coordinationview.CheckoutDocument{APIVersion: coordinationview.CheckoutAPIVersion, Kind: coordinationview.CheckoutKind,
		CheckoutManifest: coordination.CheckoutManifest{ParentTask: "parent1x", PlanVersion: 2, PlanHash: "abc", Children: []coordination.CheckoutChild{
			{ID: "contract", Repository: "Smana/a", Worktree: "contract", Room: "aaaabbbb", Issue: "https://github.com/Smana/a/issues/1",
				PR: "https://github.com/Smana/a/pull/2", DependsOn: []string{}, Artifacts: []v1alpha1.LandedArtifact{art}, MergeCommitSHA: f.merge},
			{ID: "consumer", Repository: "Smana/b", Worktree: "consumer", Room: "bbbbcccc", Issue: "https://github.com/Smana/b/issues/3",
				PR: "https://github.com/Smana/b/pull/4", DependsOn: []string{"contract"}, Artifacts: []v1alpha1.LandedArtifact{art}, HeadSHA: f.head},
		}},
		IntegrationAttempts: []coordination.IntegrationAttemptView{}}
}

func (f family) checkout(g Git, d coordinationview.CheckoutDocument) CoordinationCheckout {
	return CoordinationCheckout{Git: g, Workspace: f.wsA, Workspaces: map[string]string{"Smana/b": f.wsB}, Document: d}
}

func TestCoordinationCheckoutCreatesDetachedVerifiedWorktrees(t *testing.T) {
	f := newFamily(t, nil)
	put(t, f.wsA, "dirty.txt", "untracked\n", 0o644)
	status, head := gitIn(t, f.wsA, "status", "--porcelain"), gitIn(t, f.wsA, "rev-parse", "HEAD")
	branchesB := gitIn(t, f.wsB, "branch", "--list")
	got, err := f.checkout(RunGit, f.document()).Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(f.parent, "a.parent1x")
	if got.Root != root || got.ManifestPath != filepath.Join(root, CoordinationManifestFile) || got.Reused || len(got.Children) != 2 {
		t.Fatalf("%+v", got)
	}
	for _, c := range got.Children {
		want := map[string]string{"contract": f.merge, "consumer": f.head}[c.ChildID]
		if c.Path != filepath.Join(root, c.ChildID) || c.SHA != want || gitIn(t, c.Path, "rev-parse", "HEAD") != want {
			t.Fatalf("%+v", c)
		}
		if b := gitIn(t, c.Path, "branch", "--show-current"); b != "" {
			t.Fatalf("%s is on branch %q, not detached", c.ChildID, b)
		}
	}
	if gitIn(t, f.wsA, "status", "--porcelain") != status || gitIn(t, f.wsA, "rev-parse", "HEAD") != head ||
		gitIn(t, f.wsB, "branch", "--list") != branchesB {
		t.Fatal("a clone's checkout or branches changed")
	}
	raw, err := os.ReadFile(got.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var d coordinationview.CheckoutDocument
	if err := json.Unmarshal(raw, &d); err != nil || d.APIVersion != coordinationview.CheckoutAPIVersion ||
		d.Children[0].Worktree != filepath.Join(root, "contract") || len(d.Children[1].Artifacts) != 1 {
		t.Fatalf("%v %s", err, raw)
	}

	again, err := f.checkout(RunGit, f.document()).Run(t.Context())
	if err != nil || !again.Reused || !slices.EqualFunc(again.Children, got.Children, func(a, b CoordinationWorktree) bool { return a.Path == b.Path && a.SHA == b.SHA }) {
		t.Fatalf("%+v %v", again, err)
	}
	if raw2, _ := os.ReadFile(got.ManifestPath); string(raw2) != string(raw) {
		t.Fatal("a reuse rewrote the manifest")
	}
}

// runRefused runs c and fails the test unless c refused, with that refusal's text.
func runRefused(t *testing.T, c CoordinationCheckout) string {
	t.Helper()
	_, err := c.Run(t.Context())
	return asRefusal(t, err).Error()
}

func asStale(t *testing.T, err error) {
	t.Helper()
	if r := asRefusal(t, err); !strings.Contains(r.Why, "checkout_stale") {
		t.Fatalf("not stale: %v", err)
	}
}

func TestCoordinationCheckoutMovingRemoteAndStaleManifestRefuse(t *testing.T) {
	t.Run("the open head moved on origin", func(t *testing.T) {
		f := newFamily(t, nil)
		put(t, f.seedB, "later.go", "package b\n", 0o644)
		gitIn(t, f.seedB, "add", "-A")
		gitIn(t, f.seedB, "commit", "-q", "-m", "later")
		gitIn(t, f.seedB, "push", "-q", "origin", "agent/bbbbcccc")
		_, err := f.checkout(RunGit, f.document()).Run(t.Context())
		asStale(t, err)
		if _, err := os.Lstat(filepath.Join(f.parent, "a.parent1x")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a refused checkout made its root")
		}
	})
	t.Run("the merge is no longer on the default branch", func(t *testing.T) {
		f := newFamily(t, nil)
		gitIn(t, f.seedA, "reset", "-q", "--hard", "HEAD~1")
		gitIn(t, f.seedA, "push", "-q", "--force", "origin", "main")
		_, err := f.checkout(RunGit, f.document()).Run(t.Context())
		asStale(t, err)
	})
	t.Run("a changed plan against an existing checkout", func(t *testing.T) {
		f := newFamily(t, nil)
		got, err := f.checkout(RunGit, f.document()).Run(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		d := f.document()
		d.PlanVersion, d.PlanHash = 3, "def"
		_, err = f.checkout(RunGit, d).Run(t.Context())
		asStale(t, err)
		if gitIn(t, got.Children[1].Path, "rev-parse", "HEAD") != f.head {
			t.Fatal("an existing worktree moved")
		}
	})
}

func TestCoordinationCheckoutSafety(t *testing.T) {
	t.Run("a missing clone names the clone command", func(t *testing.T) {
		f := newFamily(t, nil)
		c := f.checkout(RunGit, f.document())
		c.Workspaces = nil
		if r := runRefused(t, c); !strings.HasSuffix(r, "; gh repo clone Smana/b") {
			t.Fatalf("%s", r)
		}
	})
	t.Run("a clone of another repository", func(t *testing.T) {
		f := newFamily(t, nil)
		c := f.checkout(RunGit, f.document())
		c.Workspaces = map[string]string{"Smana/b": f.wsA}
		_ = runRefused(t, c)
	})
	t.Run("a workspace outside the family", func(t *testing.T) {
		f := newFamily(t, nil)
		d := f.document()
		d.Children[0].Repository = "Smana/c"
		c := f.checkout(RunGit, d)
		c.Workspaces = map[string]string{"Smana/b": f.wsB}
		_ = runRefused(t, c)
	})
	t.Run("flagged files need one combined confirmation", func(t *testing.T) {
		f := newFamily(t, map[string]string{".husky/pre-commit": "#!/bin/sh\n", ".envrc": "export X=1\n"})
		c := f.checkout(RunGit, f.document())
		_ = runRefused(t, c) // no form
		ask := &confirmer{answer: false}
		c.Confirm = ask.confirm
		_ = runRefused(t, c)
		if len(ask.asked) != 1 || !slices.Contains(ask.asked[0], "consumer/.envrc") {
			t.Fatalf("asked %v", ask.asked)
		}
		if _, err := os.Lstat(filepath.Join(f.parent, "a.parent1x")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a declined checkout made its root")
		}
		ask.answer = true
		if _, err := c.Run(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("an existing root without a manifest is refused, not repaired", func(t *testing.T) {
		f := newFamily(t, nil)
		if err := os.Mkdir(filepath.Join(f.parent, "a.parent1x"), 0o755); err != nil {
			t.Fatal(err)
		}
		if r := runRefused(t, f.checkout(RunGit, f.document())); !strings.Contains(r, "a.parent1x") {
			t.Fatalf("%s", r)
		}
	})
	t.Run("a failure after the first worktree reports the incomplete root", func(t *testing.T) {
		f := newFamily(t, nil)
		adds := 0
		g := func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 1 && args[0] == "worktree" && args[1] == "add" {
				if adds++; adds == 2 {
					return "", errors.New("disk full")
				}
			}
			for _, a := range args {
				if slices.Contains([]string{"checkout", "switch", "reset", "clone", "branch", "update-ref"}, a) && a == args[0] {
					t.Errorf("git %v", args)
				}
			}
			return RunGit(ctx, dir, args...)
		}
		_, err := f.checkout(g, f.document()).Run(t.Context())
		root := filepath.Join(f.parent, "a.parent1x")
		if err == nil || !strings.Contains(err.Error(), "incomplete") || !strings.Contains(err.Error(), root) {
			t.Fatalf("%v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "contract")); err != nil {
			t.Fatal("the first worktree was removed")
		}
		if _, err := os.Stat(filepath.Join(root, CoordinationManifestFile)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a manifest was written for an incomplete family")
		}
	})
	t.Run("a hostile document is refused before any git call", func(t *testing.T) {
		f := newFamily(t, nil)
		for _, mut := range []func(*coordinationview.CheckoutDocument){
			func(d *coordinationview.CheckoutDocument) { d.Children[1].ID = "../escape" },
			func(d *coordinationview.CheckoutDocument) { d.ParentTask = "../x" },
			func(d *coordinationview.CheckoutDocument) { d.Children[1].Room = "x/../../y" },
			func(d *coordinationview.CheckoutDocument) { d.Children[1].HeadSHA = "HEAD" },
			func(d *coordinationview.CheckoutDocument) { d.APIVersion = "v2" },
		} {
			d := f.document()
			mut(&d)
			calls := 0
			g := func(ctx context.Context, dir string, args ...string) (string, error) {
				calls++
				return RunGit(ctx, dir, args...)
			}
			if _, err := f.checkout(g, d).Run(t.Context()); err == nil || calls != 0 {
				t.Fatalf("%v after %d calls", err, calls)
			}
		}
	})
}

func coordinationCall(t *testing.T, s session, args map[string]any, ws string) map[string]any {
	t.Helper()
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/call", "params": map[string]any{"name": toolCoordinationCheckout, "arguments": args}})
	for {
		m := s.next(t)
		switch {
		case m["method"] == "roots/list":
			s.send(t, map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": map[string]any{"roots": []any{map[string]any{"uri": "file://" + ws}}}})
		case m["method"] == "elicitation/create":
			t.Fatalf("no form expected: %v", m)
		case m["id"] == 9.0 && m["method"] == nil:
			return m
		}
	}
}

func TestRelayCoordinationCheckout(t *testing.T) {
	f := newFamily(t, nil)
	b := newMCPBroker(t)
	start := func() session {
		s := relayTo(t, b, tok)
		s.request(t, 1, "initialize", map[string]any{"capabilities": map[string]any{"roots": map[string]any{}}})
		return s
	}
	t.Run("no document is refused", func(t *testing.T) {
		text, _, isErr := resultOf(coordinationCall(t, start(), map[string]any{"parent": "parent1x"}, f.wsA))
		if !isErr || !strings.Contains(text, "not_checked_out") {
			t.Fatalf("%q", text)
		}
	})
	t.Run("a field beyond parent and workspaces is refused", func(t *testing.T) {
		text, _, isErr := resultOf(coordinationCall(t, start(), map[string]any{"parent": "parent1x", "confirm": true}, f.wsA))
		if !isErr || !strings.Contains(text, "invalid_arguments") {
			t.Fatalf("%q", text)
		}
	})
	t.Run("the family is checked out from the broker's document", func(t *testing.T) {
		b.mu.Lock()
		b.checkoutDoc = f.document()
		b.mu.Unlock()
		_, sc, isErr := resultOf(coordinationCall(t, start(), map[string]any{"parent": "parent1x", "workspaces": map[string]any{"Smana/b": f.wsB}}, f.wsA))
		if isErr || sc["root"] != filepath.Join(f.parent, "a.parent1x") || sc["reused"] != false {
			t.Fatalf("%v", sc)
		}
	})
}
