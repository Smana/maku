// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type health struct{ checked, healthy bool }

func (h health) Checked() bool          { return h.checked }
func (h health) Healthy(time.Time) bool { return h.healthy }

// A connection no call has tried yet is NotChecked, never an outage: neither its Connected nor
// the Factory's GitHubUp says it is down.
func TestNeverCheckedIsNoOutage(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		h      health
		status metav1.ConditionStatus
	}{{health{}, metav1.ConditionUnknown}, {health{checked: true}, metav1.ConditionFalse}, {health{checked: true, healthy: true}, metav1.ConditionTrue}} {
		if got, _ := connection(tc.h, now); got != tc.status {
			t.Errorf("%+v: Connected %s, want %s", tc.h, got, tc.status)
		}
	}
	if !githubUp([]ghHealth{health{}, health{checked: true, healthy: true}}, now) {
		t.Error("a never-checked connection counted as down")
	}
	if githubUp([]ghHealth{health{}, health{checked: true}}, now) {
		t.Error("a checked, unhealthy connection is down")
	}
}

// A repository listed without its factory connection (gh nil) is not connected, and no outage
// of GitHub's.
func TestStatusWithoutAFactoryConnection(t *testing.T) {
	conns := &repoConns{order: []string{"acme/app"}, by: map[string]repoConn{"acme/app": {}}}
	if got, msg := conns.connection("acme/app", time.Now()); got != metav1.ConditionFalse || msg != "no GitHub connection" {
		t.Errorf("Connected %s %q", got, msg)
	}
	if got, _ := conns.connection("acme/other", time.Now()); got != metav1.ConditionFalse {
		t.Errorf("an unlisted repository: Connected %s", got)
	}
	if !conns.githubUp(time.Now()) {
		t.Error("no connection is no outage")
	}
}
