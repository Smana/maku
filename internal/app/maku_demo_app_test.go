// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const demoTestKey = "-----BEGIN RSA PRIVATE KEY-----\nSECRETKEYBODY\n-----END RSA PRIVATE KEY-----\n"

var (
	demoActionRE   = regexp.MustCompile(`action="([^"]*)"`)
	demoManifestRE = regexp.MustCompile(`name="manifest" value="([^"]*)"`)
)

// syncBuf is a goroutine-safe buffer: the callback runs on the server's goroutine.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type demoRun struct {
	out    *syncBuf
	err    chan error
	dir    string
	base   chan string
	conv   *httptest.Server
	gotAPI chan string
}

// startDemo runs `maku demo app` against a fake GitHub API; open reports the page URL.
func startDemo(t *testing.T, extra ...string) *demoRun {
	t.Helper()
	d := &demoRun{out: &syncBuf{}, err: make(chan error, 1), dir: filepath.Join(t.TempDir(), "app"),
		base: make(chan string, 1), gotAPI: make(chan string, 4)}
	d.conv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.gotAPI <- r.Method + " " + r.URL.Path + " auth=" + r.Header.Get("Authorization") + " accept=" + r.Header.Get("Accept")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 4242, "slug": "maku-demo-x", "pem": demoTestKey,
			"html_url": "https://github.com/apps/maku-demo-x", "webhook_secret": "WHSECRET", "client_secret": "CSECRET"})
	}))
	t.Cleanup(d.conv.Close)
	m := Maku{Dir: t.TempDir(), HC: d.conv.Client(), Out: d.out, Err: io.Discard,
		Open: func(_ context.Context, u string) { d.base <- u }, DemoTimeout: 10 * time.Second}
	args := append([]string{"demo", "app", "--dir", d.dir, "--api", d.conv.URL, "--github", "https://gh.example"}, extra...)
	go func() { d.err <- m.Run(context.Background(), args) }()
	return d
}

func (d *demoRun) page(t *testing.T) (action, manifest string) {
	t.Helper()
	var u string
	select {
	case u = <-d.base:
	case <-time.After(5 * time.Second):
		t.Fatal("the browser was never opened")
	}
	resp, err := d.get(t, u)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	a, m := demoActionRE.FindSubmatch(b), demoManifestRE.FindSubmatch(b)
	if a == nil || m == nil {
		t.Fatalf("form not found in %s", b)
	}
	return html.UnescapeString(string(a[1])), html.UnescapeString(string(m[1]))
}

func (d *demoRun) callback(t *testing.T, base, query string) int {
	t.Helper()
	resp, err := d.get(t, base+"/callback?"+query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestDemoAppFormCarriesTheManifest(t *testing.T) {
	d := startDemo(t, "--name", "my-demo", "--owner", "acme")
	action, manifest := d.page(t)
	u, err := url.Parse(action)
	if err != nil || u.Host != "gh.example" || u.Path != "/organizations/acme/settings/apps/new" || len(u.Query().Get("state")) < 32 {
		t.Fatalf("action = %q", action)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(manifest), &got); err != nil {
		t.Fatal(err)
	}
	redirect, _ := got["redirect_url"].(string)
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") || !strings.HasSuffix(redirect, "/callback") {
		t.Fatalf("redirect_url = %q", redirect)
	}
	delete(got, "redirect_url")
	want := `{"name":"my-demo","url":"https://github.com/Smana/maku","hook_attributes":{"url":"https://github.com/Smana/maku","active":false},"public":false,` +
		`"default_permissions":{"contents":"write","pull_requests":"write","issues":"write","checks":"read","statuses":"write","metadata":"read"},"default_events":[]}`
	var w map[string]any
	_ = json.Unmarshal([]byte(want), &w)
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("manifest\n got %s\nwant %s", gb, wb)
	}
	// Finish the flow so the server shuts down.
	base := strings.TrimSuffix(redirect, "/callback")
	d.callback(t, base, "code=c1&state="+u.Query().Get("state"))
	if err := <-d.err; err != nil {
		t.Fatal(err)
	}
}

