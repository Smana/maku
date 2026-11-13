// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyMakesTheBrokersCallWithTheTokenFile(t *testing.T) {
	var gotAuth, gotPath, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"echo-light","choices":[{"message":{"content":"{\"intent\":\"status\",\"text\":\"\",\"confidence\":0.9}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	token := filepath.Join(t.TempDir(), "classifier")
	if err := os.WriteFile(token, []byte("projected-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), []string{"classify", "--url", srv.URL + "/clients/classifier/v1", "--token-file", token, "--reply", "where is it at?"}, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("classify: %v (stderr %s)", err, stderr.String())
	}
	if gotAuth != "Bearer projected-token" || gotPath != "/clients/classifier/v1/chat/completions" || gotModel != "tier-light" {
		t.Fatalf("request auth %q path %q model %q", gotAuth, gotPath, gotModel)
	}
	for _, want := range []string{"intent=status\n", "outcome=ok\n", "model=echo-light\n"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout %q lacks %q", stdout.String(), want)
		}
	}
}

func TestClassifyFailsOnARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	token := filepath.Join(t.TempDir(), "classifier")
	if err := os.WriteFile(token, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), []string{"classify", "--url", srv.URL, "--token-file", token, "--reply", "x"}, nil, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("classify against a 403 = %v, want an error naming 403", err)
	}
}
