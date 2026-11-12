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
	AccessToken(ctx context.Context, id int64, repos []string, perms map[string]string) (forge.Token, map[string]string, []string, error)
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
	if err != nil {
		if result == ResultScopeMismatch {
			m.log().Error("run token: GitHub granted another scope than asked; the token was revoked", "repository", repo, "role", role)
		}
		// Only the result's sentinel is wrapped: errors.Is works, and no lower layer's text rides along.
		return forge.Token{}, fmt.Errorf("minter: mint %s's token on %s: %w", role, repo, sentinel(result))
	}
	return tok, nil
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
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return forge.Token{}, fmt.Errorf("%q is not owner/name", repo)
	}
	perms, err := m.table().For(role, enforced)
	if err != nil {
		return forge.Token{}, err
	}
	id, err := m.Client.Installation(ctx, owner, name)
	if err == nil {
		var tok forge.Token
		if tok, _, _, err = m.Client.AccessToken(ctx, id, []string{name}, perms); err == nil {
			return tok, nil
		}
	}
	if errors.Is(err, forge.ErrNotInstalled) {
		m.Client.Evict(owner, name) // the installation may be gone: look it up again next time
	}
	return forge.Token{}, err
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

// Revoke revokes token, best effort, counting ok or error. Its error never carries the token.
func (m *Minter) Revoke(ctx context.Context, token string) error {
	err := m.Client.Revoke(ctx, token)
	result := ResultOK
	if err != nil {
		result = ResultError
	}
	if m.Metrics != nil {
		m.Metrics.RunTokenRevoke(ctx, result)
	}
	if err != nil {
		return errors.New("minter: revoke an installation token failed")
	}
	return nil
}

// CheckGranted is, per repository (owner/name), the permissions the table may ask that the
// repository's installation does not grant: empty when it grants them all. A repository whose
// installation or grants cannot be read is left out, and its error joined.
func (m *Minter) CheckGranted(ctx context.Context, repos []string) (map[string]Permissions, error) {
	need := Union(m.table())
	out := map[string]Permissions{}
	var errs []error
	for _, repo := range repos {
		owner, name, ok := strings.Cut(repo, "/")
		if !ok {
			errs = append(errs, fmt.Errorf("%q is not owner/name", repo))
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
			if g := granted[k]; g == "" || (v == "write" && g != "write") {
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
