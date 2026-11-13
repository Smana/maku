// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/drills"
)

func drillServer(t *testing.T, gatewayLog string) *Server {
	t.Helper()
	planted := &factoryv1.Task{ObjectMeta: metav1.ObjectMeta{Name: "taska234", Namespace: "agent-system",
		Labels: map[string]string{factoryv1.LabelDrillInjection: "true"}}}
	plain := &factoryv1.Task{ObjectMeta: metav1.ObjectMeta{Name: "taskb234", Namespace: "agent-system"}}
	cfg := sponsoredCfg()
	cfg.Hardening = config.Hardening{Drills: config.HardeningDrills{GatewayLog: config.GatewayLog{URL: gatewayLog}}}
	return &Server{Auth: byToken{"d": {Kind: envelope.ActorSystem, ID: DrillPrincipal}, "b": broker,
		"h": {Kind: envelope.ActorHuman, ID: "github:smana"}},
		Cfg: cfg, Namespace: "agent-system", Rooms: fake.NewClientBuilder().WithScheme(scheme()).WithObjects(planted, plain).Build(),
		Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
}

func postDrillManifest(s *Server, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/hardening/drills", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

const drillManifest = `{"id":"drill-1","scenarios":[{"scenario":"stranger-comment","task":"taska234","marker":"0123456789abcdef"}]}`

// HR38: the drill planter, and only it, hands the factory a manifest once per id; each scenario is
// the default suite's and each task a drill task. The factory keeps it for its runner.
func TestDrillManifestRoute(t *testing.T) {
	s := drillServer(t, "http://victoria-logs:9428")
	if w := postDrillManifest(s, "d", drillManifest); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var cm corev1.ConfigMap
	if err := s.Rooms.Get(t.Context(), client.ObjectKey{Namespace: "agent-system", Name: drills.ConfigMapName("drill-1")}, &cm); err != nil ||
		cm.Labels[drills.ManifestLabel] != "true" {
		t.Fatalf("%v %v", cm.Labels, err)
	}
	if w := postDrillManifest(s, "d", drillManifest); w.Code != http.StatusOK {
		t.Fatalf("a retry: %d %s", w.Code, w.Body)
	}
	for name, c := range map[string]struct {
		token, body string
		code        int
		reason      string
	}{
		"another manifest under the id": {"d", strings.Replace(drillManifest, "0123456789abcdef", "fedcba9876543210", 1), http.StatusConflict, "drill_exists"},
		"the broker":                    {"b", strings.Replace(drillManifest, "drill-1", "drill-2", 1), http.StatusForbidden, "not_permitted"},
		"a person":                      {"h", strings.Replace(drillManifest, "drill-1", "drill-2", 1), http.StatusForbidden, "not_permitted"},
		"a scenario not the suite's":    {"d", strings.Replace(drillManifest, "stranger-comment", "made-up", 1), http.StatusBadRequest, "bad_manifest"},
		"a task that is no drill's":     {"d", strings.Replace(strings.Replace(drillManifest, "taska234", "taskb234", 1), "drill-1", "drill-2", 1), http.StatusBadRequest, "not_a_drill_task"},
		"no such task":                  {"d", strings.Replace(strings.Replace(drillManifest, "taska234", "taskc234", 1), "drill-1", "drill-2", 1), http.StatusNotFound, "unknown_task"},
	} {
		t.Run(name, func(t *testing.T) {
			w := postDrillManifest(s, c.token, c.body)
			if w.Code != c.code || !strings.Contains(w.Body.String(), `"`+c.reason+`"`) {
				t.Fatalf("%d %s, want %d %s", w.Code, w.Body, c.code, c.reason)
			}
		})
	}
	// With no gateway log the drill runner is off, and so is the route.
	if w := postDrillManifest(drillServer(t, ""), "d", drillManifest); w.Code != http.StatusNotFound {
		t.Fatalf("the route with the runner off: %d", w.Code)
	}
}
