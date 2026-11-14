// SPDX-License-Identifier: Apache-2.0

// Package admission proves the factory chart's run token Role and its admission policy on a real
// API server (envtest): with runTokens.perRun the factory may write a run's token Secret and
// nothing else of the runs namespace's Secrets, and may read none (v4 D10, plan PR3).
package admission

import (
	"bytes"
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
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const (
	release     = "agent-system"
	runsNS      = "agents"
	factoryUser = "system:serviceaccount:" + release + ":agent-factory"
	runName     = "maku-run-abcdefgh"
)

// render is the chart's perRun objects that act in the runs namespace, plus the policy.
func render(t *testing.T) []client.Object {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal("helm is not on PATH: mise install")
	}
	args := []string{"template", "agent-factory", filepath.Join("..", ".."), "-n", release,
		"--set", "image.tag=v0.0.0@sha256:0000", "--set", "config.repository=Smana/demo",
		"--set", "config.runs.group=maku.ogenki.io", "--set", "config.runs.namePrefix=maku-run-",
		"--set", "runTokens.enabled=true", "--set", "apps.agents.existingSecret=agents-app", "--set", "runTokens.perRun=true",
		"--show-only", "templates/rbac.yaml", "--show-only", "templates/run-token-policy.yaml"}
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
		switch {
		case len(u.Object) == 0:
		case u.GetNamespace() == runsNS, strings.HasPrefix(u.GetKind(), "ValidatingAdmissionPolicy"):
			objs = append(objs, u)
		}
	}
	if len(objs) != 4 {
		t.Fatalf("want the runs Role, its binding, the policy and its binding; rendered %d", len(objs))
	}
	return objs
}

func tokenSecret(name string, mut ...func(*corev1.Secret)) *corev1.Secret {
	run := strings.TrimSuffix(name, "-git")
	s := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: runsNS,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "maku-factory", "maku.ogenki.io/run": run},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "maku.ogenki.io/v1alpha1", Kind: "AgentRun", Name: run,
				UID: "0b5e2f8c-0000-4000-8000-000000000001"}}},
		Data: map[string][]byte{"token": []byte("ghs_x")},
	}
	for _, m := range mut {
		m(s)
	}
	return s
}

func denied(err error) bool {
	return apierrors.IsInvalid(err) || (apierrors.IsForbidden(err) && strings.Contains(err.Error(), "ValidatingAdmissionPolicy"))
}

func TestRunTokenPolicy(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset: run `task test`, which sets it")
	}
	objs := render(t)
	ctrl.SetLogger(logr.Discard())
	e := &envtest.Environment{}
	cfg, err := e.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop() })
	admin, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	u, err := e.AddUser(envtest.User{Name: factoryUser}, nil)
	if err != nil {
		t.Fatal(err)
	}
	factory, err := client.New(u.Config(), client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, o := range []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: release}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: runsNS}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "agent-model", Namespace: runsNS}, Data: map[string][]byte{"api_key": []byte("sk")}},
	} {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	for _, o := range objs {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	apply := func(s *corev1.Secret) error {
		ac := corev1ac.Secret(s.Name, s.Namespace).WithLabels(s.Labels).WithData(s.Data)
		for _, o := range s.OwnerReferences {
			ac = ac.WithOwnerReferences(metav1ac.OwnerReference().WithAPIVersion(o.APIVersion).WithKind(o.Kind).WithName(o.Name).WithUID(o.UID))
		}
		return factory.Apply(ctx, ac, client.FieldOwner("maku-factory-minter"), client.ForceOwnership)
	}
	// The policy loads on its own clock: wait for its first refusal.
	for deadline := time.Now().Add(30 * time.Second); ; {
		err := apply(tokenSecret("agent-model-git", func(s *corev1.Secret) { s.Name = "agent-model" }))
		if denied(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run token policy was not enforced within 30 s (last: %v)", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	if err := apply(tokenSecret(runName + "-git")); err != nil {
		t.Fatalf("the factory's apply of its run token Secret: %v", err)
	}
	if err := apply(tokenSecret(runName+"-git", func(s *corev1.Secret) { s.Data["token"] = []byte("ghs_y") })); err != nil {
		t.Fatalf("the factory's refresh of its run token Secret: %v", err)
	}
	for _, tc := range []struct {
		name string
		do   func() error
	}{
		{"a patch of agent-model", func() error {
			s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "agent-model", Namespace: runsNS}}
			return factory.Patch(ctx, s, client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":{"x":"y"}}}`)))
		}},
		{"a create without the label", func() error {
			return apply(tokenSecret("maku-run-bbbbbbbb-git", func(s *corev1.Secret) { delete(s.Labels, "app.kubernetes.io/managed-by") }))
		}},
		{"a create owned by another run", func() error {
			return apply(tokenSecret("maku-run-cccccccc-git", func(s *corev1.Secret) { s.OwnerReferences[0].Name = "maku-run-dddddddd" }))
		}},
		{"a create of another name", func() error { return apply(tokenSecret("maku-run-abcdefgh-other")) }},
		{"a delete of agent-model", func() error {
			return factory.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "agent-model", Namespace: runsNS}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.do(); !denied(err) {
				t.Fatalf("want the policy's refusal, got %v", err)
			}
		})
	}
	// A denied patch answers with no object: agent-model's data never reaches the factory.
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "agent-model", Namespace: runsNS}}
	_ = factory.Patch(ctx, s, client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":{"x":"y"}}}`)))
	if len(s.Data) != 0 {
		t.Fatal("a denied patch returned the Secret's data")
	}
	if err := factory.Get(ctx, client.ObjectKey{Namespace: runsNS, Name: runName + "-git"}, &corev1.Secret{}); !apierrors.IsForbidden(err) {
		t.Fatalf("get is the RBAC's to refuse: %v", err)
	}
	if err := factory.Delete(ctx, tokenSecret(runName+"-git")); err != nil {
		t.Fatalf("the factory deletes its run token Secret: %v", err)
	}
}
