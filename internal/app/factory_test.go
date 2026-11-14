// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/api"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/configsource"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/killswitch"
	"github.com/Smana/maku/internal/factory/meter"
	"github.com/Smana/maku/internal/factory/tracing"
	"github.com/Smana/maku/internal/runwatch"
)

func TestGitHubAPIURL(t *testing.T) {
	t.Setenv("ROOMS_GITHUB_API_URL", "")
	if got := githubAPIURL(); got != "https://api.github.com/" {
		t.Fatalf("default API root = %q", got)
	}
	t.Setenv("ROOMS_GITHUB_API_URL", "https://e2e-stub.e2e.svc.cluster.local/")
	if got := githubAPIURL(); got != "https://e2e-stub.e2e.svc.cluster.local/" {
		t.Fatalf("API root = %q", got)
	}
	t.Setenv("ROOMS_GITHUB_API_URL", "http://e2e-stub.e2e.svc.cluster.local/")
	_, err := factoryApp(t.TempDir()).UserLogin(t.Context(), "Smana", "demo", 583231)
	if err == nil || !strings.Contains(err.Error(), "the API URL must be https://<host>/") {
		t.Fatalf("invalid API root: %v, want the existing client validation", err)
	}
}

func TestRunFactoryRefuses(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	for _, c := range []struct {
		name   string
		getenv func(string) string
		want   string
	}{
		{"without its environment names every missing variable", env(), "FACTORY_CONFIG is not set\nPOD_NAMESPACE is not set"},
		// A bad config fails the rollout (§4): the pod never starts.
		{"with a config it cannot read", env("FACTORY_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"), "POD_NAMESPACE", "agent-system"),
			"no such file or directory"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := RunFactory(ctx, slog.New(slog.DiscardHandler), c.getenv)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), "agent-factory: ") {
				t.Fatalf("RunFactory = %v, want an error naming %q", err, c.want)
			}
		})
	}
}

func TestFactoryManagerOptions(t *testing.T) {
	native := runwatch.Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "run-"}
	for _, tc := range []struct {
		ns string
		rk runwatch.Kind // the runs kind: the loop below has its own kind
	}{{"agent-system", runwatch.DefaultKind}, {"factory-test", native}} {
		ns, rk := tc.ns, tc.rk
		t.Run(ns, func(t *testing.T) {
			o := factoryManagerOptions(ns, rk, configsource.Kinds())
			if !o.LeaderElection || o.LeaderElectionID != "agent-factory.agents.ogenki.io" || o.LeaderElectionNamespace != ns ||
				!o.LeaderElectionReleaseOnCancel {
				t.Fatalf("leader election: %+v", o)
			}
			if o.GracefulShutdownTimeout == nil || *o.GracefulShutdownTimeout != managerDrain {
				t.Fatalf("the manager's drain must fit the pod's grace: %v", o.GracefulShutdownTimeout)
			}
			if o.Metrics.BindAddress != "0" || o.HealthProbeBindAddress != "0" {
				t.Fatal(":9090 serves metrics and probes; the manager's own listeners stay off")
			}
			if o.Client.Cache == nil || o.Client.Cache.Unstructured {
				t.Fatal("unstructured reads (a stop's run list) must reach the API server")
			}
			// The chart's Roles grant exactly these namespaces: a cluster-wide informer is refused.
			want := map[string][]string{"Task": {ns}, "Room": {ns}, "ConfigMap": {ns}, rk.GVK().Kind: {rk.Namespace},
				"Factory": {ns}, "Repository": {ns}, "MergePolicy": {ns}}
			if len(o.Cache.ByObject) != len(want) || o.Cache.DefaultNamespaces != nil {
				t.Fatalf("cached kinds: %v, default namespaces %v", o.Cache.ByObject, o.Cache.DefaultNamespaces)
			}
			for obj, by := range o.Cache.ByObject {
				var kind string
				switch x := obj.(type) {
				case *factoryv1.Task:
					kind = "Task"
				case *v1alpha1.Room:
					kind = "Room"
				case *corev1.ConfigMap:
					kind = "ConfigMap"
					// Only the stop object: the factory never lists the namespace's ConfigMaps.
					if by.Field == nil || by.Field.String() != "metadata.name="+killswitch.ConfigMap {
						t.Fatalf("ConfigMaps cached by %v", by.Field)
					}
				case *factoryv1.Factory:
					kind = "Factory"
				case *factoryv1.Repository:
					kind = "Repository"
				case *factoryv1.MergePolicy:
					kind = "MergePolicy"
				case *unstructured.Unstructured:
					kind = x.GroupVersionKind().Kind
					if x.GroupVersionKind() != rk.GVK() {
						t.Fatalf("watches %v, want the AgentRun GVK", x.GroupVersionKind())
					}
				default:
					t.Fatalf("caches %T", obj)
				}
				var got []string
				for n := range by.Namespaces {
					got = append(got, n)
				}
				if !slices.Equal(got, want[kind]) {
					t.Fatalf("%s cached in %v, want %v", kind, got, want[kind])
				}
			}
		})
	}
}

