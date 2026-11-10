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
