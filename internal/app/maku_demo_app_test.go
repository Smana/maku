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
	"sync/atomic"
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
	out     *syncBuf
	err     chan error
	dir     string
	base    chan string
	conv    *httptest.Server
	gotAPI  chan string
	entered chan struct{}                 // a conversion signals here when it holds
	hold    atomic.Pointer[chan struct{}] // when set, a conversion waits for it to close
}

// startDemo runs `maku demo app` against a fake GitHub API; open reports the page URL.
func startDemo(t *testing.T, extra ...string) *demoRun {
	t.Helper()
	d := &demoRun{out: &syncBuf{}, err: make(chan error, 1), dir: filepath.Join(t.TempDir(), "app"),
		base: make(chan string, 1), gotAPI: make(chan string, 4), entered: make(chan struct{}, 4)}
	d.conv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := d.hold.Load(); h != nil {
			d.entered <- struct{}{}
			<-*h
		}
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
	if err := <-d.err; err != nil {
		t.Fatal(err)
	}
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

func (d *demoRun) getHost(t *testing.T, u, host string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := d.conv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func TestDemoAppRefusesAForeignHostAndASecondCallback(t *testing.T) {
	d := startDemo(t)
	action, manifest := d.page(t)
	var m struct {
		Redirect string `json:"redirect_url"`
	}
	_ = json.Unmarshal([]byte(manifest), &m)
	base := strings.TrimSuffix(m.Redirect, "/callback")
	state := func() string { u, _ := url.Parse(action); return u.Query().Get("state") }()
	if c := d.getHost(t, base+"/", "evil.example"); c != http.StatusMisdirectedRequest {
		t.Fatalf("page, foreign host: %d", c)
	}
	if c := d.getHost(t, base+"/callback?code=c&state="+state, "evil.example:80"); c != http.StatusMisdirectedRequest {
		t.Fatalf("callback, foreign host: %d", c)
	}
	hold := make(chan struct{})
	d.hold.Store(&hold)
	first := make(chan int, 1)
	go func() { first <- d.callback(t, base, "code=c&state="+state) }()
	select {
	case <-d.entered: // the first callback is inside its conversion
	case <-time.After(5 * time.Second):
		t.Fatal("the conversion never started")
	}
	if c := d.callback(t, base, "code=c2&state="+state); c != http.StatusBadRequest {
		t.Fatalf("second callback: %d", c)
	}
	close(hold)
	if c := <-first; c != http.StatusOK {
		t.Fatalf("first callback: %d", c)
	}
	if err := <-d.err; err != nil {
		t.Fatal(err)
	}
	if len(d.gotAPI) != 1 {
		t.Fatalf("conversions = %d", len(d.gotAPI))
	}
}

func TestDemoAppWriteReplacesASymlinkAndNarrowsTheDir(t *testing.T) {
	d := startDemo(t)
	if err := os.MkdirAll(d.dir, 0o755); err != nil { //nolint:gosec // the test needs a loose dir
		t.Fatal(err)
	}
	if err := os.Chmod(d.dir, 0o755); err != nil { //nolint:gosec // as above
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(d.dir, "private_key")); err != nil {
		t.Fatal(err)
	}
	action, manifest := d.page(t)
	var m struct {
		Redirect string `json:"redirect_url"`
	}
	_ = json.Unmarshal([]byte(manifest), &m)
	u, _ := url.Parse(action)
	d.callback(t, strings.TrimSuffix(m.Redirect, "/callback"), "code=c&state="+u.Query().Get("state"))
	if err := <-d.err; err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("the symlink was followed: %q", b)
	}
	fi, err := os.Lstat(filepath.Join(d.dir, "private_key"))
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("private_key: %v, %v", fi, err)
	}
	if di, _ := os.Stat(d.dir); di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", di.Mode().Perm())
	}
	if ents, _ := filepath.Glob(filepath.Join(d.dir, ".tmp-*")); len(ents) != 0 {
		t.Fatalf("temp files left: %v", ents)
	}
}

func TestDemoAppRefusedConversionLeaksNothing(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"message":"PLANTEDBODY","pem":"PLANTEDPEM"}`)
	}))
	defer api.Close()
	dir := filepath.Join(t.TempDir(), "app")
	base := make(chan string, 1)
	out := &syncBuf{}
	m := Maku{Dir: t.TempDir(), HC: api.Client(), Out: out, Err: io.Discard,
		Open: func(_ context.Context, u string) { base <- u }, DemoTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		errc <- m.Run(context.Background(), []string{"demo", "app", "--dir", dir, "--api", api.URL})
	}()
	d := &demoRun{base: base, conv: api}
	action, manifest := d.page(t)
	var mf struct {
		Redirect string `json:"redirect_url"`
	}
	_ = json.Unmarshal([]byte(manifest), &mf)
	u, _ := url.Parse(action)
	d.callback(t, strings.TrimSuffix(mf.Redirect, "/callback"), "code=c&state="+u.Query().Get("state"))
	err := <-errc
	if err == nil || strings.Contains(err.Error(), "PLANTED") || strings.Contains(out.String(), "PLANTED") {
		t.Fatalf("err = %v, out = %q", err, out.String())
	}
}

func TestDemoAppRejectsABadOwner(t *testing.T) {
	m := Maku{Dir: t.TempDir(), Out: io.Discard, Err: io.Discard, Open: func(context.Context, string) {}}
	err := m.Run(context.Background(), []string{"demo", "app", "--dir", t.TempDir(), "--owner", "../evil"})
	if err == nil || !strings.Contains(err.Error(), "--owner") {
		t.Fatalf("err = %v", err)
	}
}
