// SPDX-License-Identifier: Apache-2.0

package gitbundle

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/hardening"
)

func TestInspectFastForward(t *testing.T) {
	f := newFixture(t)
	old := f.remoteHead("refs/heads/agent/t-1")
	f.commit(map[string]string{"a.txt": "1\n2\n", "c.txt": "c1\nc2\n"}, "two: add c")
	f.commit(map[string]string{"gone.txt": "", "run.sh": "echo hi\n"}, "three: drop gone")
	git(t, f.work, "", "update-index", "--chmod=+x", "run.sh")
	git(t, f.work, "", "commit", "--quiet", "--amend", "--no-edit")
	head := git(t, f.work, "", "rev-parse", "HEAD")

	in, err := f.cache.Mirror("Owner/Repo").Inspect(context.Background(), f.bundle(old+"..agent/t-1"), "agent/t-1", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if in.Kind != FastForward || in.Head != head || in.OldHead != old || in.Ref != "refs/heads/agent/t-1" {
		t.Fatalf("Inspect = %+v, want fast_forward %s from %s", in, head, old)
	}
	if in.BaseHead != f.remoteHead("refs/heads/main") {
		t.Errorf("BaseHead = %s", in.BaseHead)
	}
	wantFiles := []File{
		{Path: "a.txt", Status: "M", Mode: "100644"},
		{Path: "c.txt", Status: "A", Mode: "100644"},
		{Path: "gone.txt", Status: "D", Mode: "100644"},
		{Path: "run.sh", Status: "A", Mode: "100755"},
	}
	if !reflect.DeepEqual(in.Files, wantFiles) {
		t.Errorf("Files = %+v, want %+v", in.Files, wantFiles)
	}
	for _, want := range []string{"2", "c1", "c2", "echo hi", "two: add c", "three: drop gone"} {
		if !slices.Contains(in.Added, want) {
			t.Errorf("Added = %q, missing %q", in.Added, want)
		}
	}
	if slices.Contains(in.Added, "b1") || slices.Contains(in.Added, "feat: one") {
		t.Errorf("Added = %q holds the branch's earlier commit", in.Added)
	}
	// From the merge base with main: b.txt, a.txt, c.txt, gone.txt, run.sh; 1+1+2+1+1 lines.
	if want := (Stats{Files: 5, Lines: 6}); in.Cumulative != want {
		t.Errorf("Cumulative = %+v, want %+v", in.Cumulative, want)
	}
}

func TestInspectRebase(t *testing.T) {
	tests := []struct {
		name   string
		branch string
		build  func(f *fixture) (bundle []byte, wantFiles []string)
	}{{
		name:   "rebased onto a newer main",
		branch: "agent/t-1",
		build: func(f *fixture) ([]byte, []string) {
			git(t, f.work, "", "checkout", "--quiet", "main")
			f.commit(map[string]string{"m.txt": "m\n"}, "main moves")
			git(t, f.work, "", "push", "--quiet", f.remote, "main")
			git(t, f.work, "", "checkout", "--quiet", "agent/t-1")
			git(t, f.work, "", "rebase", "--quiet", "main")
			f.commit(map[string]string{"d.txt": "d\n"}, "after rebase")
			return f.bundle("main..agent/t-1"), []string{"b.txt", "d.txt"}
		},
	}, {
		name:   "branch absent on the remote",
		branch: "agent/t-2",
		build: func(f *fixture) ([]byte, []string) {
			git(t, f.work, "", "checkout", "--quiet", "-b", "agent/t-2", "main")
			f.commit(map[string]string{"e.txt": "e\n", "gone.txt": ""}, "new branch")
			return f.bundle("main..agent/t-2"), []string{"e.txt", "gone.txt"}
		},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			bundle, wantFiles := tt.build(f)
			in, err := f.cache.Mirror("owner/repo").Inspect(context.Background(), bundle, tt.branch, "main")
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if in.Kind != Rebase {
				t.Fatalf("Kind = %s, want rebase", in.Kind)
			}
			var got []string
			for _, file := range in.Files {
				got = append(got, file.Path)
			}
			if !reflect.DeepEqual(got, wantFiles) {
				t.Errorf("Files = %q, want %q (cumulative from the merge base)", got, wantFiles)
			}
			if in.Cumulative.Files != len(wantFiles) {
				t.Errorf("Cumulative = %+v", in.Cumulative)
			}
			if tt.branch == "agent/t-2" && in.OldHead != "" {
				t.Errorf("OldHead = %q, want none", in.OldHead)
			}
		})
	}
}

