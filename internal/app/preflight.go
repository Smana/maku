// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/preflight"
)

// preflightHTTPTimeout bounds one component's /preflight answer, its checks included.
const preflightHTTPTimeout = 40 * time.Second

// Preflight is `maku-run-controller preflight` (v4 D24): the install's preflight pod. It runs the
// cluster checks through its own ServiceAccount, collects the components' /preflight, prints the
// JSON lines to stdout and the summary to the termination log, and exits 1 on a fail (and, with
// --strict, on a warn). The chart runs it as a Helm test hook and as a suspended CronJob.
func Preflight(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "/etc/maku-preflight/config.json", "the chart's preflight config")
	strict := fs.Bool("strict", false, "fail on a warning too")
	term := fs.String("termination-log", "/dev/termination-log", "where the one-line summary goes")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := preflight.LoadRunConfig(*cfgPath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "preflight:", err)
		return 2
	}
	rest, err := ctrl.GetConfig()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "preflight: the in-cluster config:", err)
		return 2
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return 2
	}
	if err := apiextv1.AddToScheme(scheme); err != nil {
		return 2
	}
	c, err := client.New(rest, client.Options{Scheme: scheme})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "preflight: the API client:", err)
		return 2
	}
	disc, err := discovery.NewDiscoveryClientForConfig(rest)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "preflight: the discovery client:", err)
		return 2
	}
	return preflight.Run(ctx, preflight.Deps{Client: c, Version: disc, HTTP: httpx.New(preflightHTTPTimeout, nil)}, cfg, stdout, *term, *strict)
}