// Every replica pings GitHub at once and then every period, so the token stays warm on a replica
// that is not leading and makes no other call.
func TestPingerPingsAtOnceThenEveryPeriod(t *testing.T) {
	ticks := make(chan time.Time)
	var calls atomic.Int32
	pinged := make(chan struct{}, 8)
	var period time.Duration
	p := &pinger{ping: func(context.Context) error {
		calls.Add(1)
		pinged <- struct{}{}
		return errors.New("a failed ping is only a stale token: the pinger goes on")
	}, every: time.Minute, ticker: func(d time.Duration) (<-chan time.Time, func()) { period = d; return ticks, func() {} }}
	if p.NeedLeaderElection() {
		t.Fatal("every replica pings")
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx) }()
	<-pinged
	ticks <- time.Time{}
	<-pinged
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || period != time.Minute {
		t.Fatalf("%d pings, period %s", calls.Load(), period)
	}
}

// R46: no tracing block, no sink, and a nil interface rather than a typed nil, which the
// reconciler would call. A configured endpoint gives an exporter without dialling it: an
// unreachable collector loses spans, never the factory's start.
func TestTaskSink(t *testing.T) {
	sink, tp, shutdown, err := taskSink(t.Context(), "")
	if err != nil || sink != nil || tp != nil {
		t.Fatalf("tracing off: %v %v %v", sink, tp, err)
	}
	shutdown(t.Context())
	sink, tp, shutdown, err = taskSink(t.Context(), "127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sink.(*tracing.Exporter); !ok {
		t.Fatalf("tracing on: %T", sink)
	}
	if tp == nil { // F15: the rooms client's provider, or its spans are dead
		t.Fatal("tracing on: no tracer provider for the rooms client")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	shutdown(ctx)
}

func TestDemoStartupChoices(t *testing.T) {
	demo := &config.Config{Demo: true, Broker: config.Broker{URL: "https://broker:8443"}}
	hc, err := brokerClient(demo)
	if err != nil || hc == nil {
		t.Fatalf("brokerClient without a CA = %v, %v; want system roots", hc, err)
	}
	src, err := usageSource(demo)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(meter.Off); !ok {
		t.Fatalf("usageSource = %T, want meter.Off", src)
	}
	if th, err := throttle(demo); th != nil || err != nil {
		t.Fatalf("no throttle source, no throttle reader: %v %v", th, err)
	}
	v, err := humanVerifier(t.Context(), demo)
	if v != nil || err != nil {
		t.Fatalf("humanVerifier in demo = %v, %v; want none and no error", v, err)
	}
	a := &api.Authenticator{ClientIDs: func() []string { return nil }}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer x.y.z")
	if _, err := a.Authenticate(r); err == nil || !strings.Contains(err.Error(), "demo_no_human_tokens") ||
		!errors.Is(err, authn.ErrUnauthenticated) {
		t.Fatalf("Authenticate = %v, want demo_no_human_tokens", err)
	}
}

// Review #518: runs.gatewayIdentity stamps every run spec.meteredBy gateway; without it, none.
func TestMeteredByFollowsTheGatewayIdentity(t *testing.T) {
	cfg := &config.Config{}
	if got := meteredBy(cfg); got != "" {
		t.Fatalf("no identity: %q", got)
	}
	cfg.Runs.GatewayIdentity = true
	if got := meteredBy(cfg); got != "gateway" {
		t.Fatalf("with the identity: %q, want gateway", got)
	}
}

// Outside demo mode today's path holds: an unreadable CA, a bad meter.url and an unreachable
// JWKS fail at start.
func TestNonDemoStartupStillFailsFast(t *testing.T) {
	cfg := &config.Config{Broker: config.Broker{CAFile: filepath.Join(t.TempDir(), "absent.pem")},
		API: config.API{HumanIssuer: "http://127.0.0.1:1", HumanJWKS: "http://127.0.0.1:1/keys"}}
	if _, err := brokerClient(cfg); err == nil {
		t.Fatal("a set but unreadable caFile must fail")
	}
	cfg.Meter.URL = "ftp://vm"
	if _, err := usageSource(cfg); err == nil {
		t.Fatal("a bad meter.url must fail")
	}
	cfg.Meter.Throttle.PromQuery = "q"
	if _, err := throttle(cfg); err == nil {
		t.Fatal("a bad meter.url must fail the PromQL throttle too")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := humanVerifier(ctx, cfg); err == nil {
		t.Fatal("an unreachable JWKS must fail construction")
	}
	// v4 D8: without humanJWKS the issuer's discovery document names it, fetched as eagerly.
	cfg.API.HumanIssuer, cfg.API.HumanJWKS = "https://127.0.0.1:1", ""
	if _, err := humanVerifier(ctx, cfg); err == nil {
		t.Fatal("an unreachable discovery document must fail construction")
	}
}

// v4 D8: the audience a generic token must hold is api.audience, or the file api.audienceFile
// names, read at use; the zitadel preset's is api.projectIDFile.
func TestAPIAudience(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "audience")
	writeID(t, file, "api://rooms")
	for name, c := range map[string]struct {
		api  config.API
		want string
	}{
		"zitadel: the project id file": {config.API{ProjectIDFile: file}, "api://rooms"},
		"generic: the literal":         {config.API{Preset: config.PresetGeneric, Audience: "https://api.rooms"}, "https://api.rooms"},
		"generic: the file":            {config.API{Preset: config.PresetGeneric, AudienceFile: file}, "api://rooms"},
		"generic: none":                {config.API{Preset: config.PresetGeneric}, ""},
		"zitadel: an unreadable file":  {config.API{ProjectIDFile: filepath.Join(dir, "absent")}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := apiAudience(c.api)(); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// Demo mode's run tokens are for the served repositories of one account, written in the runs'
// namespace as the two Secrets the run-controller mounts; two accounts cannot share one installation.
func TestRunTokensServeTheListedRepositories(t *testing.T) {
	cfg := &config.Config{Demo: true, Runs: config.Runs{Kind: runwatch.Kind{Namespace: "runs"}},
		Repositories: []config.Repository{{Name: "Smana/a"}, {Name: "Smana/b"}}}
	ws, err := runTokens(cfg, served(cfg), nil, slog.New(slog.DiscardHandler))
	if err != nil || len(ws) != 2 {
		t.Fatalf("%+v %v", ws, err)
	}
	for i, name := range []string{"agent-git", "agent-git-read"} {
		if w := ws[i]; w.Name != name || w.Namespace != "runs" || !w.NeedLeaderElection() || w.Every != 30*time.Minute {
			t.Errorf("writer %d: %+v", i, w)
		}
	}
	cfg.Repositories = append(cfg.Repositories, config.Repository{Name: "Other/c"})
	if _, err := runTokens(cfg, served(cfg), nil, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "different owners") {
		t.Fatalf("err = %v", err)
	}
	// Neither demo nor runTokens: the factory writes no Secret (octo-sts, or the operator, fills them).
	cfg.Demo = false
	if ws, err := runTokens(cfg, served(cfg), nil, slog.New(slog.DiscardHandler)); err != nil || ws != nil {
		t.Fatalf("off: %+v %v", ws, err)
	}
	cfg.RunTokens = config.RunTokens{Enabled: true, AppIDFile: "/x/app_id", PrivateKeyFile: "/x/private_key"}
	cfg.Repositories = cfg.Repositories[:2]
	if ws, err := runTokens(cfg, served(cfg), nil, slog.New(slog.DiscardHandler)); err != nil || len(ws) != 2 {
		t.Fatalf("runTokens.enabled: %+v %v", ws, err)
	}
}

// v4 D10: runTokens.perRun builds the per-run minter, on the leader, for repositories of any
// owner; without it the shared writer stays (D25).
func TestPerRunTokens(t *testing.T) {
	cfg := &config.Config{Runs: config.Runs{Kind: runwatch.Kind{Group: "maku.ogenki.io", Namespace: "runs", NamePrefix: "maku-run-"}},
		Repositories: []config.Repository{{Name: "Smana/a"}, {Name: "Other/c"}},
		RunTokens:    config.RunTokens{Enabled: true, AppIDFile: "/x/app_id", PrivateKeyFile: "/x/private_key"}}
	if rt := perRunTokens(cfg, func() *config.Config { return cfg }, nil, nil, nil, "agent-factory", nil, slog.New(slog.DiscardHandler)); rt != nil {
		t.Fatal("perRun off: no minter")
	}
	cfg.RunTokens.PerRun = true
	rt := perRunTokens(cfg, func() *config.Config { return cfg }, nil, nil, nil, "agent-factory", nil, slog.New(slog.DiscardHandler))
	if rt == nil || !rt.NeedLeaderElection() || rt.Kind != cfg.Runs.Kind || rt.TaskNamespace != "agent-factory" || rt.Reserved == nil || rt.Enforced == nil {
		t.Fatalf("%+v", rt)
	}
	cfg.RunTokens.Enabled = false
	if rt := perRunTokens(cfg, func() *config.Config { return cfg }, nil, nil, nil, "agent-factory", nil, slog.New(slog.DiscardHandler)); rt != nil {
		t.Fatal("perRun without runTokens.enabled (outside the demo): no App, no minter")
	}
}

func served(cfg *config.Config) func() []string {
	return func() []string {
		var out []string
		for _, r := range cfg.Repos() {
			out = append(out, r.Name)
		}
		return out
	}
}

// Demo mode serves public repositories only: one private repository stops the start, naming it;
// a failed read stops it too (fail closed).
func TestDemoRequiresPublicRepositories(t *testing.T) {
	answers := map[string]bool{"o/open": true, "o/closed": false}
	pub := func(_ context.Context, name string) (bool, error) {
		if name == "o/broken" {
			return false, errors.New("boom")
		}
		return answers[name], nil
	}
	if err := requirePublic(t.Context(), []string{"o/open"}, pub); err != nil {
		t.Fatal(err)
	}
	err := requirePublic(t.Context(), []string{"o/open", "o/closed"}, pub)
	if err == nil || err.Error() != "demo: o/closed is private; the demo serves public repositories only" {
		t.Fatalf("private: %v", err)
	}
	if err := requirePublic(t.Context(), []string{"o/broken"}, pub); err == nil || !strings.Contains(err.Error(), "o/broken") {
		t.Fatalf("broken: %v", err)
	}
}

// The demo's one App: with the merger files naming the factory App's own, connectMerger takes the
// connect path (no Absent merger) and reads the same key. The context is cancelled so no request
// leaves; the error is the cancelled mint, past the key and id reads.
func TestConnectMergerOverTheFactoryAppsFiles(t *testing.T) {
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idFile, keyFile := filepath.Join(dir, "app_id"), filepath.Join(dir, "private_key")
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(idFile, []byte("12345\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pemKey, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Demo: true,
		GitHub:       config.GitHub{AppIDFile: idFile, PrivateKeyFile: keyFile, MergerAppIDFile: idFile, MergerKeyFile: keyFile},
		Repositories: []config.Repository{{Classes: map[string]config.Class{"docs": {Shadow: true}}}}}
	if !cfg.NeedsMerger() {
		t.Fatal("a shadow class must need the merger")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m, err := connectMerger(ctx, slog.New(slog.DiscardHandler), cfg, "Smana/demo")
	if _, absent := m.(forge.Absent); absent {
		t.Fatal("connectMerger returned Absent over the factory App's files")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancelled mint (the key and id were read and signed)", err)
	}
}
