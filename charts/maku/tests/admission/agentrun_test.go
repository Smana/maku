// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/Smana/maku/internal/runwatch"
)

const factoryUser = "system:serviceaccount:" + release + ":agent-factory"

func TestAgentRunPolicies(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset: run `task test`, which sets it")
	}
	policy := render(t, "templates/agentrun-policies.yaml", 4,
		"agentRunPolicies.creators={"+factoryUser+"}", "agentRunPolicies.factoryServiceAccount=agent-factory")
	ctrl.SetLogger(logr.Discard())
	e := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "..", "agent-factory-crds", "files")}, ErrorIfCRDPathMissing: true}
	cfg, err := e.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop() })
	admin, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	u, err := e.AddUser(envtest.User{Name: factoryUser, Groups: []string{"system:masters"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	factory, err := client.New(u.Config(), client.Options{})
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
	// enforced once another user's create is refused
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := admin.Create(ctx, agentRun(), client.DryRunAll)
		if deniedByPolicy(err) {
			if !strings.Contains(err.Error(), "only the factory creates AgentRuns") {
				t.Fatalf("another user's create is refused, but not by the creator policy: %v", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the creator policy was not enforced within 30 s (last: %v)", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := factory.Create(ctx, agentRun()); err != nil {
		t.Fatalf("the factory's create is refused: %v", err)
	}
	// the patch-limit policy loads on its own clock: wait for its first refusal
	spec := map[string]any{"spec": map[string]any{"budget": map[string]any{"maxTokens": 1}}}
	for deadline = time.Now().Add(30 * time.Second); ; {
		if err := patch(t, factory, spec); deniedByPolicy(err) {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("the patch-limit policy was not enforced within 30 s (last: %v)", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	for _, tc := range []struct {
		name    string
		as      client.Client
		patch   map[string]any
		allowed bool
	}{
		{"the factory changes the spec", factory, spec, false},
		{"the factory relabels the run", factory, map[string]any{"metadata": map[string]any{"labels": map[string]any{"agents.ogenki.io/task": "other"}}}, false},
		{"the factory sets another annotation", factory, ann(map[string]any{"example.com/x": "y"}), false},
		{"the factory suspends the run", factory, ann(map[string]any{runwatch.OperatingModeAnnotation: "Suspended"}), false},
		{"the factory meters the run", factory, ann(map[string]any{usageKey: "20"}), true},
		{"the factory records the pull request", factory, ann(map[string]any{"agents.ogenki.io/pull-request": "https://github.com/o/r/pull/1"}), true},
		{"the factory revokes the run", factory, ann(map[string]any{runwatch.RevokedAnnotation: "factory"}), true},
		{"another writer changes the spec", admin, spec, true},
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
