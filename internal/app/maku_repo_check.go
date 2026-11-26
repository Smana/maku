// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/fence"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/install"
	"github.com/Smana/maku/internal/maku"
	"github.com/Smana/maku/internal/preflight"
)

// repoCluster is what `maku repo check` reads of the cluster: the namespace's Repositories, and the
// factory's GET /preflight?repository= (WF11).
type repoCluster struct {
	Repositories func(ctx context.Context, ns string) ([]factoryv1.Repository, error)
	Preflight    func(ctx context.Context, ns, repo string) ([]preflight.Result, error)
}

// repoCheckFactoryChecks are the factory's repository checks, in the order they print.
var repoCheckFactoryChecks = []string{"apps", "merge-gate", "private-code"}

// repoCheckFixes is the one fix of each code a line can fail or warn with: the factory's checks'
// (preflight.Catalog) and the CLI's own (fence, repository).
var repoCheckFixes = map[string]string{
	"fence_missing":       "commit repo/.github/agent-factory.yaml (written by maku repo add) to the default branch.",
	"fence_other_factory": "the repository is fenced to another factory: change the fence only if that factory's owner agrees.",
	"fence_unreadable":    "run gh auth login with an account that can read the repository, then run again.",
	"repository_missing":  "commit factory/repository-<name>.yaml (written by maku repo add) where your GitOps applies the factory's objects, in this namespace.",
	"repository_unread":   "wait for the factory to read the Repository (its Factory must be running in this namespace), then run again.",
	"repository_refused":  "kubectl describe repository the object: its Accepted message names the field to change.",
	"bindings_invalid":    "kubectl describe repository the object: its BindingsValid message names the binding to change.",

	preflight.Undecided:       "the check could not decide: run again once the factory has read the repository, or read the factory's log.",
	"app_not_installed":       "install both Apps on the repository (the links maku repo add printed).",
	"app_permission_missing":  "accept the App's requested permissions in its installation's settings on GitHub.",
	"merge_gate_not_required": "run github/apply-ruleset.sh (written by maku repo add), or require the maku/merge-gate status on the default branch.",
	"merge_gate_rules_unread": "let the factory App read the default branch's rules (its installation's permissions), then run again.",
	"checks_missing":          "write .github/agent-checks.yaml: docs/make-a-repository-ready.md says how, and examples/agent-ready/ has one per stack.",
	"toolchain_missing":       "pin the checks' tools in mise.toml: the sandbox installs them from it.",
	"agents_missing":          "add AGENTS.md: how to build, test and lint this repository, in a few lines.",
	"readiness_unread":        "run gh auth login with an account that can read the repository, then run again.",

	"private_code_unacknowledged": "set spec.privateCode.acknowledged: true on the Repository once its owner agrees its code goes to the model provider (maku repo add --acknowledge-private-code).",
}

// repoLine is one printed check.
type repoLine struct {
	check   string
	outcome preflight.Outcome
	code    string // a FAIL or WARN's code, for its fix
	message string
}

