// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func fakeReader(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func gvisor() *nodev1.RuntimeClass {
	return &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: "runsc"}
}

func newPreflight(t *testing.T, c client.Reader, cfg Config) *Preflight {
	t.Helper()
	return &Preflight{Client: c, Cfg: cfg, Log: slog.New(slog.DiscardHandler)}
}

func TestPreflightIsUnknownBeforeTheFirstTick(t *testing.T) {
	p := newPreflight(t, fakeReader(t, gvisor()), testConfig(t))
	if got := p.Result(); got.State != PreflightUnknown {
		t.Fatalf("before the first tick: %+v", got)
	}
}

func TestPreflightFailsWhenTheRuntimeClassIsMissing(t *testing.T) {
	p := newPreflight(t, fakeReader(t), testConfig(t))
	p.Tick(t.Context())
	got := p.Result()
	want := `RuntimeClass "gvisor" not found: install the sandbox runtime on your nodes, or set sandbox.runtimeClassName`
	if got.State != PreflightFailed || got.Check != "runtimeClass" || got.Message != want || got.At.IsZero() {
		t.Fatalf("missing class: %+v", got)
	}
}

func TestPreflightPassesWhenTheRuntimeClassExists(t *testing.T) {
	p := newPreflight(t, fakeReader(t, gvisor()), testConfig(t))
	p.Tick(t.Context())
	if got := p.Result(); got.State != PreflightPassed {
		t.Fatalf("class present: %+v", got)
	}
}

func TestPreflightUnsandboxedMakesNoAPICall(t *testing.T) {
	calls := 0
	c := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			calls++
			return errors.New("no call expected")
		}}).Build()
	cfg := testConfig(t)
	cfg.RuntimeClassName, cfg.AllowUnsandboxed = "", true
	p := newPreflight(t, c, cfg)
	p.Tick(t.Context())
	if got := p.Result(); got.State != PreflightPassed || calls != 0 {
		t.Fatalf("unsandboxed: %+v after %d calls", got, calls)
	}
}

func TestPreflightALaterTickFlipsFailedToPassed(t *testing.T) {
	c := fakeReader(t)
	p := newPreflight(t, c, testConfig(t))
	p.Tick(t.Context())
	if got := p.Result(); got.State != PreflightFailed {
		t.Fatalf("first tick: %+v", got)
	}
	if err := c.Create(t.Context(), gvisor()); err != nil {
		t.Fatal(err)
	}
	p.Tick(t.Context())
	if got := p.Result(); got.State != PreflightPassed {
		t.Fatalf("second tick: %+v", got)
	}
}

func TestPreflightATransientErrorKeepsTheLastResult(t *testing.T) {
	broken := false
	c := fake.NewClientBuilder().WithObjects(gvisor()).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, w client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			if broken {
				return errors.New("connection refused")
			}
			return w.Get(ctx, k, o, opts...)
		}}).Build()
	p := newPreflight(t, c, testConfig(t))
	broken = true
	p.Tick(t.Context())
	if got := p.Result(); got.State != PreflightUnknown {
		t.Fatalf("an API error before any result keeps Unknown: %+v", got)
	}
	broken = false
	p.Tick(t.Context())
	broken = true
	p.Tick(t.Context())
	if got := p.Result(); got.State != PreflightPassed {
		t.Fatalf("an API error keeps the last result: %+v", got)
	}
}

func TestPreflightNeedsLeaderElection(t *testing.T) {
	if !(&Preflight{}).NeedLeaderElection() {
		t.Fatal("the preflight runs on the leader only")
	}
}
