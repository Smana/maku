// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Smana/maku/internal/httpx"
)

const (
	demoAppURL     = "https://github.com/Smana/maku"
	demoAppTimeout = 5 * time.Minute
	demoMaxBody    = 1 << 20
)

// demoPermissions is the whole of what the demo's GitHub App may do.
var demoPermissions = map[string]string{
	"actions":       "write",
	"contents":      "write",
	"pull_requests": "write",
	"issues":        "write",
	"checks":        "read",
	"statuses":      "write",
	"metadata":      "read",
}

var demoOwnerRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

var demoFormPage = template.Must(template.New("form").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>maku demo app</title></head>
<body onload="document.getElementById('f').submit()">
<form id="f" method="post" action="{{.Action}}">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<noscript><button type="submit">Create the GitHub App</button></noscript>
</form>
<p>Sending you to GitHub to create the demo App...</p>
</body></html>`))

// demoManifest is the App manifest GitHub's flow takes (redirect_url names this process's callback).
func demoManifest(name, redirect string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"name":                name,
		"url":                 demoAppURL,
		"hook_attributes":     map[string]any{"url": demoAppURL, "active": false},
		"redirect_url":        redirect,
		"public":              false,
		"default_permissions": demoPermissions,
		"default_events":      []string{},
	})
}

// demo runs `maku demo app`: GitHub's App manifest flow with a localhost callback.
func (r Maku) demo(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "app" {
		return fmt.Errorf("demo app\n%s", makuUsage)
	}
	fs := flag.NewFlagSet("demo app", flag.ContinueOnError)
	dir := fs.String("dir", "", "the directory the App's id, key and slug are written to")
	owner := fs.String("owner", "", "an organization to own the App (default: your account)")
	name := fs.String("name", "", "the App's name (default: maku-demo-<random>)")
	api := fs.String("api", "https://api.github.com", "the GitHub API base URL")
	ghBase := fs.String("github", "https://github.com", "the GitHub base URL the form posts to")
	pos, err := flags(fs, args[1:])
	if err != nil {
		return err
	}
	if *owner != "" && !demoOwnerRE.MatchString(*owner) {
		return errors.New("demo app: --owner is not a GitHub organization name")
	}
	if len(pos) > 0 || *dir == "" {
		return errors.New("demo app: --dir is required and nothing else is positional")
	}
	state, err := randHex(16)
	if err != nil {
		return err
	}
	if *name == "" {
		suffix, err := randHex(3)
		if err != nil {
			return err
		}
		*name = "maku-demo-" + suffix
	}
	// Bind loopback only: the callback carries a one-hour code that converts to the App's key.
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	base := "http://" + ln.Addr().String()
	manifest, err := demoManifest(*name, base+"/callback")
	if err != nil {
		return err
	}
	action := strings.TrimRight(*ghBase, "/") + "/settings/apps/new"
	if *owner != "" {
		action = strings.TrimRight(*ghBase, "/") + "/organizations/" + url.PathEscape(*owner) + "/settings/apps/new"
	}
	action += "?state=" + url.QueryEscape(state)

	type result struct{ err error }
	done := make(chan result, 1)
	var once sync.Once
	var used atomic.Bool // the state is good for one callback
	finish := func(err error) { once.Do(func() { done <- result{err} }) }

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = demoFormPage.Execute(w, map[string]any{"Action": action, "Manifest": string(manifest)})
	})
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			http.Error(w, "unexpected state: start again from maku demo app", http.StatusBadRequest)
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "no code", http.StatusBadRequest)
			return
		}
		if !used.CompareAndSwap(false, true) {
			http.Error(w, "this callback was already used", http.StatusBadRequest)
			return
		}
		if err := r.demoConvert(req.Context(), *api, code, *dir); err != nil {
			http.Error(w, "the App could not be created: see the terminal", http.StatusBadGateway)
			finish(err)
			return
		}
		_, _ = io.WriteString(w, "The App is created. You can close this tab and go back to the terminal.")
		finish(nil)
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

	_, _ = fmt.Fprintf(r.Out, "Open %s/ to create the GitHub App %q (the browser should open by itself)\n", base, *name)
	open := r.Open
	if open == nil {
		open = openBrowser
	}
	open(ctx, base+"/")

	timeout := r.DemoTimeout
	if timeout == 0 {
		timeout = demoAppTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-done:
		if res.err != nil {
			return res.err
		}
	case <-timer.C:
		return fmt.Errorf("demo app: no GitHub callback within %s: run maku demo app again", timeout)
	case <-ctx.Done():
		return ctx.Err()
	}
	htmlURL, _ := os.ReadFile(filepath.Join(*dir, "html_url"))
	_, err = fmt.Fprintf(r.Out, "Now install the App on your repository: %s/installations/new\n", strings.TrimSpace(string(htmlURL)))
	return err
}

// demoConvert trades the callback's code for the App's credentials and writes the three the demo needs.
// An error names the status only: the response is the App's secrets.
func (r Maku) demoConvert(ctx context.Context, api, code, dir string) error {
	endpoint := strings.TrimRight(api, "/") + "/app-manifests/" + url.PathEscape(code) + "/conversions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil) //nolint:gosec // the API base is a flag the developer or a test sets
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	hc := r.HC
	if hc == nil {
		hc = httpx.New(makuTimeout, nil)
	}
	resp, err := hc.Do(req) //nolint:gosec // as above
	if err != nil {
		return fmt.Errorf("demo app: converting the manifest code: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("demo app: GitHub refused the manifest code (HTTP %d): the code lasts one hour and works once, run maku demo app again", resp.StatusCode)
	}
	var out struct {
		ID      int64  `json:"id"`
		Slug    string `json:"slug"`
		PEM     string `json:"pem"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, demoMaxBody)).Decode(&out); err != nil {
		return errors.New("demo app: GitHub's answer was not the App's JSON")
	}
	if out.ID == 0 || out.Slug == "" || out.PEM == "" || out.HTMLURL == "" {
		return errors.New("demo app: GitHub's answer lacks the App's id, slug, key or URL")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	//nolint:gosec // a directory needs the x bit; owner-only
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	// html_url goes last: it is the marker that the set is complete.
	for _, f := range []struct{ name, val string }{
		{"private_key", out.PEM}, {"app_id", strconv.FormatInt(out.ID, 10)},
		{"slug", out.Slug}, {"html_url", out.HTMLURL},
	} {
		if err := writeOwnerOnly(filepath.Join(dir, f.name), []byte(f.val)); err != nil {
			return err
		}
	}
	return nil
}

// writeOwnerOnly writes path with mode 0600 through a temporary file renamed over it: a rename replaces a
// symlink at path rather than following it, and a reader never sees half a file.
func writeOwnerOnly(path string, b []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// openBrowser opens u; a failure is ignored, the URL is printed.
func openBrowser(ctx context.Context, u string) {
	bin := "xdg-open"
	if runtime.GOOS == "darwin" {
		bin = "open"
	}
	if p, err := exec.LookPath(bin); err == nil {
		_ = exec.CommandContext(ctx, p, u).Start() //nolint:gosec // p is xdg-open or open from PATH, u is our loopback URL
	}
}
