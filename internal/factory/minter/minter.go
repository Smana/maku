// SPDX-License-Identifier: Apache-2.0

package minter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/hardening"
)

// AppClient is the App calls the minter makes: *forge.AppClient with an installation cache.
type AppClient interface {
	Installation(ctx context.Context, owner, name string) (int64, error)
	Evict(owner, name string)
	AccessToken(ctx context.Context, id int64, owner string, repos []string, perms map[string]string) (forge.Token, map[string]string, []string, error)
	Revoke(ctx context.Context, token string) error
	Granted(ctx context.Context, id int64) (map[string]string, error)
}

var _ AppClient = (*forge.AppClient)(nil)

// Metrics is what the minter counts: fmetrics.Set's RunTokenMint and RunTokenRevoke.
type Metrics interface {
	RunTokenMint(ctx context.Context, role, result string)
	RunTokenRevoke(ctx context.Context, result string)
}

// The mint results (plan PR6): a closed set, the metric's result label.
const (
	ResultOK                   = "ok"
	ResultNotInstalled         = "not_installed"
	ResultPermissionNotGranted = "permission_not_granted"
	ResultScopeMismatch        = "scope_mismatch"
	ResultError                = "error"
)

// Minter mints a run's or the publisher's token for one repository and role, from the agents App.
type Minter struct {
	Client  AppClient
	Table   Table        // Roles when nil
	Metrics Metrics      // nil counts nothing
	Log     *slog.Logger // nil logs nothing
}

func (m *Minter) table() Table {
	if m.Table == nil {
		return Roles
	}
	return m.Table
}

func (m *Minter) log() *slog.Logger {
	if m.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return m.Log
}

// Mint is a token on repo (owner/name) alone, with exactly the table's permissions for role under
// the enforced output types. Whatever fails, the error names a result and never the token.
func (m *Minter) Mint(ctx context.Context, repo, role string, enforced []hardening.OutputType) (forge.Token, error) {
	tok, err := m.mint(ctx, repo, role, enforced)
	result := Result(err)
	if m.Metrics != nil {
		m.Metrics.RunTokenMint(ctx, role, result)
	}
	if err == nil {
		return tok, nil
	}
	// Only sentinels are wrapped: errors.Is works, and no lower layer's text rides along.
	out := fmt.Errorf("minter: mint %s's token on %s: %w", role, repo, sentinel(result))
	if result == ResultScopeMismatch {
		// forge revoked the token it refused, or failed to: count and say which, truthfully.
		var re *forge.RevokeError
		switch {
		case errors.As(err, &re):
			m.countRevoke(ctx, re)
			m.log().Error("run token: GitHub granted another scope than asked, and revoking it failed: it lives until it expires",
				"repository", repo, "role", role, "status", re.Status)
			out = fmt.Errorf("%w; revoking it: %w", out, re)
		case errors.Is(err, forge.ErrRevokeFailed):
			m.countRevoke(ctx, forge.ErrRevokeFailed)
			m.log().Error("run token: GitHub granted another scope than asked, and revoking it failed: it lives until it expires",
				"repository", repo, "role", role)
			out = fmt.Errorf("%w; revoking it: %w", out, forge.ErrRevokeFailed)
		default:
			m.countRevoke(ctx, nil)
			m.log().Error("run token: GitHub granted another scope than asked; the token was revoked", "repository", repo, "role", role)
		}
	}
	return forge.Token{}, out
}

func (m *Minter) countRevoke(ctx context.Context, err error) {
	if m.Metrics == nil {
		return
	}
	if err != nil {
		m.Metrics.RunTokenRevoke(ctx, ResultError)
		return
	}
	m.Metrics.RunTokenRevoke(ctx, ResultOK)
}

// ErrMintFailed is a mint that failed for any reason outside the named results.
var ErrMintFailed = errors.New("the mint failed")

