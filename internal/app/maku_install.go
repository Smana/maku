// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/Smana/maku/internal/githubapps"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/install"
)

// errInstallBare is IN6's refusal: the bare command is reserved, and the wizard never applies.
var errInstallBare = errors.New("maku install only writes files: run maku install --print --out DIR")

// install runs `maku install --print` (D24, IN6): it asks the seam questions (or reads --answers),
// creates the two Apps through GitHub's manifest flow when asked, and writes values, Secrets, the
// factory objects, the ruleset and the GitOps files under --out. It applies nothing.
func (r Maku) install(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	printMode := fs.Bool("print", false, "write the install's files (the only mode)")
	out := fs.String("out", "", "the directory to write; - prints values.yaml alone")
	answersPath := fs.String("answers", "", "answer every question from this file (strict)")
	force := fs.Bool("force", false, "write into a non-empty --out (secrets/ is never overwritten)")
	noWait := fs.Bool("no-wait", false, "do not wait for the Apps' installation")
	pos, err := flags(fs, args)
	if err != nil {
		return err
	}
	if !*printMode {
		return errInstallBare
	}
	if len(pos) > 0 || *out == "" {
		return errors.New("install: --out DIR is required, and nothing is positional")
	}
	in := r.In
	if in == nil {
		in = os.Stdin
	}
	stdin := bufio.NewReader(in)
	var a install.Answers
	if *answersPath != "" {
		raw, err := os.ReadFile(*answersPath)
		if err != nil {
			return err
		}
		if a, err = install.DecodeAnswers(raw); err != nil {
			return err
		}
	} else {
		if a, err = install.Ask(stdin, r.Out); err != nil {
			return err
		}
	}
	a, warns, err := install.Resolve(a)
	if err != nil {
		return err
	}
	errOut := r.Err
	if errOut == nil {
		errOut = os.Stderr
	}
	for _, w := range warns {
		_, _ = fmt.Fprintln(errOut, "warning: "+w)
	}
	if *out == "-" && a.Bool("apps.create") {
		return errors.New("install: --out - prints values.yaml alone, and creating the Apps needs secrets/ for their keys: pass --out DIR, or reuse existing Apps (apps.create: false)")
	}
	hc := r.HC
	if hc == nil {
		hc = httpx.New(makuTimeout, nil)
	}
	digest := r.Digest
	if digest == nil {
		digest = install.Registry{HC: hc}.Digest
	}
	pins, err := install.ResolvePins(ctx, a, digest)
	if err != nil {
		return err
	}
	rnd := r.Rand
	if rnd == nil {
		rnd = rand.Reader
	}
	cookie, err := randomKey(rnd)
	if err != nil {
		return err
	}
	webhook, err := randomKey(rnd)
	if err != nil {
		return err
	}
	inputs := install.Inputs{Answers: a, Pins: pins, CookieSecret: cookie, WebhookKey: webhook}
	if *out == "-" {
		vals, err := install.Render(inputs)
		if err != nil {
			return err
		}
		_, err = r.Out.Write(vals["values.yaml"])
		return err
	}
	if a.Bool("apps.create") {
		if err := r.createApps(ctx, &inputs, hc, stdin, *noWait); err != nil {
			return err
		}
	}
	tree, err := install.Render(inputs)
	if err != nil {
		return err
	}
	if err := install.Write(*out, tree, *force); err != nil {
		return err
	}
	_, err = fmt.Fprintf(r.Out, "wrote %d files under %s; its README.md lists the commands to run, in order. Nothing was applied.\n", len(tree), *out)
	return err
}

// createApps runs the manifest flow for the two Apps (IN8): the agents App, then the factory App with
// the merger folded in and the account link's callback (R5). Their keys reach inputs only, and from
// there secrets/. It then waits for both installations, unless noWait.
func (r Maku) createApps(ctx context.Context, in *install.Inputs, hc *http.Client, stdin *bufio.Reader, noWait bool) error {
	a := in.Answers
	prefix := a.String("apps.name")
	agents, err := githubapps.Manifest(githubapps.AgentsApp, prefix+"-agents", githubapps.ManifestOptions{})
	if err != nil {
		return err
	}
	factory, err := githubapps.Manifest(githubapps.FactoryApp, prefix+"-factory", githubapps.ManifestOptions{
		WebURL: a.String("web.url"),
		Merger: githubapps.MergerOptions{Folded: true, Reruns: true, Status: true},
	})
	if err != nil {
		return err
	}
	web := strings.TrimSuffix(a.String("github.url"), "/")
	api := "https://api.github.com"
	if web != "https://github.com" {
		api = web + "/api/v3"
	}
	owner := a.String("github.owner")
	open := r.Open
	if open == nil {
		open = openBrowser
	}
	got := make([]install.AppInfo, 2)
	flow := githubapps.Flow{
		Command: "install", GitHub: web, API: api, Owner: owner, HC: hc, Out: r.Out, Open: open, Timeout: r.DemoTimeout,
		Save: func(i int, c githubapps.Credentials) error {
			got[i] = install.AppInfo{ID: c.ID, Slug: c.Slug, HTMLURL: c.HTMLURL, ClientID: c.ClientID, PEM: c.PEM.Reveal(), ClientSecret: c.ClientSecret.Reveal()}
			return nil
		},
	}
	if flow.Timeout == 0 {
		flow.Timeout = demoAppTimeout
	}
	run := r.RunFlow
	if run == nil {
		run = func(ctx context.Context, f githubapps.Flow, ms ...githubapps.AppManifest) ([]githubapps.App, error) { return f.Run(ctx, ms...) }
	}
	apps, err := run(ctx, flow, agents, factory)
	if err != nil {
		return err
	}
	in.Agents, in.Factory = got[0], got[1]
	_, _ = fmt.Fprintf(r.Out, "Install both Apps on %s:\n  %s\n  %s\n", strings.Join(a.List("repositories"), ", "), apps[0].InstallURL(), apps[1].InstallURL())
	if noWait {
		return nil
	}
	_, _ = fmt.Fprint(r.Out, "Press Enter once both are installed (or run with --no-wait): ")
	_, err = stdin.ReadString('\n')
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// randomKey is 32 random bytes, base64url: oauth2-proxy's cookie secret, the webhook's key.
func randomKey(r io.Reader) (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
