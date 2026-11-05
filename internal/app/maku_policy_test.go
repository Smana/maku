// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
)

const policyConfig = "testdata/policy-factory.yaml"

func policyRepo(t *testing.T) string {
	t.Helper()
	c, err := config.Load(policyConfig)
	if err != nil {
		t.Fatal(err)
	}
	return c.Repos()[0].Name
}

func TestPolicyExportThenCheck(t *testing.T) {
	repo := policyRepo(t)
	var out bytes.Buffer
	m := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	if err := m.run(t.Context(), []string{"policy", "export", "--config", policyConfig, "--repository", repo}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	file := filepath.Join(t.TempDir(), "agent-factory.yaml")
	if err := os.WriteFile(file, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := m.run(t.Context(), []string{"policy", "check", "--config", policyConfig, "--repository", repo, file}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "equivalent") {
		t.Fatalf("%s", out.String())
	}
}

func TestPolicyCheckNamesWhatIsWrong(t *testing.T) {
	repo := policyRepo(t)
	file := filepath.Join(t.TempDir(), "agent-factory.yaml")
	if err := os.WriteFile(file, []byte("factory: aws-0\nclasses: {docs-links: {mode: live, author: agents, harness: plain}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), []string{"policy", "check", "--config", policyConfig, "--repository", repo, file})
	if err == nil || !strings.Contains(err.Error(), "revert") {
		t.Fatalf("got %v", err)
	}
}

func TestPolicyRefusesAnUnlistedRepository(t *testing.T) {
	var out bytes.Buffer
	err := Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), []string{"policy", "export", "--config", policyConfig, "--repository", "o/nope"})
	if err == nil || !strings.Contains(err.Error(), "not listed") {
		t.Fatalf("got %v", err)
	}
}

// check is for a repository's own CI: a file naming another factory gives the repository away.
func TestPolicyCheckRefusesAnotherFactory(t *testing.T) {
	repo := policyRepo(t)
	file := filepath.Join(t.TempDir(), "agent-factory.yaml")
	if err := os.WriteFile(file, []byte("factory: gcp-0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Maku{Dir: t.TempDir(), Out: &out, Err: &out}.run(t.Context(), []string{"policy", "check", "--config", policyConfig, "--repository", repo, file})
	if err == nil || !strings.Contains(err.Error(), "gcp-0") || !strings.Contains(err.Error(), "aws-0") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(out.String(), "equivalent") {
		t.Fatalf("%s", out.String())
	}
}
