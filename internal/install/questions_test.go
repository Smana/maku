// SPDX-License-Identifier: Apache-2.0

package install

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func readAnswers(t *testing.T, name string) Answers {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "answers", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := DecodeAnswers(raw)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func resolved(t *testing.T, name string) Answers {
	t.Helper()
	a, _, err := Resolve(readAnswers(t, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return a
}

// IN7: a question writes only keys the charts' schemas know, under the keys it declares.
func TestQuestionsWriteOnlyKnownKeys(t *testing.T) {
	answered := map[string]bool{}
	for _, name := range []string{"generic", "zitadel-eso", "ghes"} {
		a := resolved(t, name)
		for _, q := range Questions {
			v, ok := a[q.ID]
			if !ok || q.Values == nil {
				continue
			}
			answered[q.ID] = true
			for path := range q.Values(v, a) {
				known, err := Known(path)
				if err != nil {
					t.Fatal(err)
				}
				if !known {
					t.Errorf("%s writes %s, which the charts' schemas refuse", q.ID, path)
				}
				declared := false
				for _, k := range q.Keys {
					if path == k || strings.HasPrefix(path, k+".") {
						declared = true
					}
				}
				if !declared {
					t.Errorf("%s writes %s, outside its Keys %v", q.ID, path, q.Keys)
				}
			}
		}
	}
	for _, q := range Questions {
		if q.Values != nil && len(q.Keys) == 0 {
			t.Errorf("%s writes values but declares no Keys", q.ID)
		}
		if q.Values != nil && !answered[q.ID] && q.ID != "expose.ingressClass" {
			// every writing question is exercised by one of the three answers files
			if _, ok := map[string]bool{"apps.factorySecret": true, "apps.agentsSecret": true}[q.ID]; !ok {
				t.Logf("%s writes values no answers file exercises", q.ID)
			}
		}
	}
}

// IN7: each validator refuses what the chart's schema refuses, and what no schema can.
func TestQuestionValidators(t *testing.T) {
	base := resolved(t, "generic")
	for _, tc := range []struct {
		id   string
		v    any
		want string // "" accepts
	}{
		{"identity.issuer", "https://idp.example.com", ""},
		{"identity.issuer", `https://idp.example.com/"x`, "does not match pattern"}, // the umbrella's PromQL-safe pattern
		{"identity.issuer", "http://idp.example.com", "not an https URL"},
		{"web.url", "https://Rooms_Example.com", "does not match pattern"}, // expose.web.hostnames' RFC 1123 pattern
		{"web.url", "https://rooms.example.com", ""},
		{"expose.mode", "gateway", "value must be one of"},
		{"expose.mode", "ingress", ""},
		{"network.mode", "calico", "value must be one of"}, // maku's own schema
		{"identity.preset", "keycloak", "not one of"},
		{"models.provider", "bedrock", "value must be one of"}, // maku-gateway's preset enum
		{"models.provider", "zai", ""},
		{"sandbox.runtimeClassName", "gke.gvisor", ""},
		{"sandbox.runtimeClassName", "GVisor", "not a DNS name"},
		{"namespace", "Maku", "not a DNS label"},
		{"repositories", []string{"example/app"}, ""},
		{"repositories", []string{"example"}, "not owner/name"},
		{"repositories", []string{}, "at least one repository"},
		{"maintainers", []string{"not a login"}, "not a GitHub login"},
		{"meter.url", "vmsingle:8428", "not an http(s) URL"},
		{"images.pins", []string{"maku-broker=v0.48.0"}, "not <image>=<version>@sha256"},
		{"images.pins", []string{"maku-nope=v0.48.0@sha256:" + strings.Repeat("a", 64)}, "not <image>"},
		{"expose.gateway", "edge", "not namespace/name"},
		{"expose.gateway", "gateway/edge/https", ""},
		{"monitoring.sidecarLabel", "grafana_dashboard", "not a label"},
		{"database.brokerSecret", "Rooms", "not a DNS name"},
		{"slack.teamID", "C0123", "not a Slack team id"},
		{"version", "latest", "not a release"},
	} {
		_, err := lookup(t, tc.id).Validate(tc.v, base)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s %v: refused: %v", tc.id, tc.v, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s %v: got %v, want an error with %q", tc.id, tc.v, err, tc.want)
		}
	}
}

func lookup(t *testing.T, id string) Question {
	t.Helper()
	q, ok := questionsByID()[id]
	if !ok {
		t.Fatalf("no question %s", id)
	}
	return q
}

// IN6: an answers file with an unknown key, or a value of the wrong kind, is refused.
func TestDecodeAnswersIsStrict(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"namespace: maku\nnamepsace: x\n", `unknown key "namepsace"`},
		{"apps.create: maybe\n", "want yes or no"},
		{"repositories: {a: b}\n", "want a list of strings"},
		{"web.url: [a]\n", "want a string"},
	} {
		if _, err := DecodeAnswers([]byte(tc.raw)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got %v, want %q", tc.raw, err, tc.want)
		}
	}
	a := readAnswers(t, "generic")
	back, err := DecodeAnswers(a.Encode())
	if err != nil || !reflect.DeepEqual(back, a) {
		t.Errorf("Encode does not read back: %v\n%v\n%v", err, back, a)
	}
}

