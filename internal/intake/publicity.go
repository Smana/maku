// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Publicity answers the one question the public-only guard asks: does GitHub report this repository
// as public? The planner reads public text only, and this is the read that decides it — one
// interface, implemented by GitHub in production and by a double in a test.
type Publicity interface {
	Public(ctx context.Context, repository string) (bool, error)
}

// Lookup reads one repository's visibility from GitHub: true for a repository GitHub reports public,
// false for one it does not. An error means GitHub could not answer, and the caller fails closed.
type Lookup func(ctx context.Context, repository string) (bool, error)

// CachedPublicity is Publicity over Lookup, cached as `repoaccess.Checker` caches a permission: one
// read per repository per TTL under a mutex, a bounded map whose oldest entry is dropped when it is
// full, and an error that is never cached — a failure is not an answer, and storing it would let one
// outage outlive the TTL it was supposed to be shorter than.
type CachedPublicity struct {
	lookup Lookup
	ttl    time.Duration
	now    func() time.Time

	mu         sync.Mutex
	cache      map[string]publicityEntry
	maxEntries int // 0 means defaultMaxEntries
}

// publicityEntry is one cached answer and when it was read.
type publicityEntry struct {
	public bool
	at     time.Time
}

// defaultMaxEntries bounds the cache; when it is full the oldest entry is dropped. A broker sees a
// small allowlist of repositories, so the bound is a leak's ceiling rather than a working set.
const defaultMaxEntries = 10_000

// NewPublicity is a Publicity over lookup, holding an answer for ttl, with now injected so a test
// can move the clock. All three are required: an unbounded TTL, a nil clock or a nil lookup would
// each leave the guard unable to answer the one question it exists for.
func NewPublicity(lookup Lookup, ttl time.Duration, now func() time.Time) *CachedPublicity {
	return &CachedPublicity{lookup: lookup, ttl: ttl, now: now}
}

// Public is whether GitHub reports repository as public, from the cache when a younger answer is
// there. The repository is read as GitHub reads it, without regard to case; one that is not
// `owner/name` is an error and asks nobody. Every error is wrapped, so a caller can log the cause
// and refuse rather than plan on an assumption.
func (c *CachedPublicity) Public(ctx context.Context, repository string) (bool, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(repository), "/")
	if !ok || owner == "" || name == "" {
		return false, fmt.Errorf("intake: %q is not owner/name, so its visibility is unknown", repository)
	}
	if c.lookup == nil || c.now == nil {
		return false, fmt.Errorf("intake: check whether %s is public: no lookup is configured", repository)
	}
	// GitHub's owner and repository names are case-insensitive, so they are one cache key.
	key := strings.ToLower(repository)
	c.mu.Lock()
	e, hit := c.cache[key]
	c.mu.Unlock()
	if hit && c.now().Sub(e.at) < c.ttl {
		return e.public, nil
	}
	public, err := c.lookup(ctx, key)
	if err != nil {
		// Never cached: the next caller asks again, and until GitHub answers it fails closed.
		return false, fmt.Errorf("intake: cannot check whether %s is public: %w", repository, err)
	}
	c.put(key, publicityEntry{public: public, at: c.now()})
	return public, nil
}

// put stores e, first dropping the oldest entry if key would grow a full cache.
func (c *CachedPublicity) put(key string, e publicityEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		c.cache = map[string]publicityEntry{}
	}
	limit := c.maxEntries
	if limit == 0 {
		limit = defaultMaxEntries
	}
	if _, known := c.cache[key]; !known && len(c.cache) >= limit {
		oldest, first := "", true
		var at time.Time
		for k, v := range c.cache {
			if first || v.at.Before(at) {
				oldest, at, first = k, v.at, false
			}
		}
		delete(c.cache, oldest)
	}
	c.cache[key] = e
}
