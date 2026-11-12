// SPDX-License-Identifier: Apache-2.0

package minter

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/fmetrics"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/hardening"
)

const secret = "ghs_SECRETVALUE"

var _ Metrics = (*fmetrics.Set)(nil)

// fakeApp is the App calls, by repository: installations[owner/name] is the id, mintErr the
// mint's answer, granted each installation's permissions.
type fakeApp struct {
	mu            sync.Mutex
	installations map[string]int64
	mintErr       error
	revokeErr     error
	granted       map[int64]map[string]string

	lookups []string
	evicted []string
	mints   []mintCall
	revoked []string
}

type mintCall struct {
	id    int64
	repos []string
	perms map[string]string
}

func (f *fakeApp) Installation(_ context.Context, owner, name string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups = append(f.lookups, owner+"/"+name)
	id, ok := f.installations[owner+"/"+name]
	if !ok {
		return 0, fmt.Errorf("find: %w", forge.ErrNotInstalled)
	}
	return id, nil
}

func (f *fakeApp) Evict(owner, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evicted = append(f.evicted, owner+"/"+name)
}

func (f *fakeApp) AccessToken(_ context.Context, id int64, repos []string, perms map[string]string) (forge.Token, map[string]string, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mints = append(f.mints, mintCall{id: id, repos: repos, perms: perms})
	if f.mintErr != nil {
		return forge.Token{}, nil, nil, f.mintErr
	}
	return forge.Token{Value: secret, ExpiresAt: time.Now().Add(time.Hour)}, perms, repos, nil
}

func (f *fakeApp) Revoke(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, token)
	return f.revokeErr
}

func (f *fakeApp) Granted(_ context.Context, id int64) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.granted[id]
	if !ok {
		return nil, errors.New("read the installation's permissions: status 404")
	}
	return g, nil
}

type fakeMetrics struct {
	mu      sync.Mutex
	mints   []string
	revokes []string
}

func (m *fakeMetrics) RunTokenMint(_ context.Context, role, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mints = append(m.mints, role+" "+result)
}

func (m *fakeMetrics) RunTokenRevoke(_ context.Context, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revokes = append(m.revokes, result)
}

func TestMintAsksTheTablesPermissions(t *testing.T) {
	app := &fakeApp{installations: map[string]int64{"o/r": 9}}
	m := &Minter{Client: app, Metrics: &fakeMetrics{}}
	enforced := []hardening.OutputType{hardening.PR}
	tok, err := m.Mint(t.Context(), "o/r", Implementer, enforced)
	if err != nil || tok.Value != secret {
		t.Fatalf("mint: %v", err)
	}
	want, _ := Roles.For(Implementer, enforced)
	if len(app.mints) != 1 || app.mints[0].id != 9 || !slices.Equal(app.mints[0].repos, []string{"r"}) || !maps.Equal(app.mints[0].perms, want) {
		t.Fatalf("mints %+v, want %v on [r]", app.mints, want)
	}
	if !slices.Equal(app.lookups, []string{"o/r"}) {
		t.Fatalf("lookups %v", app.lookups)
	}
}

func TestMintUsesItsTable(t *testing.T) {
	app := &fakeApp{installations: map[string]int64{"o/r": 9}}
	m := &Minter{Client: app, Table: TableD10}
	if _, err := m.Mint(t.Context(), "o/r", Reviewer, nil); err != nil {
		t.Fatal(err)
	}
	if app.mints[0].perms["pull_requests"] != "write" {
		t.Fatalf("perms %v: the D10 reviewer", app.mints[0].perms)
	}
}

func TestMintResults(t *testing.T) {
	cases := []struct {
		name   string
		repo   string
		role   string
		err    error
		result string
		evicts bool
	}{
		{"ok", "o/r", Implementer, nil, "ok", false},
		{"not installed at lookup", "o/missing", Implementer, nil, "not_installed", true},
		{"not installed at mint", "o/r", Implementer, fmt.Errorf("mint: %w", forge.ErrNotInstalled), "not_installed", true},
		{"permission not granted", "o/r", Tester, fmt.Errorf("mint: %w", forge.ErrPermissionNotGranted), "permission_not_granted", false},
		{"scope mismatch", "o/r", Reviewer, fmt.Errorf("mint: %w", forge.ErrScopeMismatch), "scope_mismatch", false},
		{"any other failure", "o/r", Triager, errors.New("mint: status 500"), "error", false},
		{"an unknown role", "o/r", "scoper", nil, "error", false},
		{"not a repository", "nope", Implementer, nil, "error", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := &fakeApp{installations: map[string]int64{"o/r": 9}, mintErr: c.err}
			met := &fakeMetrics{}
			_, err := (&Minter{Client: app, Metrics: met}).Mint(t.Context(), c.repo, c.role, nil)
			if (err == nil) != (c.result == "ok") {
				t.Fatalf("err = %v", err)
			}
			if !slices.Equal(met.mints, []string{c.role + " " + c.result}) {
				t.Fatalf("metrics %v, want %s %s", met.mints, c.role, c.result)
			}
			if got := len(app.evicted) == 1; got != c.evicts {
				t.Fatalf("evicted %v", app.evicted)
			}
		})
	}
}