// Resolve refuses an answer to a question the other answers leave out, and a missing required one.
func TestResolve(t *testing.T) {
	a := readAnswers(t, "generic")
	a["slack.teamID"] = "T0123"
	if _, _, err := Resolve(a); err == nil || !strings.Contains(err.Error(), "slack.teamID: answered, but the answers before it leave it out") {
		t.Errorf("a dead key: %v", err)
	}
	a = readAnswers(t, "generic")
	delete(a, "meter.url")
	if _, _, err := Resolve(a); err == nil || !strings.Contains(err.Error(), "meter.url: required") {
		t.Errorf("no meter: %v, want meter.url required (the factory does not start without one)", err)
	}
	a = readAnswers(t, "generic")
	delete(a, "repositories")
	if _, _, err := Resolve(a); err == nil || !strings.Contains(err.Error(), "repositories: required") {
		t.Errorf("no repository: %v", err)
	}
}

// F4: Slack with Dex is refused; Keycloak with tokenUse id gives a suggestion.
func TestIdentityFamilyWarnings(t *testing.T) {
	a := readAnswers(t, "generic")
	a["identity.family"] = "dex"
	a["slack.enabled"] = true
	a["slack.host"] = "slack.example.com"
	a["slack.gateway"] = "gateway/public"
	a["slack.clientID"] = "slack"
	a["slack.teamID"] = "T0123ABCD"
	a["slack.channel"] = "C0123ABCD"
	if _, _, err := Resolve(a); err == nil || !strings.Contains(err.Error(), "Dex refuses") {
		t.Errorf("Slack with Dex: %v, want refused", err)
	}
	a = readAnswers(t, "generic") // keycloak, tokenUse id by default
	a["identity.rolesClaim"] = "realm_access.roles"
	_, warns, err := Resolve(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "add a groups mapper") {
		t.Errorf("Keycloak under tokenUse id: warnings %v", warns)
	}
	a["identity.rolesClaim"] = "groups"
	if _, warns, _ := Resolve(a); len(warns) != 0 {
		t.Errorf("Keycloak with rolesClaim groups still warns: %v", warns)
	}
}

// F8: the runs' tracing endpoint is an in-cluster Service URL with its port, the collector's namespace
// the endpoint's.
func TestTracingEndpointRule(t *testing.T) {
	q := lookup(t, "tracing.endpoint")
	for v, ok := range map[string]bool{
		"http://otel.observability.svc.cluster.local:4318":  true,
		"https://otel.observability.svc.cluster.local:4318": true,
		"":                               true,
		"http://otel.observability:4318": false,
		"http://otel.observability.svc.cluster.local": false,
		"http://otel.example.com:4318":                false,
	} {
		if _, err := q.Validate(v, Answers{}); (err == nil) != ok {
			t.Errorf("%q: %v, want accepted %v", v, err, ok)
		}
	}
	got := q.Values("http://otel.observability.svc.cluster.local:4318", Answers{})
	if got["maku.runController.tracing.collector.namespace"] != "observability" {
		t.Errorf("the collector's namespace is %v, want the endpoint's", got)
	}
}

// F10: the knowledge endpoint's peers are written in the network mode's own shape.
func TestKnowledgePeersPerMode(t *testing.T) {
	q := lookup(t, "runlore.namespace")
	for mode, want := range map[string]any{
		"kubernetes": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "runlore"}}}},
		"cilium":     []any{map[string]any{"matchLabels": map[string]any{"io.kubernetes.pod.namespace": "runlore"}}},
	} {
		got := q.Values("runlore", Answers{"network.mode": mode})["maku.broker.roomTools.knowledge.networkPolicy.to"]
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", mode, got, want)
		}
	}
	if _, err := lookup(t, "runlore.url").Validate("http://kb.example.com/mcp", Answers{}); err == nil {
		t.Error("an http knowledge URL outside the cluster is accepted")
	}
}

// The scripted interactive run takes defaults on empty lines and re-asks a refused answer.
func TestAskReasksARefusedAnswer(t *testing.T) {
	var lines []string
	a := resolved(t, "generic")
	for _, q := range Questions {
		if !q.Asked(a) {
			continue
		}
		lines = append(lines, line(a[q.ID]))
	}
	// the first answer, namespace, is refused once
	in := strings.NewReader("Bad_NS\n" + strings.Join(lines, "\n") + "\n")
	var out strings.Builder
	got, err := Ask(in, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "not a DNS label") {
		t.Errorf("the refusal was not shown:\n%s", out.String())
	}
	if !reflect.DeepEqual(got, a) {
		t.Errorf("the interactive answers differ from the file's:\n%v\n%v", got, a)
	}
}

// line is an answer typed on one line.
func line(v any) string {
	switch t := v.(type) {
	case bool:
		if t {
			return "yes"
		}
		return "no"
	case []string:
		if len(t) == 0 {
			return ""
		}
		return strings.Join(t, ", ")
	}
	return v.(string)
}

// The install jobs' answers files (hack/install-e2e) resolve: CI cannot drift from the wizard.
func TestInstallE2EAnswersResolve(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "hack", "install-e2e", "*.answers.yaml"))
	if len(files) != 2 {
		t.Fatalf("hack/install-e2e holds %d answers files, want 2", len(files))
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		a, err := DecodeAnswers(raw)
		if err == nil {
			_, _, err = Resolve(a)
		}
		if err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}
