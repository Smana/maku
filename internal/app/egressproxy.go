// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	toolscache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	"github.com/Smana/maku/internal/egressproxy"
	"github.com/Smana/maku/internal/metrics"
	"github.com/Smana/maku/internal/runctl"
)

// EgressProxy runs the egress proxy (v4 D6) until ctx ends: :3128 the proxy, :9090 its metrics,
// :9091 /healthz and /readyz (once the pod watch has synced). It reads the run-controller's own config
// file for the runs namespace and the denied ranges, so the two cannot disagree. It needs no leader:
// every replica serves.
func EgressProxy(ctx context.Context, log *slog.Logger, getenv func(string) string) error {
	path := getenv("RUN_CONTROLLER_CONFIG")
	if path == "" {
		path = defaultRunControllerConfig
	}
	cfg, err := runctl.LoadConfig(path)
	if err != nil {
		return err
	}
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return err
	}
	// Only the run-controller's pods in the runs namespace: the label is the selector the run-pod
	// policy guarantees, since only the run-controller creates pods there.
	sel := labels.SelectorFromSet(labels.Set{runctl.LabelManagedBy: "run-controller"})
	c, err := cache.New(ctrl.GetConfigOrDie(), cache.Options{Scheme: s,
		DefaultNamespaces: map[string]cache.Config{cfg.Namespace: {}}, DefaultLabelSelector: sel})
	if err != nil {
		return fmt.Errorf("egress-proxy: cache: %w", err)
	}
	x := egressproxy.NewIndex()
	inf, err := c.GetInformer(ctx, &corev1.Pod{})
	if err != nil {
		return fmt.Errorf("egress-proxy: pod informer: %w", err)
	}
	if _, err := inf.AddEventHandler(podIndexHandler(x)); err != nil {
		return err
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	count, bytes, err := metrics.NewEgressProxyMetrics(reg)
	if err != nil {
		return err
	}
	d := &egressproxy.Dialer{Resolver: net.DefaultResolver, Deny: egressproxy.MustCIDRs(cfg.EgressProxy.DenyCIDRs...),
		Dial: (&net.Dialer{}).DialContext}
	maxAll, maxClient, err := tunnelCaps(getenv)
	if err != nil {
		return err
	}
	p := &egressproxy.Proxy{Index: x, Dial: d.DialContext, Log: log, Count: count, Bytes: bytes,
		MaxTunnels: maxAll, MaxTunnelsPerClient: maxClient}
	var synced atomic.Bool
	probes := http.NewServeMux()
	probes.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	probes.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !synced.Load() {
			http.Error(w, "pod watch not synced", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	errLog := slog.NewLogLogger(log.Handler(), slog.LevelWarn)
	// The proxy's server: a tunnel is long-lived, so only the request headers and an idle keep-alive
	// connection are timed; the proxy clears a hijacked tunnel's deadlines, and its own idle timeout
	// ends it.
	servers := map[string]*http.Server{
		":3128": {Handler: p, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8 << 10, ErrorLog: errLog},
		":9090": opsServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}), log),
		":9091": opsServer(probes, log),
	}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return c.Start(gctx) })
	g.Go(func() error {
		if !c.WaitForCacheSync(gctx) {
			return gctx.Err()
		}
		synced.Store(true)
		log.Info("egress-proxy ready", "namespace", cfg.Namespace)
		return nil
	})
	for addr, srv := range servers {
		srv.Addr = addr
		g.Go(func() error {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("egress-proxy %s: %w", addr, err)
			}
			return nil
		})
	}
	g.Go(func() error {
		<-gctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(gctx), 10*time.Second)
		defer cancel()
		for _, srv := range servers {
			_ = srv.Shutdown(sctx)
		}
		return nil
	})
	log.Info("egress-proxy starting", "namespace", cfg.Namespace)
	return g.Wait()
}

// The tunnel caps' defaults, when the chart sets none (egressProxy.maxTunnels, maxTunnelsPerClient).
const (
	defaultMaxTunnels          = 1024
	defaultMaxTunnelsPerClient = 64
)

// tunnelCaps reads MAX_TUNNELS and MAX_TUNNELS_PER_CLIENT: positive integers, defaulted when unset.
func tunnelCaps(getenv func(string) string) (all, perClient int, err error) {
	read := func(key string, def int) (int, error) {
		v := getenv(key)
		if v == "" {
			return def, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("egress-proxy: %s %q is not a positive integer", key, v)
		}
		return n, nil
	}
	if all, err = read("MAX_TUNNELS", defaultMaxTunnels); err != nil {
		return 0, 0, err
	}
	if perClient, err = read("MAX_TUNNELS_PER_CLIENT", defaultMaxTunnelsPerClient); err != nil {
		return 0, 0, err
	}
	return all, perClient, nil
}

// podIndexHandler feeds the index from the pod informer; a delete may arrive as a tombstone.
func podIndexHandler(x *egressproxy.Index) toolscache.ResourceEventHandlerFuncs {
	upsert := func(o any) {
		if p, ok := o.(*corev1.Pod); ok {
			x.Upsert(p)
		}
	}
	return toolscache.ResourceEventHandlerFuncs{
		AddFunc:    upsert,
		UpdateFunc: func(_, n any) { upsert(n) },
		DeleteFunc: func(o any) {
			if t, ok := o.(toolscache.DeletedFinalStateUnknown); ok {
				o = t.Obj
			}
			if p, ok := o.(*corev1.Pod); ok {
				x.Delete(p.Namespace, p.Name)
			}
		},
	}
}
