// SPDX-License-Identifier: Apache-2.0

package fence

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	for name, tc := range map[string]struct {
		in, want string
		ok       bool
	}{
		"one factory":               {"factory: aws-0\n", "aws-0", true},
		"a comment":                 {"# owner: platform\nfactory: gcp-0\n", "gcp-0", true},
		"empty":                     {"", "", false},
		"no factory key":            {"owner: aws-0\n", "", false},
		"an unknown key":            {"factory: aws-0\nalso: gcp-0\n", "", false},
		"a list":                    {"factory: [aws-0, gcp-0]\n", "", false},
		"not a name":                {"factory: AWS 0\n", "", false},
		"an empty name":             {"factory: \"\"\n", "", false},
		"not yaml":                  {"factory: [\n", "", false},
		"classes beside factory":    {"factory: aws-0\nclasses: {docs-links: {mode: live}}\n", "aws-0", true},
		"invalid classes still own": {"factory: aws-0\nclasses: [not, a, map]\n", "aws-0", true},
		"classes without factory":   {"classes: {}\n", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Parse([]byte(tc.in))
			if (err == nil) != tc.ok || got != tc.want {
				t.Fatalf("%q %v", got, err)
			}
		})
	}
}

// file is one repository's fence file as GitHub would answer it.
type file struct {
	body  string
	found bool
	err   error
}

type files struct {
	mu sync.Mutex
	by map[string]file
}

func (f *files) set(repo string, v file) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.by[repo] = v
}

func (f *files) read(_ context.Context, repo string) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.by[repo]
	if !ok {
		return nil, false, errors.New("no connection")
	}
	return []byte(v.body), v.found, v.err
}

// D7: a repository is this factory's only when its default branch names this factory. Absent,
// unreadable or naming another, it is not; a read that fails keeps the last answer until it is
// Stale, then it is not either.
func TestOwnership(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	fs := &files{by: map[string]file{
		"Smana/mine":    {"factory: aws-0\n", true, nil},
		"Smana/theirs":  {"factory: gcp-0\n", true, nil},
		"Smana/absent":  {"", false, nil},
		"Smana/garbled": {"factory: [\n", true, nil},
	}}
	var recorded sync.Map
	f := &Fence{Name: "aws-0", Read: fs.read, Repositories: listed("Smana/mine", "Smana/theirs", "Smana/absent", "Smana/garbled", "Smana/unwired"),
		Stale: 15 * time.Minute, Now: func() time.Time { return now },
		OnState: func(_ context.Context, repo string, owned bool) { recorded.Store(repo, owned) }}
	if f.Owned("Smana/mine") {
		t.Fatal("owned before the first read")
	}
	_ = f.Refresh(t.Context())
	for repo, want := range map[string]bool{"Smana/mine": true, "smana/MINE": true, "Smana/theirs": false, "Smana/absent": false,
		"Smana/garbled": false, "Smana/unwired": false, "Smana/unlisted": false} {
		if got := f.Owned(repo); got != want {
			t.Errorf("%s: owned %v, want %v", repo, got, want)
		}
	}
	if v, _ := recorded.Load("Smana/mine"); v != true {
		t.Fatal("the gauge hears the state")
	}
	if v, _ := recorded.Load("Smana/theirs"); v != false {
		t.Fatal("the gauge hears an unowned repository")
	}
	fs.set("Smana/mine", file{err: errors.New("github down")})
	now = now.Add(10 * time.Minute)
	_ = f.Refresh(t.Context())
	if !f.Owned("Smana/mine") {
		t.Fatal("a failed read keeps a fresh answer")
	}
	now = now.Add(6 * time.Minute)
	if f.Owned("Smana/mine") {
		t.Fatal("past Stale with no fresh read, the repository is not owned")
	}
	fs.set("Smana/mine", file{"factory: gcp-0\n", true, nil})
	_ = f.Refresh(t.Context())
	if f.Owned("Smana/mine") {
		t.Fatal("the fence moved to another factory")
	}
}

