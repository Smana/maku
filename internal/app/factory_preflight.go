// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/minter"
	"github.com/Smana/maku/internal/preflight"
)

// brokerService is the broker's Service when broker.url names none: a route to it reaches the
// broker around the proxy (F5).
const brokerService = "room-broker"

// appGrants is one App's installations, checked against its required set.
type appGrants struct {
	Name    string
	Checker grantChecker
}

// gateState is the merge gate observer's last read per repository (D11), which the preflight serves
// without a read of its own.
type gateState struct {
	mu sync.Mutex
	m  map[string]string // lowercased repository → required, not, unread
}

func (g *gateState) set(repo, v string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[string]string{}
	}
	g.m[strings.ToLower(repo)] = v
}

func (g *gateState) get(repo string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.m[strings.ToLower(repo)]
}

// record wraps the observer's gauge: the read is kept, then passed on.
func (g *gateState) record(next func(ctx context.Context, repo string, required bool)) func(ctx context.Context, repo string, required bool) {
	return func(ctx context.Context, repo string, required bool) {
		g.set(repo, map[bool]string{true: "required", false: "not"}[required])
		next(ctx, repo, required)
	}
}

// unread wraps the observer's unread counter.
func (g *gateState) unread(next func(ctx context.Context, repo string)) func(ctx context.Context, repo string) {
	return func(ctx context.Context, repo string) {
		g.set(repo, "unread")
		next(ctx, repo)
	}
}

// factoryChecks are the factory's own preflight checks (v4 D24, IN3): its Apps' installations and
// grants, the merge gate's last read, private code, the in-cluster exposure and its IdP. Each runs
// with the factory's own credentials and network.
type factoryChecks struct {
	Live      func() *config.Config
	Repos     func() []string
	Apps      []appGrants
	Gate      *gateState
	Private   *privateCodeTracker
	Kube      client.Reader
	Namespace string
	HC        *http.Client
}

func (f factoryChecks) source() preflight.Source {
	return func(ctx context.Context) []preflight.Result {
		checks := []struct {
			id  string
			run func(context.Context) preflight.Result
		}{
			{"apps", f.apps}, {"merge-gate", f.mergeGate}, {"private-code", f.privateCode}, {"exposure", f.exposure}, {"oidc", f.oidc},
		}
		out := make([]preflight.Result, len(checks))
		var wg sync.WaitGroup
		for i, c := range checks {
			wg.Go(func() { out[i] = preflight.Bounded(ctx, preflight.CheckTimeout, c.id, c.run) })
		}
		wg.Wait()
		return out
	}
}

// preflightAppNeed is the factory App's required set: the demo's one App's map, or the factory
// App's, the merger's included when folded.
func preflightAppNeed(cfg *config.Config, folded bool) minter.Permissions {
	if cfg.Demo {
		return minter.Permissions(demoPermissions)
	}
	return factoryAppNeed(cfg, folded)
}

// apps checks each App's installation on each served repository, and its grants, one repository at
// a time so a refusal names it.
func (f factoryChecks) apps(ctx context.Context) preflight.Result {
	if len(f.Apps) == 0 || f.Repos == nil {
		return skip("no App to check")
	}
	repos := f.Repos()
	if len(repos) == 0 {
		return skip("no repository is served")
	}
	for _, a := range f.Apps {
		for _, repo := range repos {
			missing, err := a.Checker.CheckGranted(ctx, []string{repo})
			switch {
			case errors.Is(err, forge.ErrNotInstalled):
				return failed("app_not_installed", fmt.Sprintf("the %s App is not installed on %s", a.Name, repo))
			case err != nil:
				return failed(preflight.Undecided, fmt.Sprintf("the %s App's installation on %s is unreadable", a.Name, repo))
			}
			if m := missing[repo]; len(m) > 0 {
				var perms []string
				for _, k := range slices.Sorted(func(yield func(string) bool) {
					for k := range m {
						if !yield(k) {
							return
						}
					}
				}) {
					perms = append(perms, k+": "+m[k])
				}
				return failed("app_permission_missing", fmt.Sprintf("the %s App lacks %s on %s", a.Name, strings.Join(perms, ", "), repo))
			}
		}
	}
	return pass(fmt.Sprintf("%d Apps on %d repositories, with their grants", len(f.Apps), len(repos)))
}

