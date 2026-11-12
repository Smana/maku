// SPDX-License-Identifier: Apache-2.0

package applier

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Smana/maku/internal/factory/forge"
)

// PublisherIdentity is the octo-sts trust policy the publisher's token is minted under:
// `.github/chainguard/publisher.sts.yaml` in each enforced repository (O5, HR11).
const PublisherIdentity = "publisher"

// PublisherTokens is where the publisher's token for one repository comes from. OctoSTSTokens is
// H2's adapter; the factory's own per-run minter (v4 D10, HR12) is meant to be a second one, minting
// from the agents App key. The applier depends on this interface only.
type PublisherTokens interface {
	For(repo string) (forge.TokenSource, error)
}

// OctoSTSTokens mints the publisher's token at octo-sts, one cached forge.OctoSTS per repository.
type OctoSTSTokens struct {
	URL       string // octo-sts's base URL
	TokenFile string // the factory's projected ServiceAccount token, its audience octo-sts's
	Identity  string // PublisherIdentity when empty
	HTTP      *http.Client
	Now       func() time.Time
	Log       *slog.Logger

	mu  sync.Mutex
	per map[string]*forge.OctoSTS
}

// For is repo's token source; GitHub's names are case-insensitive, so one source serves every
// spelling.
func (o *OctoSTSTokens) For(repo string) (forge.TokenSource, error) {
	if o.URL == "" || o.TokenFile == "" || o.HTTP == nil || o.Now == nil {
		return nil, errors.New("publisher: octo-sts needs a URL, a token file, an HTTP client and a clock")
	}
	id := o.Identity
	if id == "" {
		id = PublisherIdentity
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.per == nil {
		o.per = map[string]*forge.OctoSTS{}
	}
	k := strings.ToLower(repo)
	if s, ok := o.per[k]; ok {
		return s, nil
	}
	s := &forge.OctoSTS{URL: o.URL, TokenFile: o.TokenFile, Scope: repo, Identity: id, HTTP: o.HTTP, Now: o.Now, Log: o.Log}
	o.per[k] = s
	return s, nil
}

// ConnectPublishers is the applier's Publisher: one forge connection per repository, as base
// with the repository set, its token from tokens. A connection that fails is not kept, so the next
// request retries it.
func ConnectPublishers(tokens PublisherTokens, base forge.Options) func(ctx context.Context, repo string) (Publisher, error) {
	var mu sync.Mutex
	conns := map[string]*forge.GitHub{}
	return func(ctx context.Context, repo string) (Publisher, error) {
		k := strings.ToLower(repo)
		mu.Lock()
		defer mu.Unlock()
		if g, ok := conns[k]; ok {
			return g, nil
		}
		src, err := tokens.For(repo)
		if err != nil {
			return nil, err
		}
		o := base
		o.Repository = repo
		g, err := forge.ConnectTokens(ctx, o, src)
		if err != nil {
			return nil, err
		}
		conns[k] = g
		return g, nil
	}
}
