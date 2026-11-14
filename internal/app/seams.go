// SPDX-License-Identifier: Apache-2.0

package app

import (
	"os"
	"path/filepath"

	"github.com/Smana/maku/internal/config"
	fconfig "github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/install"
)

// readMounted reads one mounted file for the start-up check.
func readMounted(p string) ([]byte, error) { return os.ReadFile(filepath.Clean(p)) }

// checkBrokerSecrets refuses to start a broker whose config names a file of a mounted Secret that is
// missing or empty, naming the Secret and its key (v4 D14). Files outside the chart's mounts are read
// at use, as before.
func checkBrokerSecrets(cfg config.Config, read func(string) ([]byte, error)) error {
	h := cfg.Human
	files := []string{h.ClientIDFile, h.RoomctlClientIDFile, h.ProjectIDFile, h.AudienceFile, cfg.FactoryCAFile, cfg.TLS.CertFile, cfg.TLS.KeyFile}
	if l := h.GitHubLink; l != nil {
		files = append(files, l.ClientIDFile, l.ClientSecretFile)
	}
	return install.CheckFiles(read, files...)
}

// checkFactorySecrets is checkBrokerSecrets for the factory's config.
func checkFactorySecrets(cfg *fconfig.Config, read func(string) ([]byte, error)) error {
	g := cfg.GitHub
	files := []string{g.AppIDFile, g.PrivateKeyFile, g.MergerAppIDFile, g.MergerKeyFile, g.AgentsAppIDFile, g.AgentsKeyFile,
		cfg.Broker.CAFile, cfg.RunLore.TokenFile,
		cfg.API.ProjectIDFile, cfg.API.AudienceFile}
	files = append(files, cfg.API.ClientIDFiles...)
	if a := cfg.API.Access; a != nil {
		files = append(files, a.ReaderFile)
	}
	if t := cfg.API.TLS; t != nil {
		files = append(files, t.CertFile, t.KeyFile)
	}
	for _, sp := range cfg.API.Sponsored {
		files = append(files, sp.ClientIDFile)
	}
	return install.CheckFiles(read, files...)
}