func TestInspectRefusals(t *testing.T) {
	tests := []struct {
		name  string
		build func(t *testing.T, f *fixture) []byte
		want  hardening.Refusal
	}{{
		name: "two refs",
		build: func(t *testing.T, f *fixture) []byte {
			f.commit(nil, "two")
			git(t, f.work, "", "branch", "other")
			return f.bundle("^main", "agent/t-1", "other")
		},
		want: hardening.RefusePushRef,
	}, {
		name: "a tag",
		build: func(t *testing.T, f *fixture) []byte {
			f.commit(nil, "two")
			git(t, f.work, "", "tag", "v1")
			return f.bundle("^main", "refs/tags/v1")
		},
		want: hardening.RefusePushRef,
	}, {
		name: "another branch",
		build: func(t *testing.T, f *fixture) []byte {
			f.commit(nil, "two")
			git(t, f.work, "", "branch", "agent/t-2")
			return f.bundle("^main", "agent/t-2")
		},
		want: hardening.RefusePushRef,
	}, {
		name: "HEAD alone",
		build: func(_ *testing.T, f *fixture) []byte {
			f.commit(nil, "two")
			return f.bundle("^main", "HEAD")
		},
		want: hardening.RefusePushRef,
	}, {
		name: "not a bundle",
		build: func(*testing.T, *fixture) []byte {
			return []byte("# v2 git bundle\nnonsense\n")
		},
		want: hardening.RefusePushRef,
	}, {
		name: "descends from neither the branch nor the base",
		build: func(t *testing.T, f *fixture) []byte {
			git(t, f.work, "", "checkout", "--quiet", "--orphan", "lonely")
			f.commit(map[string]string{"z.txt": "z\n"}, "orphan")
			git(t, f.work, "", "branch", "-f", "agent/t-1", "lonely")
			return f.bundle("agent/t-1")
		},
		want: hardening.RefusePushHistory,
	}, {
		name: "missing prerequisites",
		build: func(_ *testing.T, f *fixture) []byte {
			prereq := f.commit(map[string]string{"p.txt": "never pushed\n"}, "local only")
			f.commit(map[string]string{"q.txt": "q\n"}, "on top")
			return f.bundle(prereq + "..agent/t-1")
		},
		want: hardening.RefusePushHistory,
	}, {
		name: "a gitlink",
		build: func(t *testing.T, f *fixture) []byte {
			sha := git(t, f.work, "", "rev-parse", "main")
			git(t, f.work, "", "update-index", "--add", "--cacheinfo", "160000,"+sha+",sub")
			git(t, f.work, "", "commit", "--quiet", "-m", "submodule")
			return f.bundle("^main", "agent/t-1")
		},
		want: hardening.RefusePushTree,
	}, {
		name: "a symlink to /etc/passwd",
		build: func(t *testing.T, f *fixture) []byte {
			if err := os.Symlink("/etc/passwd", filepath.Join(f.work, "pw")); err != nil {
				t.Fatal(err)
			}
			f.commit(nil, "link")
			return f.bundle("^main", "agent/t-1")
		},
		want: hardening.RefusePushTree,
	}, {
		name: "a symlink escaping through a/../../x",
		build: func(t *testing.T, f *fixture) []byte {
			if err := os.Symlink("a/../../x", filepath.Join(f.work, "esc")); err != nil {
				t.Fatal(err)
			}
			f.commit(nil, "link")
			return f.bundle("^main", "agent/t-1")
		},
		want: hardening.RefusePushTree,
	}, {
		name: "a .git/hooks path",
		build: func(t *testing.T, f *fixture) []byte {
			f.commit(nil, "base") // a commit to build on
			git(t, f.work, "", "update-ref", "refs/heads/agent/t-1", dotGitCommit(t, f, ".git"))
			return f.bundle("^main", "agent/t-1")
		},
		want: hardening.RefusePushTree,
	}, {
		name: "a .GIT path in a subdirectory",
		build: func(t *testing.T, f *fixture) []byte {
			git(t, f.work, "", "update-ref", "refs/heads/agent/t-1", dotGitCommit(t, f, ".GIT"))
			return f.bundle("^main", "agent/t-1")
		},
		want: hardening.RefusePushTree,
	}, {
		name: "an object failing fsck",
		build: func(t *testing.T, f *fixture) []byte {
			tree := git(t, f.work, "", "rev-parse", "HEAD^{tree}")
			parent := git(t, f.work, "", "rev-parse", "HEAD")
			raw := "tree " + tree + "\nparent " + parent + "\nauthor no email at all\n" +
				"committer Fixture <fixture@example.com> 1700000000 +0000\n\nbad\n"
			sha := git(t, f.work, raw, "hash-object", "-t", "commit", "--literally", "-w", "--stdin")
			git(t, f.work, "", "update-ref", "refs/heads/agent/t-1", sha)
			return f.bundle("^main", "agent/t-1")
		},
		want: hardening.RefusePushTree,
	}, {
		name: "a change too large to read",
		build: func(_ *testing.T, f *fixture) []byte {
			f.commit(map[string]string{"big.txt": strings.Repeat("a line of text\n", 80000)}, "big")
			return f.bundle("^main", "agent/t-1")
		},
		want: hardening.RefusePushSize,
	}, {
		name: "a bundle over MaxBundle",
		build: func(t *testing.T, _ *fixture) []byte {
			b := make([]byte, hardening.MaxBundle+1)
			if _, err := rand.Read(b); err != nil {
				t.Fatal(err)
			}
			return b
		},
		want: hardening.RefusePushSize,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			bundle := tt.build(t, f)
			_, err := f.cache.Mirror("owner/repo").Inspect(context.Background(), bundle, "agent/t-1", "main")
			t.Logf("Inspect: %v", err)
			if got := reason(err); got != tt.want {
				t.Fatalf("Inspect err = %v, want refusal %s", err, tt.want)
			}
			var r *Refused
			if errors.As(err, &r) && len(r.Detail) > maxDetail+2*maxDetail {
				t.Errorf("Detail is unbounded: %d bytes", len(r.Detail))
			}
		})
	}
}

