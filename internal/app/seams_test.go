// SPDX-License-Identifier: Apache-2.0

package app

import (
	"os"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/config"
	fconfig "github.com/Smana/maku/internal/factory/config"
)

// fakeFiles reads a fixed set of files: a stand-in for the pod's mounts.
func fakeFiles(files map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if v, ok := files[p]; ok {
			return []byte(v), nil
		}
		return nil, os.ErrNotExist
	}
}

// v4 D14: a broker whose config names a mounted Secret's file that is missing or empty does not
// start, and says which Secret and key; with every file present it starts as before.
func TestBrokerSecretSeams(t *testing.T) {
	cfg := config.Config{Human: config.HumanConfig{ClientIDFile: "/etc/room-broker/oidc/web-client-id",
		RoomctlClientIDFile: "/etc/room-broker/oidc/cli-client-id"}}
	cfg.TLS.CertFile, cfg.TLS.KeyFile = "/etc/room-broker/tls/tls.crt", "/etc/room-broker/tls/tls.key"
	all := map[string]string{"/etc/room-broker/oidc/web-client-id": "rooms-web", "/etc/room-broker/oidc/cli-client-id": "maku-cli",
		"/etc/room-broker/tls/tls.crt": "crt", "/etc/room-broker/tls/tls.key": "key"}
	if err := checkBrokerSecrets(cfg, fakeFiles(all)); err != nil {
		t.Fatalf("every file present: %v", err)
	}
	all["/etc/room-broker/oidc/cli-client-id"] = "\n"
	err := checkBrokerSecrets(cfg, fakeFiles(all))
	want := "brokerOIDC: Secret broker.oidc.existingSecret key cli-client-id is missing or empty (docs/install-secrets.md)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want %q", err, want)
	}
	// The plan's case: the GitHub link's client secret mounted empty.
	cfg.Human.GitHubLink = &config.GitHubLinkConfig{ClientIDFile: "/etc/room-broker/github-link/client_id",
		ClientSecretFile: "/etc/room-broker/github-link/client_secret"}
	all["/etc/room-broker/oidc/cli-client-id"] = "maku-cli"
	all["/etc/room-broker/github-link/client_id"], all["/etc/room-broker/github-link/client_secret"] = "Iv1.abc", ""
	err = checkBrokerSecrets(cfg, fakeFiles(all))
	want = "githubLink: Secret broker.githubLink.existingSecret key client_secret is missing or empty (docs/install-secrets.md)"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
	// Files outside every chart mount are not this check's: a hand-written manifest's paths.
	if err := checkBrokerSecrets(config.Config{Human: config.HumanConfig{ClientIDFile: "/etc/e2e/config/client-id"}}, fakeFiles(nil)); err != nil {
		t.Fatal(err)
	}
}

// The factory names its missing App key the same way; an optional mount (the intake's token) is
// refused at use, as before, never at start.
func TestFactorySecretSeams(t *testing.T) {
	cfg := &fconfig.Config{}
	cfg.GitHub.AppIDFile, cfg.GitHub.PrivateKeyFile = "/etc/agent-factory-github/app_id", "/etc/agent-factory-github/private_key"
	cfg.RunLore.TokenFile = "/etc/agent-factory-intake/token"
	cfg.API.ClientIDFiles = []string{"/etc/agent-factory-oidc/rooms-proxy-client-id"}
	files := map[string]string{"/etc/agent-factory-github/app_id": "1", "/etc/agent-factory-oidc/rooms-proxy-client-id": "web"}
	err := checkFactorySecrets(cfg, fakeFiles(files))
	want := "factoryApp: Secret agent-factory-github key private_key is missing or empty (docs/install-secrets.md)"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
	files["/etc/agent-factory-github/private_key"] = "pem"
	if err := checkFactorySecrets(cfg, fakeFiles(files)); err != nil {
		t.Fatalf("every file present: %v", err)
	}
}
