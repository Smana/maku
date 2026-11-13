// SPDX-License-Identifier: Apache-2.0

package githubapps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	agentsPEM    = "-----BEGIN RSA PRIVATE KEY-----\nAGENTSKEYBODY\n-----END RSA PRIVATE KEY-----\n"
	factoryPEM   = "-----BEGIN RSA PRIVATE KEY-----\nFACTORYKEYBODY\n-----END RSA PRIVATE KEY-----\n"
	clientSecret = "FACTORYCLIENTSECRET"
	hookSecret   = "WEBHOOKSECRET"
)

var (
	actionRE   = regexp.MustCompile(`action="([^"]*)"`)
	manifestRE = regexp.MustCompile(`name="manifest" value="([^"]*)"`)
	// The states the deterministic Rand gives: 16 bytes of 0x11, then 16 of 0x22.
	state1 = strings.Repeat("11", 16)
	state2 = strings.Repeat("22", 16)
)

// plain reaches the loopback listener: the flow's own page and callback, never egress.
var plain = &http.Client{Timeout: 5 * time.Second}

// syncBuf is a goroutine-safe buffer: the callback runs on the server's goroutine.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// fakeGitHub answers a conversion per code: a1 is the agents App, f1 the factory App.
type fakeGitHub struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []string
}

func newFakeGitHub(t *testing.T, answers map[string]map[string]any) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.calls = append(g.calls, r.Method+" "+r.URL.Path+" auth="+r.Header.Get("Authorization"))
		g.mu.Unlock()
		_, rest, _ := strings.Cut(r.URL.Path, "/app-manifests/")
		code := strings.TrimSuffix(rest, "/conversions")
		a, ok := answers[code]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(a)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) Calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

func defaultAnswers() map[string]map[string]any {
	return map[string]map[string]any{
		"a1": {"id": 11, "slug": "acme-agents", "html_url": "https://github.com/apps/acme-agents", "pem": agentsPEM,
			"client_id": "Iv1.agents", "client_secret": "AGENTSCLIENTSECRET", "webhook_secret": hookSecret},
		"f1": {"id": 22, "slug": "acme-factory", "html_url": "https://github.com/apps/acme-factory", "pem": factoryPEM,
			"client_id": "Iv1.factory", "client_secret": clientSecret, "webhook_secret": hookSecret},
	}
}

func twoManifests(t *testing.T) []AppManifest {
	t.Helper()
	a, err := Manifest(AgentsApp, "acme-agents", ManifestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f, err := Manifest(FactoryApp, "acme-factory", ManifestOptions{WebURL: testWeb, Merger: MergerOptions{Status: true}})
	if err != nil {
		t.Fatal(err)
	}
	return []AppManifest{a, f}
}

type flowRun struct {
	out   *syncBuf
	base  chan string
	err   chan error
	apps  chan []App
	saved *[]Credentials
	gh    *fakeGitHub
}

// startFlow runs f against gh in the background; opening the browser reports the page's URL.
func startFlow(t *testing.T, f Flow, gh *fakeGitHub, ms ...AppManifest) *flowRun {
	t.Helper()
	r := &flowRun{out: &syncBuf{}, base: make(chan string, 1), err: make(chan error, 1), apps: make(chan []App, 1), saved: &[]Credentials{}, gh: gh}
	var mu sync.Mutex
	if f.API == "" {
		f.API = gh.srv.URL
	}
	f.HC = gh.srv.Client()
	f.Out = r.out
	f.Open = func(_ context.Context, u string) { r.base <- u }
	if f.Timeout == 0 {
		f.Timeout = 10 * time.Second
	}
	if f.Rand == nil {
		f.Rand = bytes.NewReader(append(bytes.Repeat([]byte{0x11}, 16), bytes.Repeat([]byte{0x22}, 16)...))
	}
	if f.Save == nil {
		f.Save = func(_ int, c Credentials) error {
			mu.Lock()
			defer mu.Unlock()
			*r.saved = append(*r.saved, c)
			return nil
		}
	}
	go func() {
		apps, err := f.Run(context.Background(), ms...)
		r.apps <- apps
		r.err <- err
	}()
	return r
}

func (r *flowRun) page(t *testing.T) string {
	t.Helper()
	select {
	case u := <-r.base:
		return strings.TrimSuffix(u, "/")
	case <-time.After(5 * time.Second):
		t.Fatal("the browser was never opened")
	}
	return ""
}

func get(t *testing.T, u string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := plain.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// form reads a form page: where it posts and the manifest it carries.
func form(t *testing.T, body string) (action *url.URL, m AppManifest) {
	t.Helper()
	a, mf := actionRE.FindStringSubmatch(body), manifestRE.FindStringSubmatch(body)
	if a == nil || mf == nil {
		t.Fatalf("form not found in %s", body)
	}
	u, err := url.Parse(html.UnescapeString(a[1]))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(mf[1])), &m); err != nil {
		t.Fatal(err)
	}
	return u, m
}