// mergeGate reads the observer's last read of each repository with merge.gateStatus.
func (f factoryChecks) mergeGate(context.Context) preflight.Result {
	if f.Live == nil || f.Gate == nil {
		return skip("no repository sets merge.gateStatus")
	}
	var gated []string
	for _, r := range f.Live().Repos() {
		if r.Merge.GateStatus {
			gated = append(gated, r.Name)
		}
	}
	if len(gated) == 0 {
		return skip("no repository sets merge.gateStatus")
	}
	var unread []string
	for _, repo := range gated {
		switch f.Gate.get(repo) {
		case "":
			return failed(preflight.Undecided, repo+": the merge gate's ruleset is not read yet: retry after the factory's first fence pass")
		case "not":
			return failed("merge_gate_not_required", repo+": the default branch does not require maku/merge-gate from the App that posts it (docs/factory.md#branch-protection-for-the-merge-gate)")
		case "unread":
			unread = append(unread, repo)
		}
	}
	if len(unread) > 0 {
		return preflight.Result{Outcome: preflight.Warn, Code: "merge_gate_rules_unread",
			Message: strings.Join(unread, ", ") + ": the branch rules are unreadable, so the gate's requirement is unknown"}
	}
	return pass(fmt.Sprintf("%d repositories require maku/merge-gate", len(gated)))
}

// privateCode is D3 (step 8 R9): a private repository without spec.privateCode.acknowledged fails,
// since the factory refuses it new tasks.
func (f factoryChecks) privateCode(context.Context) preflight.Result {
	if f.Live == nil || f.Private == nil {
		return skip("no repository is served")
	}
	var unack []string
	for _, r := range f.Live().Repos() {
		vis := f.Private.Visibility(r.Name)
		if vis == "" {
			return failed(preflight.Undecided, r.Name+": its visibility is not read yet: retry after the factory's first fence pass")
		}
		if vis != "public" && !r.PrivateCodeAcknowledged() {
			unack = append(unack, r.Name)
		}
	}
	if len(unack) > 0 {
		return failed("private_code_unacknowledged", strings.Join(unack, ", ")+": private code without spec.privateCode.acknowledged is refused new tasks (D3)")
	}
	return pass("every private repository is acknowledged")
}

// routeKinds are the Gateway API route kinds that can carry traffic to a Service, each with the
// versions to try in order: a cluster serves the newest it has.
var routeKinds = []struct {
	kind     string
	versions []string
}{
	{"HTTPRoute", []string{"v1", "v1beta1"}},
	{"GRPCRoute", []string{"v1", "v1alpha2"}},
	{"TLSRoute", []string{"v1", "v1alpha3", "v1alpha2"}},
}

// brokerTarget is the broker's Service and namespace, read from broker.url's host: a cluster Service
// name (name, name.ns, name.ns.svc[.domain]). ok is false for a host outside the cluster.
func brokerTarget(live func() *config.Config, ns string) (name, namespace string, ok bool) {
	if live == nil || live() == nil || live().Broker.URL == "" {
		return brokerService, ns, true
	}
	u, err := url.Parse(live().Broker.URL)
	if err != nil || u.Hostname() == "" || net.ParseIP(u.Hostname()) != nil {
		return "", "", false
	}
	labels := strings.Split(u.Hostname(), ".")
	switch {
	case len(labels) == 1:
		return labels[0], ns, true
	case len(labels) == 2 || labels[2] == "svc":
		return labels[0], labels[1], true
	}
	return "", "", false
}

// refTargets reports whether a Gateway API backendRef (or a mirror's) names the Service name in
// namespace: kind Service in the core group, its namespace the route's when unset.
func refTargets(ref map[string]any, routeNS, name, namespace string) bool {
	group, _ := ref["group"].(string)
	kind, _ := ref["kind"].(string)
	n, _ := ref["name"].(string)
	refNS, _ := ref["namespace"].(string)
	if refNS == "" {
		refNS = routeNS
	}
	return group == "" && (kind == "" || kind == "Service") && n == name && refNS == namespace
}