// repoCheck runs `maku repo check owner/name` (WF11): the fence read from the default branch with the
// developer's gh, the Repository's conditions, and the factory's three repository checks. It exits 0
// when no line fails or warns, 1 otherwise, 2 on a usage error or an unreachable cluster.
func (r Maku) repoCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("repo check", flag.ContinueOnError)
	ns := fs.String("namespace", "", "the factory's namespace (default: the context's)")
	factory := fs.String("factory", "agent-factory", "the factory's name, when no Repository names it")
	kubeconfig := fs.String("kubeconfig", "", "the kubeconfig (default: KUBECONFIG, then ~/.kube/config)")
	kubecontext := fs.String("context", "", "the kubeconfig context (default: its current one)")
	pos, err := flags(fs, args)
	if err != nil {
		return &maku.ExitError{Code: 2, Err: err}
	}
	if len(pos) != 1 || !install.ValidRepository(pos[0]) {
		return &maku.ExitError{Code: 2, Err: fmt.Errorf("repo check: one owner/name is required\n%s", makuUsage)}
	}
	repo := pos[0]
	open := r.RepoCluster
	if open == nil {
		open = kubeRepoCluster
	}
	c, ctxNS, err := open(*kubeconfig, *kubecontext)
	if err != nil {
		return &maku.ExitError{Code: 2, Err: fmt.Errorf("repo check: the cluster: %w", err)}
	}
	if *ns == "" {
		*ns = ctxNS
	}

	var lines []repoLine
	repoObj, line, err := findRepository(ctx, c, *ns, repo)
	if err != nil {
		return &maku.ExitError{Code: 2, Err: fmt.Errorf("repo check: %w", err)}
	}
	name := *factory
	if repoObj != nil {
		name = repoObj.Spec.FactoryRef
	}
	lines = append(lines, r.fenceLine(ctx, repo, name), r.readinessLine(ctx, repo), line)
	lines = append(lines, factoryLines(ctx, c, *ns, repo)...)

	failed := false
	for _, l := range lines {
		if _, err := fmt.Fprintf(r.Out, "%-4s  %-14s %s\n", strings.ToUpper(string(l.outcome)), l.check, l.message); err != nil {
			return err
		}
		if l.outcome == preflight.Fail || l.outcome == preflight.Warn {
			failed = true
			fix := repoCheckFixes[l.code]
			if l.code == "fence_invalid" {
				fix = "the file is one line: factory: " + name
			}
			if fix != "" {
				if _, err := fmt.Fprintf(r.Out, "      Fix: %s\n", fix); err != nil {
					return err
				}
			}
		}
	}
	if failed {
		return &maku.ExitError{Code: 1, Err: fmt.Errorf("%s is not onboarded: fix the lines above", repo)}
	}
	return nil
}

// fenceLine reads .github/agent-factory.yaml on repo's default branch, as the factory does.
func (r Maku) fenceLine(ctx context.Context, repo, factory string) repoLine {
	fail := func(code, msg string) repoLine {
		return repoLine{check: "fence", outcome: preflight.Fail, code: code, message: msg}
	}
	g, err := r.readRepo(ctx, repo)
	if err != nil {
		return fail("fence_unreadable", err.Error())
	}
	path := "repos/" + repo + "/contents/" + fence.Path + "?ref=" + url.QueryEscape(g.DefaultBranch)
	code, body, err := r.gh()(ctx, path)
	switch {
	case err != nil:
		return fail("fence_unreadable", err.Error())
	case code == 404:
		return fail("fence_missing", fence.Path+" is missing on the default branch ("+g.DefaultBranch+")")
	case code != 200:
		return fail("fence_unreadable", fmt.Sprintf("GET %s: HTTP %d: %s", path, code, ghMessage(body)))
	}
	var f struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(body, &f); err != nil || f.Encoding != "base64" {
		return fail("fence_unreadable", fence.Path+" is not a file")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(f.Content, "\n", ""))
	if err != nil {
		return fail("fence_unreadable", fence.Path+" is not base64")
	}
	got, err := fence.Parse(raw)
	switch {
	case err != nil:
		return fail("fence_invalid", fence.Path+": "+err.Error())
	case got != factory:
		return fail("fence_other_factory", fmt.Sprintf("names %s, this factory is %s", got, factory))
	}
	return repoLine{check: "fence", outcome: preflight.Pass, message: fence.Path + " names " + factory}
}

// readiness are the files that make a repository ready for agent runs (docs/make-a-repository-ready.md),
// in the order they are reported, each with the code of its fix.
var readiness = []struct{ path, code string }{
	{".github/agent-checks.yaml", "checks_missing"},
	{"mise.toml", "toolchain_missing"},
	{"AGENTS.md", "agents_missing"},
}