// dotGitCommit is a commit on HEAD whose tree adds <dir>/hooks/post-checkout (dir is ".git" at the
// root, or "sub/<dir>" otherwise), built with mktree since git refuses such a path in an index.
func dotGitCommit(t *testing.T, f *fixture, dir string) string {
	t.Helper()
	blob := git(t, f.work, "#!/bin/sh\nexit 1\n", "hash-object", "-w", "--stdin")
	hooks := git(t, f.work, "100755 blob "+blob+"\tpost-checkout\n", "mktree")
	dotgit := git(t, f.work, "040000 tree "+hooks+"\thooks\n", "mktree")
	inner := "040000 tree " + dotgit + "\t" + dir + "\n"
	if dir != ".git" {
		inner = "040000 tree " + git(t, f.work, inner, "mktree") + "\tsub\n"
	}
	root := git(t, f.work, "", "ls-tree", "HEAD") + "\n" + inner
	tree := git(t, f.work, root, "mktree")
	return git(t, f.work, "", "commit-tree", tree, "-p", "HEAD", "-m", "hooks")
}

func TestInspectNoHooksNoToken(t *testing.T) {
	const token = "ghs_testtoken123"
	f := newFixture(t)
	bin, log := fakeGit(t)
	f.cache.Git = bin
	f.cache.Token = func(context.Context, string) (string, error) { return token, nil }
	m := f.cache.Mirror("owner/repo")
	ctx := context.Background()

	old := f.remoteHead("refs/heads/agent/t-1")
	f.commit(map[string]string{"c.txt": "c\n"}, "two")
	good := f.bundle(old + "..agent/t-1")
	if _, err := m.Inspect(ctx, good, "agent/t-1", "main"); err != nil {
		t.Fatalf("Inspect: %v", err)
	}

	// Hooks the mirror would run, were hooks on.
	sentinel := filepath.Join(t.TempDir(), "hook-ran")
	hook := "#!/bin/sh\ntouch '" + sentinel + "'\n"
	for _, name := range []string{"reference-transaction", "pre-push", "post-checkout", "post-merge", "pre-auto-gc"} {
		if err := os.WriteFile(filepath.Join(m.dir, "hooks", name), []byte(hook), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(t, f.work, "", "update-ref", "refs/heads/agent/t-1", dotGitCommit(t, f, ".git"))
	if _, err := m.Inspect(ctx, f.bundle(old+"..agent/t-1"), "agent/t-1", "main"); reason(err) != hardening.RefusePushTree {
		t.Fatalf("Inspect of a .git/hooks path = %v, want push_tree", err)
	}
	in, err := m.Inspect(ctx, good, "agent/t-1", "main")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if err := m.Push(ctx, in); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("a hook ran")
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	header := authHeader(token)
	headerSeen := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, token) {
			t.Errorf("the raw token reached git: %q", line)
		}
		if !strings.Contains(line, header[len("Authorization: Basic "):]) {
			continue
		}
		if !strings.HasPrefix(line, "ENV GIT_CONFIG_VALUE_") {
			t.Errorf("the token's header outside GIT_CONFIG_VALUE_*: %q", line)
		}
		headerSeen = true
	}
	if !headerSeen {
		t.Error("the token never reached git through GIT_CONFIG_VALUE_*")
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "ENV HOME=") || strings.HasPrefix(line, "ENV GIT_CONFIG_GLOBAL=") && line != "ENV GIT_CONFIG_GLOBAL=/dev/null" {
			t.Errorf("git inherited %q", line)
		}
	}
}

