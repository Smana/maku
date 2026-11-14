// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

	"github.com/Smana/maku/internal/preflight"
)

var httpRouteV1 = schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}

func exposeRoute(name string, hosts ...string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(httpRouteV1)
	u.SetName(name)
	u.SetNamespace("maku")
	hs := make([]any, len(hosts))
	for i, h := range hosts {
		hs[i] = h
	}
	_ = unstructured.SetNestedSlice(u.Object, hs, "spec", "hostnames")
	return u
}

func outsideClient(t *testing.T, objs ...client.Object) client.Reader {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	s.AddKnownTypeWithName(httpRouteV1, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(httpRouteV1.GroupVersion().WithKind("HTTPRouteList"), &unstructured.UnstructuredList{})
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

// webProxy answers as oauth2-proxy in front of the broker: /ping is OK, /.well-known/roomctl is
// skip-auth, everything else redirects to sign in.
func webProxy(ping string) http.Handler {
	return webProxyDoc(ping, `{"issuer":"https://idp.example.com","preset":"generic","tokenUse":"id","humanMode":"oidc"}`)
}

func webProxyDoc(ping, discovery string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ping":
			_, _ = io.WriteString(w, ping)
		case "/.well-known/roomctl":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, discovery)
		default:
			http.Redirect(w, r, "/oauth2/sign_in", http.StatusFound)
		}
	})
}

// slackRoute answers as the gateway with the exact POST /slack/events route; wide also answers GET /.
func slackRoute(wide bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/slack/events" && r.Method == http.MethodPost:
			http.Error(w, "unsigned", http.StatusUnauthorized)
		case wide:
			_, _ = io.WriteString(w, "slack app")
		default:
			http.NotFound(w, r)
		}
	})
}

// outside runs the outside checks with each hostname served by its handler, and every other
// hostname unresolved.
func outside(t *testing.T, hosts map[string]http.Handler, objs ...client.Object) []preflight.Result {
	t.Helper()
	srvs := map[string]*httptest.Server{}
	for h, handler := range hosts {
		srvs[h] = httptest.NewServer(handler)
		t.Cleanup(srvs[h].Close)
	}
	d := OutsideDeps{
		Resolve: func(_ context.Context, host string) error {
			if _, ok := srvs[host]; !ok {
				return errors.New("no such host")
			}
			return nil
		},
		HC:      &http.Client{},
		BaseURL: func(host string) string { return srvs[host].URL },
	}
	return OutsideChecks(t.Context(), outsideClient(t, objs...), "maku", d)
}

func codes(rs []preflight.Result) string {
	var out []string
	for _, r := range rs {
		if r.Check != "exposure" || r.Component != "cli" {
			out = append(out, "bad:"+r.Check+"@"+r.Component)
		}
		out = append(out, string(r.Outcome)+"/"+r.Code)
	}
	return strings.Join(out, " ")
}

// The exposure row from where the user stands (IN2, F7): each host resolves, the web host is
// oauth2-proxy's and serves /.well-known/roomctl, and the Slack host answers POST /slack/events only.
func TestOutsideChecks(t *testing.T) {
	web, slack := exposeRoute("rooms-web", "rooms.example.com"), exposeRoute("factory-slack-events", "slack.example.com")
	for name, c := range map[string]struct {
		hosts map[string]http.Handler
		objs  []client.Object
		want  string
	}{
		"no expose route":                       {nil, nil, "skip/"},
		"both hosts answer":                     {map[string]http.Handler{"rooms.example.com": webProxy("OK"), "slack.example.com": slackRoute(false)}, []client.Object{web, slack}, "pass/ pass/"},
		"an unresolved host":                    {map[string]http.Handler{"slack.example.com": slackRoute(false)}, []client.Object{web, slack}, "fail/host_unresolved pass/"},
		"a Slack route too wide":                {map[string]http.Handler{"rooms.example.com": webProxy("OK"), "slack.example.com": slackRoute(true)}, []client.Object{web, slack}, "pass/ fail/slack_route_too_wide"},
		"a web host that is not oauth2-proxy's": {map[string]http.Handler{"rooms.example.com": http.NotFoundHandler()}, []client.Object{web}, "fail/route_unanswered"},
		"a /ping that is not oauth2-proxy's":    {map[string]http.Handler{"rooms.example.com": webProxy("pong")}, []client.Object{web}, "fail/route_unanswered"},
		"a broker in local mode":                {map[string]http.Handler{"rooms.example.com": webProxyDoc("OK", `{"url":"https://rooms.example.com","humanMode":"local"}`)}, []client.Object{web}, "pass/"},
		"an oidc broker without an issuer":      {map[string]http.Handler{"rooms.example.com": webProxyDoc("OK", `{"humanMode":"oidc"}`)}, []client.Object{web}, "fail/route_unanswered"},
		"a route without a hostname":            {nil, []client.Object{exposeRoute("rooms-web")}, "fail/undecided"},
		"an Ingress's host": {map[string]http.Handler{"rooms.example.com": webProxy("OK")}, []client.Object{&networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "rooms-web", Namespace: "maku"},
			Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "rooms.example.com"}}}}}, "pass/"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := codes(outside(t, c.hosts, c.objs...)); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// failing answers every read with an error that carries text the API server returned: it never
// reaches a result.
type failing struct{ client.Reader }

func (failing) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New(`Unauthorized: token "sk-live-abcdef" expired at https://10.0.0.1:6443`)
}

func TestOutsideChecksFixedMessages(t *testing.T) {
	rs := OutsideChecks(t.Context(), failing{}, "maku", OutsideDeps{})
	for _, r := range rs {
		if r.Code != preflight.Undecided || strings.Contains(r.Message, "sk-live") || strings.Contains(r.Message, "10.0.0.1") {
			t.Fatalf("%+v", r)
		}
	}
	if len(rs) != 2 {
		t.Fatalf("one undecided per route: %+v", rs)
	}
}
