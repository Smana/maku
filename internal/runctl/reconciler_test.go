// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"context"
	"log/slog"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

func newFakeReconciler(t *testing.T, cfg Config, conflictOnce *bool, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	base := []client.Object{
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "agent-model", Namespace: "agents"},
			Data: map[string][]byte{"api_key": []byte("sk"), "model": []byte("claude-sonnet-5-5")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "agent-git", Namespace: "agents"},
			Data: map[string][]byte{"token": []byte("github_pat_x")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "room-broker-tls", Namespace: "agent-platform"},
			Data: map[string][]byte{"ca.crt": []byte("CA")}},
	}
	b := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.AgentRun{}, &corev1.Pod{}).WithObjects(append(base, objs...)...)
	if conflictOnce != nil {
		b = b.WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if subResourceName == "status" && *conflictOnce {
					*conflictOnce = false
					return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "agentruns"}, obj.GetName(), nil)
				}
				return c.Status().Update(ctx, obj, opts...)
			},
		})
	}
	c := b.Build()
	r := &Reconciler{
		Client:    c,
		APIReader: c,
		Cfg:       cfg,
		Now:       func() time.Time { return now },
		Log:       slog.New(slog.DiscardHandler),
	}
	return r, c
}

func TestReconcileStatusConflictAfterPodCreateRecoversConditions(t *testing.T) {
	for _, tc := range []struct {
		name            string
		runtimeClass    string
		allowUnsandbox  bool
		priorHold       bool
		podRunsOnRetry  bool
		wantUnsandboxed bool
		wantPhase       string
	}{
		{
			name:            "unsandboxed pod recovers Ready=Created and Unsandboxed=True",
			runtimeClass:    "",
			allowUnsandbox:  true,
			wantUnsandboxed: true,
			wantPhase:       "Pending",
		},
		{
			name:            "sandboxed pod recovers Ready=Created without Unsandboxed",
			runtimeClass:    "gvisor",
			allowUnsandbox:  false,
			wantUnsandboxed: false,
			wantPhase:       "Pending",
		},
		{
			name:            "unsandboxed pod already running on retry keeps Running phase and recovers conditions",
			runtimeClass:    "",
			allowUnsandbox:  true,
			podRunsOnRetry:  true,
			wantUnsandboxed: true,
			wantPhase:       "Running",
		},
		{
			name:            "prior Ready=False hold overwritten to Ready=Created on retry after conflict",
			runtimeClass:    "",
			allowUnsandbox:  true,
			priorHold:       true,
			wantUnsandboxed: true,
			wantPhase:       "Pending",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.NetworkPolicy = NetworkPolicyNone
			cfg.RuntimeClassName = tc.runtimeClass
			cfg.AllowUnsandboxed = tc.allowUnsandbox

			ar := run("run-abcdefgh")
			if tc.priorHold {
				ar.Status.Phase = "Pending"
				setCond(&ar.Status, "Ready", metav1.ConditionFalse, "SecretMissing", "waiting")
			}

			conflictOnce := true
			rec, c := newFakeReconciler(t, cfg, &conflictOnce, ar)
			req := ctrl.Request{NamespacedName: key("run-abcdefgh")}

			// First reconcile creates the pod, then fails the status write with 409 Conflict.
			if _, err := rec.Reconcile(t.Context(), req); !apierrors.IsConflict(err) {
				t.Fatalf("first reconcile: want 409 Conflict, got %v", err)
			}
			pod := &corev1.Pod{}
			if err := c.Get(t.Context(), key("run-abcdefgh"), pod); err != nil {
				t.Fatalf("pod was not created before conflict: %v", err)
			}
			if tc.podRunsOnRetry {
				pod.Status.Phase = corev1.PodRunning
				pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
				if err := c.Status().Update(t.Context(), pod); err != nil {
					t.Fatal(err)
				}
			}

			// Second reconcile observes the existing pod and must record Ready=Created and Unsandboxed.
			if _, err := rec.Reconcile(t.Context(), req); err != nil {
				t.Fatalf("second reconcile: %v", err)
			}
			got := &v1alpha1.AgentRun{}
			if err := c.Get(t.Context(), key("run-abcdefgh"), got); err != nil {
				t.Fatal(err)
			}
			if got.Status.Phase != tc.wantPhase {
				t.Errorf("phase = %q, want %q", got.Status.Phase, tc.wantPhase)
			}
			ready := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
			if ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != "Created" {
				t.Errorf("Ready condition = %+v, want True/Created", ready)
			}
			unsandboxed := apimeta.FindStatusCondition(got.Status.Conditions, "Unsandboxed")
			if tc.wantUnsandboxed {
				if unsandboxed == nil || unsandboxed.Status != metav1.ConditionTrue || unsandboxed.Reason != "AllowUnsandboxed" {
					t.Errorf("Unsandboxed condition = %+v, want True/AllowUnsandboxed", unsandboxed)
				}
			} else if unsandboxed != nil {
				t.Errorf("Unsandboxed condition = %+v, want absent for sandboxed pod", unsandboxed)
			}
		})
	}
}

func TestReconcileSandboxedPodNotMarkedUnsandboxedAfterConfigDrift(t *testing.T) {
	cfg := testConfig(t)
	cfg.NetworkPolicy = NetworkPolicyNone
	rec, c := newFakeReconciler(t, cfg, nil, run("run-abcdefgh"))
	req := ctrl.Request{NamespacedName: key("run-abcdefgh")}
	if _, err := rec.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	// Config changes to empty RuntimeClassName while the gvisor pod is still alive.
	rec.Cfg.RuntimeClassName, rec.Cfg.AllowUnsandboxed = "", true
	if _, err := rec.Reconcile(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	got := &v1alpha1.AgentRun{}
	if err := c.Get(t.Context(), key("run-abcdefgh"), got); err != nil {
		t.Fatal(err)
	}
	if cond := apimeta.FindStatusCondition(got.Status.Conditions, "Unsandboxed"); cond != nil {
		t.Fatalf("a gvisor pod must not be marked Unsandboxed after config drift: %+v", cond)
	}
}