func TestDemoAppPersonalAccountAction(t *testing.T) {
	d := startDemo(t)
	action, manifest := d.page(t)
	u, _ := url.Parse(action)
	if u.Path != "/settings/apps/new" {
		t.Fatalf("action = %q", action)
	}
	var m struct {
		Name     string `json:"name"`
		Redirect string `json:"redirect_url"`
	}
	_ = json.Unmarshal([]byte(manifest), &m)
	if !strings.HasPrefix(m.Name, "maku-demo-") {
		t.Fatalf("name = %q", m.Name)
	}
	d.callback(t, strings.TrimSuffix(m.Redirect, "/callback"), "code=c&state="+u.Query().Get("state"))
	<-d.err
}

func TestDemoAppWrongStateIsRefused(t *testing.T) {
	d := startDemo(t)
	action, manifest := d.page(t)
	var m struct {
		Redirect string `json:"redirect_url"`
	}
	_ = json.Unmarshal([]byte(manifest), &m)
	base := strings.TrimSuffix(m.Redirect, "/callback")
	if c := d.callback(t, base, "code=c&state=wrong"); c != http.StatusBadRequest {
		t.Fatalf("status = %d", c)
	}
	if c := d.callback(t, base, "code=c"); c != http.StatusBadRequest {
		t.Fatalf("no state: status = %d", c)
	}
	select {
	case g := <-d.gotAPI:
		t.Fatalf("GitHub was called with a wrong state: %s", g)
	default:
	}
	if _, err := os.Stat(d.dir); err == nil {
		t.Fatal("files written for a wrong state")
	}
	u, _ := url.Parse(action)
	d.callback(t, base, "code=c&state="+u.Query().Get("state"))
	if err := <-d.err; err != nil {
		t.Fatal(err)
	}
}

func TestDemoAppWritesCredentialsAndNeverPrintsTheKey(t *testing.T) {
	d := startDemo(t)
	action, manifest := d.page(t)
	var m struct {
		Redirect string `json:"redirect_url"`
	}
	_ = json.Unmarshal([]byte(manifest), &m)
	u, _ := url.Parse(action)
	if c := d.callback(t, strings.TrimSuffix(m.Redirect, "/callback"), "code=thecode&state="+u.Query().Get("state")); c != http.StatusOK {
		t.Fatalf("status = %d", c)
	}
	if err := <-d.err; err != nil {
		t.Fatal(err)
	}
	if g := <-d.gotAPI; g != "POST /app-manifests/thecode/conversions auth= accept=application/vnd.github+json" {
		t.Fatalf("conversion call = %q", g)
	}
	for name, want := range map[string]string{"private_key": demoTestKey, "app_id": "4242", "slug": "maku-demo-x", "html_url": "https://github.com/apps/maku-demo-x"} {
		b, err := os.ReadFile(filepath.Join(d.dir, name))
		if err != nil || string(b) != want {
			t.Fatalf("%s = %q, %v", name, b, err)
		}
		if fi, _ := os.Stat(filepath.Join(d.dir, name)); fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v", name, fi.Mode().Perm())
		}
	}
	if fi, _ := os.Stat(d.dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", fi.Mode().Perm())
	}
	out := d.out.String()
	for _, secret := range []string{"SECRETKEYBODY", "BEGIN RSA", "WHSECRET", "CSECRET"} {
		if strings.Contains(out, secret) {
			t.Fatalf("stdout leaks %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "Now install the App on your repository: https://github.com/apps/maku-demo-x/installations/new") {
		t.Fatalf("stdout:\n%s", out)
	}
}

func TestDemoAppTimeout(t *testing.T) {
	m := Maku{Dir: t.TempDir(), Out: &syncBuf{}, Err: io.Discard, Open: func(context.Context, string) {}, DemoTimeout: 50 * time.Millisecond}
	err := m.Run(context.Background(), []string{"demo", "app", "--dir", filepath.Join(t.TempDir(), "x")})
	if err == nil || !strings.Contains(err.Error(), "no GitHub callback") {
		t.Fatalf("err = %v", err)
	}
}

func TestDemoAppNeedsDir(t *testing.T) {
	m := Maku{Dir: t.TempDir(), Out: io.Discard, Err: io.Discard}
	if err := m.Run(context.Background(), []string{"demo", "app"}); err == nil || !strings.Contains(err.Error(), "--dir") {
		t.Fatalf("err = %v", err)
	}
}

func (d *demoRun) get(t *testing.T, u string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	return d.conv.Client().Do(req)
}