func TestErrorLineRedacted(t *testing.T) {
	const token = "ghs_testtoken123"
	tests := []struct {
		name, stderr, want string
	}{
		{"the token", "fatal: auth\nremote: bad token ghs_testtoken123\n", "remote: bad token [redacted]"},
		{"the header", "x\n" + authHeader(token) + "\n", "[redacted]"},
		{"a control character", "fatal: \x1b[31mred\n", `fatal: \x1b[31mred`},
		{"a long line", strings.Repeat("y", 500), strings.Repeat("y", maxDetail)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastLine(redact(tt.stderr, token)); got != tt.want {
				t.Errorf("lastLine(redact) = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBundleFetchHTTPSOnly: in the factory only https is allowed; a bundle file must still fetch.
func TestBundleFetchHTTPSOnly(t *testing.T) {
	f := newFixture(t)
	old := f.remoteHead("refs/heads/agent/t-1")
	m := f.cache.Mirror("owner/repo")
	ctx := context.Background()
	f.commit(map[string]string{"c.txt": "c\n"}, "two")
	if _, err := m.Inspect(ctx, f.bundle(old+"..agent/t-1"), "agent/t-1", "main"); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	f.commit(map[string]string{"d.txt": "d\n"}, "three")
	p := filepath.Join(t.TempDir(), "b.bundle")
	if err := os.WriteFile(p, f.bundle(old+"..agent/t-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.cache.Protocols = []string{"https"}
	if _, err := m.run(ctx, "", []string{"GIT_NO_LAZY_FETCH=1"}, "bundle", "verify", "--quiet", p); err != nil {
		t.Fatalf("bundle verify with https only: %v", err)
	}
	if _, err := m.fetchBundle(ctx, "", p, "+refs/heads/agent/t-1:refs/maku/out/test"); err != nil {
		t.Fatalf("bundle fetch with https only: %v", err)
	}
	if _, err := m.run(ctx, "", nil, "fetch", "--quiet", "--", "file://"+f.remote, "main"); err == nil {
		t.Fatal("a file:// fetch passed the https-only allow-list")
	}
}

func TestPushLease(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*fixture, *Mirror, Inspection) {
		f := newFixture(t)
		old := f.remoteHead("refs/heads/agent/t-1")
		f.commit(map[string]string{"c.txt": "c\n"}, "two")
		m := f.cache.Mirror("owner/repo")
		in, err := m.Inspect(ctx, f.bundle(old+"..agent/t-1"), "agent/t-1", "main")
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		return f, m, in
	}

	t.Run("updates the ref", func(t *testing.T) {
		f, m, in := setup(t)
		if err := m.Push(ctx, in); err != nil {
			t.Fatalf("Push: %v", err)
		}
		if got := f.remoteHead(in.Ref); got != in.Head {
			t.Fatalf("remote = %s, want %s", got, in.Head)
		}
	})

	t.Run("creates an absent branch", func(t *testing.T) {
		f := newFixture(t)
		git(t, f.work, "", "checkout", "--quiet", "-b", "agent/t-2", "main")
		f.commit(map[string]string{"e.txt": "e\n"}, "new")
		m := f.cache.Mirror("owner/repo")
		in, err := m.Inspect(ctx, f.bundle("main..agent/t-2"), "agent/t-2", "main")
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		if err := m.Push(ctx, in); err != nil {
			t.Fatalf("Push: %v", err)
		}
		if got := f.remoteHead(in.Ref); got != in.Head {
			t.Fatalf("remote = %s, want %s", got, in.Head)
		}
	})

	t.Run("a remote rejecting the ref is ErrPushRejected", func(t *testing.T) {
		f := newFixture(t)
		git(t, f.work, "", "checkout", "--quiet", "-b", "agent/t-2", "main")
		f.commit(map[string]string{"e.txt": "e\n"}, "new")
		m := f.cache.Mirror("owner/repo")
		in, err := m.Inspect(ctx, f.bundle("main..agent/t-2"), "agent/t-2", "main")
		if err != nil {
			t.Fatalf("Inspect: %v", err)
		}
		// A ref hidden from receive-pack is refused as "[remote rejected]" (a pre-receive hook would
		// not run: the local receive-pack inherits core.hooksPath=/dev/null).
		git(t, f.remote, "", "config", "receive.hideRefs", "refs/heads/agent")
		err = m.Push(ctx, in)
		if !errors.Is(err, ErrPushRejected) || errors.Is(err, ErrLeaseLost) {
			t.Fatalf("Push = %v, want ErrPushRejected", err)
		}
		if got := f.remoteHead(in.Ref); got != "" {
			t.Fatalf("remote = %s, want the ref still absent", got)
		}
	})

	t.Run("a moved remote loses the lease", func(t *testing.T) {
		f, m, in := setup(t)
		git(t, f.work, "", "checkout", "--quiet", "-b", "elsewhere", in.OldHead)
		moved := f.commit(map[string]string{"x.txt": "x\n"}, "someone else")
		git(t, f.work, "", "push", "--quiet", "--force", f.remote, "elsewhere:agent/t-1")
		if err := m.Push(ctx, in); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("Push = %v, want ErrLeaseLost", err)
		}
		if got := f.remoteHead(in.Ref); got != moved {
			t.Fatalf("remote = %s, want it left at %s", got, moved)
		}
	})

	t.Run("a remote already at head pushes nothing", func(t *testing.T) {
		f, m, in := setup(t)
		git(t, f.work, "", "push", "--quiet", f.remote, "agent/t-1")
		bin, log := fakeGit(t)
		f.cache.Git = bin
		if err := m.Push(ctx, in); err != nil {
			t.Fatalf("Push = %v, want nil", err)
		}
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "ARG push\n") {
			t.Fatal("Push ran git push on a remote already at head")
		}
	})
}

func TestMirrorConcurrency(t *testing.T) {
	ctx := context.Background()

	t.Run("inspections serialise on the mirror lock", func(t *testing.T) {
		f := newFixture(t)
		old := f.remoteHead("refs/heads/agent/t-1")
		f.commit(map[string]string{"c.txt": "c\n"}, "two")
		bundle := f.bundle(old + "..agent/t-1")
		m := f.cache.Mirror("owner/repo")
		if f.cache.Mirror("OWNER/Repo") != m {
			t.Fatal("one repository has two mirrors")
		}

		m.lock.Lock()
		done := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				_, err := m.Inspect(ctx, bundle, "agent/t-1", "main")
				done <- err
			})
		}
		select {
		case err := <-done:
			t.Fatalf("Inspect ran while the mirror was locked: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
		m.lock.Unlock()
		wg.Wait()
		for range 2 {
			if err := <-done; err != nil {
				t.Fatalf("Inspect: %v", err)
			}
		}
		git(t, m.dir, "", "--git-dir", m.dir, "fsck", "--connectivity-only")
	})

	corrupt := []struct {
		name    string
		corrupt func(t *testing.T, dir string)
	}{{
		name: "garbage HEAD",
		corrupt: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("garbage\x00\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}, {
		name: "objects removed",
		corrupt: func(t *testing.T, dir string) {
			if err := os.RemoveAll(filepath.Join(dir, "objects")); err != nil {
				t.Fatal(err)
			}
		},
	}, {
		name: "packs removed",
		corrupt: func(t *testing.T, dir string) {
			packs, err := filepath.Glob(filepath.Join(dir, "objects", "pack", "*"))
			if err != nil || len(packs) == 0 {
				t.Fatalf("no packs to remove: %v", err)
			}
			for _, p := range packs {
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
			}
		},
	}, {
		name: "mirror replaced by a file",
		corrupt: func(t *testing.T, dir string) {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dir, []byte("not a repository"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}}
	for _, tt := range corrupt {
		t.Run("corrupt mirror: "+tt.name, func(t *testing.T) {
			f := newFixture(t)
			old := f.remoteHead("refs/heads/agent/t-1")
			f.commit(map[string]string{"c.txt": "c\n"}, "two")
			bundle := f.bundle(old + "..agent/t-1")
			m := f.cache.Mirror("owner/repo")
			if _, err := m.Inspect(ctx, bundle, "agent/t-1", "main"); err != nil {
				t.Fatalf("first Inspect: %v", err)
			}
			tt.corrupt(t, m.dir)
			in, err := m.Inspect(ctx, bundle, "agent/t-1", "main")
			if err != nil {
				t.Fatalf("Inspect on a corrupt mirror: %v", err)
			}
			if in.Kind != FastForward || len(in.Files) != 1 {
				t.Fatalf("Inspect = %+v", in)
			}
		})
	}
}
