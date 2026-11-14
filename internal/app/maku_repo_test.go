// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/install"
)

// ghReply is one canned answer of the GitHub API.
type ghReply struct {
	status int
	body   string
}

var (
	forbidden    = ghReply{403, `{"message":"Must have admin rights to Repository."}`}
	notProtected = ghReply{404, `{"message":"Branch not protected"}`}
	notFound     = ghReply{404, `{"message":"Not Found"}`}
)

// fakeGH stands in for the developer's gh: a reply per API path, the public repository
// acme/my-service on main with no required check and both Apps by default. Never the real API.
type fakeGH struct {
	repo, rules, protection, fence ghReply
	calls                          *[]string
}

func (f fakeGH) api(_ context.Context, path string) (int, []byte, error) {
	if f.calls != nil {
		*f.calls = append(*f.calls, path)
	}
	or := func(r, def ghReply) (int, []byte, error) {
		if r.status == 0 {
			r = def
		}
		return r.status, []byte(r.body), nil
	}
	switch path {
	case "repos/acme/my-service":
		return or(f.repo, ghReply{200, `{"default_branch":"main","private":false,"visibility":"public"}`})
	case "repos/acme/my-service/rules/branches/main":
		return or(f.rules, ghReply{200, `[]`})
	case "repos/acme/my-service/branches/main/protection/required_status_checks":
		return or(f.protection, notProtected)
	case "repos/acme/my-service/contents/.github/agent-factory.yaml?ref=main":
		return or(f.fence, ghReply{200, `{"encoding":"base64","content":"ZmFjdG9yeTogYWdlbnQtZmFjdG9yeQo="}`})
	case "user":
		return 200, []byte(`{"login":"alice"}`), nil
	case "apps/acme-factory":
		return 200, []byte(`{"id":123,"slug":"acme-factory","html_url":"https://github.com/apps/acme-factory"}`), nil
	case "apps/acme-agents":
		return 200, []byte(`{"id":124,"slug":"acme-agents","html_url":"https://github.com/apps/acme-agents"}`), nil
	}
	return 404, []byte(`{"message":"Not Found"}`), nil
}

func runRepoAddOut(t *testing.T, gh fakeGH, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	r := Maku{Dir: t.TempDir(), Out: &out, Err: &out, GH: gh.api}
	err := r.Run(t.Context(), append([]string{"repo", "add", "--factory-app", "acme-factory", "--agents-app", "acme-agents"}, args...))
	return out.String(), err
}

func runRepoAdd(t *testing.T, gh fakeGH, args ...string) error {
	t.Helper()
	_, err := runRepoAddOut(t, gh, args...)
	return err
}

const requiredCI = `[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"ci"},{"context":"maku/merge-gate","integration_id":123}]}}]`

