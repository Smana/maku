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
// appended since (audit S-9). The log is append-only, so a fold through seq N stays the fold of
// events 1..N, but only for the Room it was folded for: nothing stops a purged room's id coming
// back, so a fold is kept with its Room's UID and another UID is a miss. Readers get clones: Add
// writes in place. The zero value is ready; max 0 means foldCacheRooms.
type foldCache struct {
	mu    sync.Mutex
	max   int
	tick  uint64
	rooms map[string]*foldEntry
}

type foldEntry struct {
	uid  string // the Room CR's UID the fold belongs to
	st   summary.State
	used uint64 // the tick of its last get or put: the smallest is evicted first
}

// get is a clone of the fold of room, the Room with that uid, and whether one is kept.
func (c *foldCache) get(room, uid string) (summary.State, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.rooms[room]
	if !ok || e.uid != uid {
		return summary.State{}, false
	}
	c.tick++
	e.used = c.tick
	return e.st.Clone(), true
}

// put keeps a clone of st as the fold of room, the Room with that uid. A kept fold of the same
// Room that has folded further stays; one of another Room is replaced. Past the bound it evicts
// the room read longest ago.
func (c *foldCache) put(room, uid string, st summary.State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rooms == nil {
		c.rooms = map[string]*foldEntry{}
	}
	c.tick++
	if e, ok := c.rooms[room]; ok {
		if e.uid != uid || st.LastSeq >= e.st.LastSeq {
			e.uid, e.st = uid, st.Clone()
		}
		e.used = c.tick
		return
	}
	c.rooms[room] = &foldEntry{uid: uid, st: st.Clone(), used: c.tick}
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
