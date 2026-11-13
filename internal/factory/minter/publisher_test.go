// SPDX-License-Identifier: Apache-2.0

package minter

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/applier"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/hardening"
)

var _ applier.PublisherTokens = (*PublisherTokens)(nil)

// clock is a settable time for the publisher's refresh.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newPublisher(app *fakeApp, enforced []hardening.OutputType) (*PublisherTokens, *clock) {
	c := &clock{t: time.Now()}
	return &PublisherTokens{Minter: &Minter{Client: app},
		Enforced: func(string) []hardening.OutputType { return enforced }, Now: c.now}, c
}

// One source per repository, whatever its spelling: GitHub's names are case-insensitive.
func TestPublisherForCachesPerRepository(t *testing.T) {
	p, _ := newPublisher(&fakeApp{installations: map[string]int64{"o/r": 9}}, nil)
	a, err := p.For("o/r")
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.For("O/R")
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.For("o/other")
	if err != nil {
		t.Fatal(err)
	}
	if a != b || a == c {
		t.Fatal("one source per lowercased repository")
	}
	if _, err := p.For("not-a-repo"); err == nil {
		t.Fatal("a repository that is not owner/name has no source")
	}
}

// Token mints the publisher's row for the repository's enforced types, holds it until ten
// minutes before it expires, and mints again after an Invalidate.
func TestPublisherToken(t *testing.T) {
	for name, enforced := range map[string][]hardening.OutputType{
		"pr":   {hardening.PR},
		"push": {hardening.PR, hardening.Push},
	} {
		t.Run(name, func(t *testing.T) {
			app := &fakeApp{installations: map[string]int64{"o/r": 9}}
			p, clk := newPublisher(app, enforced)
			src, err := p.For("o/r")
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if tok, err := src.Token(t.Context()); err != nil || tok != secret {
					t.Fatalf("token: %v", err)
				}
			}
			want, _ := Roles.For(Publisher, enforced)
			if len(app.mints) != 1 || !slices.Equal(app.mints[0].repos, []string{"r"}) || !maps.Equal(app.mints[0].perms, want) {
				t.Fatalf("mints %+v, want one of %v on [r]", app.mints, want)
			}
			clk.add(49 * time.Minute) // 11 minutes left: still held
			if _, err := src.Token(t.Context()); err != nil || len(app.mints) != 1 {
				t.Fatalf("a token with 11 minutes left was re-minted: %d mints, %v", len(app.mints), err)
			}
			clk.add(2 * time.Minute) // 9 minutes left
			if _, err := src.Token(t.Context()); err != nil || len(app.mints) != 2 {
				t.Fatalf("a token with 9 minutes left was kept: %d mints, %v", len(app.mints), err)
			}
			inv, ok := src.(interface{ Invalidate() })
			if !ok {
				t.Fatal("the source cannot drop a refused token")
			}
			inv.Invalidate()
			if _, err := src.Token(t.Context()); err != nil || len(app.mints) != 3 {
				t.Fatalf("Invalidate did not force a mint: %d mints, %v", len(app.mints), err)
			}
		})
	}
}

// A failed mint is the minter's error, never a token, and the next call mints again.
func TestPublisherTokenFails(t *testing.T) {
	app := &fakeApp{installations: map[string]int64{}}
	p, _ := newPublisher(app, nil)
	src, err := p.For("o/r")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		tok, err := src.Token(t.Context())
		if tok != "" || !errors.Is(err, forge.ErrNotInstalled) || strings.Contains(err.Error(), secret) {
			t.Fatalf("token %q, err %v", tok, err)
		}
	}
	if len(app.lookups) != 2 {
		t.Fatalf("a failed mint is retried at the next call: %d lookups", len(app.lookups))
	}
}
