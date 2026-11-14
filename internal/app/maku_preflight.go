// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"flag"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/internal/maku"
)

// preflight runs `maku preflight` (v4 D24, IN5): the release's install preflight from its CronJob,
// with the outside checks from this machine.
func (r Maku) preflight(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	var o maku.PreflightOptions
	fs.StringVar(&o.Namespace, "namespace", "", "the release's namespace (default: the context's)")
	fs.StringVar(&o.Release, "release", "", "the Helm release (default: the namespace's only preflight)")
	kubeconfig := fs.String("kubeconfig", "", "the kubeconfig (default: KUBECONFIG, then ~/.kube/config)")
	kubecontext := fs.String("context", "", "the kubeconfig context (default: its current one)")
	fs.BoolVar(&o.Strict, "strict", false, "fail on a warning too")
	fs.BoolVar(&o.JSON, "json", false, "print the JSON lines instead of the table")
	fs.BoolVar(&o.Keep, "keep", false, "keep the Job and its pod")
	fs.BoolVar(&o.NoOutside, "no-outside", false, "skip the outside checks")
	fs.DurationVar(&o.Timeout, "timeout", 0, "how long to wait for the Job (default: its deadline plus a minute)")
	pos, err := flags(fs, args)
	if err != nil {
		return &maku.ExitError{Code: 2, Err: err}
	}
	if len(pos) > 0 {
		return &maku.ExitError{Code: 2, Err: errors.New("preflight: nothing is positional")}
	}
	kube := r.Kube
	if kube == nil {
		kube = kubeCluster
	}
	c, ns, err := kube(*kubeconfig, *kubecontext)
	if err != nil {
		return &maku.ExitError{Code: 2, Err: fmt.Errorf("preflight: the cluster: %w", err)}
	}
	if o.Namespace == "" {
		o.Namespace = ns
	}
	return maku.RunPreflight(ctx, c, o, r.Out)
}

// kubeCluster is client-go's kubeconfig loading, as kubectl's: --kubeconfig, else KUBECONFIG, else
// ~/.kube/config; the context's namespace, else default.
func kubeCluster(kubeconfig, kubecontext string) (maku.PreflightCluster, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = kubeconfig
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: kubecontext})
	rest, err := cc.ClientConfig()
	if err != nil {
		return maku.PreflightCluster{}, "", err
	}
	ns, _, err := cc.Namespace()
	if err != nil {
		return maku.PreflightCluster{}, "", err
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return maku.PreflightCluster{}, "", err
	}
	c, err := client.New(rest, client.Options{Scheme: scheme})
	if err != nil {
		return maku.PreflightCluster{}, "", err
	}
	cs, err := kubernetes.NewForConfig(rest)
	if err != nil {
		return maku.PreflightCluster{}, "", err
	}
	return maku.PreflightCluster{Client: c, Logs: func(ctx context.Context, namespace, pod string) ([]byte, error) {
		return cs.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{}).DoRaw(ctx)
	}}, ns, nil
}
