// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// podFails refuses every pod create with err, as the quota admission plugin does.
type podFails struct {
	client.Client
	err error
}

func (c podFails) Create(ctx context.Context, o client.Object, opts ...client.CreateOption) error {
	if _, ok := o.(*corev1.Pod); ok {
		return c.err
	}
	return c.Client.Create(ctx, o, opts...)
}

func TestEnvtestQuotaExceededWaits(t *testing.T) {
	// The quota admission plugin's two refusals a retry cures: the quota is full, or it is not yet
	// populated (status unknown for quota: right after install, or after a key is added).
	for name, msg := range map[string]string{
		"run-aaaaqqqq": "exceeded quota: runs, requested: pods=1, used: pods=25, limited: pods=25",
		"run-ccccqqqq": "status unknown for quota: runs, resources: limits.cpu",
	} {
		t.Run(msg, func(t *testing.T) {
			e := startEnv(t)
			e.modelSecret(t)
			e.r.Client = podFails{e.c, apierrors.NewForbidden(corev1.Resource("pods"), name, errors.New(msg))}
			create(t, e, run(name))
			res := e.reconcile(t, name)
			got := e.get(t, name)
			c := apimeta.FindStatusCondition(got.Status.Conditions, "Ready")
			if got.Status.Phase != "Pending" || c == nil || c.Reason != "QuotaExceeded" || res.RequeueAfter != quotaRetry {
				t.Fatalf("a run the quota refuses waits: %+v, %+v", got.Status, res)
			}
			if apimeta.IsStatusConditionTrue(got.Status.Conditions, "Invalid") {
				t.Fatalf("a run the quota refuses is never Invalid: %+v", got.Status)
			}
		})
	}
}

func TestEnvtestAnotherForbiddenStillFails(t *testing.T) {
	e := startEnv(t)
	e.modelSecret(t)
	e.r.Client = podFails{e.c, apierrors.NewForbidden(corev1.Resource("pods"), "run-bbbbqqqq", errors.New("violates PodSecurity"))}
	create(t, e, run("run-bbbbqqqq"))
	e.reconcile(t, "run-bbbbqqqq")
	got := e.get(t, "run-bbbbqqqq")
	c := apimeta.FindStatusCondition(got.Status.Conditions, "Invalid")
	if got.Status.Phase != "Failed" || c == nil || c.Reason != "CreateFailed" {
		t.Fatalf("another Forbidden ends the run: %+v", got.Status)
	}
}
