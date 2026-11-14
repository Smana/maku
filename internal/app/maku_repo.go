// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/install"
	"github.com/Smana/maku/internal/maku"
)

// mergeGateContext is the status the factory posts itself: never one of a repository's own checks.
const mergeGateContext = "maku/merge-gate"

// stringsFlag is a repeatable string flag.
type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }

// repo runs `maku repo add` and `maku repo check` (workflows review WF11).
func (r Maku) repo(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("repo: add or check\n%s", makuUsage)
	}
	switch args[0] {
	case "add":
		return r.repoAdd(ctx, args[1:])
	case "check":
		return r.repoCheck(ctx, args[1:])
	}
	return fmt.Errorf("repo: unknown subcommand %q: add or check\n%s", args[0], makuUsage)
}

// ghHTTPStatus is gh api's "(HTTP 404)" on stderr for an answer that is not a 2xx.
var ghHTTPStatus = regexp.MustCompile(`\(HTTP (\d{3})\)`)

// ghAPI is the developer's gh: GET path with its authentication and host. An HTTP answer is a status
// and a body; err is gh failing to ask (not installed, not logged in).
func ghAPI(ctx context.Context, path string) (int, []byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "gh", "api", path) //nolint:gosec // gh with a path built from a checked owner/name, never through a shell
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return 200, stdout.Bytes(), nil
	}
	if m := ghHTTPStatus.FindSubmatch(stderr.Bytes()); m != nil {
		code, _ := strconv.Atoi(string(m[1]))
		return code, stdout.Bytes(), nil
	}
	return 0, nil, fmt.Errorf("gh api %s: %w: %s", path, err, maku.OneLine(stderr.String()))
}

func (r Maku) gh() func(ctx context.Context, path string) (int, []byte, error) {
	if r.GH != nil {
		return r.GH
	}
	return ghAPI
}

// ghJSON reads path into v; any answer but a 200 is an error naming the status.
func (r Maku) ghJSON(ctx context.Context, path string, v any) error {
	code, body, err := r.gh()(ctx, path)
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("GET %s: HTTP %d: %s", path, code, ghMessage(body))
	}
	return json.Unmarshal(body, v)
}

func ghMessage(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Message
}

// ghRepo is what the onboarding reads of a repository.
type ghRepo struct {
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Visibility    string `json:"visibility"`
}

func (r Maku) readRepo(ctx context.Context, repo string) (ghRepo, error) {
	var g ghRepo
	if err := r.ghJSON(ctx, "repos/"+repo, &g); err != nil {
		return g, fmt.Errorf("cannot read %s with your gh login: %w", repo, err)
	}
	if g.DefaultBranch == "" {
		return g, fmt.Errorf("%s has no default branch", repo)
	}
	return g, nil
}