func TestFlowCreatesTwoAppsInOrderOnOneListener(t *testing.T) {
	gh := newFakeGitHub(t, defaultAnswers())
	r := startFlow(t, Flow{Command: "install", GitHub: "https://gh.example"}, gh, twoManifests(t)...)
	base := r.page(t)

	_, body := get(t, base+"/")
	action, m := form(t, body)
	if m.Name != "acme-agents" || m.RedirectURL != base+"/callback" || action.Query().Get("state") != state1 ||
		action.Host != "gh.example" || action.Path != "/settings/apps/new" {
		t.Fatalf("first form: %s, %+v", action, m)
	}
	// The first App's callback hands the browser straight on to the second App's form.
	code, body := get(t, base+"/callback?code=a1&state="+state1)
	if code != http.StatusOK {
		t.Fatalf("first callback: %d %s", code, body)
	}
	action, m = form(t, body)
	if m.Name != "acme-factory" || m.RedirectURL != base+"/callback" || action.Query().Get("state") != state2 {
		t.Fatalf("second form: %s, %+v", action, m)
	}
	if len(m.CallbackURLs) != 1 || m.CallbackURLs[0] != testWeb+"/link/github/callback" {
		t.Fatalf("second form's callback_urls = %v", m.CallbackURLs)
	}
	if code, body := get(t, base+"/callback?code=f1&state="+state2); code != http.StatusOK || !strings.Contains(body, "created") {
		t.Fatalf("second callback: %d %s", code, body)
	}
	if err := <-r.err; err != nil {
		t.Fatal(err)
	}
	apps := <-r.apps
	want := []App{
		{ID: 11, Slug: "acme-agents", HTMLURL: "https://github.com/apps/acme-agents", ClientID: "Iv1.agents"},
		{ID: 22, Slug: "acme-factory", HTMLURL: "https://github.com/apps/acme-factory", ClientID: "Iv1.factory"},
	}
	if fmt.Sprint(apps) != fmt.Sprint(want) {
		t.Fatalf("apps = %v, want %v", apps, want)
	}
	if apps[1].InstallURL() != "https://github.com/apps/acme-factory/installations/new" {
		t.Fatalf("install URL = %s", apps[1].InstallURL())
	}
	saved := *r.saved
	if len(saved) != 2 || saved[0].App != want[0] || saved[1].App != want[1] ||
		saved[0].PEM.Reveal() != agentsPEM || saved[1].PEM.Reveal() != factoryPEM || saved[1].ClientSecret.Reveal() != clientSecret {
		t.Fatalf("saved = %v", saved)
	}
	calls := gh.Calls()
	if len(calls) != 2 || calls[0] != "POST /app-manifests/a1/conversions auth=" || calls[1] != "POST /app-manifests/f1/conversions auth=" {
		t.Fatalf("conversions = %v", calls)
	}
}

// Each App has its own state, good for one callback: a wrong, missing, crossed or replayed state is
// refused before GitHub is called.
func TestFlowRefusesAReplayedOrCrossedState(t *testing.T) {
	gh := newFakeGitHub(t, defaultAnswers())
	r := startFlow(t, Flow{Command: "install"}, gh, twoManifests(t)...)
	base := r.page(t)
	for name, q := range map[string]string{
		"crossed": "code=a1&state=" + state2, "wrong": "code=a1&state=nope", "missing": "code=a1", "no code": "state=" + state1,
	} {
		if code, _ := get(t, base+"/callback?"+q); code != http.StatusBadRequest {
			t.Fatalf("first App, %s state: %d", name, code)
		}
	}
	if calls := gh.Calls(); len(calls) != 0 {
		t.Fatalf("GitHub was called: %v", calls)
	}
	if code, _ := get(t, base+"/callback?code=a1&state="+state1); code != http.StatusOK {
		t.Fatalf("first callback: %d", code)
	}
	if code, _ := get(t, base+"/callback?code=a1&state="+state1); code != http.StatusBadRequest {
		t.Fatalf("replayed state: %d", code)
	}
	if calls := gh.Calls(); len(calls) != 1 {
		t.Fatalf("a replay reached GitHub: %v", calls)
	}
	if code, _ := get(t, base+"/callback?code=f1&state="+state2); code != http.StatusOK {
		t.Fatalf("second callback: %d", code)
	}
	if err := <-r.err; err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/callback?code=f1&state="+state2, nil)
	if resp, err := plain.Do(req); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("a callback after the flow was served")
		}
	}
}

