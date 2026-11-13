// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"context"
	"strings"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// tokenAPI answers each TokenRequest with an expiry capped at limit, as an apiserver started with
// --service-account-max-token-expiration does, and records what was asked.
func tokenAPI(t *testing.T, now time.Time, limit time.Duration, err error, asked *[]string) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		SubResourceCreate: func(_ context.Context, _ client.Client, sub string, obj, subObj client.Object, _ ...client.SubResourceCreateOption) error {
			tr := subObj.(*authenticationv1.TokenRequest)
			bound := "unbound"
			if b := tr.Spec.BoundObjectRef; b != nil {
				bound = b.APIVersion + "/" + b.Kind + " " + b.Name + " " + string(b.UID)
			}
			*asked = append(*asked, sub+" "+obj.GetNamespace()+"/"+obj.GetName()+" "+strings.Join(tr.Spec.Audiences, ",")+" "+bound)
			if err != nil {
				return err
			}
			d := min(time.Duration(*tr.Spec.ExpirationSeconds)*time.Second, limit)
			tr.Status = authenticationv1.TokenRequestStatus{Token: "t", ExpirationTimestamp: metav1.NewTime(now.Add(d))}
			return nil
		},
	}).Build()
}

// An API error reading the model Secret is an error to retry, not a hold that names a missing Secret.
func TestInputsModelSecretErrorIsNoHold(t *testing.T) {
	boom := apierrors.NewServiceUnavailable("apiserver down")
	c := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return boom
		},
	}).Build()
	r := &Reconciler{Client: c, APIReader: c, Cfg: testConfig(t)}
	if _, _, h, err := r.inputs(t.Context(), run("run-abcdefgh"), time.Now()); err == nil || h != (hold{}) {
		t.Fatalf("hold %+v err %v, want no hold and the error", h, err)
	}
}

// GW9: gateway tokens live the longest run's deadline plus 600 s; an apiserver that caps tokens under
// that refuses gateway runs, naming the cap.
func TestPreflightGatewayTokenTTL(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts/token"}, "run-controller", nil)
	for name, tc := range map[string]struct {
		limit    time.Duration
		err      error
		state    string
		message  string
		retrySun bool
	}{
		"uncapped": {limit: 24 * time.Hour, state: PreflightPassed},
		"capped": {limit: time.Hour, state: PreflightFailed,
			message: "the apiserver caps ServiceAccount tokens at 1h0m0s; gateway runs need 8h10m0s (budget.maxMinutes 480 plus 600 s)"},
		"forbidden": {err: forbidden, state: PreflightFailed, retrySun: true,
			message: "the run-controller may not request its own ServiceAccount token (maku/run-controller): it needs create on serviceaccounts/token for that name (the chart's run-controller-token Role)"},
	} {
		var asked []string
		p := newPreflight(t, fakeReader(t, gvisor()), gatewayTestConfig(t))
		p.Kube, p.Now = tokenAPI(t, now, tc.limit, tc.err, &asked), func() time.Time { return now }
		p.Self = PodRef{Name: "run-controller-7d9f-x2", UID: "uid-rc"}
		gauge := map[string]bool{}
		p.Passed = func(check string, ok bool) { gauge[check] = ok }
		soon := p.Tick(t.Context())
		got := p.Result()
		if got.State != tc.state || (tc.message != "" && (got.Check != "gateway-token-ttl" || !strings.HasPrefix(got.Message, tc.message))) {
			t.Errorf("%s: %+v, want %s %q", name, got, tc.state, tc.message)
		}
		if soon != tc.retrySun {
			t.Errorf("%s: retry soon %v", name, soon)
		}
		if ok, seen := gauge["gateway-token-ttl"]; !seen || ok != (tc.state == PreflightPassed) {
			t.Errorf("%s: gauge %v", name, gauge)
		}
		if len(asked) != 1 || asked[0] != "token maku/run-controller maku-gateway.preflight v1/Pod run-controller-7d9f-x2 uid-rc" {
			t.Errorf("%s: token requests %v", name, asked)
		}
	}
	// Without its own pod (the downward API's POD_NAME and POD_UID), the controller requests no token:
	// an unbound one would outlive it.
	var unbound []string
	p := newPreflight(t, fakeReader(t, gvisor()), gatewayTestConfig(t))
	p.Kube = tokenAPI(t, now, 24*time.Hour, nil, &unbound)
	p.Tick(t.Context())
	if got := p.Result(); got.State != PreflightFailed || got.Check != "gateway-token-ttl" || !strings.Contains(got.Message, "POD_NAME and POD_UID") || len(unbound) != 0 {
		t.Errorf("no pod reference: %+v, requests %v", got, unbound)
	}
	// secret mode requests no token.
	var asked []string
	p = newPreflight(t, fakeReader(t, gvisor()), testConfig(t))
	p.Kube = tokenAPI(t, now, time.Hour, nil, &asked)
	if p.Tick(t.Context()); p.Result().State != PreflightPassed || len(asked) != 0 {
		t.Fatalf("secret mode: %+v, requests %v", p.Result(), asked)
	}
}
