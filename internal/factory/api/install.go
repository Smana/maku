// SPDX-License-Identifier: Apache-2.0

package api

import "net/http"

// InstallRoute is the factory's part of the install's state (GET /api/install on the broker, v4
// D3): bounded codes, the web UI's banner.
const InstallRoute = "GET /v1/install"

// InstallPrivateCodeUnacknowledged is the factory's banner code: a served repository is private
// code without spec.privateCode.acknowledged (v4 D3, step 7 R1). The broker's config names it
// config.UnsafePrivateCodeUnacknowledged.
const InstallPrivateCodeUnacknowledged = "private_code_unacknowledged"

// install is GET /v1/install for any signed-in human: the factory's banner codes, never a
// repository name. nil InstallUnsafe lists none.
func (s *Server) install(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.policyReader(w, r); !ok {
		return
	}
	out := []string{}
	if s.InstallUnsafe != nil {
		out = append(out, s.InstallUnsafe()...)
	}
	reply(w, http.StatusOK, map[string][]string{"unsafe": out})
}
