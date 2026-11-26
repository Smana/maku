// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/maku"
	"github.com/Smana/maku/internal/preflight"
)

// fakeWorld is GitHub (gh) and the cluster (its Repositories, the factory's /preflight) as maku
// repo check reads them.
type fakeWorld struct {
	gh           fakeGH
	repositories []factoryv1.Repository
	checks       []preflight.Result
	unreachable  bool
}

func repository(conds ...metav1.Condition) factoryv1.Repository {
	return factoryv1.Repository{ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "maku"},
		Spec:   factoryv1.RepositorySpec{FactoryRef: "agent-factory", Repository: "acme/my-service"},
		Status: factoryv1.RepositoryStatus{Conditions: conds}}
}

func cond(typ string, s metav1.ConditionStatus, reason string) metav1.Condition {
	return metav1.Condition{Type: typ, Status: s, Reason: reason}
}

func factoryResults(override ...preflight.Result) []preflight.Result {
	out := []preflight.Result{
		{Check: "apps", Outcome: preflight.Pass, Message: "2 Apps on 1 repositories, with their grants"},
		{Check: "merge-gate", Outcome: preflight.Pass, Message: "1 repositories require maku/merge-gate"},
		{Check: "private-code", Outcome: preflight.Pass, Message: "every private repository is acknowledged"},
	}
	for _, o := range override {
		for i := range out {
			if out[i].Check == o.Check {
				out[i] = o
			}
		}
	}
	return out
}

var (
	accepted = repository(cond(factoryv1.ConditionAccepted, metav1.ConditionTrue, "Accepted"),
		cond(factoryv1.ConditionBindingsValid, metav1.ConditionTrue, "BindingsValid"))
	healthy = fakeWorld{repositories: []factoryv1.Repository{accepted}, checks: factoryResults()}
)

func with(w fakeWorld, edit func(*fakeWorld)) fakeWorld { edit(&w); return w }

func fenceNames(name string) fakeWorld {
	return with(healthy, func(w *fakeWorld) {
		w.gh.fence = ghReply{200, `{"encoding":"base64","content":"` + b64("factory: "+name+"\n") + `"}`}
	})
}

func runRepoCheck(t *testing.T, w fakeWorld, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	var asked []string
	r := Maku{Dir: t.TempDir(), Out: &out, Err: &out, GH: w.gh.api,
		RepoCluster: func(_, _ string) (repoCluster, string, error) {
			return repoCluster{
				Repositories: func(_ context.Context, ns string) ([]factoryv1.Repository, error) {
					if ns != "maku" {
						t.Errorf("listed namespace %q", ns)
					}
					return w.repositories, nil
				},
				Preflight: func(_ context.Context, _, repo string) ([]preflight.Result, error) {
					asked = append(asked, repo)
					if w.unreachable {
						return nil, errors.New("no Ready factory pod in maku")
					}
					return w.checks, nil
				},
			}, "maku", nil
		}}
	err := r.Run(t.Context(), append([]string{"repo", "check"}, args...))
	for _, a := range asked {
		if a != "acme/my-service" {
			t.Errorf("asked the factory for %s", a)
		}
	}
	var ee *maku.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &ee):
		return out.String(), ee.Code
	}
	t.Fatalf("%v\n%s", err, out.String())
	return "", -1
}

