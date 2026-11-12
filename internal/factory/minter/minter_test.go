// SPDX-License-Identifier: Apache-2.0

package minter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	mintErr       error   // every mint's answer
	mintErrs      []error // the first mints' answers, before mintErr
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

func (f *fakeApp) AccessToken(_ context.Context, id int64, owner string, repos []string, perms map[string]string) (forge.Token, map[string]string, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mints = append(f.mints, mintCall{id: id, repos: repos, perms: perms})
	if len(f.mintErrs) > 0 {
		err := f.mintErrs[0]
		f.mintErrs = f.mintErrs[1:]
		if err != nil {
			return forge.Token{}, nil, nil, err
		}
	} else if f.mintErr != nil {
		return forge.Token{}, nil, nil, f.mintErr
	}
	full := make([]string, len(repos))
	for i, r := range repos {
		full[i] = owner + "/" + r
	}
	return forge.Token{Value: secret, ExpiresAt: time.Now().Add(time.Hour)}, perms, full, nil
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
		evicts int // a not-installed answer is evicted and retried once
	}{
		{"ok", "o/r", Implementer, nil, "ok", 0},
		{"not installed at lookup", "o/missing", Implementer, nil, "not_installed", 2},
		{"not installed at mint", "o/r", Implementer, fmt.Errorf("mint: %w", forge.ErrNotInstalled), "not_installed", 2},
		{"permission not granted", "o/r", Tester, fmt.Errorf("mint: %w", forge.ErrPermissionNotGranted), "permission_not_granted", 0},
		{"scope mismatch", "o/r", Reviewer, fmt.Errorf("mint: %w", forge.ErrScopeMismatch), "scope_mismatch", 0},
		{"any other failure", "o/r", Triager, errors.New("mint: status 500"), "error", 0},
		{"an unknown role", "o/r", "scoper", nil, "error", 0},
		{"not a repository", "nope", Implementer, nil, "error", 0},
		{"a name that climbs the path", "o/a..b", Implementer, nil, "error", 0},
		{"an owner that is not a name", "o w/r", Implementer, nil, "error", 0},
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
			if len(app.evicted) != c.evicts {
				t.Fatalf("evicted %v, want %d", app.evicted, c.evicts)
			}
			if c.repo != "o/r" && c.repo != "o/missing" && len(app.lookups) != 0 {
				t.Fatalf("a bad name reached the App: %v", app.lookups)
			}
		})
	}
}

// An App uninstalled and reinstalled has a new installation id: the stale one answers 404, the
// minter evicts it and retries the lookup and the mint once, in the same call.
func TestReinstallRetriesOnce(t *testing.T) {
	app := &fakeApp{installations: map[string]int64{"o/r": 9}, mintErrs: []error{fmt.Errorf("mint: %w", forge.ErrNotInstalled)}}
	met := &fakeMetrics{}
	tok, err := (&Minter{Client: app, Metrics: met}).Mint(t.Context(), "o/r", Implementer, nil)
	if err != nil || tok.Value != secret {
		t.Fatalf("err = %v", err)
	}
	if !slices.Equal(app.evicted, []string{"o/r"}) || len(app.lookups) != 2 || len(app.mints) != 2 {
		t.Fatalf("evicted %v lookups %v mints %d", app.evicted, app.lookups, len(app.mints))
	}
	if !slices.Equal(met.mints, []string{"implementer ok"}) {
		t.Fatalf("metrics %v: one mint, one count", met.mints)
	}
}

// A scope mismatch's revoke is counted and logged as it went.
func TestScopeMismatchRevokeIsReported(t *testing.T) {
	for _, c := range []struct {
		name   string
		err    error
		result string
		log    string
	}{
		{"revoked", fmt.Errorf("mint: %w; the token was revoked", forge.ErrScopeMismatch), "ok", "the token was revoked"},
		{"revoke failed", fmt.Errorf("mint: %w; revoking it: %w", forge.ErrScopeMismatch, &forge.RevokeError{Status: 500}), "error",
			"revoking it failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			app := &fakeApp{installations: map[string]int64{"o/r": 9}, mintErr: c.err}
			met := &fakeMetrics{}
			var buf strings.Builder
			m := &Minter{Client: app, Metrics: met, Log: slog.New(slog.NewTextHandler(&buf, nil))}
			if _, err := m.Mint(t.Context(), "o/r", Implementer, nil); !errors.Is(err, forge.ErrScopeMismatch) {
				t.Fatalf("err = %v", err)
			}
			if !slices.Equal(met.revokes, []string{c.result}) || !slices.Equal(met.mints, []string{"implementer scope_mismatch"}) {
				t.Fatalf("revokes %v mints %v", met.revokes, met.mints)
			}
			if !strings.Contains(buf.String(), c.log) || strings.Contains(buf.String(), secret) {
				t.Fatalf("log %q, want %q", buf.String(), c.log)
			}
		})
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
	if len(app.mints) != 2 || app.mints[0].id != 1 || app.mints[1].id != 2 || app.mints[1].repos[0] != "s" {
		t.Fatalf("mints %+v", app.mints)
	}
}

func TestRevoke(t *testing.T) {
	for _, c := range []struct {
		name   string
		err    error
		result string
		says   string // what the returned error says
	}{
		{"ok", nil, "ok", ""},
		{"a status is passed on", &forge.RevokeError{Status: 500}, "error", "status 500"},
		{"any other error is reduced to its sentinel", errors.New("revoke: " + secret), "error", "revoke an installation token failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			app := &fakeApp{revokeErr: c.err}
			met := &fakeMetrics{}
			err := (&Minter{Client: app, Metrics: met}).Revoke(t.Context(), secret)
			if (err == nil) != (c.err == nil) {
				t.Fatalf("err = %v", err)
			}
			if err != nil && (strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), c.says) || !errors.Is(err, forge.ErrRevokeFailed)) {
				t.Fatalf("err = %v, want ErrRevokeFailed saying %q and never the token", err, c.says)
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
	admin := maps.Clone(full)
	admin["contents"] = "admin"
	app := &fakeApp{installations: map[string]int64{"o/full": 1, "o/lacking": 2, "o/admin": 3},
		granted: map[int64]map[string]string{1: full, 2: lacking, 3: admin}}
	got, err := (&Minter{Client: app}).CheckGranted(t.Context(), []string{"o/full", "o/lacking", "o/admin"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["o/full"]) != 0 {
		t.Fatalf("a complete installation lacks %v", got["o/full"])
	}
	if len(got["o/admin"]) != 0 {
		t.Fatalf("admin covers write: lacks %v", got["o/admin"])
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
