// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The stub stands in for Slack's Web API behind maku-slack's apiBaseURL: chat.postMessage answers
// ok with a ts and is recorded, with its bearer, for the scenario to read back; without a bearer it
// answers as Slack does, ok false and not_authed.
func TestSlackPostMessage(t *testing.T) {
	_, ts := newStubServer(t)
	post := func(bearer string) map[string]any {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/chat.postMessage",
			strings.NewReader(`{"channel":"C0123ABCD","thread_ts":"1.2","text":"room merged"}`))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var got map[string]any
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&got) != nil {
			t.Fatalf("status %d", resp.StatusCode)
		}
		return got
	}
	if got := post("xoxb-e2e"); got["ok"] != true || got["ts"] == "" || got["channel"] != "C0123ABCD" {
		t.Fatalf("posted: %v", got)
	}
	if got := post(""); got["ok"] != false || got["error"] != "not_authed" {
		t.Fatalf("no bearer: %v", got)
	}
	status, raw := request(t, ts, http.MethodGet, "/__slack", nil)
	posts := decodeResponse[[]map[string]any](t, raw)
	if status != http.StatusOK || len(posts) != 1 || posts[0]["channel"] != "C0123ABCD" || posts[0]["threadTS"] != "1.2" ||
		posts[0]["text"] != "room merged" || posts[0]["token"] != "xoxb-e2e" {
		t.Fatalf("%d %v", status, posts)
	}
}

// Each client-credentials grant is recorded by client, so a scenario proves who signed in.
func TestClientGrantsRecorded(t *testing.T) {
	_, ts := newStubServer(t)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/token",
		strings.NewReader("grant_type=client_credentials&audience=maku-cli"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("maku-cli", "secret")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	_, raw := request(t, ts, http.MethodGet, "/__grants", nil)
	if got := decodeResponse[[]string](t, raw); len(got) != 1 || got[0] != "maku-cli" {
		t.Fatalf("grants %v", got)
	}
}