func TestRepoCheck(t *testing.T) {
	cases := map[string]struct {
		world fakeWorld
		code  int
		line  string
	}{
		"all good":        {healthy, 0, "PASS  merge-gate     1 repositories require maku/merge-gate"},
		"the fence":       {healthy, 0, "PASS  fence          .github/agent-factory.yaml names agent-factory"},
		"accepted":        {healthy, 0, "PASS  repository     Accepted"},
		"no fence":        {with(healthy, func(w *fakeWorld) { w.gh.fence = notFound }), 1, "FAIL  fence          .github/agent-factory.yaml is missing on the default branch"},
		"another factory": {fenceNames("other"), 1, "FAIL  fence          names other, this factory is agent-factory"},
		"a fence the factory cannot read": {with(healthy, func(w *fakeWorld) {
			w.gh.fence = ghReply{200, `{"encoding":"base64","content":"` + b64("factory: agent-factory\nextra: 1\n") + `"}`}
		}), 1, "Fix: the file is one line: factory: agent-factory"},
		"ready":              {healthy, 0, "PASS  readiness      .github/agent-checks.yaml, mise.toml and AGENTS.md are on the default branch"},
		"no checks":          {with(healthy, func(w *fakeWorld) { w.gh.checks = notFound }), 1, "WARN  readiness      missing on the default branch: .github/agent-checks.yaml"},
		"no checks, its fix": {with(healthy, func(w *fakeWorld) { w.gh.checks = notFound }), 1, "Fix: write .github/agent-checks.yaml: docs/make-a-repository-ready.md"},
		"nothing ready": {with(healthy, func(w *fakeWorld) { w.gh.checks, w.gh.mise, w.gh.agents = notFound, notFound, notFound }), 1,
			"WARN  readiness      missing on the default branch: .github/agent-checks.yaml, mise.toml, AGENTS.md"},
		"only AGENTS.md missing": {with(healthy, func(w *fakeWorld) { w.gh.agents = notFound }), 1, "Fix: add AGENTS.md"},
		"readiness unreadable": {with(healthy, func(w *fakeWorld) { w.gh.mise = ghReply{500, `{"message":"boom"}`} }), 1,
			"WARN  readiness      cannot read mise.toml: HTTP 500"},
		"repository unknown": {with(healthy, func(w *fakeWorld) { w.repositories = nil }), 1, "FAIL  repository     no Repository serves acme/my-service"},
		"repository refused": {with(healthy, func(w *fakeWorld) {
			w.repositories = []factoryv1.Repository{repository(cond(factoryv1.ConditionAccepted, metav1.ConditionFalse, "Invalid"))}
		}), 1, "FAIL  repository     Accepted is False"},
		"repository unread": {with(healthy, func(w *fakeWorld) { w.repositories = []factoryv1.Repository{repository()} }), 1,
			"FAIL  repository     Accepted is not set yet"},
		"a binding refused": {with(healthy, func(w *fakeWorld) {
			w.repositories = []factoryv1.Repository{repository(cond(factoryv1.ConditionAccepted, metav1.ConditionTrue, "Accepted"),
				cond(factoryv1.ConditionBindingsValid, metav1.ConditionFalse, "MissingParameter"))}
		}), 1, "FAIL  repository     BindingsValid is False: MissingParameter"},
		"gate not required": {with(healthy, func(w *fakeWorld) {
			w.checks = factoryResults(preflight.Result{Check: "merge-gate", Outcome: preflight.Fail, Code: "merge_gate_not_required"})
		}), 1, "      Fix: run github/apply-ruleset.sh (written by maku repo add), or require the maku/merge-gate status on the default branch."},
		"private unacknowledged": {with(healthy, func(w *fakeWorld) {
			w.checks = factoryResults(preflight.Result{Check: "private-code", Outcome: preflight.Fail, Code: "private_code_unacknowledged"})
		}), 1, "FAIL  private-code   private_code_unacknowledged"},
		"an App missing": {with(healthy, func(w *fakeWorld) {
			w.checks = factoryResults(preflight.Result{Check: "apps", Outcome: preflight.Fail, Code: "app_not_installed", Message: "the agents App is not installed on acme/my-service"})
		}), 1, "FAIL  apps           app_not_installed: the agents App is not installed on acme/my-service"},
		"the gate's rules unread": {with(healthy, func(w *fakeWorld) {
			w.checks = factoryResults(preflight.Result{Check: "merge-gate", Outcome: preflight.Warn, Code: "merge_gate_rules_unread"})
		}), 1, "WARN  merge-gate     merge_gate_rules_unread"},
		"the factory unreachable": {with(healthy, func(w *fakeWorld) { w.unreachable = true }), 1, "FAIL  apps           undecided: no Ready factory pod in maku"},
		"a factory without the filter": {with(healthy, func(w *fakeWorld) {
			w.checks = append(factoryResults(), preflight.Result{Check: "oidc", Outcome: preflight.Pass})
		}), 1, "the factory answered checks for every repository"},
	}
	for name, c := range cases {
		out, code := runRepoCheck(t, c.world, "acme/my-service")
		if code != c.code || !strings.Contains(out, c.line) {
			t.Errorf("%s: exit %d, want %d and %q\n%s", name, code, c.code, c.line, out)
		}
	}
}

// Every FAIL code maps to exactly one fix sentence: the table covers each code the three factory
// checks can fail or warn with, from the catalog.
func TestRepoCheckEveryCodeHasAFix(t *testing.T) {
	for _, c := range preflight.Catalog {
		if c.ID != "apps" && c.ID != "merge-gate" && c.ID != "private-code" {
			continue
		}
		for _, code := range append(append([]string{preflight.Undecided}, c.Fail...), c.Warn...) {
			if repoCheckFixes[code] == "" {
				t.Errorf("%s: %s has no fix", c.ID, code)
			}
		}
	}
}

func TestRepoCheckUsage(t *testing.T) {
	var out bytes.Buffer
	r := Maku{Dir: t.TempDir(), Out: &out, Err: &out}
	for _, args := range [][]string{{"repo", "check"}, {"repo", "check", "nope"}} {
		var ee *maku.ExitError
		if err := r.Run(t.Context(), args); !errors.As(err, &ee) || ee.Code != 2 {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
