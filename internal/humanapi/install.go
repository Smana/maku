// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"net/http"
	"slices"

	"github.com/Smana/maku/internal/config"
)

// install is GET /api/install: the install's state, readable by every signed-in human (plan PR9).
// unsafe lists the chart's settings that weaken a run's isolation, as bounded codes
// (config.Unsafe*), then the factory's (config.FactoryInstallCodes, v4 D3) read with the caller's
// own token; the web UI shows a banner on every page while it is non-empty. A factory that cannot
// be read adds nothing: its own condition and gauge still say it.
func (s *Server) install(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	unsafe := append([]string{}, s.Unsafe...)
	if s.FactoryInstall != nil && p.AccessToken != "" {
		codes, err := s.FactoryInstall(r.Context(), p.AccessToken)
		if err != nil {
			s.log().Warn("install: the factory's codes are unreadable", "err", err)
		}
		for _, c := range codes {
			if slices.Contains(config.FactoryInstallCodes, c) && !slices.Contains(unsafe, c) {
				unsafe = append(unsafe, c)
			}
		}
	}
	writeJSON(w, struct {
		Unsafe []string `json:"unsafe"`
	}{unsafe})
}
