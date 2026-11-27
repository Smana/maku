// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
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
		return &config.Config{Repositories: []config.Repository{{Name: "o/r", Merge: config.Merge{GateStatus: new(true)}}}}
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

// v4 D3, step 8 R9: the preflight's private-code check fails, no longer warns, on private code without
// its acknowledgement.
func TestFactoryPreflightPrivateCodeFails(t *testing.T) {
	g := newPrivateCodeRig(t, false)
	g.f.SetRepositoryInfo(forge.RepositoryInfo{FullName: "Smana/demo", Private: true, Visibility: "private"})
	g.pc.Observe(t.Context(), "Smana/demo", true)
	r := factoryChecks{Live: func() *config.Config { return g.cfg }, Private: g.pc}.privateCode(t.Context())
	if r.Outcome != preflight.Fail || r.Code != "private_code_unacknowledged" {
		t.Fatalf("got %s %s, want fail private_code_unacknowledged", r.Outcome, r.Code)
	}
	g.cfg.Repositories[0].PrivateCode = &config.PrivateCode{Acknowledged: true}
	if r := (factoryChecks{Live: func() *config.Config { return g.cfg }, Private: g.pc}).privateCode(t.Context()); r.Outcome != preflight.Pass {
		t.Fatalf("acknowledged: %s %s", r.Outcome, r.Code)
	}
}

// WF11: maku repo check asks the factory for one repository's checks: apps, merge-gate and
// private-code, judged on that repository alone, another's failure never leaking in.
func TestPreflightFiltersToOneRepository(t *testing.T) {
	live := func() *config.Config {
		return &config.Config{Repositories: []config.Repository{
			{Name: "acme/my-service", Merge: config.Merge{GateStatus: new(true)}},
			{Name: "acme/other", Merge: config.Merge{GateStatus: new(true)}},
		}}
	}
	g := &gateState{}
	g.set("acme/my-service", "required")
	g.set("acme/other", "not")
	pc := &privateCodeTracker{vis: map[string]string{"acme/my-service": "public", "acme/other": "private"}}
	apps := []appGrants{{Name: "factory", Checker: repoGrants{errs: map[string]error{"acme/other": forge.ErrNotInstalled}}}}
	f := factoryChecks{Live: live, Repos: servedRepos("acme/my-service", "acme/other"), Apps: apps, Gate: g, Private: pc}

	res := f.forRepository("ACME/my-service")(t.Context())
	var ids []string
	for _, r := range res {
		ids = append(ids, r.Check)
		if r.Outcome != preflight.Pass {
			t.Errorf("%s: %+v", r.Check, r)
		}
		if strings.Contains(r.Message, "acme/other") {
			t.Fatalf("another repository leaked: %s", r.Message)
		}
	}
	if strings.Join(ids, ",") != "apps,merge-gate,private-code" {
		t.Fatalf("got %v", ids)
	}
	for _, r := range f.forRepository("acme/other")(t.Context()) {
		if r.Outcome != preflight.Fail {
			t.Errorf("acme/other %s: %+v", r.Check, r)
		}
	}
	// A repository the factory does not serve: nothing to judge, and nothing passes.
	for _, r := range f.forRepository("acme/unknown")(t.Context()) {
		if r.Outcome != preflight.Fail || r.Code != preflight.Undecided || !strings.Contains(r.Message, "acme/unknown is not served") {
			t.Errorf("unknown %s: %+v", r.Check, r)
		}
	}
}

// GET /preflight?repository=owner/name serves forRepository; without the parameter, the whole set.
func TestPreflightRouteFiltersOnTheQuery(t *testing.T) {
	all := func(context.Context) []preflight.Result {
		return []preflight.Result{{Check: "oidc", Outcome: preflight.Pass}}
	}
	one := func(repo string) preflight.Source {
		return func(context.Context) []preflight.Result {
			return []preflight.Result{{Check: "apps", Outcome: preflight.Pass, Message: repo}}
		}
	}
	h := preflightHandler(all, one, nil)
	for target, want := range map[string]string{
		"/preflight":                            `"check":"oidc"`,
		"/preflight?repository=acme/my-service": `"message":"acme/my-service"`,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", target, rec.Code, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/preflight?repository=not-a-repo", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a malformed repository: %d", rec.Code)
	}
}

