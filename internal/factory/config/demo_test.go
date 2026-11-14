// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// demoGood is good as a laptop demo: demo true, the broker over TLS with its CA, no meter, no human
// API keys, the gate's verdict as a commit status and no policy-bot, the portable runs group and
// a scoper row.
var demoGood = func() string {
	s := "demo: true\n" + good
	for _, r := range [][2]string{
		{`broker: {url: "https://room-broker.agent-system.svc.cluster.local:8443", caFile: /etc/agent-factory/openbao-ca/ca.crt, `, `broker: {url: "https://room-broker.maku.svc:8443", caFile: /etc/agent-factory/broker-ca/ca.crt, `},
		{"      policyBotLogin: ogenki-merge-gate[bot]\n", "      gateStatus: true\n      policyBot: false\n"},
		{"  humanIssuer: https://auth.ogenki.io\n  humanJWKS: https://auth.ogenki.io/oauth/v2/keys\n  clientIDFiles: [/etc/agent-factory-oidc/rooms-proxy-client-id, /etc/agent-factory-oidc/roomctl-client-id]\n", ""},
	} {
		if !strings.Contains(s, r[0]) {
			panic("demoGood: no " + r[0])
		}
		s = strings.Replace(s, r[0], r[1], 1)
	}
	if i, j := strings.Index(s, "\nmeter:\n"), strings.Index(s, "\nrepositories:\n"); i >= 0 && j > i {
		s = s[:i] + s[j:] // no meter; the repositories list stays last
	}
	return s + "runs: {group: maku.ogenki.io}\nloops: {docs: {scope: scoper}}\n"
}()

func TestDemoGoodParses(t *testing.T) {
	if _, err := Parse([]byte(demoGood)); err != nil {
		t.Fatalf("demoGood: %v", err)
	}
}

func TestDemoStillRefuses(t *testing.T) {
	for name, c := range map[string]struct{ from, to, err string }{
		"api.access set":         {"  listen: \":8443\"\n", "  listen: \":8443\"\n  access: {readerFile: /r, ttl: 1m}\n", "demo: api.access needs an identity provider"},
		"a humanIssuer":          {"  listen: \":8443\"\n", "  listen: \":8443\"\n  humanIssuer: https://auth.ogenki.io\n", "demo: the factory API takes no human tokens"},
		"prices without a meter": {"runs: {group", "meter: {prices: {agent-default: {input: 1, output: 2}}}\nruns: {group", "demo: meter.prices needs a meter, and the demo has none"},
		"gateStatus false":       {"gateStatus: true", "gateStatus: false", "demo: merge.gateStatus must be true, or no gate verdict reaches the pull request"},
		"an http broker":         {`url: "https://room-broker.maku.svc:8443"`, `url: "http://room-broker.maku.svc:8080"`, `broker.url "http://room-broker.maku.svc:8080" must be an https:// URL`},
		"no broker.caFile":       {"caFile: /etc/agent-factory/broker-ca/ca.crt, ", "", "broker.caFile is required"},
		"a plain roomsURL host":  {"roomsURL: https://rooms.priv.gcp.ogenki.io", "roomsURL: http://rooms.example.io", "roomsURL must be https://<host>"},
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(demoGood, c.from, c.to, 1)
			if raw == demoGood {
				t.Fatalf("the edit %q changed nothing", c.from)
			}
			_, err := Parse([]byte(raw))
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("got %v, want %q", err, c.err)
			}
		})
	}
}