// The fence decides ownership alone (MP13): a class policy it cannot read is the observer's
// problem, and it reaches the observer only from a repository this factory owns.
func TestRefreshHandsTheOwnedFileToOnFile(t *testing.T) {
	fs := &files{by: map[string]file{
		"o/mine":   {body: "factory: aws-0\nclasses: {}\n", found: true},
		"o/theirs": {body: "factory: gcp-0\n", found: true},
		"o/broken": {err: errors.New("github down")},
	}}
	var got []string
	f := &Fence{Name: "aws-0", Read: fs.read, Repositories: listed("o/mine", "o/theirs", "o/broken"),
		Stale: time.Hour, Now: time.Now,
		OnFile: func(_ context.Context, repo string, body []byte) { got = append(got, repo+"="+string(body)) }}
	if err := f.Refresh(t.Context()); err == nil {
		t.Fatal("the failed read is reported")
	}
	if want := []string{"o/mine=factory: aws-0\nclasses: {}\n"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A hardening block extends a repository's protected paths (hardening design, Protected paths) and
// never decides ownership: one that does not parse is hardening's to read closed, not the fence's.
func TestParseAcceptsHardening(t *testing.T) {
	for name, in := range map[string]string{
		"a hardening map":    "factory: gcp-0\nhardening:\n  protectedPaths: [{path: scripts/ci/**, on: blocked}]\n  minAuthorTrust: merged\n",
		"a hardening scalar": "factory: gcp-0\nhardening: 3\n",
		"beside classes":     "factory: gcp-0\nclasses: {}\nhardening: {}\n",
		"an unknown sub-key": "factory: gcp-0\nhardening: {slack: true}\n",
		"an empty hardening": "factory: gcp-0\nhardening:\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Parse([]byte(in))
			if err != nil || got != "gcp-0" {
				t.Fatalf("%q %v, want gcp-0", got, err)
			}
		})
	}
	if _, err := Parse([]byte("hardening: {}\n")); err == nil {
		t.Fatal("a hardening block without factory is not owned")
	}
}

func listed(names ...string) func() []string { return func() []string { return names } }

// Factory resources D12: the list is read per pass, so a repository a publish adds is read on the
// next pass, and one it removes is no longer owned, its state dropped.
func TestRefreshFollowsTheListedRepositories(t *testing.T) {
	fs := &files{by: map[string]file{
		"o/a": {body: "factory: aws-0\n", found: true},
		"o/b": {body: "factory: aws-0\n", found: true},
	}}
	names := []string{"o/a"}
	f := &Fence{Name: "aws-0", Read: fs.read, Repositories: func() []string { return names },
		Stale: time.Hour, Now: time.Now}
	_ = f.Refresh(t.Context())
	if !f.Owned("o/a") || f.Owned("o/b") {
		t.Fatalf("first pass: a %v, b %v", f.Owned("o/a"), f.Owned("o/b"))
	}
	names = []string{"o/b"}
	_ = f.Refresh(t.Context())
	if f.Owned("o/a") || !f.Owned("o/b") {
		t.Fatalf("after the publish: a %v, b %v", f.Owned("o/a"), f.Owned("o/b"))
	}
	f.mu.RLock()
	_, kept := f.state["o/a"]
	f.mu.RUnlock()
	if kept {
		t.Fatal("a removed repository's state lingers")
	}
}

// A Kick refreshes at once, between periods; kicks coalesce and never block.
func TestKickRefreshesBetweenPeriods(t *testing.T) {
	fs := &files{by: map[string]file{"o/a": {body: "factory: aws-0\n", found: true}}}
	var mu sync.Mutex
	names, passes := []string{"o/none"}, 0
	read := func(ctx context.Context, repo string) ([]byte, bool, error) {
		mu.Lock()
		passes++
		mu.Unlock()
		return fs.read(ctx, repo)
	}
	f := &Fence{Name: "aws-0", Read: read, Repositories: func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(names) },
		Every: time.Hour, Stale: time.Hour, Now: time.Now}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = f.Start(ctx) }()
	wait := func(what string, ok func() bool) {
		deadline := time.Now().Add(5 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	wait("no first pass", func() bool { mu.Lock(); defer mu.Unlock(); return passes > 0 })
	mu.Lock()
	names = []string{"o/a"}
	mu.Unlock()
	f.Kick()
	f.Kick()
	wait("a kick did not refresh before the hour", func() bool { return f.Owned("o/a") })
}
