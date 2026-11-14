// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
)

const maxRequestBody = 1 << 20

var gitProjectRoot = "/srv/git"

// Stub is the in-process external-service emulator. Its state is safe for concurrent requests.
type Stub struct {
	key             *rsa.PrivateKey
	handler         http.Handler
	issues          *IssueStore
	modelCalls      atomic.Int64
	plannerCalls    atomic.Int64
	scoperCalls     atomic.Int64
	scoperHandoffs  atomic.Int64
	classifierCalls atomic.Int64

	// received is what each model request carried, as the provider behind a gateway sees it.
	receivedMu sync.Mutex
	received   []ModelRequest
	// traces are the trace ids the OTLP sink received, each once (plan v4 step 6, Task 2).
	traces []string
	// codes are the authorization codes issued and not yet redeemed (plan v4 step 6, Task 9).
	codes codeStore
	// slack is the chat.postMessage calls received as Slack's Web API (plan v4 step 6, Task 16).
	slack slackPosts
	// oauth is GitHub's OAuth web flow, for the broker's GitHub account link (v4 D8).
	oauth *oauthFlow
}

// New builds a stub with one throwaway signing key. It never contacts an external service.
func New() *Stub {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("e2e-stub: generate throwaway RSA key: " + err.Error())
	}
	s := &Stub{key: key, issues: NewIssueStore(), oauth: newOAuthFlow()}
	mux := http.NewServeMux()
	s.oauth.routes(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /__counts", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]int{
			"total": s.ModelCalls(), "planner": s.PlannerCalls(), "scoper": s.ScoperCalls(),
			"classifier": s.ClassifierCalls(),
		})
	})
	mux.HandleFunc("GET /__received", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.Received())
	})
	mux.HandleFunc("GET /__traces", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.Traces())
	})
	mux.HandleFunc("POST /v1/traces", s.otlpTraces)
	mux.HandleFunc("GET /oauth/v2/keys", s.jwks)
	mux.HandleFunc("GET /.well-known/openid-configuration", s.discovery)
	mux.HandleFunc("GET /test/id-token", s.testIDToken)
	mux.HandleFunc("POST /token", s.token)
	mux.HandleFunc("GET /oauth/v2/authorize", s.authorize)
	mux.HandleFunc("POST /api/chat.postMessage", s.slackPostMessage)
	mux.HandleFunc("GET /__slack", s.slackReceived)
	mux.HandleFunc("GET /__grants", s.clientGrants)
	mux.HandleFunc("GET /v2/users/dev", s.activeUser)
	mux.HandleFunc("POST /v2/users/dev/links/_search", s.gitHubLink)
	mux.HandleFunc("GET /app", gitHubApp)
	mux.HandleFunc("GET /repos/owner/repo/installation", gitHubInstallation)
	mux.HandleFunc("POST /app/installations/1/access_tokens", gitHubInstallationToken)
	mux.HandleFunc("DELETE /installation/token", gitHubRevokeToken)
	mux.HandleFunc("GET /repos/owner/repo", gitHubRepository)
	mux.HandleFunc("GET /user/123", gitHubUser)
	mux.HandleFunc("GET /repos/owner/repo/collaborators/dev/permission", gitHubPermission)
	mux.HandleFunc("/owner/repo.git/", gitHTTP)
	s.registerIssueRoutes(mux)
	mux.HandleFunc("POST /v1/chat/completions", s.completion)
	s.handler = boundedBodies(mux)
	return s
}

// Handler returns the complete stub HTTP API.
func (s *Stub) Handler() http.Handler { return s.handler }

// ModelCalls reports model requests received so far; non-model routes never change it.
func (s *Stub) ModelCalls() int { return int(s.modelCalls.Load()) }

// PlannerCalls reports governed plan/v2 model requests.
func (s *Stub) PlannerCalls() int { return int(s.plannerCalls.Load()) }

// ClassifierCalls reports intent_classify model requests.
func (s *Stub) ClassifierCalls() int { return int(s.classifierCalls.Load()) }

// ScoperCalls reports scoper model requests, including the finish after a handoff.
func (s *Stub) ScoperCalls() int { return int(s.scoperCalls.Load()) }

// Issue returns one issue from the same store the HTTP routes use.
func (s *Stub) Issue(number int) (Issue, bool) { return s.issues.Issue(number) }

// SetBody edits one issue body in the same store the HTTP routes use.
func (s *Stub) SetBody(number int, body string) (Issue, bool) { return s.issues.SetBody(number, body) }

// Comments returns all issue comments from the same store the HTTP routes use.
func (s *Stub) Comments(number int) []Comment { return s.issues.Comments(number) }

// LabelEvents returns all label additions from the same store the HTTP routes use.
func (s *Stub) LabelEvents(number int) []LabelEvent { return s.issues.LabelEvents(number) }

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
