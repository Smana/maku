// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"

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

// Snapshot is the last file read for a repository, and what it came to.
type Snapshot struct {
	Hash        string
	HasClasses  bool
	Err         error
	Divergences []string
}

// Watch observes each owned repository's file as the fence reads it. It decides nothing: stage 1
// compares, and the gate keeps reading the central config.
type Watch struct {
	Cfg     *config.Config
	Metrics Recorder
	Log     *slog.Logger

	mu    sync.RWMutex
	state map[string]Snapshot
}

// Observe reads one file. It logs only when the file or its result changes: the fence reads every
// five minutes. The load-error counter counts every failed read, so a rate shows it is still bad.
func (w *Watch) Observe(ctx context.Context, repository string, body []byte) {
	rc := w.listed(repository)
	if rc == nil {
		return
	}
	sum := sha256.Sum256(body)
	s := Snapshot{Hash: hex.EncodeToString(sum[:])}
	f, err := Parse(body)
	if err == nil {
		s.HasClasses = f.Classes != nil
		err = Validate(f, w.Cfg)
	}
	switch {
	case errors.Is(err, ErrGuardrail):
		w.Metrics.PolicyLoadError(ctx, repository, ReasonGuardrail)
	case err != nil:
		w.Metrics.PolicyLoadError(ctx, repository, ReasonInvalid)
	case s.HasClasses:
		s.Divergences = Compare(rc, f, w.Cfg)
		w.Metrics.PolicyDivergence(ctx, repository, len(s.Divergences))
	default:
		w.Metrics.PolicyDivergence(ctx, repository, 0)
	}
	s.Err = err
	w.mu.Lock()
	if w.state == nil {
		w.state = map[string]Snapshot{}
	}
	key := strings.ToLower(repository)
	prev, seen := w.state[key]
	w.state[key] = s
	w.mu.Unlock()
	if !seen || prev.Hash != s.Hash || !slices.Equal(prev.Divergences, s.Divergences) {
		w.log().Info("repository merge policy read", "repository", repository, "hash", s.Hash,
			"classes", s.HasClasses, "err", err, "divergences", s.Divergences)
	}
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
