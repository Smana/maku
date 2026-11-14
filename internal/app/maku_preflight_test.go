// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/maku"
)

// maku preflight reads the kubeconfig and context it is given, takes the context's namespace unless
// --namespace names one, and exits 2 on a cluster it cannot reach or a positional argument.
func TestMakuPreflightFlags(t *testing.T) {
	var gotConfig, gotContext string
	var out bytes.Buffer
	r := Maku{Dir: t.TempDir(), Out: &out, Err: &out, Kube: func(kubeconfig, kubecontext string) (maku.PreflightCluster, string, error) {
		gotConfig, gotContext = kubeconfig, kubecontext
		return maku.PreflightCluster{}, "", errors.New("unreachable")
	}}
	err := r.preflight(t.Context(), []string{"--kubeconfig", "/k", "--context", "k3d-x", "--strict"})
	var e *maku.ExitError
	if !errors.As(err, &e) || e.Code != 2 || !strings.Contains(err.Error(), "unreachable") || gotConfig != "/k" || gotContext != "k3d-x" {
		t.Fatalf("%v (%q %q)", err, gotConfig, gotContext)
	}
	if err := r.preflight(t.Context(), []string{"extra"}); !errors.As(err, &e) || e.Code != 2 {
		t.Fatalf("a positional argument: %v", err)
	}
	if err := r.preflight(t.Context(), []string{"--nope"}); !errors.As(err, &e) || e.Code != 2 {
		t.Fatalf("an unknown flag: %v", err)
	}
}