// readinessLine reads the readiness files on repo's default branch with the developer's gh: a WARN
// naming every missing one, with the fix of the first. A run still starts without them; it cannot
// check its own work.
func (r Maku) readinessLine(ctx context.Context, repo string) repoLine {
	warn := func(code, msg string) repoLine {
		return repoLine{check: "readiness", outcome: preflight.Warn, code: code, message: msg}
	}
	g, err := r.readRepo(ctx, repo)
	if err != nil {
		return warn("readiness_unread", err.Error())
	}
	var missing []string
	code := ""
	for _, f := range readiness {
		path := "repos/" + repo + "/contents/" + f.path + "?ref=" + url.QueryEscape(g.DefaultBranch)
		switch status, body, err := r.gh()(ctx, path); {
		case err != nil:
			return warn("readiness_unread", "cannot read "+f.path+": "+err.Error())
		case status == http.StatusNotFound:
			missing = append(missing, f.path)
			if code == "" {
				code = f.code
			}
		case status != http.StatusOK:
			return warn("readiness_unread", fmt.Sprintf("cannot read %s: HTTP %d: %s", f.path, status, ghMessage(body)))
		}
	}
	if len(missing) > 0 {
		return warn(code, "missing on the default branch: "+strings.Join(missing, ", "))
	}
	return repoLine{check: "readiness", outcome: preflight.Pass, message: ".github/agent-checks.yaml, mise.toml and AGENTS.md are on the default branch"}
}

// findRepository is the Repository serving repo in ns, and its line: Accepted, then BindingsValid.
func findRepository(ctx context.Context, c repoCluster, ns, repo string) (*factoryv1.Repository, repoLine, error) {
	fail := func(code, msg string) repoLine {
		return repoLine{check: "repository", outcome: preflight.Fail, code: code, message: msg}
	}
	list, err := c.Repositories(ctx, ns)
	if err != nil {
		return nil, repoLine{}, fmt.Errorf("list the Repositories in %s: %w", ns, err)
	}
	var found *factoryv1.Repository
	for i := range list {
		if strings.EqualFold(list[i].Spec.Repository, repo) {
			found = &list[i]
			if meta.IsStatusConditionTrue(found.Status.Conditions, factoryv1.ConditionAccepted) {
				break
			}
		}
	}
	if found == nil {
		return nil, fail("repository_missing", fmt.Sprintf("no Repository serves %s in namespace %s", repo, ns)), nil
	}
	conds := found.Status.Conditions
	acc := meta.FindStatusCondition(conds, factoryv1.ConditionAccepted)
	switch {
	case acc == nil:
		return found, fail("repository_unread", "Accepted is not set yet on Repository "+found.Name+": the factory has not read it"), nil
	case acc.Status != metav1.ConditionTrue:
		return found, fail("repository_refused", conditionText(acc)+" (Repository "+found.Name+")"), nil
	}
	if b := meta.FindStatusCondition(conds, factoryv1.ConditionBindingsValid); b != nil && b.Status == metav1.ConditionFalse {
		return found, fail("bindings_invalid", conditionText(b)+" (Repository "+found.Name+")"), nil
	}
	return found, repoLine{check: "repository", outcome: preflight.Pass, message: "Accepted"}, nil
}

func conditionText(c *metav1.Condition) string {
	s := c.Type + " is " + string(c.Status)
	if c.Reason != "" {
		s += ": " + c.Reason
	}
	if c.Message != "" {
		s += ": " + maku.OneLine(c.Message)
	}
	return s
}

// factoryLines are the factory's three repository checks. An unreachable factory fails each
// undecided; a factory that ignores the filter (an older image) answers for every repository, which
// is never read as this one's.
func factoryLines(ctx context.Context, c repoCluster, ns, repo string) []repoLine {
	undecided := func(msg string) []repoLine {
		out := make([]repoLine, len(repoCheckFactoryChecks))
		for i, id := range repoCheckFactoryChecks {
			out[i] = repoLine{check: id, outcome: preflight.Fail, code: preflight.Undecided, message: preflight.Undecided + ": " + msg}
		}
		return out
	}
	results, err := c.Preflight(ctx, ns, repo)
	if err != nil {
		return undecided(maku.OneLine(err.Error()))
	}
	by := map[string]preflight.Result{}
	for _, res := range results {
		by[res.Check] = res
	}
	if len(by) != len(repoCheckFactoryChecks) {
		return undecided("the factory answered checks for every repository, not for " + repo + ": upgrade it to this maku's release")
	}
	out := make([]repoLine, 0, len(repoCheckFactoryChecks))
	for _, id := range repoCheckFactoryChecks {
		res, ok := by[id]
		if !ok {
			return undecided("the factory answered checks for every repository, not for " + repo + ": upgrade it to this maku's release")
		}
		l := repoLine{check: id, outcome: res.Outcome, code: res.Code, message: res.Message}
		if res.Outcome == preflight.Fail || res.Outcome == preflight.Warn {
			l.message = res.Code
			if res.Message != "" {
				l.message += ": " + res.Message
			}
		}
		out = append(out, l)
	}
	return out
}