// An eviction makes the next mint look the installation up again: the real cache, through forge.
func TestNotInstalledEvictsTheInstallation(t *testing.T) {
	app := &fakeApp{installations: map[string]int64{"o/r": 9}, mintErr: forge.ErrNotInstalled}
	m := &Minter{Client: app}
	if _, err := m.Mint(t.Context(), "o/r", Implementer, nil); !errors.Is(err, forge.ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
	if !slices.Equal(app.evicted, []string{"o/r"}) {
		t.Fatalf("evicted %v", app.evicted)
	}
	app.mintErr = nil
	if _, err := m.Mint(t.Context(), "o/r", Implementer, nil); err != nil {
		t.Fatal(err)
	}
	if len(app.lookups) != 2 {
		t.Fatalf("lookups %v", app.lookups)
	}
}

func TestTwoOwnersMintThroughTwoInstallations(t *testing.T) {
	app := &fakeApp{installations: map[string]int64{"a/r": 1, "b/s": 2}}
	m := &Minter{Client: app}
	for _, repo := range []string{"a/r", "b/s"} {
		if _, err := m.Mint(t.Context(), repo, Implementer, nil); err != nil {
			t.Fatal(err)
		}
	}
	if app.mints[0].id != 1 || app.mints[1].id != 2 || app.mints[1].repos[0] != "s" {
		t.Fatalf("mints %+v", app.mints)
	}
}

func TestRevoke(t *testing.T) {
	for _, c := range []struct {
		name   string
		err    error
		result string
	}{{"ok", nil, "ok"}, {"error", errors.New("revoke: status 500 " + secret), "error"}} {
		t.Run(c.name, func(t *testing.T) {
			app := &fakeApp{revokeErr: c.err}
			met := &fakeMetrics{}
			err := (&Minter{Client: app, Metrics: met}).Revoke(t.Context(), secret)
			if (err == nil) != (c.err == nil) {
				t.Fatalf("err = %v", err)
			}
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Fatalf("the error quotes the token: %v", err)
			}
			if !slices.Equal(app.revoked, []string{secret}) || !slices.Equal(met.revokes, []string{c.result}) {
				t.Fatalf("revoked %v metrics %v", app.revoked, met.revokes)
			}
		})
	}
}

// No mint error carries the token, even if a lower layer's did.
func TestMintErrorsNeverQuoteTheToken(t *testing.T) {
	app := &fakeApp{installations: map[string]int64{"o/r": 9}, mintErr: errors.New("mint: " + secret)}
	_, err := (&Minter{Client: app}).Mint(t.Context(), "o/r", Implementer, nil)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckGranted(t *testing.T) {
	full := map[string]string{"contents": "write", "pull_requests": "write", "issues": "write", "checks": "read",
		"actions": "read", "metadata": "read"}
	lacking := maps.Clone(full)
	lacking["issues"] = "read"
	delete(lacking, "actions")
	app := &fakeApp{installations: map[string]int64{"o/full": 1, "o/lacking": 2}, granted: map[int64]map[string]string{1: full, 2: lacking}}
	got, err := (&Minter{Client: app}).CheckGranted(t.Context(), []string{"o/full", "o/lacking"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["o/full"]) != 0 {
		t.Fatalf("a complete installation lacks %v", got["o/full"])
	}
	if !maps.Equal(got["o/lacking"], Permissions{"issues": "write", "actions": "read"}) {
		t.Fatalf("lacking %v", got["o/lacking"])
	}
	if _, err := (&Minter{Client: app}).CheckGranted(t.Context(), []string{"o/absent"}); !errors.Is(err, forge.ErrNotInstalled) {
		t.Fatalf("an uninstalled repository: %v", err)
	}
}

func TestUnion(t *testing.T) {
	want := Permissions{"contents": "write", "pull_requests": "write", "issues": "write", "checks": "read",
		"actions": "read", "metadata": "read"}
	if got := Union(TableHR12); !maps.Equal(got, want) {
		t.Fatalf("Union(HR12) = %v", got)
	}
}
