// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
)

const maxRequestBody = 1 << 20

// Stub is the in-process external-service emulator. Its state is safe for concurrent requests.
type Stub struct {
	key        *rsa.PrivateKey
	handler    http.Handler
	modelCalls atomic.Int64
}

// New builds a stub with one throwaway signing key. It never contacts an external service.
func New() *Stub {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("e2e-stub: generate throwaway RSA key: " + err.Error())
	}
	s := &Stub{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /oauth/v2/keys", s.jwks)
	mux.HandleFunc("POST /token", s.token)
	mux.HandleFunc("GET /v2/users/dev", s.activeUser)
	mux.HandleFunc("POST /v2/users/dev/links/_search", s.gitHubLink)
	mux.HandleFunc("GET /app", gitHubApp)
	mux.HandleFunc("GET /repos/owner/repo/installation", gitHubInstallation)
	mux.HandleFunc("POST /app/installations/1/access_tokens", gitHubInstallationToken)
	mux.HandleFunc("GET /repos/owner/repo", gitHubRepository)
	mux.HandleFunc("GET /user/123", gitHubUser)
	mux.HandleFunc("GET /repos/owner/repo/collaborators/dev/permission", gitHubPermission)
	s.handler = boundedBodies(mux)
	return s
}

// Handler returns the complete stub HTTP API.
func (s *Stub) Handler() http.Handler { return s.handler }

// ModelCalls reports model requests received so far; non-model routes never change it.
func (s *Stub) ModelCalls() int { return int(s.modelCalls.Load()) }

func boundedBodies(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxRequestBody {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		}
		next.ServeHTTP(w, r)
	})
}

func decodeBody(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
		}
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
