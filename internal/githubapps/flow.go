// SPDX-License-Identifier: Apache-2.0

package githubapps

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Smana/maku/internal/httpx"
)

const (
	defaultTimeout = 5 * time.Minute
	maxBody        = 1 << 20
	// convertTimeout bounds one code's conversion and Save. They run on Run's context without its
	// cancellation: once GitHub has consumed the one-time code, the key must reach Save.
	convertTimeout = time.Minute
	redacted       = "[redacted]"
)

var ownerRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

var formPage = template.Must(template.New("form").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>maku {{.Command}}</title></head>
<body onload="document.getElementById('f').submit()">
<form id="f" method="post" action="{{.Action}}">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<noscript><button type="submit">Create the GitHub App {{.Name}}</button></noscript>
</form>
<p>Sending you to GitHub to create the App {{.Name}}...</p>
</body></html>`))

// Secret is a credential GitHub returned once. It prints, encodes and logs as [redacted]; Reveal is
// how its value reaches the 0600 file it goes to, and nowhere else.
type Secret string

// Reveal is the value, for the file it is written to.
func (s Secret) Reveal() string { return string(s) }

// String is [redacted].
func (Secret) String() string { return redacted }

// GoString is [redacted], for %#v.
func (Secret) GoString() string { return redacted }

// Format writes [redacted] whatever the verb.
func (Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// MarshalJSON is "[redacted]".
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// MarshalText is [redacted].
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// LogValue is [redacted].
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// UnmarshalJSON reads the value from GitHub's answer.
func (s *Secret) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, (*string)(s)) }

// App is what may be shown of a created App.
type App struct {
	ID       int64
	Slug     string
	HTMLURL  string
	ClientID string
}

// InstallURL is where the App is installed on the owner's repositories.
func (a App) InstallURL() string { return strings.TrimRight(a.HTMLURL, "/") + "/installations/new" }

// Credentials are an App and the secrets its conversion returned. The webhook secret is not kept: no
// App has an active webhook.
type Credentials struct {
	App
	PEM          Secret // the App's private key
	ClientSecret Secret // the App's OAuth client secret: the factory App's account link (R5)
}

// Flow runs GitHub's App manifest flow for one App or several, one after the other, on one loopback
// listener. Each App has its own state, checked in constant time and good for one callback. The
// callback's code is traded for the App's credentials, which go to Save and nowhere else.
type Flow struct {
	Command string                              // the maku command errors name, such as "demo app" or "install"
	GitHub  string                              // the web base the form posts to; empty is https://github.com
	API     string                              // the API base; empty is https://api.github.com
	Owner   string                              // an organization to own the Apps; empty is the user's account
	HC      *http.Client                        // the conversion call; nil is a 30 s client
	Out     io.Writer                           // progress lines; never a credential
	Open    func(ctx context.Context, u string) // opens the browser; nil only prints the URL
	Timeout time.Duration                       // the wait for each App's callback; zero is 5 min
	Rand    io.Reader                           // the states; nil is crypto/rand
	// Save persists the i-th App's credentials before the browser is told it exists. An error stops the
	// flow: the next App is not offered. A rerun after a failure passes only the manifests of the Apps
	// not yet saved: the error names those that exist.
	Save func(i int, c Credentials) error
}

// step is one App's turn in the flow.
type step struct {
	manifest []byte
	name     string
	action   string
	state    string
	oauth    bool // the App's OAuth client is required (it has callback URLs)
}

// Run creates the Apps ms describes, in order, and returns what may be shown of them.
func (f Flow) Run(ctx context.Context, ms ...AppManifest) ([]App, error) {
	cmd := f.Command
	if len(ms) == 0 {
		return nil, fmt.Errorf("%s: no App to create", cmd)
	}
	if f.Save == nil {
		return nil, fmt.Errorf("%s: the flow has no Save for the Apps' credentials", cmd)
	}
	if f.Owner != "" && !ownerRE.MatchString(f.Owner) {
		return nil, fmt.Errorf("%s: %q is not a GitHub organization name", cmd, f.Owner)
	}
	web := f.GitHub
	if web == "" {
		web = "https://github.com"
	}
	if u, err := url.Parse(web); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("%s: the GitHub base is not an http(s) URL", cmd)
	}
	if f.API == "" {
		f.API = "https://api.github.com"
	}
	if err := checkAPI(f.API); err != nil {
		return nil, fmt.Errorf("%s: %w", cmd, err)
	}
	action := strings.TrimRight(web, "/") + "/settings/apps/new"
	if f.Owner != "" {
		action = strings.TrimRight(web, "/") + "/organizations/" + url.PathEscape(f.Owner) + "/settings/apps/new"
	}
	rnd := f.Rand
	if rnd == nil {
		rnd = rand.Reader
	}
	out := f.Out
	if out == nil {
		out = io.Discard
	}

	// Bind loopback only: the callback carries a one-hour code that converts to the App's key.
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	base := "http://" + ln.Addr().String()
	steps := make([]step, len(ms))
	for i, m := range ms {
		m.RedirectURL = base + "/callback"
		b, err := json.Marshal(m)
		if err != nil {
			_ = ln.Close()
			return nil, err
		}
		raw := make([]byte, 16)
		if _, err := io.ReadFull(rnd, raw); err != nil {
			_ = ln.Close()
			return nil, err
		}
		state := hex.EncodeToString(raw)
		steps[i] = step{manifest: b, name: m.Name, action: action + "?state=" + url.QueryEscape(state), state: state, oauth: len(m.CallbackURLs) > 0}
	}

	var (
		mu       sync.Mutex
		cur      int  // the App whose callback is awaited
		inflight bool // its conversion is running: a second callback is refused
		closing  bool // Run is returning: no conversion starts
		running  sync.WaitGroup
		apps     = make([]App, 0, len(ms))
	)
	convCtx := context.WithoutCancel(ctx)
	progress := make(chan int, len(ms)) // the index of each App created
	failed := make(chan error, 1)
	var failOnce sync.Once
	fail := func(err error) { failOnce.Do(func() { failed <- err }) }

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		i := cur
		mu.Unlock()
		if i >= len(steps) {
			http.Error(w, "every App is created: go back to the terminal", http.StatusGone)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = formPage.Execute(w, map[string]any{"Command": cmd, "Action": steps[i].action, "Manifest": string(steps[i].manifest), "Name": steps[i].name})
	})
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		mu.Lock()
		i := cur
		if i >= len(steps) || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(steps[i].state)) != 1 {
			mu.Unlock()
			http.Error(w, "unexpected state: start again from maku "+cmd, http.StatusBadRequest)
			return
		}
		code := q.Get("code")
		if code == "" {
			mu.Unlock()
			http.Error(w, "no code", http.StatusBadRequest)
			return
		}
		if inflight || closing {
			mu.Unlock()
			http.Error(w, "this callback was already used", http.StatusBadRequest)
			return
		}
		inflight = true
		running.Add(1)
		mu.Unlock()
		defer running.Done()

		// Not the request's context: a closed tab must not cancel a conversion GitHub has begun.
		cctx, cancel := context.WithTimeout(convCtx, convertTimeout)
		defer cancel()
		c, err := f.convert(cctx, code, steps[i].oauth)
		if err == nil {
			if serr := f.Save(i, c); serr != nil {
				err = fmt.Errorf("%s: the GitHub App %s was created, but its credentials could not be saved: %w; delete it at %s and run again", cmd, c.Slug, serr, c.HTMLURL)
			}
		}
		if err != nil {
			http.Error(w, "the App could not be created: see the terminal", http.StatusBadGateway)
			fail(err)
			return
		}
		mu.Lock()
		apps = append(apps, c.App)
		cur++
		inflight = false
		next := cur
		mu.Unlock()
		progress <- i
		if next < len(steps) {
			// The browser goes straight on to the next App's form.
			http.Redirect(w, req, "/", http.StatusSeeOther)
			return
		}
		if len(steps) == 1 {
			_, _ = io.WriteString(w, "The App is created. You can close this tab and go back to the terminal.")
			return
		}
		_, _ = io.WriteString(w, "The Apps are created. You can close this tab and go back to the terminal.")
	})
	// A request for any other Host is a DNS-rebinding page talking to this loopback port.
	guarded := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Host != ln.Addr().String() {
			http.Error(w, "unexpected host", http.StatusMisdirectedRequest)
			return
		}
		mux.ServeHTTP(w, req)
	})
	srv := &http.Server{Handler: guarded, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	_, _ = fmt.Fprintf(out, "Open %s/ to create the GitHub App %q (the browser should open by itself)\n", base, steps[0].name)
	if f.Open != nil {
		f.Open(ctx, base+"/")
	}
	timeout := f.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	// stop lets no conversion start, waits for the one running, and names the Apps that exist.
	stop := func(err error) ([]App, error) {
		mu.Lock()
		closing = true
		mu.Unlock()
		running.Wait()
		mu.Lock()
		defer mu.Unlock()
		if len(apps) == 0 {
			return nil, err
		}
		names := make([]string, len(apps))
		for i, a := range apps {
			names[i] = a.Slug + " (" + a.HTMLURL + ")"
		}
		if len(names) == 1 {
			return nil, fmt.Errorf("%w; the GitHub App %s exists and its credentials are saved: a rerun creates only the others", err, names[0])
		}
		return nil, fmt.Errorf("%w; the GitHub Apps %s exist and their credentials are saved: a rerun creates only the others", err, strings.Join(names, ", "))
	}
	for done := 0; done < len(steps); {
		timer := time.NewTimer(timeout)
		select {
		case i := <-progress:
			timer.Stop()
			done++
			if i+1 < len(steps) {
				_, _ = fmt.Fprintf(out, "The GitHub App %q is created. The same tab goes on to create %q\n", steps[i].name, steps[i+1].name)
			}
		case err := <-failed:
			timer.Stop()
			return stop(err)
		case <-timer.C:
			return stop(fmt.Errorf("%s: no GitHub callback within %s: run maku %s again", cmd, timeout, cmd))
		case <-ctx.Done():
			timer.Stop()
			return stop(ctx.Err())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	return apps, nil
}

// checkAPI refuses an API base the code should not be sent to: https, or plain http on loopback only.
func checkAPI(api string) error {
	u, err := url.Parse(api)
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("the GitHub API base is not a URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		h := u.Hostname()
		if ip := net.ParseIP(h); h == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return errors.New("the GitHub API base must be https (plain http on loopback only)")
}

// convert trades a callback's code for the App's credentials. An error names the status only: the
// code and the answer are credentials.
func (f Flow) convert(ctx context.Context, code string, oauth bool) (Credentials, error) {
	cmd := f.Command
	endpoint := strings.TrimRight(f.API, "/") + "/app-manifests/" + url.PathEscape(code) + "/conversions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil) //nolint:gosec // the API base is the developer's or a test's
	if err != nil {
		return Credentials{}, fmt.Errorf("%s: building the manifest code's conversion", cmd)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	hc := f.HC
	if hc == nil {
		hc = httpx.New(30*time.Second, nil)
	}
	resp, err := hc.Do(req) //nolint:gosec // as above
	if err != nil {
		// A *url.Error carries the URL, and the code in its path: keep only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return Credentials{}, fmt.Errorf("%s: converting the manifest code: %w", cmd, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return Credentials{}, fmt.Errorf("%s: GitHub refused the manifest code (HTTP %d): the code lasts one hour and works once, run maku %s again", cmd, resp.StatusCode, cmd)
	}
	var body struct {
		ID           int64  `json:"id"`
		Slug         string `json:"slug"`
		HTMLURL      string `json:"html_url"`
		ClientID     string `json:"client_id"`
		PEM          Secret `json:"pem"`
		ClientSecret Secret `json:"client_secret"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&body); err != nil {
		return Credentials{}, fmt.Errorf("%s: GitHub's answer was not the App's JSON", cmd)
	}
	if body.ID == 0 || body.Slug == "" || body.PEM == "" || body.HTMLURL == "" {
		return Credentials{}, fmt.Errorf("%s: GitHub's answer lacks the App's id, slug, key or URL", cmd)
	}
	if oauth && (body.ClientID == "" || body.ClientSecret == "") {
		return Credentials{}, fmt.Errorf("%s: GitHub's answer lacks the App's OAuth client id or secret", cmd)
	}
	return Credentials{
		App: App{ID: body.ID, Slug: body.Slug, HTMLURL: body.HTMLURL, ClientID: body.ClientID},
		PEM: body.PEM, ClientSecret: body.ClientSecret,
	}, nil
}
