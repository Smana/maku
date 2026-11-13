// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/version"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/internal/runctl"
)

// Versioner is the API server's version endpoint (discovery).
type Versioner interface {
	ServerVersion() (*version.Info, error)
}

// CRD is one CRD the install needs, at a version it must serve.
type CRD struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// SecretRef is one Secret of step 5's registry the values enable, with the keys it must hold.
type SecretRef struct {
	Seam      string   `json:"seam"`
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Keys      []string `json:"keys"`
}

// ClusterConfig is what the preflight pod checks against the API server: the chart renders it.
type ClusterConfig struct {
	MinKubernetes   string      `json:"minKubernetes"`
	CRDs            []CRD       `json:"crds"`
	RuntimeClass    string      `json:"runtimeClass"`
	UnsafeAllowRunc bool        `json:"unsafeAllowRunc"`
	Secrets         []SecretRef `json:"secrets"`
}

// ClusterChecks runs IN2's four cluster checks (kubernetes, crds, runtimeclass, secrets) through
// the preflight pod's own API access. A read it cannot make, an RBAC denial included, is undecided.
func ClusterChecks(ctx context.Context, c client.Reader, v Versioner, cfg ClusterConfig) []Result {
	var out []Result
	timed := func(check string, run func() []Result) {
		start := time.Now()
		rs := run()
		for i := range rs {
			rs[i].Check, rs[i].Seconds = check, time.Since(start).Seconds()
		}
		out = append(out, rs...)
	}
	timed("kubernetes", func() []Result { return kubernetesVersion(v, cfg.MinKubernetes) })
	timed("crds", func() []Result { return crds(ctx, c, cfg.CRDs) })
	timed("runtimeclass", func() []Result { return runtimeClass(ctx, c, cfg) })
	timed("secrets", func() []Result { return secrets(ctx, c, cfg.Secrets) })
	return out
}

func one(o Outcome, code, msg string) []Result {
	return []Result{{Outcome: o, Code: code, Message: msg}}
}

func kubernetesVersion(v Versioner, floor string) []Result {
	info, err := v.ServerVersion()
	if err != nil {
		return one(Fail, Undecided, "the API server's version is unreadable")
	}
	major, minor := num(info.Major), num(info.Minor)
	fm, fn, _ := strings.Cut(floor, ".")
	got := fmt.Sprintf("%d.%d", major, minor)
	if major < num(fm) || (major == num(fm) && minor < num(fn)) {
		return one(Fail, "kubernetes_too_old", fmt.Sprintf("Kubernetes %s is below the floor %s (D26)", got, floor))
	}
	return one(Pass, "", "Kubernetes "+got)
}

// num reads a version part: GKE and EKS write minors as "30+".
func num(s string) int {
	n, _ := strconv.Atoi(strings.TrimRight(s, "+"))
	return n
}

func crds(ctx context.Context, c client.Reader, want []CRD) []Result {
	var out []Result
	for _, w := range want {
		var got apiextv1.CustomResourceDefinition
		err := c.Get(ctx, client.ObjectKey{Name: w.Name}, &got)
		switch {
		case apierrors.IsNotFound(err):
			out = append(out, Result{Outcome: Fail, Code: "crd_missing", Message: fmt.Sprintf("CRD %s is not installed: install the agent-factory-crds chart first", w.Name)})
			continue
		case err != nil:
			out = append(out, Result{Outcome: Fail, Code: Undecided, Message: fmt.Sprintf("CRD %s is unreadable (%s)", w.Name, reason(err))})
			continue
		}
		served := slices.ContainsFunc(got.Spec.Versions, func(v apiextv1.CustomResourceDefinitionVersion) bool { return v.Name == w.Version && v.Served })
		if !served {
			out = append(out, Result{Outcome: Fail, Code: "crd_version", Message: fmt.Sprintf("CRD %s does not serve %s: upgrade the CRD chart", w.Name, w.Version)})
		}
	}
	if len(out) == 0 {
		return one(Pass, "", fmt.Sprintf("%d CRDs served", len(want)))
	}
	return out
}

func runtimeClass(ctx context.Context, c client.Reader, cfg ClusterConfig) []Result {
	if cfg.UnsafeAllowRunc || cfg.RuntimeClass == "" {
		return one(Warn, "unsafe_allow_runc", "sandbox.unsafeAllowRunc: runs use the node's own runtime, with no sandbox (D18)")
	}
	refusal, soon, err := runctl.RuntimeClassRefusal(ctx, c, cfg.RuntimeClass)
	switch {
	case err != nil:
		return one(Fail, Undecided, fmt.Sprintf("RuntimeClass %s is unreadable (%s)", cfg.RuntimeClass, reason(err)))
	case soon:
		return one(Fail, Undecided, fmt.Sprintf("the preflight may not read RuntimeClass %s: its ClusterRole needs get on it", cfg.RuntimeClass))
	case refusal != "":
		return one(Fail, "runtimeclass_missing", fmt.Sprintf("RuntimeClass %s is not installed: install the sandbox runtime on your nodes, or set sandbox.runtimeClassName", cfg.RuntimeClass))
	}
	return one(Pass, "", "RuntimeClass "+cfg.RuntimeClass)
}

func secrets(ctx context.Context, c client.Reader, refs []SecretRef) []Result {
	var out []Result
	for _, ref := range refs {
		var s corev1.Secret
		err := c.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, &s)
		switch {
		case apierrors.IsNotFound(err):
			out = append(out, Result{Outcome: Fail, Code: "secret_missing",
				Message: fmt.Sprintf("%s: Secret %s/%s is missing (docs/install-secrets.md)", ref.Seam, ref.Namespace, ref.Name)})
			continue
		case err != nil:
			out = append(out, Result{Outcome: Fail, Code: Undecided,
				Message: fmt.Sprintf("%s: Secret %s/%s is unreadable (%s)", ref.Seam, ref.Namespace, ref.Name, reason(err))})
			continue
		}
		for _, k := range ref.Keys {
			if len(strings.TrimSpace(string(s.Data[k]))) == 0 {
				out = append(out, Result{Outcome: Fail, Code: "secret_key_missing",
					Message: fmt.Sprintf("%s: Secret %s/%s has no key %s, or it is empty (docs/install-secrets.md)", ref.Seam, ref.Namespace, ref.Name, k)})
			}
		}
	}
	if len(out) == 0 {
		return one(Pass, "", fmt.Sprintf("%d Secrets with their keys", len(refs)))
	}
	return out
}

// reason is an API error's class, never its text: a status message may quote the object.
func reason(err error) string {
	if r := apierrors.ReasonForError(err); r != "" {
		return string(r)
	}
	return "unreachable"
}
