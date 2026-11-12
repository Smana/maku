// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/runwatch"
)

// runTokenSecret is the factory's Secret for run, expiring at exp; mut bends it.
func runTokenSecret(run *v1alpha1.AgentRun, exp time.Time, mut ...func(*corev1.Secret)) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: run.Name + "-git", Namespace: "agents",
			Labels:          map[string]string{runwatch.RunTokenManagedByLabel: runwatch.RunTokenManagedBy, runwatch.RunTokenRunLabel: run.Name},
			Annotations:     map[string]string{runwatch.RunTokenExpiresAnnotation: exp.UTC().Format(time.RFC3339)},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: run.Name, UID: run.UID}},
		},
		Data: map[string][]byte{"token": []byte("ghs_x")},
	}
	for _, m := range mut {
		m(s)
	}
	return s
}

// Plan PR5: in perRun mode a run's pod waits for its own Secret, the factory's, with life left.
func TestEnvtestPerRunToken(t *testing.T) {
	e := startEnv(t)
	e.modelSecret(t)
	e.r.Cfg.GitTokens, e.r.Cfg.GitReadSecret = GitTokensPerRun, ""

	ready := func(t *testing.T, name string) *metav1.Condition {
		t.Helper()
		return apimeta.FindStatusCondition(e.get(t, name).Status.Conditions, "Ready")
	}
	hasPod := func(name string) bool { return e.c.Get(e.ctx, key(name), &corev1.Pod{}) == nil }
	for _, tc := range []struct {
		name, run, reason, msg string
		secret                 func(*v1alpha1.AgentRun) *corev1.Secret
	}{
		{"no Secret", "run-aaaaaaaa", "SecretMissing", "not found", nil},
		{"another run's Secret", "run-bbbbbbbb", "SecretMissing", "the run's token Secret is not the factory's",
			func(r *v1alpha1.AgentRun) *corev1.Secret {
				return runTokenSecret(r, now.Add(time.Hour), func(s *corev1.Secret) { s.OwnerReferences[0].UID = "someone-else" })
			}},
		{"not the factory's label", "run-cccccccc", "SecretMissing", "the run's token Secret is not the factory's",
			func(r *v1alpha1.AgentRun) *corev1.Secret {
				return runTokenSecret(r, now.Add(time.Hour), func(s *corev1.Secret) { delete(s.Labels, runwatch.RunTokenManagedByLabel) })
			}},
		{"no token", "run-dddddddd", "SecretMissing", "has no token",
			func(r *v1alpha1.AgentRun) *corev1.Secret {
				return runTokenSecret(r, now.Add(time.Hour), func(s *corev1.Secret) { s.Data = nil })
			}},
		{"expiring", "run-eeeeeeee", "SecretExpiring", "expires within",
			func(r *v1alpha1.AgentRun) *corev1.Secret { return runTokenSecret(r, now.Add(4*time.Minute)) }},
		{"no expiry", "run-ffffffff", "SecretExpiring", "expires within",
			func(r *v1alpha1.AgentRun) *corev1.Secret {
				return runTokenSecret(r, now.Add(time.Hour), func(s *corev1.Secret) { s.Annotations = nil })
			}},
		{"fresh", "run-gggggggg", "Created", "", func(r *v1alpha1.AgentRun) *corev1.Secret { return runTokenSecret(r, now.Add(time.Hour)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			create(t, e, run(tc.run))
			r := e.get(t, tc.run)
			if tc.secret != nil {
				if err := e.c.Create(e.ctx, tc.secret(r)); err != nil {
					t.Fatal(err)
				}
			}
			res := e.reconcile(t, tc.run)
			c := ready(t, tc.run)
			if c == nil || c.Reason != tc.reason || !strings.Contains(c.Message, tc.msg) {
				t.Fatalf("Ready %+v, want %s %q", c, tc.reason, tc.msg)
			}
			if got := hasPod(tc.run); got != (tc.reason == "Created") {
				t.Fatalf("pod created %v", got)
			}
			if tc.reason != "Created" && res.RequeueAfter == 0 {
				t.Fatal("a held run must requeue")
			}
		})
	}

	// A running pod is never deleted for a later expiry: the factory refreshes the Secret in place.
	t.Run("running, then expiring", func(t *testing.T) {
		e.markReady(t, "run-gggggggg")
		e.reconcile(t, "run-gggggggg")
		e.r.Now = func() time.Time { return now.Add(58 * time.Minute) }
		defer func() { e.r.Now = func() time.Time { return now } }()
		e.reconcile(t, "run-gggggggg")
		if got := e.get(t, "run-gggggggg"); got.Status.Phase != "Running" || !hasPod("run-gggggggg") {
			t.Fatalf("a running run must keep its pod: %+v", got.Status)
		}
	})
}
