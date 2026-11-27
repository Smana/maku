// SPDX-License-Identifier: Apache-2.0

package api

import (
	"cmp"
	"net/http"

	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/version"
)

// InstallRoute is the factory's part of the install's state (GET /api/install on the broker, v4
// D3): bounded codes and non-sensitive environment metadata.
const InstallRoute = "GET /v1/install"

// InstallPrivateCodeUnacknowledged is the factory's banner code: a served repository is private
// code without spec.privateCode.acknowledged (v4 D3, step 7 R1). The broker's config names it
// config.UnsafePrivateCodeUnacknowledged.
const InstallPrivateCodeUnacknowledged = "private_code_unacknowledged"

type installResponse struct {
	Unsafe          []string `json:"unsafe"`
	Version         string   `json:"version"`
	Factory         string   `json:"factory,omitempty"`
	Namespace       string   `json:"namespace,omitempty"`
	DefaultHarness  string   `json:"defaultHarness,omitempty"`
	PolicyStage     string   `json:"policyStage,omitempty"`
	Hardening       string   `json:"hardening,omitempty"`
	RoutinesUIWrite *bool    `json:"routinesUIWrite,omitempty"`
	Demo            bool     `json:"demo,omitempty"`
}

// install is GET /v1/install for any signed-in human: the factory's banner codes and bounded
// non-sensitive environment metadata, never a repository name. nil InstallUnsafe lists none.
func (s *Server) install(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.policyReader(w, r); !ok {
		return
	}
	unsafe := []string{}
	if s.InstallUnsafe != nil {
		unsafe = append(unsafe, s.InstallUnsafe()...)
	}
	resp := installResponse{
		Unsafe:    unsafe,
		Version:   version.Version,
		Namespace: s.Namespace,
	}
	if s.Cfg != nil {
		if cfg := s.Cfg.Current(); cfg != nil {
			resp.Factory = cfg.Factory
			resp.DefaultHarness = cfg.DefaultHarness()
			resp.PolicyStage = cfg.PolicyManagement.EffectiveStage()
			resp.Hardening = cmp.Or(string(cfg.Hardening.Mode), string(hardening.ModeOff))
			uiWrite := cfg.Routines.UIWrite
			resp.RoutinesUIWrite = &uiWrite
			resp.Demo = cfg.Demo
		}
	}
	reply(w, http.StatusOK, resp)
}
