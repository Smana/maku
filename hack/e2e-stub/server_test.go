// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newStubServer(t *testing.T) (*Stub, *httptest.Server) {
	t.Helper()
	stub := New()
	ts := httptest.NewTLSServer(stub.Handler())
	t.Cleanup(ts.Close)
	return stub, ts
}

func request(t *testing.T, ts *httptest.Server, method, path string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, ts.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("PRIVATE KEY")) || bytes.Contains(raw, []byte("private_key")) {
		t.Fatalf("%s %s returned private key material", method, path)
	}
	return resp.StatusCode, raw
}

func decodeResponse[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("invalid JSON response %q: %v", raw, err)
	}
	return out
}

func TestStubFailsClosed(t *testing.T) {
	stub, ts := newStubServer(t)
	for _, path := range []string{
		"/",
		"/unsupported",
		"/v2/users/other",
		"/v2/users/other/links/_search",
		"/user/124",
		"/repos/owner/other",
		"/repos/other/repo",
		"/repos/owner/other/installation",
		"/repos/owner/other/collaborators/dev/permission",
	} {
		t.Run(strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			method := http.MethodGet
			if strings.HasSuffix(path, "/_search") {
				method = http.MethodPost
			}
			status, _ := request(t, ts, method, path, nil)
			if status != http.StatusNotFound {
				t.Fatalf("%s %s status = %d, want 404", method, path, status)
			}
		})
	}
	if got := stub.ModelCalls(); got != 0 {
		t.Fatalf("non-model requests changed model calls to %d", got)
	}
}

func TestStubRejectsOversizedBodies(t *testing.T) {
	_, ts := newStubServer(t)
	status, _ := request(t, ts, http.MethodPost, "/v2/users/dev/links/_search", bytes.Repeat([]byte("x"), (1<<20)+1))
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d, want 413", status)
	}
}

func TestModelCountsRoute(t *testing.T) {
	_, ts := newStubServer(t)
	assertCounts := func(want map[string]int) {
		t.Helper()
		status, raw := request(t, ts, http.MethodGet, "/__counts", nil)
		if status != http.StatusOK {
			t.Fatalf("GET /__counts status = %d, body = %s", status, raw)
		}
		got := decodeResponse[map[string]int](t, raw)
		for name, value := range want {
			if got[name] != value {
				t.Errorf("GET /__counts %s = %d, want %d (all counts: %#v)", name, got[name], value, got)
			}
		}
	}

	assertCounts(map[string]int{"total": 0, "planner": 0, "scoper": 0})
	for name, body := range map[string]string{
		"planner": `{"response_format":{"type":"json_object"},"messages":[{"role":"system","content":"You plan the issues a repository's maintainers will work from"}]}`,
		"scoper":  `{"messages":[{"role":"user","content":"agent-factory scoper\nYou are the scoper"}]}`,
		"other":   `{"messages":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			status, raw := request(t, ts, http.MethodPost, "/v1/chat/completions", []byte(body))
			if status != http.StatusOK {
				t.Fatalf("POST /v1/chat/completions status = %d, body = %s", status, raw)
			}
		})
	}
	assertCounts(map[string]int{"total": 3, "planner": 1, "scoper": 1})
	status, _ := request(t, ts, http.MethodGet, "/healthz", nil)
	if status != http.StatusNoContent {
		t.Fatalf("GET /healthz status = %d, want 204", status)
	}
	assertCounts(map[string]int{"total": 3, "planner": 1, "scoper": 1})
}

func TestStubRejectsWrongMethods(t *testing.T) {
	_, ts := newStubServer(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/oauth/v2/keys"},
		{http.MethodGet, "/token"},
		{http.MethodPost, "/repos/owner/repo"},
	} {
		status, _ := request(t, ts, tc.method, tc.path, nil)
		if status != http.StatusMethodNotAllowed {
			t.Errorf("%s %s status = %d, want 405", tc.method, tc.path, status)
		}
	}
}
