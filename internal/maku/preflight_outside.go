// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"slices"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/preflight"
)

// The routes the umbrella's expose renders (charts/agent-factory/templates/expose.yaml), by name.
const (
	exposeWeb   = "rooms-web"
	exposeSlack = "factory-slack-events"
)

// OutsideDeps are the outside checks' seams; the zero value is the real resolver, a bounded client
// and https.
type OutsideDeps struct {
	Resolve func(ctx context.Context, host string) error
	HC      *http.Client
	BaseURL func(host string) string
}

func (d OutsideDeps) withDefaults() OutsideDeps {
	if d.Resolve == nil {
		d.Resolve = func(ctx context.Context, host string) error {
			_, err := net.DefaultResolver.LookupHost(ctx, host)
			return err
		}
	}
	hc := httpx.New(10*time.Second, nil)
	if d.HC != nil {
		c := *d.HC
		hc = &c
	}
	// A redirect is an answer to judge (oauth2-proxy's sign-in), never one to follow.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	d.HC = hc
	if d.BaseURL == nil {
		d.BaseURL = func(host string) string { return "https://" + host }
	}
	return d
}

// OutsideChecks is IN2's exposure row from where the user stands (v4 D24, F7): the hostnames of the
// release's web and Slack routes (HTTPRoute or Ingress) each resolve; the web host answers
// oauth2-proxy's /ping and serves /.well-known/roomctl through it; the Slack host answers an unsigned
// POST /slack/events with 401 and nothing else with a 200. One result per host; none is skip.
func OutsideChecks(ctx context.Context, c client.Reader, ns string, d OutsideDeps) []preflight.Result {
	d = d.withDefaults()
	var out []preflight.Result
	for _, s := range []struct {
		route string
		check func(context.Context, OutsideDeps, string) preflight.Result
	}{{exposeWeb, checkWebHost}, {exposeSlack, checkSlackHost}} {
		hosts, found, err := routeHosts(ctx, c, ns, s.route)
		if err != nil {
			// A fixed message: the API server's error text stays out of the report.
			out = append(out, outsideResult(preflight.Fail, preflight.Undecided, fmt.Sprintf("the %s route is unreadable: your kubeconfig needs get on HTTPRoutes and Ingresses in %s", s.route, ns)))
			continue
		}
		if found && len(hosts) == 0 {
			out = append(out, outsideResult(preflight.Fail, preflight.Undecided, fmt.Sprintf("the %s route names no hostname: nothing to check from here", s.route)))
			continue
		}
		for _, h := range hosts {
			if err := d.Resolve(ctx, h); err != nil {
				out = append(out, outsideResult(preflight.Fail, "host_unresolved", h+" does not resolve from this machine"))
				continue
			}
			out = append(out, s.check(ctx, d, h))
		}
	}
	if len(out) == 0 {
		return []preflight.Result{outsideResult(preflight.Skip, "", "the release exposes no web or Slack route (expose.mode none)")}
	}
	return out
}

func outsideResult(o preflight.Outcome, code, msg string) preflight.Result {
	return preflight.Result{Check: "exposure", Component: string(preflight.InCLI), Outcome: o, Code: code, Message: msg}
}

// checkWebHost: oauth2-proxy answers /ping with OK, and lets /.well-known/roomctl through to the
// broker, which answers JSON naming the issuer (F7: maku configure reads it there).
func checkWebHost(ctx context.Context, d OutsideDeps, host string) preflight.Result {
	base := d.BaseURL(host)
	if code, body := statusOf(ctx, d.HC, http.MethodGet, base+"/ping"); code != http.StatusOK || !bytes.Equal(bytes.TrimSpace(body), []byte("OK")) {
		return outsideResult(preflight.Fail, "route_unanswered", fmt.Sprintf("%s/ping is not oauth2-proxy's (HTTP %d): the web route must reach rooms-web-proxy", host, code))
	}
	code, body := statusOf(ctx, d.HC, http.MethodGet, base+"/.well-known/roomctl")
	var doc struct {
		Issuer    string `json:"issuer"`
		HumanMode string `json:"humanMode"`
	}
	// An oidc broker names its issuer; a local one names none (it needs no IdP).
	if code != http.StatusOK || json.Unmarshal(body, &doc) != nil || (doc.Issuer == "" && doc.HumanMode != "local") {
		return outsideResult(preflight.Fail, "route_unanswered", fmt.Sprintf("%s/.well-known/roomctl does not reach the broker (HTTP %d): maku configure reads it there", host, code))
	}
	return outsideResult(preflight.Pass, "", host+": oauth2-proxy, and /.well-known/roomctl through it")
}

// checkSlackHost: the route is POST /slack/events exactly. An unsigned event is the Slack app's 401;
// a GET there or at / must be the gateway's 404 (or 405), never a 2xx.
func checkSlackHost(ctx context.Context, d OutsideDeps, host string) preflight.Result {
	base := d.BaseURL(host)
	for _, path := range []string{"/", "/slack/events"} {
		if code, _ := statusOf(ctx, d.HC, http.MethodGet, base+path); code >= 200 && code < 300 {
			return outsideResult(preflight.Fail, "slack_route_too_wide", fmt.Sprintf("%s answers GET %s with HTTP %d: the Slack route must be POST /slack/events only", host, path, code))
		}
	}
	if code, _ := statusOf(ctx, d.HC, http.MethodPost, base+"/slack/events"); code != http.StatusUnauthorized {
		return outsideResult(preflight.Fail, "route_unanswered", fmt.Sprintf("%s answers an unsigned POST /slack/events with HTTP %d, not the Slack app's 401", host, code))
	}
	return outsideResult(preflight.Pass, "", host+": POST /slack/events only, signatures checked")
}

// routeHosts are a route's hostnames: the HTTPRoute's (v1, else v1beta1), else the Ingress's rules'.
// found is false when neither exists.
func routeHosts(ctx context.Context, c client.Reader, ns, name string) (hosts []string, found bool, err error) {
	for _, v := range []string{"v1", "v1beta1"} {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: v, Kind: "HTTPRoute"})
		err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, u)
		if err == nil {
			hs, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "hostnames")
			return hs, true, nil
		}
		if meta.IsNoMatchError(err) || isNotRegistered(err) {
			continue
		}
		if client.IgnoreNotFound(err) != nil {
			return nil, false, err
		}
		break
	}
	var ing networkingv1.Ingress
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &ing); err != nil {
		if client.IgnoreNotFound(err) == nil || meta.IsNoMatchError(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var hs []string
	for _, r := range ing.Spec.Rules {
		if r.Host != "" && !slices.Contains(hs, r.Host) {
			hs = append(hs, r.Host)
		}
	}
	return hs, true, nil
}

// isNotRegistered is a kind the client's scheme does not know: the version is not this client's.
func isNotRegistered(err error) bool { return runtime.IsNotRegisteredError(err) }
