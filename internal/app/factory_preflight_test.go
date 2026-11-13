// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
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

var gw = func(v, kind string) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: v, Kind: kind}
}

// xroute is a route of kind at version v in namespace maku, with one rule whose backendRefs are refs.
func xroute(gvk schema.GroupVersionKind, name string, refs ...map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetName(name)
	u.SetNamespace("maku")
	list := make([]any, len(refs))
	for i, r := range refs {
		list[i] = r
	}
	_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"backendRefs": list}}, "spec", "rules")
	return u
}

func route(name, backend string, port int64) *unstructured.Unstructured {
	return xroute(gw("v1", "HTTPRoute"), name, map[string]any{"name": backend, "port": port})
}

// servedOnly answers NoKindMatch for a route list whose version the cluster does not serve, as the
// API server's discovery does.
type servedOnly struct {
	client.Reader
	served map[schema.GroupVersionKind]bool
}

func (s servedOnly) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if u, ok := list.(*unstructured.UnstructuredList); ok {
		gvk := u.GroupVersionKind()
		if !s.served[gvk] {
			return &meta.NoKindMatchError{GroupKind: gvk.GroupKind(), SearchedVersions: []string{gvk.Version}}
		}
	}
	return s.Reader.List(ctx, list, opts...)
}

// exposureClient serves the route kinds at the versions given (all of them at v1 when none is given).
func exposureClient(t *testing.T, served []schema.GroupVersionKind, objs ...client.Object) client.Reader {
	t.Helper()
	if served == nil {
		served = []schema.GroupVersionKind{gw("v1", "HTTPRoute"), gw("v1", "GRPCRoute"), gw("v1alpha2", "TLSRoute")}
	}
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	set := map[schema.GroupVersionKind]bool{}
	for _, g := range served {
		s.AddKnownTypeWithName(g, &unstructured.Unstructured{})
		lg := g.GroupVersion().WithKind(g.Kind + "List")
		s.AddKnownTypeWithName(lg, &unstructured.UnstructuredList{})
		set[lg] = true
	}
	return servedOnly{Reader: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build(), served: set}
}

// F5: a route to the broker, on any port, of any route kind the cluster serves, fails; one to the
// proxy passes; a scope the factory cannot read is undecided, never a pass.
func TestFactoryPreflightExposure(t *testing.T) {
	broker := func(url string) func() *config.Config {
		return func() *config.Config { return &config.Config{Broker: config.Broker{URL: url}} }
	}
	same := broker("https://room-broker.maku.svc.cluster.local:8443")
	for name, c := range map[string]struct {
		live   func() *config.Config
		served []schema.GroupVersionKind
		objs   []client.Object
		code   string
	}{
		"a route to the web proxy":           {same, nil, []client.Object{route("web", "rooms-web-proxy", 80)}, ""},
		"no route at all":                    {same, nil, nil, ""},
		"a route to the broker's ops port":   {same, nil, []client.Object{route("ops", "room-broker", 9090)}, "broker_exposed_around_proxy"},
		"a route to the broker's human port": {same, nil, []client.Object{route("bypass", "room-broker", 8080)}, "broker_exposed_around_proxy"},
		"an Ingress to the broker's human port": {same, nil, []client.Object{&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "rooms", Namespace: "maku"},
			Spec: networkingv1.IngressSpec{DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
				Name: "room-broker", Port: networkingv1.ServiceBackendPort{Name: "human"}}}}}}, "broker_exposed_around_proxy"},
		"a GRPCRoute to the broker": {same, nil, []client.Object{xroute(gw("v1", "GRPCRoute"), "g", map[string]any{"name": "room-broker", "port": int64(8443)})}, "broker_exposed_around_proxy"},
		"a TLSRoute to the broker":  {same, nil, []client.Object{xroute(gw("v1alpha2", "TLSRoute"), "t", map[string]any{"name": "room-broker", "port": int64(8443)})}, "broker_exposed_around_proxy"},
		"an HTTPRoute served at v1beta1 only": {same, []schema.GroupVersionKind{gw("v1beta1", "HTTPRoute")},
			[]client.Object{xroute(gw("v1beta1", "HTTPRoute"), "old", map[string]any{"name": "room-broker", "port": int64(8080)})}, "broker_exposed_around_proxy"},
		"a backendRef naming the broker's namespace": {same, nil, []client.Object{xroute(gw("v1", "HTTPRoute"), "x", map[string]any{"name": "room-broker", "namespace": "maku", "port": int64(8080)})}, "broker_exposed_around_proxy"},
		"a room-broker of another namespace":         {same, nil, []client.Object{xroute(gw("v1", "HTTPRoute"), "x", map[string]any{"name": "room-broker", "namespace": "elsewhere", "port": int64(8080)})}, ""},
		"a backendRef to another kind":               {same, nil, []client.Object{xroute(gw("v1", "HTTPRoute"), "x", map[string]any{"name": "room-broker", "kind": "ServiceImport", "group": "multicluster.x-k8s.io"})}, ""},
		"a mirror to the broker": {same, nil, []client.Object{func() client.Object {
			u := xroute(gw("v1", "HTTPRoute"), "m")
			_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"filters": []any{map[string]any{"type": "RequestMirror",
				"requestMirror": map[string]any{"backendRef": map[string]any{"name": "room-broker", "port": int64(8080)}}}}}}, "spec", "rules")
			return u
		}()}, "broker_exposed_around_proxy"},
		"the broker in another namespace":   {broker("https://room-broker.agent-system.svc:8443"), nil, nil, preflight.Undecided},
		"a broker outside the cluster":      {broker("https://rooms.example.com:8443"), nil, nil, preflight.Undecided},
		"the broker by its short name":      {broker("https://room-broker:8443"), nil, nil, ""},
		"a cluster without the Gateway API": {same, []schema.GroupVersionKind{}, nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := factoryChecks{Live: c.live, Kube: exposureClient(t, c.served, c.objs...), Namespace: "maku"}
			r := byCheck(f.source()(t.Context()))["exposure"]
			if (c.code == "" && r.Outcome != preflight.Pass) || (c.code != "" && r.Code != c.code) {
				t.Fatalf("%+v", r)
			}
		})
	}
}
