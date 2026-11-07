// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// coordinationCase is one shared fixture under testdata/coordination, the set the API package's
// schema assertions read too.
type coordinationCase struct {
	Description string         `json:"description"`
	Want        string         `json:"want"`
	Layer       string         `json:"layer"`
	Message     string         `json:"message,omitempty"`
	Task        v1alpha1.Task  `json:"task"`
	Update      *v1alpha1.Task `json:"update,omitempty"`
}

func coordinationCases(t *testing.T) map[string]coordinationCase {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "coordination", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	out := map[string]coordinationCase{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		var c coordinationCase
		if err := dec.Decode(&c); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out[strings.TrimSuffix(filepath.Base(f), ".json")] = c
	}
	return out
}

// MR2, MR10, MR16: the API server accepts the legacy, parent, child and integration shapes and
// refuses every CRD-layer invariant with its message. A status-only invariant is refused on the
// status write, the create having passed. An admission-layer case is accepted here and refused by
// the reconciler's lineage check: CRD CEL reads only metadata.name. Installing the CRD also
// proves the new rules' CEL cost fits the API server's budget.
func TestEnvtestCoordinationFixtures(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset: run `task test`, which sets it")
	}
	cases := coordinationCases(t)
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "config", "crd")}, ErrorIfCRDPathMissing: true}
	restCfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = env.Stop() }()
	c, err := client.New(restCfg, client.Options{Scheme: scheme()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "agent-system"}}); err != nil {
		t.Fatal(err)
	}
	// write creates the Task, then writes its status (a create drops it), then applies the edit.
	write := func(cc coordinationCase) error {
		tk := cc.Task.DeepCopy()
		st := tk.Status
		if err := c.Create(ctx, tk); err != nil {
			return err
		}
		if !statusIsEmpty(st) {
			tk.Status = st
			if err := c.Status().Update(ctx, tk); err != nil {
				return err
			}
		}
		if cc.Update == nil {
			return nil
		}
		up := cc.Update.DeepCopy()
		up.ResourceVersion = tk.ResourceVersion
		return c.Update(ctx, up)
	}
	for name, cc := range cases {
		t.Run(name, func(t *testing.T) {
			err := write(cc)
			switch {
			case cc.Layer == "admission":
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				// The API server accepts it; the reconciler's lineage check refuses it before any
				// claim, whatever its parent (Task 9).
				var got v1alpha1.Task
				if err := c.Get(ctx, client.ObjectKeyFromObject(&cc.Task), &got); err != nil {
					t.Fatal(err)
				}
				r := &Reconciler{Client: c, Namespace: "agent-system"}
				if _, err := r.checkCoordinationLineage(ctx, &got); lineageReason(err) == "" {
					t.Errorf("admitted, want %q: %v", cc.Message, err)
				}
			case cc.Want == "accept":
				if err != nil {
					t.Errorf("refused: %v", err)
				}
			case err == nil:
				t.Errorf("accepted, want %q", cc.Message)
			case !strings.Contains(err.Error(), cc.Message):
				t.Errorf("refused for another reason, want %q: %v", cc.Message, err)
			}
		})
	}
}

func statusIsEmpty(s v1alpha1.TaskStatus) bool { return reflect.DeepEqual(s, v1alpha1.TaskStatus{}) }
