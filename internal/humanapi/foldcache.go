// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"math"
	"sync"

	"github.com/Smana/maku/internal/summary"
)

// foldCacheRooms bounds the fold cache: the rooms whose summaries were read last.
const foldCacheRooms = 256

// foldCache keeps each recently read room's summary fold, so a summary folds only what the log
// appended since (audit S-9). The log is append-only and retention deletes whole rooms, whose ids
// never come back, so a fold through seq N stays the fold of events 1..N. Readers get clones:
// Add writes in place. The zero value is ready; max 0 means foldCacheRooms.
type foldCache struct {
	mu    sync.Mutex
	max   int
	tick  uint64
	rooms map[string]*foldEntry
}

type foldEntry struct {
	st   summary.State
	used uint64 // the tick of its last get or put: the smallest is evicted first
}

// get is a clone of room's fold, and whether one is kept.
func (c *foldCache) get(room string) (summary.State, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.rooms[room]
	if !ok {
		return summary.State{}, false
	}
	c.tick++
	e.used = c.tick
	return e.st.Clone(), true
}

// put keeps a clone of st as room's fold unless the kept one has folded further, then evicts the
// room read longest ago past the bound.
func (c *foldCache) put(room string, st summary.State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rooms == nil {
		c.rooms = map[string]*foldEntry{}
	}
	c.tick++
	if e, ok := c.rooms[room]; ok {
		if st.LastSeq >= e.st.LastSeq {
			e.st = st.Clone()
		}
		e.used = c.tick
		return
	}
	c.rooms[room] = &foldEntry{st: st.Clone(), used: c.tick}
	limit := c.max
	if limit <= 0 {
		limit = foldCacheRooms
	}
	if len(c.rooms) > limit {
		oldest, at := "", uint64(math.MaxUint64)
		for id, e := range c.rooms {
			if e.used < at {
				oldest, at = id, e.used
			}
		}
		delete(c.rooms, oldest)
	}
}
