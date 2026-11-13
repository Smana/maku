// SPDX-License-Identifier: Apache-2.0

package gitbundle

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/hardening"
)

// fixture is a bare "GitHub" remote over file:// and a work clone that builds bundles. The remote
// has main (a.txt, gone.txt) and agent/t-1 (main plus b.txt).
type fixture struct {
	t      *testing.T
	remote string
	work   string
	cache  *Cache
}

// gitEnv isolates fixture git from the developer's configuration, without touching HOME.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com",
		"GIT_TERMINAL_PROMPT=0")
}

// git runs git in dir and returns its trimmed stdout; a failure fails the test.
func git(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, remote: filepath.Join(root, "remote.git"), work: filepath.Join(root, "work")}
	git(t, root, "", "init", "--quiet", "--bare", "-b", "main", f.remote)
	git(t, f.remote, "", "config", "uploadpack.allowFilter", "true")
	git(t, f.remote, "", "config", "uploadpack.allowAnySHA1InWant", "true")
	git(t, root, "", "init", "--quiet", "-b", "main", f.work)
	f.commit(map[string]string{"a.txt": "1\n", "gone.txt": "x\n"}, "init")
	git(t, f.work, "", "push", "--quiet", f.remote, "main")
	git(t, f.work, "", "checkout", "--quiet", "-b", "agent/t-1")
	f.commit(map[string]string{"b.txt": "b1\n"}, "feat: one")
	git(t, f.work, "", "push", "--quiet", f.remote, "agent/t-1")
	f.cache = &Cache{
		Root:      filepath.Join(root, "cache"),
		Remote:    func(string) string { return "file://" + f.remote },
		Protocols: []string{"file"},
	}
	return f
}

// commit writes files (an empty content removes the file) and commits them in the work clone.
func (f *fixture) commit(files map[string]string, msg string) string {
	f.t.Helper()
	for name, content := range files {
		p := filepath.Join(f.work, name)
		if content == "" {
			if err := os.Remove(p); err != nil {
				f.t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	git(f.t, f.work, "", "add", "-A")
	git(f.t, f.work, "", "commit", "--quiet", "--allow-empty", "-m", msg)
	return git(f.t, f.work, "", "rev-parse", "HEAD")
}

// remoteHead is ref's SHA on the remote, "" when absent.
func (f *fixture) remoteHead(ref string) string {
	f.t.Helper()
	out := git(f.t, f.remote, "", "for-each-ref", "--format=%(objectname)", ref)
	return out
}

// bundle builds a bundle of revs in the work clone.
func (f *fixture) bundle(revs ...string) []byte {
	f.t.Helper()
	p := filepath.Join(f.t.TempDir(), "out.bundle")
	git(f.t, f.work, "", append([]string{"bundle", "create", "--quiet", p}, revs...)...)
	b, err := os.ReadFile(p)
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

// reason is err's refusal, "" when it is not one.
func reason(err error) hardening.Refusal {
	var r *Refused
	if errors.As(err, &r) {
		return r.Reason
	}
	return ""
}

// fakeGit writes a git that records its argv and env to log, then runs the real git.
func fakeGit(t *testing.T) (bin, log string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin, log = filepath.Join(dir, "git"), filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"{ echo CALL; for a in \"$@\"; do printf 'ARG %s\\n' \"$a\"; done; env | while IFS= read -r l; do printf 'ENV %s\\n' \"$l\"; done; } >> '" + log + "'\n" +
		"exec '" + gitPath + "' \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}