func TestFlowGHESPostsToTheOrganizationsSettings(t *testing.T) {
	gh := newFakeGitHub(t, defaultAnswers())
	ms := twoManifests(t)[:1]
	r := startFlow(t, Flow{Command: "install", GitHub: "https://ghe.example.com/", API: "", Owner: "acme"}, gh, ms...)
	base := r.page(t)
	_, body := get(t, base+"/")
	action, _ := form(t, body)
	if action.Scheme != "https" || action.Host != "ghe.example.com" || action.Path != "/organizations/acme/settings/apps/new" {
		t.Fatalf("action = %s", action)
	}
	get(t, base+"/callback?code=a1&state="+state1)
	if err := <-r.err; err != nil {
		t.Fatal(err)
	}

	// GHES serves its API under /api/v3.
	gh2 := newFakeGitHub(t, defaultAnswers())
	r = startFlow(t, Flow{Command: "install", GitHub: "https://ghe.example.com", API: gh2.srv.URL + "/api/v3", Owner: "acme"}, gh2, ms...)
	get(t, r.page(t)+"/callback?code=a1&state="+state1)
	if err := <-r.err; err != nil {
		t.Fatal(err)
	}
	if calls := gh2.Calls(); len(calls) != 1 || calls[0] != "POST /api/v3/app-manifests/a1/conversions auth=" {
		t.Fatalf("conversions = %v", calls)
	}
}

// captureStd runs fn with os.Stdout, os.Stderr and the log package's output, which slog's default
// logger writes through, captured.
func captureStd(t *testing.T, fn func()) string {
	t.Helper()
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr, oldLog := os.Stdout, os.Stderr, log.Writer()
	os.Stdout, os.Stderr = wr, wr
	log.SetOutput(wr)
	got := make(chan string, 1)
	go func() { b, _ := io.ReadAll(rd); got <- string(b) }()
	defer func() {
		os.Stdout, os.Stderr = oldOut, oldErr
		log.SetOutput(oldLog)
	}()
	fn()
	os.Stdout, os.Stderr = oldOut, oldErr
	log.SetOutput(oldLog)
	_ = wr.Close()
	return <-got
}

var secrets = []string{"AGENTSKEYBODY", "FACTORYKEYBODY", "BEGIN RSA", clientSecret, "AGENTSCLIENTSECRET", hookSecret, "PLANTEDCODE"}

func leaks(s string) string {
	for _, x := range secrets {
		if strings.Contains(s, x) {
			return x
		}
	}
	return ""
}

// R3: the conversion's keys and client secret reach Save, and nothing else: not the terminal, not the
// browser, not stdout, stderr or the log.
func TestFlowNeverPrintsTheKeys(t *testing.T) {
	var shown, errText string
	var apps []App
	std := captureStd(t, func() {
		answers := defaultAnswers()
		answers["PLANTEDCODE"] = answers["a1"]
		gh := newFakeGitHub(t, answers)
		r := startFlow(t, Flow{Command: "install"}, gh, twoManifests(t)...)
		base := r.page(t)
		_, b1 := get(t, base+"/callback?code=PLANTEDCODE&state="+state1)
		_, b2 := get(t, base+"/callback?code=f1&state="+state2)
		shown = b1 + b2
		if err := <-r.err; err != nil {
			errText = err.Error()
		}
		apps = <-r.apps
		shown += r.out.String()
	})
	if errText != "" {
		t.Fatal(errText)
	}
	for where, s := range map[string]string{"stdout/stderr/log": std, "the browser and Out": shown, "the apps": fmt.Sprintf("%v %+v %#v", apps, apps, apps)} {
		if x := leaks(s); x != "" {
			t.Fatalf("%s leaks %q:\n%s", where, x, s)
		}
	}
}

// Credentials print, encode and log redacted: a stray %v or a logged struct shows no key.
func TestCredentialsRedact(t *testing.T) {
	c := Credentials{App: App{ID: 1, Slug: "s"}, PEM: Secret(factoryPEM), ClientSecret: Secret(clientSecret)}
	var log bytes.Buffer
	slog.New(slog.NewJSONHandler(&log, nil)).Info("x", "c", c, "pem", c.PEM)
	j, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{fmt.Sprintf("%v %+v %#v %s %q %x", c, c, c, c.PEM, c.PEM, c.PEM), c.PEM.String(), c.PEM.GoString(), string(j), log.String()} {
		if x := leaks(s); x != "" {
			t.Fatalf("%q leaks %q", s, x)
		}
	}
	if c.PEM.Reveal() != factoryPEM {
		t.Fatal("Reveal is not the value")
	}
}

