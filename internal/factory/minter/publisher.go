// SPDX-License-Identifier: Apache-2.0

package minter

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/hardening"
)

// publisherRefreshWithin is how long before expiry a publisher token is minted again (plan PR7):
// the applier's writes then never carry a token in its last minutes.
const publisherRefreshWithin = 10 * time.Minute

// tokenMinter is what PublisherTokens asks of the Minter.
type tokenMinter interface {
	Mint(ctx context.Context, repo, role string, enforced []hardening.OutputType) (forge.Token, error)
}

var _ tokenMinter = (*Minter)(nil)

// PublisherTokens is the second applier.PublisherTokens (plan PR7, HR12), beside octo-sts: the
// publisher's token minted from the agents App, with the role table's publisher row for the
// repository's enforced output types. The publisher writes as the agents App, so its login stays
// agentsLogin (HR11).
type PublisherTokens struct {
	Minter tokenMinter
	// Enforced is a repository's enforced output types, read at each mint (the live config).
	Enforced func(repo string) []hardening.OutputType
	Now      func() time.Time // time.Now when nil

	mu  sync.Mutex
	per map[string]*publisherSource
}

// For is repo's token source; GitHub's names are case-insensitive, so one source serves every
// spelling.
func (p *PublisherTokens) For(repo string) (forge.TokenSource, error) {
	if p.Minter == nil || p.Enforced == nil {
		return nil, errors.New("publisher: the minter needs an App and each repository's enforced outputs")
	}
	if _, _, err := split(repo); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.per == nil {
		p.per = map[string]*publisherSource{}
	}
	k := strings.ToLower(repo)
	if s, ok := p.per[k]; ok {
		return s, nil
	}
	s := &publisherSource{p: p, repo: repo}
	p.per[k] = s
	return s, nil
}

func (p *PublisherTokens) now() time.Time {
	if p.Now == nil {
		return time.Now()
	}
	return p.Now()
}

// publisherSource is one repository's publisher token, cached until ten minutes before it expires
// and only while the repository's enforced set is the one it was minted under: the publisher's
// permissions follow that set (push adds contents: write), so a changed set mints afresh.
type publisherSource struct {
	p    *PublisherTokens
	repo string

	mu       sync.Mutex
	tok      forge.Token
	enforced []hardening.OutputType // the set tok was minted under
}

// Token returns the cached token while it has more than ten minutes to live and the enforced set is
// unchanged, else mints. A failed mint returns the minter's error, which never carries a token.
func (s *publisherSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	enforced := slices.Clone(s.p.Enforced(s.repo))
	slices.Sort(enforced)
	enforced = slices.Compact(enforced)
	if s.tok.Value != "" && slices.Equal(enforced, s.enforced) && s.p.now().Before(s.tok.ExpiresAt.Add(-publisherRefreshWithin)) {
		return s.tok.Value, nil
	}
	tok, err := s.p.Minter.Mint(ctx, s.repo, Publisher, enforced)
	if err != nil {
		s.tok, s.enforced = forge.Token{}, nil
		return "", err
	}
	s.tok, s.enforced = tok, enforced
	return tok.Value, nil
}

// Invalidate drops the cached token: GitHub answered 401 to it, so the next call mints again.
func (s *publisherSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tok = forge.Token{}
}
