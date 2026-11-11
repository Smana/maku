// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/metrics"
	"github.com/Smana/maku/internal/runrequest"
)

type fakeCatalogue struct {
	calls  []string
	err    error
	byUser map[string]bool // access token -> maintainer
}

func (f *fakeCatalogue) Catalogue(_ context.Context, token, repository, onBehalfOf string) (catalogue.Catalogue, error) {
	f.calls = append(f.calls, token+"|"+repository+"|"+onBehalfOf)
	if f.err != nil {
		return catalogue.Catalogue{}, f.err
	}
	return catalogue.Catalogue{Repository: repository, Maintainer: f.byUser[token]}, nil
}

type countReads map[string]int

func (c countReads) IntakeCatalogueRead(_ context.Context, outcome string) { c[outcome]++ }

type tick struct{ t time.Time }

func (c *tick) now() time.Time { return c.t }

func human(sub string) authn.Principal {
	return authn.Principal{Kind: envelope.ActorHuman, ID: "human:" + sub, Sub: sub, AccessToken: "tok-" + sub}
}

func TestCatalogueCacheTTL(t *testing.T) {
	clock := &tick{time.Unix(1_000, 0)}
	src := &fakeCatalogue{}
	c := NewCatalogueCache(src, nil, clock.now)
	p := human("1")
	for range 3 {
		if _, err := c.Get(t.Context(), p, "Smana/cloud-native-ref", ""); err != nil {
			t.Fatal(err)
		}
	}
	clock.t = clock.t.Add(59 * time.Second)
	if _, err := c.Get(t.Context(), p, "smana/CLOUD-native-ref", ""); err != nil {
		t.Fatal(err)
	}
	if len(src.calls) != 1 {
		t.Fatalf("calls = %q: within 60 s, whatever the case, one read", src.calls)
	}
	clock.t = clock.t.Add(2 * time.Second)
	if _, err := c.Get(t.Context(), p, "Smana/cloud-native-ref", ""); err != nil || len(src.calls) != 2 {
		t.Fatalf("calls = %q, %v: after 60 s, a new read", src.calls, err)
	}
}

// G7: the answer carries the caller's maintainer flag, so one caller's is never served to another.
func TestCatalogueCacheKeyedByCaller(t *testing.T) {
	src := &fakeCatalogue{byUser: map[string]bool{"tok-1": true}}
	c := NewCatalogueCache(src, nil, nil)
	a, _ := c.Get(t.Context(), human("1"), "Smana/r", "")
	b, _ := c.Get(t.Context(), human("2"), "Smana/r", "")
	if !a.Maintainer || b.Maintainer || len(src.calls) != 2 {
		t.Fatalf("a=%t b=%t calls=%q: two callers, two entries", a.Maintainer, b.Maintainer, src.calls)
	}
	// A sponsored principal's answer is the Slack user's.
	slack := authn.Principal{Kind: envelope.ActorSystem, ID: "system:slack-app", Sponsored: true, AccessToken: "tok-s"}
	_, _ = c.Get(t.Context(), slack, "Smana/r", "slack:T/U1")
	_, _ = c.Get(t.Context(), slack, "Smana/r", "slack:T/U2")
	_, _ = c.Get(t.Context(), slack, "Smana/r", "slack:T/U1")
	if len(src.calls) != 4 {
		t.Fatalf("calls = %q: two Slack users, two entries", src.calls)
	}
}

func TestCatalogueCacheBounded(t *testing.T) {
	src := &fakeCatalogue{}
	c := NewCatalogueCache(src, nil, nil)
	for i := range catalogueCap + 1 {
		if _, err := c.Get(t.Context(), human(fmt.Sprint(i)), "Smana/r", ""); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.entries) != catalogueCap || c.order.Len() != catalogueCap {
		t.Fatalf("entries = %d, order = %d", len(c.entries), c.order.Len())
	}
	n := len(src.calls)
	_, _ = c.Get(t.Context(), human("0"), "Smana/r", "") // the oldest was evicted
	if len(src.calls) != n+1 {
		t.Fatal("the 257th entry did not evict the oldest")
	}
	_, _ = c.Get(t.Context(), human("2"), "Smana/r", "") // the newest ones stay
	if len(src.calls) != n+1 {
		t.Fatal("a recent entry was evicted")
	}
}

func TestCatalogueCacheNeverCachesAnError(t *testing.T) {
	src := &fakeCatalogue{err: errors.New("down")}
	c := NewCatalogueCache(src, nil, nil)
	for range 2 {
		if _, err := c.Get(t.Context(), human("1"), "Smana/r", ""); !errors.Is(err, errCatalogueUnavailable) {
			t.Fatalf("err = %v", err)
		}
	}
	if len(src.calls) != 2 {
		t.Fatalf("calls = %q: an error is read again", src.calls)
	}
	src.err = runrequest.ErrRefused{Status: http.StatusNotFound, Reason: "not_found"}
	_, err := c.Get(t.Context(), human("1"), "Smana/r", "")
	var ref runrequest.ErrRefused
	if !errors.As(err, &ref) || errors.Is(err, errCatalogueUnavailable) || ref.Reason != "not_found" {
		t.Fatalf("a refusal the factory meant stays a refusal: %v", err)
	}
	src.err = runrequest.ErrRefused{Status: http.StatusServiceUnavailable, Reason: "refused"}
	if _, err := c.Get(t.Context(), human("1"), "Smana/r", ""); !errors.Is(err, errCatalogueUnavailable) {
		t.Fatalf("a 503 is unavailable: %v", err)
	}
	var none *CatalogueCache
	if _, err := none.Get(t.Context(), human("1"), "Smana/r", ""); !errors.Is(err, errCatalogueUnavailable) {
		t.Fatalf("no cache: %v", err)
	}
}

func TestCatalogueReadsMetric(t *testing.T) {
	var _ CatalogueReads = (*metrics.Set)(nil)
	reads := countReads{}
	src := &fakeCatalogue{}
	c := NewCatalogueCache(src, reads, nil)
	_, _ = c.Get(t.Context(), human("1"), "Smana/r", "") // miss
	_, _ = c.Get(t.Context(), human("1"), "Smana/r", "") // hit
	src.err = errors.New("down")
	_, _ = c.Get(t.Context(), human("2"), "Smana/r", "") // error
	if reads[metrics.CatalogueMiss] != 1 || reads[metrics.CatalogueHit] != 1 || reads[metrics.CatalogueError] != 1 {
		t.Fatalf("reads = %v", reads)
	}
}
