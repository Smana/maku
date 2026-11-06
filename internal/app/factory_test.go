// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	"github.com/Smana/maku/internal/factory/killswitch"
	"github.com/Smana/maku/internal/factory/meter"
	"github.com/Smana/maku/internal/factory/tracing"
	"github.com/Smana/maku/internal/runwatch"
)

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
			o := factoryManagerOptions(ns, rk)
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
			want := map[string][]string{"Task": {ns}, "Room": {ns}, "ConfigMap": {ns}, rk.GVK().Kind: {rk.Namespace}}
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
	if throttle(demo) != nil {
		t.Fatal("no logsUrl, no throttle reader")
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
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := humanVerifier(ctx, cfg); err == nil {
		t.Fatal("an unreachable JWKS must fail construction")
	}
}

// Demo mode's git token is for the served repositories of one account, written in the runs'
// namespace; two accounts cannot share one installation.
func TestDemoGitTokenServesTheListedRepositories(t *testing.T) {
	cfg := &config.Config{Demo: true, Runs: runwatch.Kind{Namespace: "runs"},
		Repositories: []config.Repository{{Name: "Smana/a"}, {Name: "Smana/b"}}}
	g, err := demoGitToken(cfg, nil, slog.New(slog.DiscardHandler))
	if err != nil || g.Namespace != "runs" || g.NeedLeaderElection() != true {
		t.Fatalf("%+v %v", g, err)
	}
	cfg.Repositories = append(cfg.Repositories, config.Repository{Name: "Other/c"})
	if _, err := demoGitToken(cfg, nil, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "different owners") {
		t.Fatalf("err = %v", err)
	}
}
