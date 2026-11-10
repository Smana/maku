// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"time"
)

func gitHubApp(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"id": 1, "slug": "maku-demo", "name": "maku-demo"})
}

func gitHubInstallation(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"id": 1, "app_id": 1, "account": map[string]any{"id": 1, "login": "owner"}})
}

func gitHubInstallationToken(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      "e2e-installation-access",
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"permissions": map[string]string{
			"contents": "write", "issues": "write", "metadata": "read", "pull_requests": "write",
		},
		"repository_selection": "selected",
	})
}

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
