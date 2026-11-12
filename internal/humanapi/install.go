// SPDX-License-Identifier: Apache-2.0

package humanapi

import "net/http"

// install is GET /api/install: the install's state, readable by every signed-in human (plan PR9).
// unsafe lists the chart's settings that weaken a run's isolation, as bounded codes
// (config.Unsafe*); the web UI shows a banner on every page while it is non-empty.
func (s *Server) install(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.principal(w, r); !ok {
		return
	}
	unsafe := s.Unsafe
	if unsafe == nil {
		unsafe = []string{}
	}
	writeJSON(w, struct {
		Unsafe []string `json:"unsafe"`
	}{unsafe})
}