// The checks are read from the branch's rulesets and its classic protection, maku/merge-gate left
// out (the factory posts it), and the files are RepoTree's, the two install links printed.
func TestRepoAddWritesTheRepositoryFromGitHub(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	out, err := runRepoAddOut(t, fakeGH{rules: ghReply{200, requiredCI},
		protection: ghReply{200, `{"contexts":["ci","lint"],"checks":[{"context":"lint"}]}`}}, "acme/my-service", "--out", dir)
	if err != nil {
		t.Fatal(err)
	}
	want, err := install.RepoTree(install.RepoInputs{Repository: "acme/my-service", Factory: "agent-factory", Namespace: "maku",
		Maintainers: []string{"alice"}, RequiredChecks: []string{"ci", "lint"}, LeakScanCheck: "ci", FactoryAppID: "123",
		FactoryLogin:      "acme-factory[bot]",
		FactoryInstallURL: "https://github.com/apps/acme-factory/installations/new",
		AgentsInstallURL:  "https://github.com/apps/acme-agents/installations/new"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range want.Paths() {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil || !bytes.Equal(got, want[p]) {
			t.Errorf("%s: %v\n%s", p, err, got)
		}
	}
	for _, s := range []string{"https://github.com/apps/acme-factory/installations/new", "https://github.com/apps/acme-agents/installations/new", "Nothing was applied"} {
		if !strings.Contains(out, s) {
			t.Errorf("the output lacks %q:\n%s", s, out)
		}
	}
}

// Review Focus 5: a protection the caller cannot read never becomes an empty requiredChecks.
func TestRepoAddUnreadableChecksFails(t *testing.T) {
	for name, gh := range map[string]fakeGH{
		"403 on the classic protection":          {protection: forbidden},
		"404 that is not 'Branch not protected'": {protection: notFound},
		"rules unreadable":                       {rules: forbidden, protection: ghReply{200, `{"contexts":["ci"]}`}},
	} {
		dir := t.TempDir()
		err := runRepoAdd(t, gh, "acme/my-service", "--out", dir)
		if err == nil || !strings.Contains(err.Error(), "cannot read the required checks of acme/my-service: pass --check") {
			t.Errorf("%s: got %v", name, err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) > 0 {
			t.Errorf("%s: wrote %d files", name, len(entries))
		}
	}
}

func TestRepoAddNoRequiredChecksFails(t *testing.T) { // readable, and empty
	err := runRepoAdd(t, fakeGH{}, "acme/my-service", "--out", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "no required check: the factory merges on one") {
		t.Fatalf("got %v", err)
	}
}

// --check names the checks: the protection is not read, so a caller without admin rights onboards.
func TestRepoAddCheckFlagSkipsTheRead(t *testing.T) {
	var calls []string
	dir := filepath.Join(t.TempDir(), "out")
	if err := runRepoAdd(t, fakeGH{protection: forbidden, rules: forbidden, calls: &calls}, "acme/my-service", "--out", dir,
		"--check", "build", "--check", "gitleaks", "--leak-scan-check", "gitleaks", "--maintainer", "bob"); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if strings.Contains(c, "protection") || strings.Contains(c, "rules/") || c == "user" {
			t.Errorf("read %s", c)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, "factory", "repository-my-service.yaml"))
	for _, s := range []string{"requiredChecks: [build, gitleaks]", "leakScanCheck: gitleaks", "maintainers: [bob]"} {
		if !bytes.Contains(got, []byte(s)) {
			t.Errorf("lacks %q:\n%s", s, got)
		}
	}
}

func TestRepoAddPrivateRepository(t *testing.T) {
	private := fakeGH{repo: ghReply{200, `{"default_branch":"main","private":true,"visibility":"private"}`}, rules: ghReply{200, requiredCI}}
	err := runRepoAdd(t, private, "acme/my-service", "--out", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "--acknowledge-private-code") {
		t.Fatalf("got %v", err)
	}
	dir := filepath.Join(t.TempDir(), "out")
	if err := runRepoAdd(t, private, "acme/my-service", "--out", dir, "--acknowledge-private-code"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "factory", "repository-my-service.yaml")); !bytes.Contains(got, []byte("privateCode: {acknowledged: true}")) {
		t.Fatalf("%s", got)
	}
}

func TestRepoAddRefusesAnExistingDirWithoutForce(t *testing.T) { // as maku install --print does
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	gh := fakeGH{rules: ghReply{200, requiredCI}}
	if err := runRepoAdd(t, gh, "acme/my-service", "--out", dir); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("got %v", err)
	}
	if err := runRepoAdd(t, gh, "acme/my-service", "--out", dir, "--force"); err != nil {
		t.Fatal(err)
	}
}

func TestRepoAddUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"no repository":  {"--out", "x"},
		"no --out":       {"acme/my-service"},
		"not owner/name": {"my-service", "--out", "x"},
	} {
		if err := runRepoAdd(t, fakeGH{}, args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	var out bytes.Buffer
	if err := (Maku{Dir: t.TempDir(), Out: &out, Err: &out, GH: fakeGH{}.api}).Run(t.Context(), []string{"repo", "add", "acme/my-service", "--out", "x"}); err == nil ||
		!strings.Contains(err.Error(), "--factory-app") {
		t.Errorf("without the Apps: %v", err)
	}
}
