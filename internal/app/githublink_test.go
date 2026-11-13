// SPDX-License-Identifier: Apache-2.0

package app

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/config"
)

// The client's files are read at use, trimmed; a missing or empty one names its path, never its
// content.
func TestGitHubLinkerReadsItsClientAtUse(t *testing.T) {
	dir := t.TempDir()
	id, secret := filepath.Join(dir, "client_id"), filepath.Join(dir, "client_secret")
	if err := os.WriteFile(id, []byte(" Iv1.abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gl, err := githubLinker(&config.GitHubLinkConfig{ClientIDFile: id, ClientSecretFile: secret, WebURL: "https://ghes.example.test"}, nil, nil)
	if err != nil || gl.WebOrigin != "https://ghes.example.test" {
		t.Fatalf("%+v %v", gl, err)
	}
	if v, err := gl.ClientID(); err != nil || v != "Iv1.abc" {
		t.Fatalf("client id: %q %v", v, err)
	}
	if _, err := gl.ClientSecret(); err == nil || !strings.Contains(err.Error(), secret) {
		t.Fatalf("a missing secret: %v", err)
	}
	if err := os.WriteFile(secret, []byte("s3cr3t-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := gl.ClientSecret(); err != nil || v != "s3cr3t-value" {
		t.Fatalf("a rotated secret: %q %v", v, err)
	}
	if err := os.WriteFile(secret, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gl.ClientSecret(); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("an empty secret: %v", err)
	}
}

// github-unlink needs one --sub and the broker's DSN, and refuses anything else before it connects.
func TestGitHubUnlinkArguments(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := func(string) string { return "" }
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no sub":       {nil, "--sub is required"},
		"a long sub":   {[]string{"--sub", strings.Repeat("a", 129)}, "--sub is required"},
		"an extra arg": {[]string{"--sub", "s", "x"}, `unexpected argument "x"`},
		"no DSN":       {[]string{"--sub", "s"}, "ROOMS_DATABASE_URL is not set"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := RunBroker(t.Context(), log, append([]string{"github-unlink"}, tc.args...), env); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
