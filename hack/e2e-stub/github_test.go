// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"testing"
	"time"
)

func TestGitHubReads(t *testing.T) {
	_, ts := newStubServer(t)

	status, raw := request(t, ts, http.MethodGet, "/app", nil)
	if status != http.StatusOK {
		t.Fatalf("App status = %d", status)
	}
	app := decodeResponse[struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	}](t, raw)
	if app.ID != 1 || app.Slug != "maku-demo" {
		t.Errorf("App = %+v", app)
	}

	status, raw = request(t, ts, http.MethodGet, "/repos/owner/repo/installation", nil)
	if status != http.StatusOK {
		t.Fatalf("installation status = %d", status)
	}
	installation := decodeResponse[struct {
		ID int64 `json:"id"`
	}](t, raw)
	if installation.ID != 1 {
		t.Errorf("installation = %+v", installation)
	}

	status, raw = request(t, ts, http.MethodPost, "/app/installations/1/access_tokens", []byte(`{"repositories":["repo"],"permissions":{"pull_requests":"write"}}`))
	if status != http.StatusCreated {
		t.Fatalf("installation token status = %d", status)
	}
	token := decodeResponse[struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}](t, raw)
	if token.Token == "" || !token.ExpiresAt.After(time.Now()) {
		t.Errorf("installation token = %+v", token)
	}

	status, raw = request(t, ts, http.MethodGet, "/repos/owner/repo", nil)
	if status != http.StatusOK {
		t.Fatalf("repository status = %d", status)
	}
	repository := decodeResponse[struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		FullName   string `json:"full_name"`
		Private    bool   `json:"private"`
		Visibility string `json:"visibility"`
	}](t, raw)
	if repository.ID != 1 || repository.Name != "repo" || repository.FullName != "owner/repo" || repository.Private || repository.Visibility != "public" {
		t.Errorf("repository = %+v", repository)
	}

	status, raw = request(t, ts, http.MethodGet, "/user/123", nil)
	if status != http.StatusOK {
		t.Fatalf("GitHub user status = %d", status)
	}
	user := decodeResponse[struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}](t, raw)
	if user.ID != 123 || user.Login != "dev" {
		t.Errorf("GitHub user = %+v", user)
	}

	status, raw = request(t, ts, http.MethodGet, "/repos/owner/repo/collaborators/dev/permission", nil)
	if status != http.StatusOK {
		t.Fatalf("permission status = %d", status)
	}
	permission := decodeResponse[struct {
		Permission string `json:"permission"`
	}](t, raw)
	if permission.Permission != "write" {
		t.Errorf("permission = %+v", permission)
	}
}
