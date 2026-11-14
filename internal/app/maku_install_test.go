// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/githubapps"
	"github.com/Smana/maku/internal/install"
)

// installRand is the generated keys the install tests expect: internal/install's goldens' cookie and
// webhook keys, in that order.
func installRand() *bytes.Reader {
	return bytes.NewReader([]byte("test-cookie-secret-32-bytes-long" + "test-webhook-key-32-bytes-long-x"))
}

func noDigest(context.Context, string, string) (string, error) {
	panic("the answers pin every image: no registry read")
}

func installMaku(t *testing.T, stdin string) (Maku, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	return Maku{Dir: t.TempDir(), Out: &out, Err: &out, In: strings.NewReader(stdin), Rand: installRand(), Digest: noDigest}, &out
}

// goldenTree reads internal/install's golden tree for name.
func goldenTree(t *testing.T, name string) map[string][]byte {
	t.Helper()
	dir := filepath.Join("..", "install", "testdata", "golden", name)
	m := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, err := os.ReadFile(p)
		m[strings.ReplaceAll(filepath.ToSlash(rel), "dot-gitignore", ".gitignore")] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func assertTree(t *testing.T, dir string, want map[string][]byte) {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		got, _ := os.ReadFile(p)
		n++
		if w, ok := want[filepath.ToSlash(rel)]; !ok || !bytes.Equal(w, got) {
			t.Errorf("%s differs from the golden", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != len(want) {
		t.Errorf("wrote %d files, the golden has %d", n, len(want))
	}
}

// IN6: --answers writes the golden tree.
func TestInstallAnswersFile(t *testing.T) {
	m, out := installMaku(t, "")
	dir := filepath.Join(t.TempDir(), "out")
	answers := filepath.Join("..", "install", "testdata", "answers", "generic.yaml")
	if err := m.Run(context.Background(), []string{"install", "--print", "--out", dir, "--answers", answers}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	assertTree(t, dir, goldenTree(t, "generic"))
	if !strings.Contains(out.String(), "Nothing was applied") {
		t.Errorf("the summary: %s", out)
	}
}

// A scripted interactive run, one line per question, writes the same tree.
func TestInstallInteractive(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "install", "testdata", "answers", "generic.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := install.DecodeAnswers(raw)
	if err != nil {
		t.Fatal(err)
	}
	if a, _, err = install.Resolve(a); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, q := range install.Questions {
		if !q.Asked(a) {
			continue
		}
		switch v := a[q.ID].(type) {
		case bool:
			lines = append(lines, map[bool]string{true: "yes", false: "no"}[v])
		case []string:
			lines = append(lines, strings.Join(v, ","))
		case string:
			lines = append(lines, v)
		}
	}
	m, out := installMaku(t, strings.Join(lines, "\n")+"\n")
	dir := filepath.Join(t.TempDir(), "out")
	if err := m.Run(context.Background(), []string{"install", "--print", "--out", dir}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	assertTree(t, dir, goldenTree(t, "generic"))
}

// IN6: the bare command is refused, --out - with App creation is refused, and --out - prints
// values.yaml alone.
func TestInstallRefusals(t *testing.T) {
	m, _ := installMaku(t, "")
	if err := m.Run(context.Background(), []string{"install"}); err == nil || err.Error() != "maku install only writes files: run maku install --print --out DIR" {
		t.Errorf("bare maku install: %v", err)
	}
	ghes := filepath.Join("..", "install", "testdata", "answers", "ghes.yaml")
	if err := m.Run(context.Background(), []string{"install", "--print", "--out", "-", "--answers", ghes}); err == nil || !strings.Contains(err.Error(), "creating the Apps needs secrets/") {
		t.Errorf("--out - with App creation: %v", err)
	}
	m, out := installMaku(t, "")
	m.Err = io.Discard // its note on the Secrets it skips
	generic := filepath.Join("..", "install", "testdata", "answers", "generic.yaml")
	if err := m.Run(context.Background(), []string{"install", "--print", "--out", "-", "--answers", generic}); err != nil {
		t.Fatal(err)
	}
	if want := goldenTree(t, "generic")["values.yaml"]; !bytes.Equal(out.Bytes(), want) {
		t.Errorf("--out - does not print values.yaml alone:\n%s", out)
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("namespace: maku\nnamepsace: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Run(context.Background(), []string{"install", "--print", "--out", t.TempDir(), "--answers", bad}); err == nil || !strings.Contains(err.Error(), `unknown key "namepsace"`) {
		t.Errorf("an unknown answers key: %v", err)
	}
}

// IN8: the two Apps through one manifest flow: the agents App, then the factory App with the merger
// folded in and the account link's callback; their keys reach secrets/ only, and the tree is the
// ghes golden's.
func TestInstallCreatesBothApps(t *testing.T) {
	pem := "-----BEGIN RSA PRIVATE KEY-----\ntest-not-a-key\n-----END RSA PRIVATE KEY-----\n"
	m, out := installMaku(t, "")
	var names []string
	m.RunFlow = func(_ context.Context, f githubapps.Flow, ms ...githubapps.AppManifest) ([]githubapps.App, error) {
		if f.GitHub != "https://ghe.example.com" || f.API != "https://ghe.example.com/api/v3" || f.Owner != "platform" {
			t.Errorf("the flow targets %s, %s, %s", f.GitHub, f.API, f.Owner)
		}
		apps := []githubapps.App{
			{ID: 3004, Slug: "maku-platform-agents", HTMLURL: "https://ghe.example.com/github-apps/maku-platform-agents", ClientID: "Iv1.agents"},
			{ID: 3003, Slug: "maku-platform-factory", HTMLURL: "https://ghe.example.com/github-apps/maku-platform-factory", ClientID: "Iv1.factory"},
		}
		for i, mf := range ms {
			names = append(names, mf.Name)
			c := githubapps.Credentials{App: apps[i], PEM: githubapps.Secret(pem)}
			if i == 1 {
				c.ClientSecret = "test-oauth-client-secret"
			}
			if err := f.Save(i, c); err != nil {
				return nil, err
			}
		}
		if got := ms[1].CallbackURLs; len(got) != 1 || got[0] != "https://rooms.example.com/link/github/callback" {
			t.Errorf("the factory App's callback is %v", got)
		}
		if ms[1].DefaultPermissions["statuses"] != "write" {
			t.Errorf("the factory App does not fold the merger's statuses: %v", ms[1].DefaultPermissions)
		}
		return apps, nil
	}
	dir := filepath.Join(t.TempDir(), "out")
	ghes := filepath.Join("..", "install", "testdata", "answers", "ghes.yaml")
	if err := m.Run(context.Background(), []string{"install", "--print", "--out", dir, "--answers", ghes, "--no-wait"}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Join(names, " ") != "maku-platform-agents maku-platform-factory" {
		t.Errorf("the Apps created: %v", names)
	}
	assertTree(t, dir, goldenTree(t, "ghes"))
	if strings.Contains(out.String(), "PRIVATE KEY") || strings.Contains(out.String(), "test-oauth-client-secret") {
		t.Errorf("the output holds key material:\n%s", out)
	}
	if !strings.Contains(out.String(), "/github-apps/maku-platform-factory/installations/new") {
		t.Errorf("no install URL printed:\n%s", out)
	}
}

// Review of #565: every refusal of --out comes before a question or an App: GitHub returns an App's
// key once, so a refused write after the flow would drop it and orphan the Apps.
func TestInstallRefusesOutBeforeTheFlow(t *testing.T) {
	ghes := filepath.Join("..", "install", "testdata", "answers", "ghes.yaml")
	for name, setup := range map[string]func(t *testing.T, dir string) []string{
		"a non-empty --out": func(t *testing.T, dir string) []string {
			if err := os.WriteFile(filepath.Join(dir, "x"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return nil
		},
		"an existing secrets/ under --force": func(t *testing.T, dir string) []string {
			if err := os.Mkdir(filepath.Join(dir, "secrets"), 0o700); err != nil {
				t.Fatal(err)
			}
			return []string{"--force"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			extra := setup(t, dir)
			m, _ := installMaku(t, "")
			m.RunFlow = func(context.Context, githubapps.Flow, ...githubapps.AppManifest) ([]githubapps.App, error) {
				t.Fatal("the App flow ran before the refusal")
				return nil, nil
			}
			m.Digest = func(context.Context, string, string) (string, error) {
				t.Fatal("the registry was read before the refusal")
				return "", nil
			}
			args := append([]string{"install", "--print", "--out", dir, "--answers", ghes}, extra...)
			if err := m.Run(context.Background(), args); err == nil {
				t.Fatal("the write was not refused")
			}
		})
	}
	// and an interactive run is refused before its first question
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m, out := installMaku(t, "maku\n")
	if err := m.Run(context.Background(), []string{"install", "--print", "--out", dir}); err == nil || out.Len() != 0 {
		t.Errorf("an interactive run into a non-empty --out: %v, asked %q", err, out)
	}
}

// Review of #565: --out - writes no Secret, so it generates no key, and says which Secrets the user
// brings instead of dropping them silently.
func TestInstallOutDashNamesTheSecretsItSkips(t *testing.T) {
	m, out := installMaku(t, "")
	var errBuf bytes.Buffer
	m.Err = &errBuf
	m.Rand = bytes.NewReader(nil) // no key may be drawn
	generic := filepath.Join("..", "install", "testdata", "answers", "generic.yaml")
	if err := m.Run(context.Background(), []string{"install", "--print", "--out", "-", "--answers", generic}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errBuf.String(), "rooms-web-proxy") || !strings.Contains(errBuf.String(), "--out DIR") {
		t.Errorf("--out - does not name the Secrets it skips: %q", errBuf.String())
	}
	if strings.Contains(out.String(), "cookie") {
		t.Errorf("values.yaml mentions a cookie secret:\n%s", out)
	}
}

// Review of #565: an App's key never prints, even through a %v of the App.
func TestAppInfoRedactsItsKeys(t *testing.T) {
	a := install.AppInfo{ID: 1, Slug: "s", PEM: githubapps.Secret("-----BEGIN RSA PRIVATE KEY-----x"), ClientSecret: githubapps.Secret("oauth-secret")}
	for _, s := range []string{fmt.Sprintf("%v", a), fmt.Sprintf("%+v", a), fmt.Sprintf("%#v", a)} {
		if strings.Contains(s, "PRIVATE KEY") || strings.Contains(s, "oauth-secret") {
			t.Errorf("an AppInfo prints its keys: %s", s)
		}
	}
}
