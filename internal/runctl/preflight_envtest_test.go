// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fixedPreflight is a preflight whose result the test sets.
type fixedPreflight struct{ res Result }

func (f *fixedPreflight) Result() Result { return f.res }

func TestEnvtestPreflightPendingHoldsTheRun(t *testing.T) {
	e := startEnv(t)
	e.modelSecret(t)
	e.r.Preflight = &fixedPreflight{Result{State: PreflightUnknown, Check: "runtimeClass", Message: "connection refused"}}
	create(t, e, run("run-pppppppp"))
	res := e.reconcile(t, "run-pppppppp")
	got := e.get(t, "run-pppppppp")
	c := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if got.Status.Phase != "Pending" || c == nil || c.Reason != "PreflightPending" || res.RequeueAfter != preflightRetry ||
		!strings.Contains(c.Message, "runtimeClass: connection refused") {
		t.Fatalf("an unknown preflight holds the run Pending: %+v, %+v", got.Status, res)
	}
	if err := e.c.Get(e.ctx, key("run-pppppppp"), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("no pod while the preflight is pending, got %v", err)
	}
}

func TestEnvtestPreflightFailedRefusesTheRun(t *testing.T) {
	e := startEnv(t)
	e.modelSecret(t)
	e.r.Preflight = &fixedPreflight{Result{State: PreflightFailed, Check: "runtimeClass", Message: `RuntimeClass "gvisor" not found`}}
	create(t, e, run("run-qqqqqqqq"))
	e.reconcile(t, "run-qqqqqqqq")
	got := e.get(t, "run-qqqqqqqq")
	c := apimeta.FindStatusCondition(got.Status.Conditions, "Invalid")
	if got.Status.Phase != "Failed" || c == nil || c.Status != metav1.ConditionTrue || c.Reason != "PreflightFailed" ||
		!strings.Contains(c.Message, `runtimeClass: RuntimeClass "gvisor" not found`) {
		t.Fatalf("a failed preflight refuses the run: %+v", got.Status)
	}
	if err := e.c.Get(e.ctx, key("run-qqqqqqqq"), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("no pod for a refused run, got %v", err)
	}
}

func TestEnvtestPreflightNeverRefusesARunWithAPod(t *testing.T) {
	e := startEnv(t)
	e.modelSecret(t)
	pf := &fixedPreflight{Result{State: PreflightPassed}}
	e.r.Preflight = pf
	create(t, e, run("run-rrrrrrrr"))
	e.reconcile(t, "run-rrrrrrrr")
	pf.res = Result{State: PreflightFailed, Check: "runtimeClass", Message: "gone"}
	e.reconcile(t, "run-rrrrrrrr")
	got := e.get(t, "run-rrrrrrrr")
	if got.Status.Phase == "Failed" || apimeta.IsStatusConditionTrue(got.Status.Conditions, "Invalid") {
		t.Fatalf("a run with a pod is never refused by a later preflight failure: %+v", got.Status)
	}
	if err := e.c.Get(e.ctx, key("run-rrrrrrrr"), &corev1.Pod{}); err != nil {
		t.Fatalf("the pod stays: %v", err)
	}
}

// PR B: a missing git Secret holds new runs Pending, naming the Secret, and never fails them.
func TestEnvtestPreflightWaitingHoldsTheRun(t *testing.T) {
	e := startEnv(t)
	e.modelSecret(t)
	const msg = "Secret agents/agent-git-read not found: reviewer and triager runs mount it (runController.gitReadSecret); create it with a token key"
	e.r.Preflight = &fixedPreflight{Result{State: PreflightWaiting, Check: "gitSecrets", Message: msg}}
	create(t, e, run("run-wwwwwwww"))
	res := e.reconcile(t, "run-wwwwwwww")
	got := e.get(t, "run-wwwwwwww")
	c := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
	if got.Status.Phase != "Pending" || c == nil || c.Reason != "PreflightPending" || res.RequeueAfter != preflightRetry ||
		!strings.Contains(c.Message, "gitSecrets: "+msg) || apimeta.IsStatusConditionTrue(got.Status.Conditions, "Invalid") {
		t.Fatalf("a waiting preflight holds the run Pending: %+v, %+v", got.Status, res)
	}
	if err := e.c.Get(e.ctx, key("run-wwwwwwww"), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("no pod while the preflight waits, got %v", err)
	}
}
