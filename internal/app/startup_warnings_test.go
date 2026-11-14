// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/fmetrics"
	"github.com/Smana/maku/internal/metrics"
)

// Offline proof 4, startup: the deployed config lists the tester in `trio` and two live classes without
// holdoutChecks. It loads without the tester, the warnings are logged and the gauge reads 1.
func TestStartupWithATesterTemplateAndALiveClassWithoutHoldouts(t *testing.T) {
	raw := strings.Replace(factoryCfg, "classes: {docs-links: {shadow: true}, revert: {shadow: true}",
		"classes: {docs-links: {live: true}, revert: {live: true}", 1)
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if roles := cfg.Templates["trio"].Roles; len(roles) != 2 || roles[0] != "implementer" || roles[1] != "reviewer" {
		t.Fatalf("trio still lists the tester: %v", roles)
	}
	exp, err := metrics.NewExporter(metrics.FactoryBuildInfo, "v0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exp.Shutdown(t.Context()) })
	m, err := fmetrics.New(exp.Meter(), nil, "agent-system", func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	m.ListRepositories("Smana/cloud-native-ref")
	var logs bytes.Buffer
	warnConfig(slog.New(slog.NewTextHandler(&logs, nil)), cfg, m)
	for _, want := range []string{"template trio: tester is the test writer", "class docs-links of Smana/cloud-native-ref is live without holdoutChecks",
		"class revert of Smana/cloud-native-ref is live without holdoutChecks"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("no warning %q:\n%s", want, logs.String())
		}
	}
	rec := httptest.NewRecorder()
	exp.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	for _, class := range []string{"docs-links", "revert"} {
		if want := `maku_factory_class_without_holdout{class="` + class + `",repository="Smana/cloud-native-ref"} 1`; !strings.Contains(string(body), want) {
			t.Errorf("missing %s", want)
		}
	}
}

const factoryCfg = `
factory: aws-0
factoryLogin: ogenki-agent-factory[bot]
agentsLogin: ogenki-agents[bot]
roomsURL: https://rooms.priv.gcp.ogenki.io
broker: {url: "https://room-broker.agent-system.svc.cluster.local:8443", caFile: /etc/agent-factory/openbao-ca/ca.crt, tokenFile: /var/run/secrets/agents/rooms/token}
github: {appIDFile: /etc/agent-factory-github/app_id, privateKeyFile: /etc/agent-factory-github/private_key, mergerAppIDFile: /etc/agent-factory-merger/app_id, mergerKeyFile: /etc/agent-factory-merger/private_key}
poll: {issues: 60s, tasks: 30s, meter: 30s}
defaults: {template: solo, tier: standard, dataClass: public, predictedClass: review}
triage: {classifierURL: "http://complexity-classifier.agent-system.svc.cluster.local:8080/v1/classify", controlPercent: 10}
tiers:
  light:    {model: agent-default, runTokens: 300000,  taskTokens: 600000,  runMinutes: 20}
  standard: {model: agent-default, runTokens: 1500000, taskTokens: 3000000, runMinutes: 45}
  frontier: {model: agent-default, runTokens: 4000000, taskTokens: 8000000, runMinutes: 90}
templates:
  solo: {roles: [implementer]}
  pair: {roles: [implementer, reviewer], maxReviewRounds: 2}
  trio: {roles: [implementer, tester, reviewer], maxReviewRounds: 2}
  investigate: {roles: [triager]}  # R38: proposes a public issue text, never writes
caps: {activeTasks: 3, concurrentRuns: 4, tasksPerDay: 20, maxTextBytes: 14336, awaitingHumanWIP: 5}
budgets: {enforceTask: false, enforcePrincipal: false, factoryDailyUSD: 42.50, humanDailyUSD: 8.50}
api:
  listen: ":8443"
  humanIssuer: https://auth.ogenki.io
  humanJWKS: https://auth.ogenki.io/oauth/v2/keys
  clientIDFiles: [/etc/agent-factory-oidc/rooms-proxy-client-id, /etc/agent-factory-oidc/roomctl-client-id]
runlore:
  listen: ":8080"
  tokenFile: /etc/agent-factory-intake/token
  minConfidence: 0.75
  dailyCap: 5
meter:
  url: http://vmsingle-victoria-metrics-k8s-stack.observability.svc:8428
  query: 'sum by (ar_agent) (gen_ai_client_token_usage_sum{ar_agent=~"system:serviceaccount:agents:run-.+", gen_ai_token_type=~"input|output"})'
  logsURL: http://victoria-logs-victoria-logs-single-server.observability.svc:9428
  throttleQuery: '_time:2m kubernetes.pod_namespace:"agent-gateway" AND kubernetes.pod_labels.gateway.networking.k8s.io/gateway-name:"agent-router" | unpack_json | filter log.http.status:429 AND log.reason:RateLimit | stats by (log.x_ar_agent) count() hits'
repositories:
  - name: Smana/cloud-native-ref
    maintainers: [Smana]
    triggerLabel: factory/ready
    classes: {docs-links: {shadow: true}, revert: {shadow: true}, docs: {}, tests: {}, dashboards: {}}
    merge:
      requiredChecks: ["Pre-commit checks 🛃", "Security scanning 🔒", "Kubernetes validation ☸", "Rendered manifest diff 📝", "Check the shell scripts 💻", "Check the documentation links 🔗", "Validate Vector Log Parsing Configuration (vlsingle)", "Validate Vector Log Parsing Configuration (vlcluster)"]
      verifyChecks: ["Pre-commit checks 🛃", "Security scanning 🔒", "Kubernetes validation ☸", "Check the shell scripts 💻", "Check the documentation links 🔗"]
      leakScanCheck: "Security scanning 🔒"
      policyBotLogin: ogenki-merge-gate[bot]
      mergerLogin: ogenki-agent-merger[bot]
      autoMergesPerDay: 10
      fixRuns: 2
      checkReruns: 1
      verifyFor: 30m
      revertWindow: 168h
      breaker: {window: 10, maxReverts: 1}
`