// requiredChecks are the default branch's required status checks: its rulesets' (readable with read
// access) and its classic protection's (readable with admin rights only), maku/merge-gate left out.
// A source that cannot be read fails: an empty list would merge on no check (Review Focus 5).
func (r Maku) requiredChecks(ctx context.Context, repo, branch string) ([]string, error) {
	unreadable := func(path string, code int, body []byte) error {
		return fmt.Errorf("cannot read the required checks of %s: pass --check NAME for each (GET %s: HTTP %d: %s)", repo, path, code, ghMessage(body))
	}
	var checks []string
	add := func(c string) {
		if c != "" && c != mergeGateContext && !slices.Contains(checks, c) {
			checks = append(checks, c)
		}
	}
	gh := r.gh()
	rulesPath := "repos/" + repo + "/rules/branches/" + branch
	code, body, err := gh(ctx, rulesPath)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, unreadable(rulesPath, code, body)
	}
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			Checks []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(body, &rules); err != nil {
		return nil, fmt.Errorf("GET %s: %w", rulesPath, err)
	}
	for _, rule := range rules {
		if rule.Type == "required_status_checks" {
			for _, c := range rule.Parameters.Checks {
				add(c.Context)
			}
		}
	}
	protPath := "repos/" + repo + "/branches/" + branch + "/protection/required_status_checks"
	code, body, err = gh(ctx, protPath)
	switch {
	case err != nil:
		return nil, err
	case code == 404 && ghMessage(body) == "Branch not protected":
		// Readable, and no classic protection.
	case code != 200:
		return nil, unreadable(protPath, code, body)
	default:
		var p struct {
			Contexts []string `json:"contexts"`
			Checks   []struct {
				Context string `json:"context"`
			} `json:"checks"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, fmt.Errorf("GET %s: %w", protPath, err)
		}
		for _, c := range p.Contexts {
			add(c)
		}
		for _, c := range p.Checks {
			add(c.Context)
		}
	}
	return checks, nil
}

// ghApp is an App as GET apps/<slug> answers it, publicly.
type ghApp struct {
	ID      int64  `json:"id"`
	Slug    string `json:"slug"`
	HTMLURL string `json:"html_url"`
}

// repoAdd runs `maku repo add` (WF11): it reads the repository's default branch, required checks and
// visibility with the developer's gh, and writes the Repository, the fence, the merge gate's ruleset
// and its script under --out. It applies nothing and opens no pull request.
func (r Maku) repoAdd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("repo add", flag.ContinueOnError)
	out := fs.String("out", "", "the directory to write")
	in := install.RepoInputs{}
	fs.StringVar(&in.Factory, "factory", "agent-factory", "the Factory's name, written in the fence")
	fs.StringVar(&in.Namespace, "namespace", "maku", "the factory's namespace")
	var maintainers, checks stringsFlag
	fs.Var(&maintainers, "maintainer", "a maintainer's GitHub login, repeatable (default: your gh login)")
	fs.Var(&checks, "check", "a required check, repeatable (default: the default branch's)")
	fs.StringVar(&in.LeakScanCheck, "leak-scan-check", "", "the required check that scans for leaks (default: the first)")
	fs.BoolVar(&in.Acknowledged, "acknowledge-private-code", false, "a private repository's code may go to the model provider (D3)")
	fs.BoolVar(&in.MergeDefaults, "merge-defaults", false, "the Factory's spec.mergeDefaults carries mergerLogin, verifyFor, revertWindow and breaker")
	factoryApp := fs.String("factory-app", "", "the factory App's slug")
	agentsApp := fs.String("agents-app", "", "the agents App's slug")
	force := fs.Bool("force", false, "write into a non-empty --out")
	pos, err := flags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *out == "" {
		return fmt.Errorf("repo add: one owner/name and --out DIR are required\n%s", makuUsage)
	}
	in.Repository = pos[0]
	if *factoryApp == "" || *agentsApp == "" {
		return errors.New("repo add: --factory-app SLUG and --agents-app SLUG name the two Apps (the ruleset bypasses the factory App, the install links are both)")
	}
	if !install.ValidRepository(in.Repository) {
		return fmt.Errorf("repo add: %q is not owner/name", in.Repository)
	}
	if err := install.CheckOut(*out, *force); err != nil {
		return err
	}
	g, err := r.readRepo(ctx, in.Repository)
	if err != nil {
		return err
	}
	in.Private = g.Private || (g.Visibility != "" && g.Visibility != "public")
	in.RequiredChecks = checks
	if len(in.RequiredChecks) == 0 {
		if in.RequiredChecks, err = r.requiredChecks(ctx, in.Repository, g.DefaultBranch); err != nil {
			return err
		}
	}
	if len(in.RequiredChecks) == 0 {
		return fmt.Errorf("%s: no required check: the factory merges on one: require a check on %s, or pass --check NAME", in.Repository, g.DefaultBranch)
	}
	if in.LeakScanCheck == "" {
		in.LeakScanCheck = in.RequiredChecks[0]
	}
	in.Maintainers = maintainers
	if len(in.Maintainers) == 0 {
		var u struct {
			Login string `json:"login"`
		}
		if err := r.ghJSON(ctx, "user", &u); err != nil {
			return fmt.Errorf("repo add: no --maintainer, and your gh login is unreadable: %w", err)
		}
		if u.Login == "" {
			return errors.New("repo add: no --maintainer, and gh names no login: pass --maintainer LOGIN")
		}
		in.Maintainers = []string{u.Login}
	}
	var fa, aa ghApp
	for _, a := range []struct {
		slug string
		dst  *ghApp
	}{{*factoryApp, &fa}, {*agentsApp, &aa}} {
		if err := r.ghJSON(ctx, "apps/"+a.slug, a.dst); err != nil {
			return fmt.Errorf("repo add: the App %s: %w", a.slug, err)
		}
	}
	in.FactoryAppID, in.FactoryLogin = strconv.FormatInt(fa.ID, 10), fa.Slug+"[bot]"
	in.FactoryInstallURL, in.AgentsInstallURL = fa.HTMLURL+"/installations/new", aa.HTMLURL+"/installations/new"
	tree, err := install.RepoTree(in)
	if err != nil {
		return err
	}
	if err := install.Write(*out, tree, *force); err != nil {
		return err
	}
	_, err = fmt.Fprintf(r.Out, "wrote %d files under %s; its README.md lists the steps, in order. Nothing was applied.\nInstall both Apps on %s:\n  %s\n  %s\n",
		len(tree), *out, in.Repository, in.FactoryInstallURL, in.AgentsInstallURL)
	return err
}
