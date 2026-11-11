// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/metrics"
	"github.com/Smana/maku/internal/runrequest"
)

// The catalogue cache's bounds (P18): an answer is good for a minute, and at most 256 are kept.
const (
	catalogueTTL = 60 * time.Second
	catalogueCap = 256
)

// errCatalogueUnavailable is a catalogue read that failed for a reason the caller cannot fix: the
// factory did not answer, answered an error, or none is configured. A refusal the factory meant
// (not_found, human_only, not_linked) is not this error: it is a runrequest.ErrRefused.
var errCatalogueUnavailable = errors.New("catalogue unavailable")

// CatalogueSource is the factory's catalogue route as the broker reads it, with the caller's own
// token; runrequest.Factory implements it.
type CatalogueSource interface {
	Catalogue(ctx context.Context, accessToken, repository, onBehalfOf string) (catalogue.Catalogue, error)
}

// CatalogueReads counts a read by outcome; *metrics.Set implements it.
type CatalogueReads interface {
	IntakeCatalogueRead(ctx context.Context, outcome string)
}

// CatalogueCache reads a repository's catalogue from the factory and keeps each answer for a
// minute, per repository and per caller: the answer carries the caller's own maintainer flag, so
// one caller's is never served to another (P18). It holds at most 256 answers, dropping the least
// recently used. An error is never cached.
type CatalogueCache struct {
	src     CatalogueSource
	reads   CatalogueReads
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List // front is the most recently used
}

type catalogueEntry struct {
	key     string
	until   time.Time
	catalog catalogue.Catalogue
}

// NewCatalogueCache is a cache over src. reads and now may be nil: no counting, the wall clock.
func NewCatalogueCache(src CatalogueSource, reads CatalogueReads, now func() time.Time) *CatalogueCache {
	if now == nil {
		now = time.Now
	}
	return &CatalogueCache{src: src, reads: reads, now: now, entries: map[string]*list.Element{}, order: list.New()}
}

// catalogueKey is (lowercase repository, caller, who the caller acts for): a sponsored principal's
// answer is the Slack user's, not the principal's.
func catalogueKey(p authn.Principal, repository, onBehalfOf string) string {
	return strings.ToLower(repository) + "\x00" + p.ID + "\x00" + onBehalfOf
}

// Get is repository's catalogue for p. A factory refusal comes back as the runrequest.ErrRefused it
// is; every other failure is errCatalogueUnavailable.
func (c *CatalogueCache) Get(ctx context.Context, p authn.Principal, repository, onBehalfOf string) (catalogue.Catalogue, error) {
	if c == nil || c.src == nil {
		return catalogue.Catalogue{}, errCatalogueUnavailable
	}
	key := catalogueKey(p, repository, onBehalfOf)
	if cat, ok := c.cached(key); ok {
		c.count(ctx, metrics.CatalogueHit)
		return cat, nil
	}
	cat, err := c.src.Catalogue(ctx, p.AccessToken, repository, onBehalfOf)
	if err != nil {
		c.count(ctx, metrics.CatalogueError)
		var ref runrequest.ErrRefused
		if errors.As(err, &ref) && ref.Status < http.StatusInternalServerError {
			return catalogue.Catalogue{}, err
		}
		return catalogue.Catalogue{}, fmt.Errorf("%w: %w", errCatalogueUnavailable, err)
	}
	c.count(ctx, metrics.CatalogueMiss)
	c.store(key, cat)
	return cat, nil
}

func (c *CatalogueCache) count(ctx context.Context, outcome string) {
	if c.reads != nil {
		c.reads.IntakeCatalogueRead(ctx, outcome)
	}
}

func (c *CatalogueCache) cached(key string) (catalogue.Catalogue, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return catalogue.Catalogue{}, false
	}
	e, _ := el.Value.(*catalogueEntry)
	if !c.now().Before(e.until) {
		c.order.Remove(el)
		delete(c.entries, key)
		return catalogue.Catalogue{}, false
	}
	c.order.MoveToFront(el)
	return e.catalog, true
}

func (c *CatalogueCache) store(key string, cat catalogue.Catalogue) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := &catalogueEntry{key: key, until: c.now().Add(catalogueTTL), catalog: cat}
	if el, ok := c.entries[key]; ok {
		el.Value = e
		c.order.MoveToFront(el)
		return
	}
	c.entries[key] = c.order.PushFront(e)
	for c.order.Len() > catalogueCap {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		if old, ok := oldest.Value.(*catalogueEntry); ok {
			delete(c.entries, old.key)
		}
	}
}