// factoryOpsPort is the factory's ops port, where /preflight is served.
const factoryOpsPort = 9090

// kubeRepoCluster is repo check's cluster from a kubeconfig, as kubectl loads it. The factory's
// /preflight is reached through a port-forward to a Ready factory pod: its ops port admits only the
// preflight pod and the scraper, and a port-forward enters the pod's own network.
func kubeRepoCluster(kubeconfig, kubecontext string) (repoCluster, string, error) {
	cfg, ns, err := kubeConfig(kubeconfig, kubecontext)
	if err != nil {
		return repoCluster{}, "", err
	}
	scheme := runtime.NewScheme()
	if err := factoryv1.AddToScheme(scheme); err != nil {
		return repoCluster{}, "", err
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return repoCluster{}, "", err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return repoCluster{}, "", err
	}
	return repoCluster{
		Repositories: func(ctx context.Context, ns string) ([]factoryv1.Repository, error) {
			var l factoryv1.RepositoryList
			err := c.List(ctx, &l, client.InNamespace(ns))
			return l.Items, err
		},
		Preflight: func(ctx context.Context, ns, repo string) ([]preflight.Result, error) {
			return forwardPreflight(ctx, cfg, cs, ns, repo)
		},
	}, ns, nil
}

// forwardPreflight GETs /preflight?repository=repo from a Ready factory pod in ns, through a
// port-forward closed on return.
func forwardPreflight(ctx context.Context, cfg *rest.Config, cs kubernetes.Interface, ns, repo string) ([]preflight.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, preflight.AllTimeout+15*time.Second)
	defer cancel()
	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=agent-factory"})
	if err != nil {
		return nil, fmt.Errorf("list the factory's pods in %s: %w", ns, err)
	}
	var pod string
	for _, p := range pods.Items {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue && pod == "" {
				pod = p.Name
			}
		}
	}
	if pod == "" {
		return nil, fmt.Errorf("no Ready factory pod (app.kubernetes.io/name=agent-factory) in %s", ns)
	}
	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return nil, err
	}
	u := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("portforward").URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, u)
	stop, ready := make(chan struct{}), make(chan struct{})
	defer close(stop)
	fw, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("0:%d", factoryOpsPort)}, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- fw.ForwardPorts() }()
	select {
	case <-ready:
	case err := <-done:
		return nil, fmt.Errorf("port-forward to %s: %w", pod, err)
	case <-ctx.Done():
		return nil, fmt.Errorf("port-forward to %s: %w", pod, ctx.Err())
	}
	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		return nil, fmt.Errorf("port-forward to %s: no local port", pod)
	}
	target := fmt.Sprintf("http://127.0.0.1:%d/preflight?repository=%s", ports[0].Local, url.QueryEscape(repo))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpx.New(preflight.AllTimeout+5*time.Second, nil).Do(req)
	if err != nil {
		return nil, fmt.Errorf("the factory's /preflight: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errors.New("the factory serves no /preflight: set preflight.enabled, or upgrade its image")
	default:
		return nil, fmt.Errorf("the factory answered /preflight with HTTP %d", resp.StatusCode)
	}
	doc, err := preflight.DecodeComponent(resp.Body)
	if err != nil {
		return nil, err
	}
	return doc.Checks, nil
}
