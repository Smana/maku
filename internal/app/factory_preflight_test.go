// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/minter"
	"github.com/Smana/maku/internal/preflight"
)

// repoGrants answers CheckGranted per repository from a table: missing permissions, or an error.
type repoGrants struct {
	missing map[string]minter.Permissions
	errs    map[string]error
}

func (f repoGrants) CheckGranted(_ context.Context, repos []string) (map[string]minter.Permissions, error) {
	out := map[string]minter.Permissions{}
	for _, r := range repos {
		if err := f.errs[r]; err != nil {
			return out, fmt.Errorf("%s: %w", r, err)
		}
		out[r] = f.missing[r]
	}
	return out, nil
}

func servedRepos(names ...string) func() []string { return func() []string { return names } }

func TestFactoryPreflightApps(t *testing.T) {
	ok := repoGrants{}
	for name, c := range map[string]struct {
		factory, agents grantChecker
		code, msg       string
	}{
		"both installed, every grant": {ok, ok, "", ""},
		"an App missing statuses: write names it": {repoGrants{missing: map[string]minter.Permissions{"o/r": {"statuses": "write"}}}, ok,
			"app_permission_missing", "statuses: write"},
		"a repository without the agents App's installation": {ok, repoGrants{errs: map[string]error{"o/r": forge.ErrNotInstalled}},
			"app_not_installed", "agents"},
	} {
		t.Run(name, func(t *testing.T) {
			f := factoryChecks{Repos: servedRepos("o/r"), Apps: []appGrants{{Name: "factory", Checker: c.factory}, {Name: "agents", Checker: c.agents}}}
			r := byCheck(f.source()(t.Context()))["apps"]
			if (c.code == "" && r.Outcome != preflight.Pass) || (c.code != "" && (r.Code != c.code || !strings.Contains(r.Message, c.msg))) {
				t.Fatalf("%+v", r)
			}
		})
	}
}

// The demo has one App: its grants are checked against the demo's own map.
func TestFactoryPreflightDemoNeed(t *testing.T) {
	need := preflightAppNeed(&config.Config{Demo: true}, false)
	if need["actions"] != "write" || need["statuses"] != "write" {
		t.Fatalf("the demo's map: %v", need)
	}
}

// The merge gate's check reads the observer's last read: a ruleset without the App's integration_id
// is not required, a read that failed warns.
func TestFactoryPreflightMergeGate(t *testing.T) {
	live := func() *config.Config {
		return &config.Config{Repositories: []config.Repository{{Name: "o/r", Merge: config.Merge{GateStatus: true}}}}
	}
	g := &gateState{}
	f := factoryChecks{Repos: servedRepos("o/r"), Live: live, Gate: g}
	if r := byCheck(f.source()(t.Context()))["merge-gate"]; r.Code != preflight.Undecided {
		t.Fatalf("no read yet: %+v", r)
	}
	g.record(func(context.Context, string, bool) {})(t.Context(), "o/r", false)
	if r := byCheck(f.source()(t.Context()))["merge-gate"]; r.Code != "merge_gate_not_required" || !strings.Contains(r.Message, "o/r") {
		t.Fatalf("%+v", r)
	}
	g.unread(func(context.Context, string) {})(t.Context(), "o/r")
	if r := byCheck(f.source()(t.Context()))["merge-gate"]; r.Outcome != preflight.Warn || r.Code != "merge_gate_rules_unread" {
		t.Fatalf("%+v", r)
	}
	g.record(func(context.Context, string, bool) {})(t.Context(), "o/r", true)
	if r := byCheck(f.source()(t.Context()))["merge-gate"]; r.Outcome != preflight.Pass {
		t.Fatalf("%+v", r)
	}
}

var httpRouteGVK = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}

func route(name, backend string, port int64) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(httpRouteGVK)
	u.SetName(name)
	u.SetNamespace("maku")
	_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"backendRefs": []any{map[string]any{"name": backend, "port": port}}}}, "spec", "rules")
	return u
}

func exposureClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	s.AddKnownTypeWithName(httpRouteGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(httpRouteGVK.GroupVersion().WithKind("HTTPRouteList"), &unstructured.UnstructuredList{})
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

// F5: a route to the broker, on any port, fails; one to the proxy passes.
func TestFactoryPreflightExposure(t *testing.T) {
	for name, c := range map[string]struct {
		objs []client.Object
		code string
	}{
		"a route to the web proxy":           {[]client.Object{route("web", "rooms-web-proxy", 80)}, ""},
		"a route to the broker's ops port":   {[]client.Object{route("ops", "room-broker", 9090)}, "broker_exposed_around_proxy"},
		"a route to the broker's human port": {[]client.Object{route("bypass", "room-broker", 8080)}, "broker_exposed_around_proxy"},
		"an Ingress to the broker's human port": {[]client.Object{&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "rooms", Namespace: "maku"},
			Spec: networkingv1.IngressSpec{DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
				Name: "room-broker", Port: networkingv1.ServiceBackendPort{Name: "human"}}}}}}, "broker_exposed_around_proxy"},
	} {
		t.Run(name, func(t *testing.T) {
			f := factoryChecks{Kube: exposureClient(t, c.objs...), Namespace: "maku"}
			r := byCheck(f.source()(t.Context()))["exposure"]
			if (c.code == "" && r.Outcome != preflight.Pass) || (c.code != "" && r.Code != c.code) {
				t.Fatalf("%+v", r)
			}
		})
	}
}
