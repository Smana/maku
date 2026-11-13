// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/Smana/maku/internal/config"
)

// installTTL is how long one read of the factory's codes serves every page view, and
// installTimeout how long a view waits for that read: past it the banner shows the chart's codes
// alone, never a slow page (review of #540).
var (
	installTTL     = 30 * time.Second
	installTimeout = 2 * time.Second
)

// installCache is the factory's codes as last read, failures included (as none): one read per
// installTTL, whoever views the page. Safe for concurrent use; the lock is held over the read.
type installCache struct {
	mu    sync.Mutex
	at    time.Time
	codes []string
}

// factoryCodes is the factory's codes, at most installTTL old, read with token when stale.
func (s *Server) factoryCodes(ctx context.Context, token string) []string {
	c := &s.installCodes
	c.mu.Lock()
	defer c.mu.Unlock()
	now := s.now()
	if !c.at.IsZero() && now.Sub(c.at) < installTTL {
		return c.codes
	}
	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	codes, err := s.FactoryInstall(ctx, token)
	if err != nil {
		s.log().Warn("install: the factory's codes are unreadable", "err", err)
		codes = nil
	}
	c.at, c.codes = now, codes
	return codes
}

// install is GET /api/install: the install's state, readable by every signed-in human (plan PR9).
// unsafe lists the chart's settings that weaken a run's isolation, as bounded codes
// (config.Unsafe*), then the factory's (config.FactoryInstallCodes, v4 D3) read with the caller's
// own token at most once per installTTL; the web UI shows a banner on every page while it is non-empty. A factory that cannot
// be read adds nothing: its own condition and gauge still say it.
func (s *Server) install(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	unsafe := append([]string{}, s.Unsafe...)
	if s.FactoryInstall != nil && p.AccessToken != "" {
		for _, c := range s.factoryCodes(r.Context(), p.AccessToken) {
			if slices.Contains(config.FactoryInstallCodes, c) && !slices.Contains(unsafe, c) {
				unsafe = append(unsafe, c)
			}
		}
	}
	writeJSON(w, struct {
		Unsafe []string `json:"unsafe"`
	}{unsafe})
}
