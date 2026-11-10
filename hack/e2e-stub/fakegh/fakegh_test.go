// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/httpx"
)

type stubProcess struct {
	url    string
	caFile string
	client *http.Client
	logs   *bytes.Buffer
}

func startStub(t *testing.T) *stubProcess {
	t.Helper()
	dir := t.TempDir()
	certFile, keyFile, caFile := writeStubCertificate(t, dir)
	bin := filepath.Join(dir, "e2e-stub")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real stub: %v\n%s", err, out)
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:8443")
	if err != nil {
		t.Fatalf("stub test port is unavailable: %v", err)
	}
	_ = listener.Close()
	logs := &bytes.Buffer{}
	cmd := exec.CommandContext(t.Context(), bin)
	cmd.Env = append(os.Environ(), "E2E_STUB_TLS_CERT_FILE="+certFile, "E2E_STUB_TLS_KEY_FILE="+keyFile)
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &stubProcess{url: "https://127.0.0.1:8443", caFile: caFile, logs: logs}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(mustRead(t, caFile))
	s.client = httpx.New(500*time.Millisecond, roots)
	deadline := time.Now().Add(15 * time.Second)
	for {
		req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodGet, s.url+"/healthz", nil)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		resp, err := s.client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusNoContent {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("real stub did not become ready: %v\n%s", err, logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func writeStubCertificate(t *testing.T, dir string) (certFile, keyFile, caFile string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "e2e-stub"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"e2e-stub.e2e.svc.cluster.local"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certFile, keyFile, caFile = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), filepath.Join(dir, "ca.crt")
	for path, data := range map[string][]byte{certFile: certPEM, keyFile: keyPEM, caFile: certPEM} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return certFile, keyFile, caFile
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (s *stubProcess) getenv(key string) string {
	switch key {
	case "E2E_STUB_URL":
		return s.url
	case "E2E_STUB_CA_FILE":
		return s.caFile
	default:
		return ""
	}
}

func runFakeGH(t *testing.T, s *stubProcess, stdin string, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := run(t.Context(), args, strings.NewReader(stdin), &out, &errOut, s.getenv)
	return out.String() + errOut.String(), err
}

func stubRequest(t *testing.T, s *stubProcess, method, path string, body any, wantStatus int) json.RawMessage {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, s.url+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := httpx.ReadBody(resp.Body, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s status = %d, body %s; want %d", method, path, resp.StatusCode, raw, wantStatus)
	}
	return raw
}

type viewedIssue struct {
	Body   string `json:"body"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	UpdatedAt time.Time `json:"updatedAt"`
	Comments  []struct {
		ID     int64 `json:"id"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"createdAt"`
	} `json:"comments"`
}

func viewIssue(t *testing.T, s *stubProcess) viewedIssue {
	t.Helper()
	out, err := runFakeGH(t, s, "", "issue", "view", "1", "--repo=owner/repo", "--json", "body,labels,updatedAt,comments")
	if err != nil {
		t.Fatalf("fakegh view: %v\n%s", err, out)
	}
	var issue viewedIssue
	if err := json.Unmarshal([]byte(out), &issue); err != nil {
		t.Fatalf("view is not GitHub-shaped JSON: %v\n%s", err, out)
	}
	return issue
}

func TestFakeGHCreateViewAndEditUseTheRealStubStore(t *testing.T) {
	s := startStub(t)
	body := "## What is wrong\nMissing verification.\n--add-label=not-a-flag\n## Acceptance check\ntask check"
	out, err := runFakeGH(t, s, body, "issue", "create", "--repo=owner/repo", "--title=docs: verification", "--body-file=-")
	if err != nil || out != "https://github.com/owner/repo/issues/1\n" {
		t.Fatalf("create output/error = %q, %v", out, err)
	}
	created := viewIssue(t, s)
	if created.Body != body || len(created.Labels) != 0 || created.UpdatedAt.IsZero() {
		t.Fatalf("created issue = %#v", created)
	}

	stubRequest(t, s, http.MethodPost, "/repos/owner/repo/issues/1/comments", map[string]any{"body": "factory status"}, http.StatusCreated)
	withComment := viewIssue(t, s)
	if len(withComment.Comments) != 1 || withComment.Comments[0].ID != 1 ||
		withComment.Comments[0].Author.Login != "maku-demo[bot]" || withComment.Comments[0].Body != "factory status" {
		t.Fatalf("comments = %#v", withComment.Comments)
	}

	if out, err := runFakeGH(t, s, "", "issue", "edit", "1", "--repo=owner/repo", "--add-label=factory/ready"); err != nil {
		t.Fatalf("label edit: %v\n%s", err, out)
	}
	labeled := viewIssue(t, s)
	if len(labeled.Labels) != 1 || labeled.Labels[0].Name != "factory/ready" || !labeled.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("labeled issue = %#v; created updatedAt %s", labeled, created.UpdatedAt)
	}
	events := stubRequest(t, s, http.MethodGet, "/repos/owner/repo/issues/1/events?per_page=100", nil, http.StatusOK)
	var actors []struct {
		Actor struct {
			Login string `json:"login"`
		} `json:"actor"`
		Label struct {
			Name string `json:"name"`
		} `json:"label"`
	}
	if err := json.Unmarshal(events, &actors); err != nil || len(actors) != 1 || actors[0].Actor.Login != "dev" || actors[0].Label.Name != "factory/ready" {
		t.Fatalf("label events = %s, decoded %#v, err %v", events, actors, err)
	}

	newBody := body + "\n\n## Decisions\n\nD1: use the stub"
	if out, err := runFakeGH(t, s, newBody, "issue", "edit", "1", "--repo=owner/repo", "--body-file=-"); err != nil {
		t.Fatalf("body edit: %v\n%s", err, out)
	}
	edited := viewIssue(t, s)
	if edited.Body != newBody || len(edited.Labels) != 1 || edited.Labels[0].Name != "factory/ready" || !edited.UpdatedAt.After(labeled.UpdatedAt) {
		t.Fatalf("edited issue = %#v; labeled updatedAt %s", edited, labeled.UpdatedAt)
	}
}

func TestFakeGHRequiresAnExplicitNonGitHubStub(t *testing.T) {
	for name, value := range map[string]string{
		"missing":       "",
		"GitHub API":    "https://api.github.com/",
		"GitHub host":   "https://github.com/",
		"non-HTTPS DNS": "http://e2e-stub.e2e.svc.cluster.local",
	} {
		t.Run(name, func(t *testing.T) {
			getenv := func(key string) string {
				if key == "E2E_STUB_URL" {
					return value
				}
				return ""
			}
			if _, err := stubBaseURL(getenv); err == nil {
				t.Fatalf("stubBaseURL(%q) succeeded", value)
			}
		})
	}
	getenv := func(string) string { return "" }
	var out bytes.Buffer
	if err := run(t.Context(), []string{"api", "repos/owner/repo/issues"}, strings.NewReader(""), &out, &out, getenv); err == nil ||
		!strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported gh command error = %v", err)
	}
}

func TestFakeGHRefusesUnsupportedFormsBeforeNetwork(t *testing.T) {
	s := startStub(t)
	for _, args := range [][]string{
		{"issue", "list", "--repo=owner/repo"},
		{"issue", "create", "--repo=owner/repo", "--title=x", "--body=inline"},
		{"issue", "view", "1", "--repo=owner/repo", "--json", "title"},
		{"issue", "edit", "1", "--repo=owner/repo", "--remove-label=factory/ready"},
		{"issue", "edit", "1", "--repo=owner/repo", "--add-label=a", "--body-file=-"},
	} {
		if out, err := runFakeGH(t, s, "", args...); err == nil {
			t.Fatalf("%v succeeded with output %q", args, out)
		}
	}
	if out, err := runFakeGH(t, s, "must not replace the body", "issue", "view", "1", "--repo=owner/repo", "--json", "body,labels,updatedAt,comments"); err == nil {
		t.Fatalf("view of missing issue succeeded: %s", out)
	} else if !strings.Contains(err.Error(), "404") && !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing issue error = %v", err)
	}
}

func TestFakeGHBodyInputIsBounded(t *testing.T) {
	s := startStub(t)
	tooLarge := strings.Repeat("x", maxBody+1)
	out, err := runFakeGH(t, s, tooLarge, "issue", "create", "--repo=owner/repo", "--title=docs: too large", "--body-file=-")
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized body output/error = %q, %v", out, err)
	}
}