type failingReader struct{ client.Reader }

func (failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return context.DeadlineExceeded
}

func TestFactoryChecksRoutinesUIWrite(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = factoryv1.AddToScheme(scheme)

	repoWithLabels := func(name string, labels map[string]string) *factoryv1.Repository {
		return &factoryv1.Repository{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "agent-system", Labels: labels},
			Spec:       factoryv1.RepositorySpec{FactoryRef: "prod", Repository: "acme/" + name},
		}
	}

	t.Run("uiWrite false passes even with Flux labels", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			repoWithLabels("r1", map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps"}),
		).Build()
		f := factoryChecks{
			Live:      func() *config.Config { return &config.Config{Routines: config.RoutinesConfig{UIWrite: false}} },
			Kube:      cl,
			Namespace: "agent-system",
		}
		res := f.routinesUIWrite(t.Context())
		if res.Outcome != preflight.Pass {
			t.Fatalf("got %+v, want Pass", res)
		}
	})

	t.Run("uiWrite true without GitOps labels passes", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			repoWithLabels("r1", map[string]string{"team": "platform"}),
		).Build()
		f := factoryChecks{
			Live:      func() *config.Config { return &config.Config{Routines: config.RoutinesConfig{UIWrite: true}} },
			Kube:      cl,
			Namespace: "agent-system",
		}
		res := f.routinesUIWrite(t.Context())
		if res.Outcome != preflight.Pass {
			t.Fatalf("got %+v, want Pass", res)
		}
	})

	t.Run("uiWrite true with Flux or Argo labels warns ui_write_gitops_managed", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			repoWithLabels("flux-repo", map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps"}),
			repoWithLabels("argo-repo", map[string]string{"argocd.argoproj.io/instance": "maku"}),
		).Build()
		f := factoryChecks{
			Live:      func() *config.Config { return &config.Config{Routines: config.RoutinesConfig{UIWrite: true}} },
			Kube:      cl,
			Namespace: "agent-system",
		}
		res := f.routinesUIWrite(t.Context())
		if res.Outcome != preflight.Warn || res.Code != "ui_write_gitops_managed" ||
			!strings.Contains(res.Message, "argo-repo") || !strings.Contains(res.Message, "flux-repo") {
			t.Fatalf("got %+v, want Warn ui_write_gitops_managed naming both repos", res)
		}
	})

	t.Run("uiWrite true fails ui_write_check_failed when Repository list fails", func(t *testing.T) {
		f := factoryChecks{
			Live:      func() *config.Config { return &config.Config{Routines: config.RoutinesConfig{UIWrite: true}} },
			Kube:      failingReader{},
			Namespace: "agent-system",
		}
		res := f.routinesUIWrite(t.Context())
		if res.Outcome != preflight.Fail || res.Code != "ui_write_check_failed" {
			t.Fatalf("got %+v, want Fail ui_write_check_failed", res)
		}
	})

	t.Run("factoryChecks.source runs routines-ui-write", func(t *testing.T) {
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			repoWithLabels("flux-repo", map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps"}),
		).Build()
		f := factoryChecks{
			Live:      func() *config.Config { return &config.Config{Routines: config.RoutinesConfig{UIWrite: true}} },
			Kube:      cl,
			Namespace: "agent-system",
		}
		results := f.source()(t.Context())
		var found bool
		for _, r := range results {
			if r.Check == "routines-ui-write" && r.Outcome == preflight.Warn && r.Code == "ui_write_gitops_managed" {
				found = true
			}
		}
		if !found {
			t.Fatalf("routines-ui-write missing or wrong in source() results: %+v", results)
		}
	})
}
