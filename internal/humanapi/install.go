// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/Smana/maku/internal/config"
	"github.com/Smana/maku/internal/runrequest"
	"github.com/Smana/maku/internal/version"
)

// installTTL is how long one read of the factory's state serves every page view, and
// installTimeout how long a view waits for that read: past it the banner shows the chart's codes
// alone, never a slow page (review of #540).
var (
	installTTL     = 30 * time.Second
	installTimeout = 2 * time.Second
)

// installCache is the factory's install state as last read, failures included (as unreachable):
// one read per installTTL, whoever views the page. Safe for concurrent use; mu is never held
// across I/O (REL-M14).
type installCache struct {
	mu   sync.Mutex
	at   time.Time
	info runrequest.InstallInfo
	ok   bool
}

type installResponse struct {
	Unsafe      []string            `json:"unsafe"`
	Version     string              `json:"version"`
	Environment *installEnvironment `json:"environment,omitempty"`
}

type installEnvironment struct {
	Broker  brokerEnvironment   `json:"broker"`
	Factory *factoryEnvironment `json:"factory,omitempty"`
}

type brokerEnvironment struct {
	Version   string `json:"version"`
	Namespace string `json:"namespace,omitempty"`
	AuthMode  string `json:"authMode"`
}

type factoryEnvironment struct {
	Reachable       bool   `json:"reachable"`
	Version         string `json:"version,omitempty"`
	Name            string `json:"name,omitempty"`
	Namespace       string `json:"namespace,omitempty"`
	DefaultHarness  string `json:"defaultHarness,omitempty"`
	PolicyStage     string `json:"policyStage,omitempty"`
	Hardening       string `json:"hardening,omitempty"`
	RoutinesUIWrite *bool  `json:"routinesUIWrite,omitempty"`
	Demo            bool   `json:"demo,omitempty"`
}

// factoryInstallInfo is the factory's install state, at most installTTL old, read with token when stale.
func (s *Server) factoryInstallInfo(ctx context.Context, token string) (runrequest.InstallInfo, bool) {
	c := &s.installCodes
	c.mu.Lock()
	now := s.now()
	if !c.at.IsZero() && now.Sub(c.at) < installTTL {
		info, ok := c.info, c.ok
		c.mu.Unlock()
		return info, ok
	}
	c.mu.Unlock()

	reqCtx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	info, err := s.FactoryInstall(reqCtx, token)
	if err != nil {
		// Caller cancellation must not poison the shared cache for installTTL (REL-M14).
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return runrequest.InstallInfo{}, false
		}
		s.log().Warn("install: the factory's state is unreadable", "err", err)
		c.mu.Lock()
		c.at, c.info, c.ok = s.now(), runrequest.InstallInfo{}, false
		c.mu.Unlock()
		return runrequest.InstallInfo{}, false
	}
	c.mu.Lock()
	c.at, c.info, c.ok = s.now(), info, true
	c.mu.Unlock()
	return info, true
}

// install is GET /api/install: the install's state and bounded environment metadata, readable by
// every signed-in human (plan PR9). unsafe lists the chart's settings that weaken a run's
// isolation, as bounded codes (config.Unsafe*), then the factory's (config.FactoryInstallCodes,
// v4 D3) read with the caller's own token at most once per installTTL; the web UI shows a banner
// on every page while it is non-empty and renders environment details on /settings. A factory that
// cannot be read adds no unsafe codes and reports reachable: false.
func (s *Server) install(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	unsafe := append([]string{}, s.Unsafe...)
	env := installEnvironment{
		Broker: brokerEnvironment{
			Version:   version.Version,
			Namespace: s.Namespace,
			AuthMode:  cmp.Or(s.HumanMode, config.HumanModeOIDC),
		},
	}
	if s.FactoryInstall != nil && p.AccessToken != "" {
		info, reachable := s.factoryInstallInfo(r.Context(), p.AccessToken)
		if reachable {
			for _, c := range info.Unsafe {
				if slices.Contains(config.FactoryInstallCodes, c) && !slices.Contains(unsafe, c) {
					unsafe = append(unsafe, c)
				}
			}
			env.Factory = &factoryEnvironment{
				Reachable:       true,
				Version:         info.Version,
				Name:            info.Factory,
				Namespace:       info.Namespace,
				DefaultHarness:  info.DefaultHarness,
				PolicyStage:     info.PolicyStage,
				Hardening:       info.Hardening,
				RoutinesUIWrite: info.RoutinesUIWrite,
				Demo:            info.Demo,
			}
		} else {
			env.Factory = &factoryEnvironment{Reachable: false}
		}
	}
	writeJSON(w, installResponse{
		Unsafe:      unsafe,
		Version:     version.Version,
		Environment: &env,
	})
}
