// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Smana/maku/internal/httpx"
)

// TokenSource is where a connection's GitHub token comes from at each call: the App key's
// installation tokens (Connect), or an exchange such as OctoSTS (ConnectTokens). A source caches
// as it sees fit and returns a token with time left to live.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Bounds on an octo-sts exchange. octo-sts answers {"token"} with no expiry, so a reply without
// expires_at is held to an installation token's hour from the exchange, as the harness's
// credential helper holds it (images/maku-harness/git_credential_agent.py).
const (
	stsLifetime      = time.Hour
	stsRefreshBefore = 10 * time.Minute
	maxSTSReply      = 64 << 10
	maxProjected     = 16 << 10
)

// OctoSTS is a TokenSource that trades the factory's projected ServiceAccount token at octo-sts
// for an installation token of Identity's trust policy on Scope (hardening UH5, HR11: the
// publisher, `.github/chainguard/publisher.sts.yaml`). The projected token is read at each exchange,
// so the kubelet's rotation needs no restart; the answer is cached until ten minutes before it
// expires. Neither token ever reaches an error or a log.
type OctoSTS struct {
	URL       string // octo-sts's base URL, https only
	TokenFile string // the projected ServiceAccount token, its audience octo-sts's
	Scope     string // owner/repo
	Identity  string // the trust policy's name, e.g. publisher
	HTTP      *http.Client
	Now       func() time.Time
	Log       *slog.Logger

	mu      sync.Mutex
	token   string
	expires time.Time
}

// Token returns the cached token while it has more than ten minutes to live, else exchanges.
func (o *OctoSTS) Token(ctx context.Context) (string, error) {
	base, err := url.Parse(o.URL)
	switch {
	case err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil:
		return "", errors.New("octo-sts: the URL must be https://<host>/, without credentials")
	case !repoRE.MatchString(o.Scope) || o.Identity == "":
		return "", errors.New("octo-sts: a scope is owner/repo and an identity is named")
	case o.HTTP == nil || o.HTTP.Transport == nil || o.Now == nil:
		return "", errors.New("octo-sts: an HTTP client from httpx.New and a clock are required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.token != "" && o.Now().Before(o.expires.Add(-stsRefreshBefore)) {
		return o.token, nil
	}
	tok, exp, err := o.exchange(ctx, base)
	if err != nil {
		if o.Log != nil {
			o.Log.Warn("octo-sts exchange failed", "scope", o.Scope, "identity", o.Identity, "err", err)
		}
		return "", err
	}
	o.token, o.expires = tok, exp
	return tok, nil
}

func (o *OctoSTS) exchange(ctx context.Context, base *url.URL) (string, time.Time, error) {
	raw, err := os.ReadFile(filepath.Clean(o.TokenFile))
	if err != nil {
		return "", time.Time{}, errors.New("octo-sts: the projected token is unreadable")
	}
	projected := strings.TrimSpace(string(raw))
	if projected == "" || len(projected) > maxProjected {
		return "", time.Time{}, errors.New("octo-sts: the projected token is empty or oversized")
	}
	u := base.JoinPath("sts", "exchange")
	u.RawQuery = url.Values{"scope": {o.Scope}, "identity": {o.Identity}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("octo-sts: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+projected)
	req.Header.Set("Accept", "application/json")
	at := o.Now()
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return "", time.Time{}, errors.New("octo-sts: the exchange did not answer") // a *url.Error quotes the URL only, but stay terse
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := httpx.ReadBody(resp.Body, maxSTSReply)
	if err != nil {
		return "", time.Time{}, errors.New("octo-sts: the reply is unreadable")
	}
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("octo-sts: status %d", resp.StatusCode) // never the body: it may quote a token
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if json.Unmarshal(body, &out) != nil || out.Token == "" {
		return "", time.Time{}, errors.New("octo-sts: the reply has no token")
	}
	exp := out.ExpiresAt
	if exp.IsZero() {
		exp = at.Add(stsLifetime)
	}
	if !exp.After(o.Now()) {
		return "", time.Time{}, errors.New("octo-sts: the reply's token has expired")
	}
	return out.Token, exp, nil
}
