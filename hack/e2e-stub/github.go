// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"strings"
	"time"
)

func gitHubApp(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"id": 1, "slug": "maku-demo", "name": "maku-demo"})
}

func gitHubInstallation(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"id": 1, "app_id": 1, "account": map[string]any{"id": 1, "login": "owner"}})
}

// gitHubInstallationToken grants what a scoped request asks, as GitHub does: the factory's minter
// refuses a token whose permissions or repositories differ from its request (v4 D10). An unscoped
// request gets the installation's own.
func gitHubInstallationToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	perms := body.Permissions
	if len(perms) == 0 {
		perms = map[string]string{"contents": "write", "issues": "write", "metadata": "read", "pull_requests": "write"}
	}
	repos := []map[string]string{}
	for _, name := range body.Repositories {
		if !strings.Contains(name, "/") {
			name = "owner/" + name
		}
		repos = append(repos, map[string]string{"full_name": name})
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":                "e2e-installation-access",
		"expires_at":           time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"permissions":          perms,
		"repositories":         repos,
		"repository_selection": "selected",
	})
}

// gitHubRevokeToken is DELETE /installation/token: the minter revokes a run's token when it ends.
func gitHubRevokeToken(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }

func gitHubRepository(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"id": 1, "name": "repo", "full_name": "owner/repo", "private": false, "visibility": "public",
		"owner": map[string]any{"id": 1, "login": "owner"}, "default_branch": "main",
	})
}

func gitHubUser(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"id": 123, "login": "dev", "type": "User"})
}

func gitHubPermission(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"permission": "write", "role_name": "write", "user": map[string]any{"id": 123, "login": "dev"},
	})
}
