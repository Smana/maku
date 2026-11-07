// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Smana/maku/internal/factory/config"
)

// The reasons a policy fails to load. Unreadable and stale arrive with repository authority: until
// then the fence's own metric reports a failed read, and nothing depends on a fresh policy.
const (
	ReasonInvalid   = "invalid"
	ReasonGuardrail = "guardrail_mismatch"
)

// Recorder is the part of the factory's metrics Watch writes.
type Recorder interface {
	PolicyLoadError(ctx context.Context, repository, reason string)
	PolicyDivergence(ctx context.Context, repository string, n int)
}

// Snapshot is the last file read for a repository, and what it came to: what a decision binds to
// (MP12). Classes holds the resolved classes of a valid file, nil otherwise.
type Snapshot struct {
	Hash        string
	HasClasses  bool
	Err         error
	Divergences []string
	Classes     map[string]Resolved
	Read        time.Time
}

// Watch observes each owned repository's file as the fence reads it. It decides nothing: the gate
// keeps reading the central config, and dual evaluation reads Snapshot beside it.
type Watch struct {
	Cfg     *config.Config
	Metrics Recorder
	Log     *slog.Logger
	Now     func() time.Time

	mu          sync.RWMutex
	state       map[string]Snapshot
	evaluations int // files parsed and validated, for the cache's test
}

// Observe reads one file. A file with no classes has no policy and records no divergence, and a
// failed read records none either: the gauge keeps the last comparison's value, and a repository
// never compared has no series, so 0 is never "nothing to compare". It logs only when the file or
// its result changes: the fence reads every five minutes. The load-error counter counts every
// failed read, so a rate shows it is still bad. A file whose hash is the last read's is not
// evaluated again: the config is the process's, so the result cannot have moved.
func (w *Watch) Observe(ctx context.Context, repository string, body []byte) {
	rc := w.listed(repository)
	if rc == nil {
		return
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	key := strings.ToLower(repository)
	w.mu.RLock()
	prev, seen := w.state[key]
	w.mu.RUnlock()
	s := prev
	if !seen || prev.Hash != hash {
		s = w.evaluate(rc, hash, body)
	}
	s.Read = w.now()
	switch {
	case s.Err != nil && OnlyGuardrail(s.Err):
		w.Metrics.PolicyLoadError(ctx, repository, ReasonGuardrail)
	case s.Err != nil:
		w.Metrics.PolicyLoadError(ctx, repository, ReasonInvalid)
	case s.HasClasses:
		w.Metrics.PolicyDivergence(ctx, repository, len(s.Divergences))
	}
	w.mu.Lock()
	if w.state == nil {
		w.state = map[string]Snapshot{}
	}
	w.state[key] = s
	w.mu.Unlock()
	if !seen || prev.Hash != s.Hash {
		w.log().Info("repository merge policy read", "repository", repository, "hash", s.Hash,
			"classes", s.HasClasses, "err", s.Err, "divergences", s.Divergences)
	}
}

// evaluate parses, validates, resolves and compares one file.
func (w *Watch) evaluate(rc *config.Repository, hash string, body []byte) Snapshot {
	w.mu.Lock()
	w.evaluations++
	w.mu.Unlock()
	s := Snapshot{Hash: hash}
	f, err := Parse(body)
	if err == nil {
		s.HasClasses = f.Classes != nil
		err = Validate(f, w.Cfg)
	}
	s.Err = err
	if err != nil || !s.HasClasses {
		return s
	}
	s.Divergences = Compare(rc, f, w.Cfg)
	s.Classes = map[string]Resolved{}
	for name, p := range f.Classes {
		r, rerr := Resolve(name, p, w.Cfg)
		if rerr != nil { // Validate resolved it: unreachable, and never half a snapshot
			return Snapshot{Hash: hash, HasClasses: true, Err: rerr}
		}
		s.Classes[name] = r
	}
	return s
}

func (w *Watch) now() time.Time {
	if w.Now == nil {
		return time.Now()
	}
	return w.Now()
}

// Snapshot is the last read of repository.
func (w *Watch) Snapshot(repository string) (Snapshot, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	s, ok := w.state[strings.ToLower(repository)]
	return s, ok
}

func (w *Watch) listed(repository string) *config.Repository {
	for _, r := range w.Cfg.Repos() {
		if strings.EqualFold(r.Name, repository) {
			return &r
		}
	}
	return nil
}

func (w *Watch) log() *slog.Logger {
	if w.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return w.Log
}
