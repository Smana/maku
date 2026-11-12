// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	return fake.NewClientBuilder().WithScheme(s).WithObjects(append(gitSecrets(), objs...)...).Build()
}

// gitSecrets are testConfig's two git Secrets, filled: the preflight's gitSecrets check passes.
func gitSecrets() []client.Object {
	var out []client.Object
	for _, n := range []string{"agent-git", "agent-git-read"} {
		out = append(out, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: "agents"}, Data: map[string][]byte{"token": []byte("t")}})
	}
	return out
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
	c := fake.NewClientBuilder().WithObjects(gitSecrets()...).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, w client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			if _, ok := o.(*nodev1.RuntimeClass); !ok {
				return w.Get(ctx, k, o, opts...)
			}
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
	c := fake.NewClientBuilder().WithObjects(append(gitSecrets(), gvisor())...).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, w client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			if broken {
				return errors.New("connection refused")
			}
			return w.Get(ctx, k, o, opts...)
		}}).Build()
	p := newPreflight(t, c, testConfig(t))
	broken = true
	if !p.Tick(t.Context()) {
		t.Fatal("an API error is undecided: Tick must say so, for the fast retry")
	}
	if got := p.Result(); got.State != PreflightUnknown || got.Check != "runtimeClass" || !strings.Contains(got.Message, "connection refused") {
		t.Fatalf("an API error before any result keeps Unknown, and says why: %+v", got)
	}
	broken = false
	if p.Tick(t.Context()) {
		t.Fatal("a decided tick is not undecided")
	}
	broken = true
	p.Tick(t.Context())
	if got := p.Result(); got.State != PreflightPassed {
		t.Fatalf("an API error keeps the last result: %+v", got)
	}
}

func TestPreflightForbiddenIsAFailureNamingTheGrant(t *testing.T) {
	c := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return apierrors.NewForbidden(schema.GroupResource{Group: "node.k8s.io", Resource: "runtimeclasses"}, "gvisor", errors.New("RBAC: access denied"))
		}}).Build()
	p := newPreflight(t, c, testConfig(t))
	// Decided (the run is refused, with the grant named), yet checked again soon: right after install
	// the ClusterRole binding may not have reached the authorizer yet.
	if !p.Tick(t.Context()) {
		t.Fatal("a Forbidden failure must be retried on the short backoff (RBAC propagation lag)")
	}
	got := p.Result()
	if got.State != PreflightFailed || got.Check != "runtimeClass" || !strings.Contains(got.Message, "get on node.k8s.io runtimeclasses") ||
		!strings.Contains(got.Message, `"gvisor"`) {
		t.Fatalf("Forbidden: %+v", got)
	}
}

func TestPreflightAMissingClassWaitsTheFullInterval(t *testing.T) {
	p := newPreflight(t, fakeReader(t), testConfig(t))
	if p.Tick(t.Context()) {
		t.Fatal("a missing RuntimeClass is decided and needs a human: no short retry")
	}
	if got := p.Result(); got.State != PreflightFailed {
		t.Fatalf("missing class: %+v", got)
	}
}

func TestPreflightRBACLagRecoversOnTheNextShortRetry(t *testing.T) {
	granted := false
	c := fake.NewClientBuilder().WithObjects(append(gitSecrets(), gvisor())...).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, w client.WithWatch, k client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			if !granted {
				return apierrors.NewForbidden(schema.GroupResource{Group: "node.k8s.io", Resource: "runtimeclasses"}, k.Name, errors.New("RBAC: access denied"))
			}
			return w.Get(ctx, k, o, opts...)
		}}).Build()
	p := newPreflight(t, c, testConfig(t))
	if !p.Tick(t.Context()) || p.Result().State != PreflightFailed {
		t.Fatalf("before the grant: %+v", p.Result())
	}
	granted = true
	if p.Tick(t.Context()) || p.Result().State != PreflightPassed {
		t.Fatalf("once the grant lands, the next tick passes and the interval is the long one again: %+v", p.Result())
	}
}

func TestPreflightRetriesUndecidedChecksWithBackoff(t *testing.T) {
	every := 10 * time.Minute
	for _, c := range []struct {
		n    int
		want time.Duration
	}{{1, 10 * time.Second}, {2, 20 * time.Second}, {3, 40 * time.Second}, {6, 320 * time.Second}, {7, every}, {40, every}} {
		if got := undecidedRetry(c.n, every); got != c.want {
			t.Errorf("retry %d: %s, want %s", c.n, got, c.want)
		}
	}
}

func TestPreflightNeedsLeaderElection(t *testing.T) {
	if !(&Preflight{}).NeedLeaderElection() {
		t.Fatal("the preflight runs on the leader only")
	}
}

// PR B: a missing git Secret fails visibly, before any run waits on it: the preflight names the
// Secret, and checks again soon (the factory's run token writer, or an ExternalSecret, may fill it
// right after install).
func TestPreflightGitSecrets(t *testing.T) {
	noRead := fakeReader(t, gvisor())
	if err := noRead.Delete(t.Context(), gitSecrets()[1]); err != nil {
		t.Fatal(err)
	}
	empty := fakeReader(t, gvisor())
	if err := empty.Update(t.Context(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "agent-git", Namespace: "agents"}, Data: map[string][]byte{"other": []byte("x")}}); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		c    client.Reader
		want string
	}{
		"no read Secret": {noRead, `Secret agents/agent-git-read not found: reviewer and triager runs mount it (runController.gitReadSecret); create it with a token key`},
		"no token key":   {empty, `Secret agents/agent-git has no token key: implementer and tester runs mount it (runController.gitSecret)`},
	} {
		p := newPreflight(t, tc.c, testConfig(t))
		if !p.Tick(t.Context()) {
			t.Errorf("%s: a missing Secret may be filled soon: want the short retry", name)
		}
		if got := p.Result(); got.State != PreflightFailed || got.Check != "gitSecrets" || got.Message != tc.want {
			t.Errorf("%s: %+v, want %q", name, got, tc.want)
		}
	}
	// unsafeSharedGitToken: one Secret, and no read Secret is looked for.
	cfg := testConfig(t)
	cfg.GitReadSecret, cfg.UnsafeSharedGitToken = "", true
	p := newPreflight(t, noRead, cfg)
	if p.Tick(t.Context()); p.Result().State != PreflightPassed {
		t.Fatalf("shared token: %+v", p.Result())
	}
}
