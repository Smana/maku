// SPDX-License-Identifier: Apache-2.0

package decision

import (
	"strings"
	"sync"
	"time"
)

// Last is one class's latest decision on a repository. Generation is the MergePolicy generation
// the decision was made under, 0 when the repository's policy comes from the file.
type Last struct {
	Verdict, Reason string
	At              time.Time
	Generation      int64
}

// Recorder keeps the latest decision per repository and class, in memory (best-effort: empty
// after a restart or a leader change). Safe for concurrent use. The zero value is ready.
type Recorder struct {
	mu sync.RWMutex
	m  map[string]map[string]Last
}

// Record keeps the decision when it is the newest by At for its repository and class.
func (r *Recorder) Record(repository, class, verdict, reason string, at time.Time, generation int64) {
	repository = strings.ToLower(repository)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]map[string]Last{}
	}
	byClass := r.m[repository]
	if byClass == nil {
		byClass = map[string]Last{}
		r.m[repository] = byClass
	}
	if old, ok := byClass[class]; ok && old.At.After(at) {
		return
	}
	byClass[class] = Last{Verdict: verdict, Reason: reason, At: at, Generation: generation}
}

// Latest is a copy of the repository's latest decision per class (the repository is lowercased).
func (r *Recorder) Latest(repository string) map[string]Last {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]Last{}
	for c, l := range r.m[strings.ToLower(repository)] {
		out[c] = l
	}
	return out
}
