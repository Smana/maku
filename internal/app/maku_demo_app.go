// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"time"

	"github.com/Smana/maku/internal/githubapps"
	"github.com/Smana/maku/internal/httpx"
)

const (
	demoAppURL     = "https://github.com/Smana/maku"
	demoAppTimeout = 5 * time.Minute
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

// demoManifest is the demo's one App: its own permission map, no webhook, not public.
func demoManifest(name string) githubapps.AppManifest {
	return githubapps.AppManifest{
		DefaultEvents:      []string{},
		DefaultPermissions: demoPermissions,
		HookAttributes:     githubapps.HookAttributes{URL: demoAppURL},
		Name:               name,
		URL:                demoAppURL,
	}
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
	if *name == "" {
		suffix, err := randHex(3)
		if err != nil {
			return err
		}
		*name = "maku-demo-" + suffix
	}
	hc := r.HC
	if hc == nil {
		hc = httpx.New(makuTimeout, nil)
	}
	open := r.Open
	if open == nil {
		open = openBrowser
	}
	flow := githubapps.Flow{
		Command: "demo app", GitHub: *ghBase, API: *api, Owner: *owner,
		HC: hc, Out: r.Out, Open: open, Timeout: r.DemoTimeout,
		Save: func(_ int, c githubapps.Credentials) error { return writeDemoApp(*dir, c) },
	}
	if flow.Timeout == 0 {
		flow.Timeout = demoAppTimeout
	}
	apps, err := flow.Run(ctx, demoManifest(*name))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(r.Out, "Now install the App on your repository: %s\n", apps[0].InstallURL())
	return err
}

// writeDemoApp writes the three files the demo needs, and the App's URL, owner-only.
func writeDemoApp(dir string, c githubapps.Credentials) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	//nolint:gosec // a directory needs the x bit; owner-only
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	// The set is staged whole, then renamed into place with html_url last: it is the marker that the set
	// is complete, and a failure while staging leaves no key without it.
	return writeOwnerOnlyAll([]ownerFile{
		{filepath.Join(dir, "private_key"), []byte(c.PEM.Reveal())},
		{filepath.Join(dir, "app_id"), []byte(strconv.FormatInt(c.ID, 10))},
		{filepath.Join(dir, "slug"), []byte(c.Slug)},
		{filepath.Join(dir, "html_url"), []byte(c.HTMLURL)},
	})
}

// ownerFile is one file of a set writeOwnerOnlyAll writes.
type ownerFile struct {
	path string
	data []byte
}

// writeOwnerOnlyAll stages every file 0600 next to its path, then renames each into place in order: a
// rename replaces a symlink at a path rather than following it, and a reader never sees half a file.
// A failure while staging removes what it staged and writes nothing.
func writeOwnerOnlyAll(files []ownerFile) (err error) {
	staged := make([]string, 0, len(files))
	defer func() {
		if err != nil {
			for _, tmp := range staged {
				_ = os.Remove(tmp)
			}
		}
	}()
	for _, f := range files {
		tmp, err := stageOwnerOnly(f.path, f.data)
		if err != nil {
			return err
		}
		staged = append(staged, tmp)
	}
	for i, f := range files {
		if err := os.Rename(staged[i], f.path); err != nil {
			staged = staged[i:]
			return err
		}
	}
	return nil
}

// stageOwnerOnly writes b to a new 0600 temporary file in path's directory, synced, and names it.
func stageOwnerOnly(path string, b []byte) (name string, err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return "", err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
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