// The portable AgentRun carries the whole loop policy, so demo takes it.
func TestDemoAllowsTheLoopPolicy(t *testing.T) {
	raw := strings.Replace(demoGood, "loops: {docs: {scope: scoper}}", "loops: {docs: {scope: scoper, stallSteps: 90, continuations: 1, judge: tier-light}}", 1)
	if raw == demoGood {
		t.Fatal("the edit changed nothing")
	}
	if _, err := Parse([]byte(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestDemoRoomsURLLocal(t *testing.T) {
	for _, u := range []string{"http://localhost", "http://localhost:8080", "http://127.0.0.1:9000"} {
		if _, err := Parse([]byte(strings.Replace(demoGood, "roomsURL: https://rooms.priv.gcp.ogenki.io", "roomsURL: "+u, 1))); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
}

func TestDemoPolicyBot(t *testing.T) {
	c, err := Parse([]byte(demoGood))
	if err != nil {
		t.Fatal(err)
	}
	if m := c.Repos()[0].Merge; m.UsesPolicyBot() || !m.GateStatus {
		t.Errorf("merge %+v: want policyBot off and gateStatus on", m)
	}
	if !(Merge{}).UsesPolicyBot() {
		t.Error("an unset policyBot must mean true")
	}
}

func TestNonDemoUnchanged(t *testing.T) {
	for name, c := range map[string]struct{ from, to, err string }{
		"policyBot false":   {"      policyBotLogin: ogenki-merge-gate[bot]\n", "      policyBotLogin: ogenki-merge-gate[bot]\n      policyBot: false\n", "merge.policyBot: false needs merge.gateStatus: true"},
		"an http broker":    {`url: "https://room-broker`, `url: "http://room-broker`, `broker.url "http://room-broker.agent-system.svc.cluster.local:8443" must be an https:// URL`},
		"no meter.url":      {"  url: http://vmsingle-victoria-metrics-k8s-stack.observability.svc:8428\n", "", "meter.url is required"},
		"no broker.caFile":  {"caFile: /etc/agent-factory/openbao-ca/ca.crt, ", "", "broker.caFile is required"},
		"an http roomsURL":  {"roomsURL: https://rooms.priv.gcp.ogenki.io", "roomsURL: http://localhost:8080", "roomsURL must be https://<host>"},
		"no human issuer":   {"  humanIssuer: https://auth.ogenki.io\n", "", "api.humanIssuer is required"},
		"no policyBotLogin": {"      policyBotLogin: ogenki-merge-gate[bot]\n", "", "merge.policyBotLogin"},
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(good, c.from, c.to, 1)
			if raw == good {
				t.Fatalf("the edit %q changed nothing", c.from)
			}
			_, err := Parse([]byte(raw))
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("got %v, want %q", err, c.err)
			}
		})
	}
	if _, err := Parse([]byte(strings.Replace(good, "      policyBotLogin:", "      gateStatus: true\n      policyBotLogin:", 1))); err != nil {
		t.Errorf("gateStatus without demo: %v", err)
	}
}

func TestDemoMeterAllOrNone(t *testing.T) {
	full := "meter:\n  url: http://m:8428\n  query: 'q'\n  logsURL: http://l:9428\n  throttleQuery: 'x'\n"
	for name, c := range map[string]struct{ meter, err string }{
		"url only":   {"meter: {url: \"http://m:8428\"}\n", "meter.query is required"},
		"query only": {"meter: {query: q}\n", "meter.url is required"},
		"all four":   {full, ""},
		"none":       {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(strings.Replace(demoGood, "\nruns: {", "\n"+c.meter+"runs: {", 1)))
			switch {
			case c.err == "" && err != nil:
				t.Fatal(err)
			case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
				t.Fatalf("got %v, want %q", err, c.err)
			}
		})
	}
	// a malformed policyBotLogin is still refused under demo with policyBot false
	if _, err := Parse([]byte(strings.Replace(demoGood, "      policyBot: false\n", "      policyBot: false\n      policyBotLogin: notabot\n", 1))); err == nil || !strings.Contains(err.Error(), "merge.policyBotLogin") {
		t.Errorf("malformed policyBotLogin: %v", err)
	}
}

// The demo has one GitHub App: the merger files name the factory App's own files, with a class in
// shadow (which needs the merger).
func TestDemoOneAppIsTheMerger(t *testing.T) {
	const own = "mergerAppIDFile: /etc/agent-factory-merger/app_id, mergerKeyFile: /etc/agent-factory-merger/private_key"
	raw := strings.Replace(demoGood, own, "mergerAppIDFile: /etc/agent-factory-github/app_id, mergerKeyFile: /etc/agent-factory-github/private_key", 1)
	if raw == demoGood {
		t.Fatal("the edit changed nothing")
	}
	// Folded, the merger's login is the factory App's (D12, plan PR9).
	raw = strings.Replace(raw, "mergerLogin: ogenki-agent-merger[bot]", "mergerLogin: ogenki-agent-factory[bot]", 1)
	c, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if c.GitHub.MergerAppIDFile != c.GitHub.AppIDFile || c.GitHub.MergerKeyFile != c.GitHub.PrivateKeyFile {
		t.Errorf("merger files %+v, want the factory App's", c.GitHub)
	}
	if !c.NeedsMerger() {
		t.Error("a shadow class needs the merger")
	}
}

// Without policy-bot, allowedFiles is what keeps a gate path from merging on a live class.
func TestDemoPolicyBotFalseNeedsAllowedFilesOnLiveClasses(t *testing.T) {
	const want = "merge.policyBot: false needs allowedFiles on every live class: without policy-bot, nothing else keeps a gate path from merging"
	for name, c := range map[string]struct{ class, err string }{
		"live without allowedFiles": {"docs: {live: true}", want},
		"live with allowedFiles":    {"docs: {live: true, allowedFiles: ['^docs/.*\\.md$']}", ""},
		"shadow without":            {"docs: {shadow: true}", ""},
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(demoGood, "classes: {docs-links: {shadow: true}, revert: {shadow: true}, docs: {}", "classes: {docs-links: {shadow: true}, revert: {shadow: true}, "+c.class, 1)
			if raw == demoGood {
				t.Fatal("the edit changed nothing")
			}
			_, err := Parse([]byte(raw))
			switch {
			case c.err == "" && err != nil:
				t.Fatal(err)
			case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
				t.Fatalf("got %v, want %q", err, c.err)
			}
		})
	}
}
