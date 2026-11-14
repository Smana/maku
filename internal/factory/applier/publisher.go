// SPDX-License-Identifier: Apache-2.0

package applier

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/gitbundle"
)

// ConnectPushers is the applier's Pushers (H3, HR24): one gitbundle mirror per repository under
// root (MAKU_GIT_CACHE), git sending the publisher's token from the same tokens as the publisher's
// API connection, each mirror re-created past maxBytes (gitbundle.DefaultMaxBytes when 0). An
// empty root connects nothing.
func ConnectPushers(tokens PublisherTokens, root string, maxBytes int64) func(ctx context.Context, repo string) (Pusher, error) {
	cache := &gitbundle.Cache{Root: root, MaxBytes: maxBytes, Token: func(ctx context.Context, repo string) (string, error) {
		src, err := tokens.For(repo)
		if err != nil {
			return "", err
		}
		return src.Token(ctx)
	}}
	return func(_ context.Context, repo string) (Pusher, error) {
		if root == "" {
			return nil, errors.New("pusher: no git cache root (MAKU_GIT_CACHE)")
		}
		return cache.Mirror(repo), nil
	}
}

// PublisherIdentity is the publisher's name beside the sandbox roles: the row the permission gauge
// records the publisher's token under (H2, HR11).
const PublisherIdentity = "publisher"

// PublisherTokens is where the publisher's token for one repository comes from: the factory's
// minter, from the agents App key (v4 D10, HR12). The applier depends on this interface only.
type PublisherTokens interface {
	For(repo string) (forge.TokenSource, error)
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
