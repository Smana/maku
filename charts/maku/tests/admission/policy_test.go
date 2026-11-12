// SPDX-License-Identifier: Apache-2.0

// Package admission proves the maku chart's AgentRun admission policy against a real API server
// (envtest): the broker's ServiceAccount may change the suspend annotations and nothing else
// (audit S-2, design D4). The policy is the chart's own render, never a copy.
package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/Smana/maku/internal/runwatch"
)

const (
	release    = "maku"
	runsNS     = "agents"
	brokerUser = "system:serviceaccount:" + release + ":room-broker"
	runName    = "run-abcdefgh"
	usageKey   = "agents.ogenki.io/usage-tokens" // the meter's: the broker must never write it
	digest     = "v0.0.0@sha256:0000"
)

func TestTheBrokerMayChangeOnlyTheSuspendAnnotations(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset: run `task test`, which sets it")
	}
	policy := render(t, "templates/broker-agentrun-policy.yaml", 2)
	ctrl.SetLogger(logr.Discard())
	e := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "crds")}, ErrorIfCRDPathMissing: true}
	cfg, err := e.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop() })
	admin, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// system:masters passes RBAC, never admission: the policy alone decides.
	u, err := e.AddUser(envtest.User{Name: brokerUser, Groups: []string{"system:masters"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := client.New(u.Config(), client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: runsNS}}); err != nil {
		t.Fatal(err)
	}
	for _, o := range policy {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if err := admin.Create(ctx, agentRun()); err != nil {
		t.Fatal(err)
	}
	waitEnforced(t, broker)

	for _, tc := range []struct {
		name    string
		as      client.Client
		patch   map[string]any
		allowed bool
	}{
		{"the broker suspends a run", broker, ann(map[string]any{runwatch.OperatingModeAnnotation: "Suspended", runwatch.RemainingSecondsAnnotation: "600"}), true},
		{"the broker resumes a run", broker, ann(map[string]any{runwatch.OperatingModeAnnotation: "Running", runwatch.ResumedAtAnnotation: "2026-11-01T00:00:00Z"}), true},
		{"the broker drops a suspend annotation", broker, ann(map[string]any{runwatch.RemainingSecondsAnnotation: nil}), true},
		{"the broker changes the spec", broker, map[string]any{"spec": map[string]any{"budget": map[string]any{"maxTokens": 1}}}, false},
		{"the broker rewrites the meter's annotation", broker, ann(map[string]any{usageKey: "0"}), false},
		{"the broker drops the meter's annotation", broker, ann(map[string]any{usageKey: nil}), false},
		{"the broker revokes the run", broker, ann(map[string]any{runwatch.RevokedAnnotation: "broker"}), false},
		{"the broker adds an annotation of its own", broker, ann(map[string]any{"example.com/x": "y"}), false},
		{"the broker relabels the run", broker, map[string]any{"metadata": map[string]any{"labels": map[string]any{"agents.ogenki.io/task": "other"}}}, false},
		{"the broker adds a finalizer", broker, map[string]any{"metadata": map[string]any{"finalizers": []string{"example.com/hold"}}}, false},
		{"another writer changes the spec", admin, map[string]any{"spec": map[string]any{"budget": map[string]any{"maxTokens": 2}}}, true},
		{"another writer rewrites the meter's annotation", admin, ann(map[string]any{usageKey: "20"}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := patch(t, tc.as, tc.patch)
			switch {
			case tc.allowed && err != nil:
				t.Fatalf("refused: %v", err)
			case !tc.allowed && !deniedByPolicy(err):
				t.Fatalf("want the policy's refusal, got %v", err)
			}
		})
	}
}

// render is one of the chart's templates, as `helm template` renders it for release "maku" from
// the quickstart values plus sets, and must hold want objects.
func render(t *testing.T, template string, want int, sets ...string) []client.Object {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal("helm is not on PATH: mise install")
	}
	chart := filepath.Join("..", "..")
	args := []string{"template", "ap", chart, "-n", release,
		"-f", filepath.Join(chart, "values-quickstart.yaml"),
		"--set", "broker.image.tag=" + digest, "--set", "runController.image.tag=" + digest,
		"--set", "runController.images.harness.tag=" + digest, "--set", "runController.images.bridge.tag=" + digest,
		"--show-only", template}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, err := exec.CommandContext(t.Context(), helm, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("helm template: %v: %s", err, ee.Stderr)
		}
		t.Fatal(err)
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(out), 4096)
	var objs []client.Object
	for {
		u := &unstructured.Unstructured{}
		if err := dec.Decode(&u.Object); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if len(u.Object) > 0 {
			objs = append(objs, u)
		}
	}
	if len(objs) != want {
		t.Fatalf("%s: want %d objects, rendered %d", template, want, len(objs))
	}
	return objs
}

func agentRun() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "maku.ogenki.io/v1alpha1", "kind": "AgentRun",
		"metadata": map[string]any{"name": runName, "namespace": runsNS,
			"labels":      map[string]any{"agents.ogenki.io/task": "t1"},
			"annotations": map[string]any{usageKey: "10", runwatch.RemainingSecondsAnnotation: "900"}},
		"spec": map[string]any{"role": "implementer", "repository": "o/r", "baseRef": "main", "principal": "human:a",
			"dataClass": "public", "size": "small", "task": map[string]any{"text": "fix it"}},
	}}
}

func ann(kv map[string]any) map[string]any {
	return map[string]any{"metadata": map[string]any{"annotations": kv}}
}

// patch merge-patches the run, as internal/suspend does.
func patch(t *testing.T, c client.Client, body map[string]any) error {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("maku.ogenki.io/v1alpha1")
	u.SetKind("AgentRun")
	u.SetNamespace(runsNS)
	u.SetName(runName)
	return c.Patch(t.Context(), u, client.RawPatch(types.MergePatchType, b))
}

// deniedByPolicy is the policy's refusal, not the CRD's own validation.
func deniedByPolicy(err error) bool {
	return (apierrors.IsInvalid(err) || apierrors.IsForbidden(err)) && strings.Contains(err.Error(), "ValidatingAdmissionPolicy")
}

// waitEnforced returns once the API server enforces the policy: it loads policies asynchronously.
func waitEnforced(t *testing.T, broker client.Client) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := patch(t, broker, ann(map[string]any{"example.com/probe": time.Now().Format(time.RFC3339Nano)}))
		if deniedByPolicy(err) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("the policy was not enforced within 30 s: does this envtest API server run the ValidatingAdmissionPolicy plugin?")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