func sentinel(result string) error {
	switch result {
	case ResultNotInstalled:
		return forge.ErrNotInstalled
	case ResultPermissionNotGranted:
		return forge.ErrPermissionNotGranted
	case ResultScopeMismatch:
		return forge.ErrScopeMismatch
	}
	return ErrMintFailed
}

func (m *Minter) mint(ctx context.Context, repo, role string, enforced []hardening.OutputType) (forge.Token, error) {
	owner, name, err := split(repo)
	if err != nil {
		return forge.Token{}, err
	}
	perms, err := m.table().For(role, enforced)
	if err != nil {
		return forge.Token{}, err
	}
	// An App uninstalled and reinstalled has a new installation id, and the cached one answers 404:
	// evict it and try once more with a fresh lookup.
	for attempt := 0; ; attempt++ {
		var tok forge.Token
		id, err := m.Client.Installation(ctx, owner, name)
		if err == nil {
			if tok, _, _, err = m.Client.AccessToken(ctx, id, owner, []string{name}, perms); err == nil {
				return tok, nil
			}
		}
		if !errors.Is(err, forge.ErrNotInstalled) {
			return forge.Token{}, err
		}
		m.Client.Evict(owner, name)
		if attempt > 0 {
			return forge.Token{}, err
		}
	}
}

// split is repo's owner and name, each a valid path segment.
func split(repo string) (string, string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return "", "", fmt.Errorf("%q is not owner/name", repo)
	}
	if err := forge.CheckNames(owner, name); err != nil {
		return "", "", err
	}
	return owner, name, nil
}

// Result is a mint error's result label.
func Result(err error) string {
	switch {
	case err == nil:
		return ResultOK
	case errors.Is(err, forge.ErrNotInstalled):
		return ResultNotInstalled
	case errors.Is(err, forge.ErrPermissionNotGranted):
		return ResultPermissionNotGranted
	case errors.Is(err, forge.ErrScopeMismatch):
		return ResultScopeMismatch
	}
	return ResultError
}

// Revoke revokes token, counting ok or error, and returns the outcome: nil, or an error that
// errors.Is forge.ErrRevokeFailed and carries the HTTP status when there was one. It never carries
// the token.
func (m *Minter) Revoke(ctx context.Context, token string) error {
	err := m.Client.Revoke(ctx, token)
	m.countRevoke(ctx, err)
	if err == nil {
		return nil
	}
	var re *forge.RevokeError
	if errors.As(err, &re) {
		return fmt.Errorf("minter: %w", re) // a status alone: safe to pass on
	}
	return fmt.Errorf("minter: %w", forge.ErrRevokeFailed)
}

// CheckGranted is, per repository (owner/name), the permissions the table may ask that the
// repository's installation does not grant: empty when it grants them all. A repository whose
// installation or grants cannot be read is left out, and its error joined.
func (m *Minter) CheckGranted(ctx context.Context, repos []string) (map[string]Permissions, error) {
	need := Union(m.table())
	out := map[string]Permissions{}
	var errs []error
	for _, repo := range repos {
		owner, name, err := split(repo)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		id, err := m.Client.Installation(ctx, owner, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", repo, err))
			continue
		}
		granted, err := m.Client.Granted(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", repo, err))
			continue
		}
		missing := Permissions{}
		for k, v := range need {
			if g := granted[k]; g == "" || (v == "write" && g != "write" && g != "admin") { // admin covers write
				missing[k] = v
			}
		}
		out[repo] = missing
	}
	return out, errors.Join(errs...)
}

// Union is every permission t may ask, over every role and every enforced combination: the App's
// required set. A permission asked write anywhere is write.
func Union(t Table) Permissions {
	out := Permissions{}
	add := func(p Permissions) {
		for k, v := range p {
			if out[k] != "write" {
				out[k] = v
			}
		}
	}
	for _, r := range t {
		add(r.Base)
		for _, c := range r.Changes {
			add(Permissions{c.Permission: c.To})
		}
	}
	return out
}
