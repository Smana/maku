// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/metrics"
	"github.com/Smana/maku/internal/preflight"
	"github.com/Smana/maku/internal/runctl"
	"github.com/Smana/maku/internal/version"
)

const defaultRunControllerConfig = "/etc/run-controller/config.yaml"

// RunController runs the run-controller (adoption 3a) until ctx ends. Its cache holds runs and
// pods in the runs' namespace only; Secrets are read uncached, so it never lists them.
func RunController(ctx context.Context, logger *slog.Logger, getenv func(string) string) error {
	path := getenv("RUN_CONTROLLER_CONFIG")
	if path == "" {
		path = defaultRunControllerConfig
	}
	cfg, err := runctl.LoadConfig(path)
	if err != nil {
		return err
	}
	lr := logr.FromSlogHandler(logger.Handler()) // as the broker and the factory do
	ctrl.SetLogger(lr)
	klog.SetLogger(lr)
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		return err
	}
	ns := map[string]cache.Config{cfg.Namespace: {}}
	drain := 30 * time.Second
	// GET /preflight (v4 D24) on the metrics port: the preflight's last result, once it exists.
	var pfRef atomic.Pointer[runctl.Preflight]
	pfHandler := preflight.Handler("run-controller", version.Version, runControllerSource(pfRef.Load, cfg.NetworkPolicy, logger), nil)
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: s, LeaderElection: true, LeaderElectionID: "run-controller", LeaderElectionNamespace: cfg.Namespace,
		LeaderElectionReleaseOnCancel: true, GracefulShutdownTimeout: &drain,
		Metrics: metricsserver.Options{BindAddress: ":9090", ExtraHandlers: runControllerExtraHandlers(preflightOn(getenv), pfHandler)}, HealthProbeBindAddress: ":9091",
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
			&v1alpha1.AgentRun{}: {Namespaces: ns}, &corev1.Pod{}: {Namespaces: ns},
		}},
	})
	if err != nil {
		return fmt.Errorf("run-controller: manager: %w", err)
	}
	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		return err
	}
	// The preflight (v4 D18, D6): new runs wait for its first result and are refused while it fails. It
	// reads the RuntimeClass uncached, so the controller needs get on that one name, never a watch; in
	// kubernetes mode it also runs a probe pod (Kube writes it).
	passed, err := metrics.NewPreflightGauge(crmetrics.Registry)
	if err != nil {
		return err
	}
	pf := &runctl.Preflight{Client: mgr.GetAPIReader(), Kube: mgr.GetClient(), Cfg: cfg, Every: runctl.DefaultPreflightEvery, Log: logger, Passed: passed,
		Self: runctl.PodRef{Name: getenv("POD_NAME"), UID: getenv("POD_UID")}}
	if err := mgr.Add(pf); err != nil {
		return err
	}
	pfRef.Store(pf)
	r := &runctl.Reconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Cfg: cfg, Now: time.Now, Log: logger, Preflight: pf}
	if err := r.SetupWithManager(mgr); err != nil {
		return err
	}
	logger.Info("run-controller starting", "namespace", cfg.Namespace, "prefix", cfg.NamePrefix, "runtimeClass", cfg.RuntimeClassName)
	return mgr.Start(ctx)
}