// routeRefs is every backendRef a route's rules send to: the rules', and their mirrors', at rule and
// at backendRef level.
func routeRefs(obj map[string]any) []map[string]any {
	var out []map[string]any
	mirrors := func(filters []any) {
		for _, f := range filters {
			fm, _ := f.(map[string]any)
			if ref, ok, _ := unstructured.NestedMap(fm, "requestMirror", "backendRef"); ok {
				out = append(out, ref)
			}
		}
	}
	rules, _, _ := unstructured.NestedSlice(obj, "spec", "rules")
	for _, rule := range rules {
		rm, _ := rule.(map[string]any)
		fs, _, _ := unstructured.NestedSlice(rm, "filters")
		mirrors(fs)
		refs, _, _ := unstructured.NestedSlice(rm, "backendRefs")
		for _, ref := range refs {
			m, _ := ref.(map[string]any)
			out = append(out, m)
			bfs, _, _ := unstructured.NestedSlice(m, "filters")
			mirrors(bfs)
		}
	}
	return out
}

// exposure (F5): no route or Ingress in the namespace targets the broker's Service, on any port: its
// human port would reach it around oauth2-proxy (rooms-web-proxy), and its bridge and ops ports are
// never public. The factory reads its own namespace only (its Role is namespaced), so a broker in
// another namespace, or outside the cluster, is undecided, never a pass. A route kind the cluster
// serves at none of its versions has no route of that kind.
func (f factoryChecks) exposure(ctx context.Context) preflight.Result {
	if f.Kube == nil {
		return skip("no cluster client")
	}
	name, ns, ok := brokerTarget(f.Live, f.Namespace)
	if !ok {
		return failed(preflight.Undecided, "broker.url is not a Service in this cluster: the routes that could reach the broker are not the factory's to read")
	}
	var found []string
	for _, rk := range routeKinds {
		for _, v := range rk.versions {
			routes := &unstructured.UnstructuredList{}
			routes.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: v, Kind: rk.kind + "List"})
			err := f.Kube.List(ctx, routes, client.InNamespace(f.Namespace))
			if meta.IsNoMatchError(err) {
				continue // not served at this version: try the next
			}
			if err != nil {
				return failed(preflight.Undecided, fmt.Sprintf("the namespace's %ss are unreadable: the factory's Role needs get and list on them (preflight.enabled)", rk.kind))
			}
			for _, r := range routes.Items {
				for _, ref := range routeRefs(r.Object) {
					if refTargets(ref, r.GetNamespace(), name, ns) {
						found = append(found, rk.kind+" "+r.GetName())
					}
				}
			}
			break
		}
	}
	var ingresses networkingv1.IngressList
	switch err := f.Kube.List(ctx, &ingresses, client.InNamespace(f.Namespace)); {
	case meta.IsNoMatchError(err):
	case err != nil:
		return failed(preflight.Undecided, "the namespace's Ingresses are unreadable: the factory's Role needs get and list on them (preflight.enabled)")
	default:
		for _, ing := range ingresses.Items {
			backends := []*networkingv1.IngressBackend{ing.Spec.DefaultBackend}
			for _, rule := range ing.Spec.Rules {
				if rule.HTTP != nil {
					for i := range rule.HTTP.Paths {
						backends = append(backends, &rule.HTTP.Paths[i].Backend)
					}
				}
			}
			for _, b := range backends {
				if b != nil && b.Service != nil && b.Service.Name == name && ing.Namespace == ns {
					found = append(found, "Ingress "+ing.Name)
				}
			}
		}
	}
	if len(found) > 0 {
		slices.Sort(found)
		return failed("broker_exposed_around_proxy", strings.Join(slices.Compact(found), ", ")+
			" targets the broker directly, around oauth2-proxy: route the web host to rooms-web-proxy (docs/exposure.md)")
	}
	if ns != f.Namespace {
		return failed(preflight.Undecided, fmt.Sprintf("the broker runs in namespace %s and the factory reads routes in %s only: check %s's routes yourself (docs/exposure.md)", ns, f.Namespace, ns))
	}
	return pass("no route reaches the broker around the proxy")
}

// oidc is the factory's own read of its humans' IdP, as the broker's.
func (f factoryChecks) oidc(ctx context.Context) preflight.Result {
	if f.Live == nil {
		return skip("no config")
	}
	cfg := f.Live()
	if cfg.Demo || cfg.API.HumanIssuer == "" {
		return skip("the factory takes no human token (demo)")
	}
	return oidcCheck(ctx, f.HC, cfg.API.HumanIssuer, cfg.API.HumanJWKS)
}