// A refused or failed conversion names the status only: the code and the answer are credentials.
func TestFlowConversionErrorsNameNoSecret(t *testing.T) {
	for name, hc := range map[string]http.HandlerFunc{
		"refused": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"message":"FACTORYKEYBODY","pem":"FACTORYKEYBODY"}`)
		},
		"not JSON": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `FACTORYKEYBODY`)
		},
		"no client secret": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":22,"slug":"f","html_url":"https://github.com/apps/f","pem":"FACTORYKEYBODY","client_id":"x"}`)
		},
	} {
		api := httptest.NewServer(hc)
		gh := &fakeGitHub{srv: api}
		saved := false
		r := startFlow(t, Flow{Command: "install", Save: func(int, Credentials) error { saved = true; return nil }}, gh, twoManifests(t)[1])
		code, body := get(t, r.page(t)+"/callback?code=PLANTEDCODE&state="+state1)
		err := <-r.err
		api.Close()
		if code != http.StatusBadGateway || err == nil || saved {
			t.Fatalf("%s: status %d, err %v, saved %v", name, code, err, saved)
		}
		if x := leaks(err.Error() + body + r.out.String()); x != "" {
			t.Fatalf("%s: leaks %q: %v %s", name, x, err, body)
		}
	}

	// A transport error carries the URL, and so the code: the flow drops it.
	api := httptest.NewServer(http.NotFoundHandler())
	gh := &fakeGitHub{srv: api}
	f := Flow{Command: "install", API: api.URL}
	api.Close()
	r := startFlow(t, f, gh, twoManifests(t)[0])
	get(t, r.page(t)+"/callback?code=PLANTEDCODE&state="+state1)
	if err := <-r.err; err == nil || leaks(err.Error()) != "" {
		t.Fatalf("transport error = %v", err)
	}
}

// A Save that fails stops the flow: the next App is never offered, so no key outlives its write.
func TestFlowStopsWhenSaveFails(t *testing.T) {
	gh := newFakeGitHub(t, defaultAnswers())
	r := startFlow(t, Flow{Command: "install", Save: func(int, Credentials) error { return errors.New("disk full") }}, gh, twoManifests(t)...)
	code, _ := get(t, r.page(t)+"/callback?code=a1&state="+state1)
	if err := <-r.err; err == nil || !strings.Contains(err.Error(), "disk full") || code != http.StatusBadGateway ||
		!strings.Contains(err.Error(), "https://github.com/apps/acme-agents") {
		t.Fatalf("status %d, err %v", code, err)
	}
}

func TestFlowTimeoutNamesTheCommand(t *testing.T) {
	gh := newFakeGitHub(t, defaultAnswers())
	r := startFlow(t, Flow{Command: "install", Timeout: 50 * time.Millisecond}, gh, twoManifests(t)...)
	r.page(t)
	if err := <-r.err; err == nil || !strings.Contains(err.Error(), "no GitHub callback") || !strings.Contains(err.Error(), "maku install") {
		t.Fatalf("err = %v", err)
	}
}

func TestFlowRefusesBadSetup(t *testing.T) {
	ms := twoManifests(t)
	save := func(int, Credentials) error { return nil }
	for name, tc := range map[string]struct {
		f    Flow
		ms   []AppManifest
		want string
	}{
		"no manifest": {Flow{Command: "install", Save: save}, nil, "no App"},
		"no save":     {Flow{Command: "install"}, ms, "Save"},
		"bad owner":   {Flow{Command: "install", Save: save, Owner: "../evil"}, ms, "organization"},
		"bad github":  {Flow{Command: "install", Save: save, GitHub: "javascript:alert(1)"}, ms, "GitHub"},
	} {
		_, err := tc.f.Run(context.Background(), tc.ms...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, tc.want)
		}
	}
}

// A request for any other Host is a DNS-rebinding page talking to the loopback port.
func TestFlowRefusesAForeignHost(t *testing.T) {
	gh := newFakeGitHub(t, defaultAnswers())
	r := startFlow(t, Flow{Command: "install"}, gh, twoManifests(t)[0])
	base := r.page(t)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/callback?code=a1&state="+state1, nil)
	req.Host = "evil.example"
	resp, err := plain.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest || len(gh.Calls()) != 0 {
		t.Fatalf("foreign host: %d, calls %v", resp.StatusCode, gh.Calls())
	}
	get(t, base+"/callback?code=a1&state="+state1)
	if err := <-r.err; err != nil {
		t.Fatal(err)
	}
}

// heldGitHub is a fake GitHub whose conversions wait for release once they have begun.
type heldGitHub struct {
	*fakeGitHub
	entered chan struct{}
	release chan struct{}
}

func newHeldGitHub(t *testing.T) *heldGitHub {
	t.Helper()
	h := &heldGitHub{fakeGitHub: &fakeGitHub{}, entered: make(chan struct{}, 4), release: make(chan struct{})}
	answers := defaultAnswers()
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.entered <- struct{}{}
		<-h.release
		_, rest, _ := strings.Cut(r.URL.Path, "/app-manifests/")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(answers[strings.TrimSuffix(rest, "/conversions")])
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *heldGitHub) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the conversion never started")
	}
}

// The code works once: a browser that gives up mid-conversion must not cost the App its key.
func TestFlowSavesWhenTheBrowserGoesAway(t *testing.T) {
	h := newHeldGitHub(t)
	r := startFlow(t, Flow{Command: "install"}, h.fakeGitHub, twoManifests(t)[0])
	base := r.page(t)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/callback?code=a1&state="+state1, nil)
		if resp, err := plain.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	h.waitEntered(t)
	cancel()                          // the tab is closed while GitHub converts the code
	time.Sleep(50 * time.Millisecond) // let the cancellation reach the server
	close(h.release)
	if err := <-r.err; err != nil {
		t.Fatal(err)
	}
	if saved := *r.saved; len(saved) != 1 || saved[0].PEM.Reveal() != agentsPEM {
		t.Fatalf("saved = %v", saved)
	}
}

// Run never returns while a conversion it started is still running: when it gives up, the App the
// conversion created is saved, and the error says it exists.
func TestFlowWaitsForAConversionInFlight(t *testing.T) {
	h := newHeldGitHub(t)
	var mu sync.Mutex
	var saved []string
	f := Flow{Command: "install", Timeout: 200 * time.Millisecond, Save: func(_ int, c Credentials) error {
		mu.Lock()
		defer mu.Unlock()
		saved = append(saved, c.Slug)
		return nil
	}}
	r := startFlow(t, f, h.fakeGitHub, twoManifests(t)...)
	base := r.page(t)
	go func() {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/callback?code=a1&state="+state1, nil)
		if resp, err := plain.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	h.waitEntered(t)
	time.AfterFunc(400*time.Millisecond, func() { close(h.release) }) // past the timeout
	err := <-r.err
	mu.Lock()
	defer mu.Unlock()
	if len(saved) != 1 {
		t.Fatalf("Run returned before the conversion's Save: saved %v, err %v", saved, err)
	}
	if err == nil || !strings.Contains(err.Error(), "acme-agents") || !strings.Contains(err.Error(), "https://github.com/apps/acme-agents") ||
		!strings.Contains(err.Error(), "exists") {
		t.Fatalf("err = %v, want it to name the App that exists", err)
	}
}

// When the second App fails, the first one exists and is saved: the error says so, by slug and URL.
func TestFlowFailureNamesTheAppsCreated(t *testing.T) {
	answers := defaultAnswers()
	delete(answers, "f1") // GitHub refuses the factory App's code
	gh := newFakeGitHub(t, answers)
	r := startFlow(t, Flow{Command: "install"}, gh, twoManifests(t)...)
	base := r.page(t)
	get(t, base+"/callback?code=a1&state="+state1)
	get(t, base+"/callback?code=f1&state="+state2)
	err := <-r.err
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") || !strings.Contains(err.Error(), "acme-agents") ||
		!strings.Contains(err.Error(), "https://github.com/apps/acme-agents") || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("err = %v", err)
	}
	if leaks(err.Error()) != "" {
		t.Fatalf("err leaks: %v", err)
	}
}

// The API base comes from the user's answers (GHES): the code goes to it, so it is https, or plain
// http on loopback only.
func TestFlowRefusesAPlainHTTPAPI(t *testing.T) {
	save := func(int, Credentials) error { return nil }
	ms := twoManifests(t)
	for _, api := range []string{"http://ghe.example.com/api/v3", "javascript:alert(1)", "https://", "ftp://ghe.example.com"} {
		_, err := Flow{Command: "install", Save: save, API: api}.Run(context.Background(), ms...)
		if err == nil || !strings.Contains(err.Error(), "API") {
			t.Errorf("API %q: err = %v", api, err)
		}
	}
	for _, api := range []string{"https://ghe.example.com/api/v3", "http://127.0.0.1:1", "http://[::1]:1", "http://localhost:1"} {
		if err := checkAPI(api); err != nil {
			t.Errorf("API %q refused: %v", api, err)
		}
	}
}
