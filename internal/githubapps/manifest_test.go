// SPDX-License-Identifier: Apache-2.0

package githubapps

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

const testWeb = "https://factory.example.com"

// decoded is a manifest as GitHub reads it: its JSON, not the Go value.
func decoded(t *testing.T, m AppManifest) map[string]any {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func permsOf(t *testing.T, m map[string]any) Permissions {
	t.Helper()
	raw, _ := m["default_permissions"].(map[string]any)
	p := Permissions{}
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("permission %s = %v", k, v)
		}
		p[k] = s
	}
	return p
}

func TestAgentsManifestIsTheAgentsSet(t *testing.T) {
	m, err := Manifest(AgentsApp, "acme-maku-agents", ManifestOptions{WebURL: testWeb})
	if err != nil {
		t.Fatal(err)
	}
	got := decoded(t, m)
	if p := permsOf(t, got); !maps.Equal(p, Agents()) {
		t.Fatalf("agents permissions = %v, want %v", p, Agents())
	}
	// The role table's invariants, read from what GitHub would be sent.
	for _, k := range []string{"statuses", "workflows", "administration"} {
		if _, ok := permsOf(t, got)[k]; ok {
			t.Errorf("the agents manifest asks for %s", k)
		}
	}
	if _, ok := got["callback_urls"]; ok {
		t.Errorf("the agents manifest has callback_urls: %v", got["callback_urls"])
	}
}

func TestFactoryManifestIsTheFoldedFactorySet(t *testing.T) {
	for name, o := range map[string]MergerOptions{
		"no option":     {},
		"status":        {Status: true},
		"status labels": {Status: true, Labels: true},
		"every option":  {Status: true, Labels: true, Reruns: true},
		"folded given":  {Folded: true, Reruns: true},
	} {
		m, err := Manifest(FactoryApp, "acme-maku-factory", ManifestOptions{WebURL: testWeb, Merger: o})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		folded := o
		folded.Folded = true // the install never makes a third App (D12)
		if p := permsOf(t, decoded(t, m)); !maps.Equal(p, Factory(folded)) {
			t.Errorf("%s: factory permissions = %v, want %v", name, p, Factory(folded))
		}
		for _, k := range []string{"workflows", "administration"} {
			if _, ok := permsOf(t, decoded(t, m))[k]; ok {
				t.Errorf("%s: the factory manifest asks for %s", name, k)
			}
		}
	}
}

func TestManifestsHaveNoWebhookAndAreNotPublic(t *testing.T) {
	for _, kind := range []Kind{AgentsApp, FactoryApp} {
		m, err := Manifest(kind, "acme-"+string(kind), ManifestOptions{WebURL: testWeb})
		if err != nil {
			t.Fatal(err)
		}
		got := decoded(t, m)
		hook, _ := got["hook_attributes"].(map[string]any)
		if hook["active"] != false {
			t.Errorf("%s: hook_attributes.active = %v", kind, hook["active"])
		}
		if ev, ok := got["default_events"].([]any); !ok || len(ev) != 0 {
			t.Errorf("%s: default_events = %v", kind, got["default_events"])
		}
		if got["public"] != false {
			t.Errorf("%s: public = %v", kind, got["public"])
		}
		if v, ok := got["request_oauth_on_install"]; !ok || v != false {
			t.Errorf("%s: request_oauth_on_install = %v, %v", kind, v, ok)
		}
		if got["name"] != "acme-"+string(kind) || got["url"] != HomepageURL || hook["url"] != HomepageURL {
			t.Errorf("%s: name, url = %v, %v, %v", kind, got["name"], got["url"], hook["url"])
		}
	}
}

// R5: the account link uses the factory App's OAuth client, so only that App has the callback.
func TestCallbackURLsOnTheFactoryAppOnly(t *testing.T) {
	for web, want := range map[string]string{
		testWeb:                   testWeb + "/link/github/callback",
		testWeb + "/":             testWeb + "/link/github/callback",
		"https://web.example/mk/": "https://web.example/mk/link/github/callback",
	} {
		m, err := Manifest(FactoryApp, "acme-maku-factory", ManifestOptions{WebURL: web})
		if err != nil {
			t.Fatal(err)
		}
		cb, _ := decoded(t, m)["callback_urls"].([]any)
		if len(cb) != 1 || cb[0] != want {
			t.Errorf("WebURL %q: callback_urls = %v, want [%s]", web, cb, want)
		}
	}
}

func TestManifestRefusesBadInput(t *testing.T) {
	for name, tc := range map[string]struct {
		kind Kind
		app  string
		o    ManifestOptions
		want string
	}{
		"no name":          {FactoryApp, "", ManifestOptions{WebURL: testWeb}, "name"},
		"a long name":      {AgentsApp, strings.Repeat("a", 35), ManifestOptions{}, "name"},
		"unknown kind":     {Kind("merger"), "x", ManifestOptions{WebURL: testWeb}, "kind"},
		"factory, no web":  {FactoryApp, "x", ManifestOptions{}, "web URL"},
		"a relative web":   {FactoryApp, "x", ManifestOptions{WebURL: "factory.example.com"}, "web URL"},
		"a web with query": {FactoryApp, "x", ManifestOptions{WebURL: testWeb + "?a=b"}, "web URL"},
		"a plain-http web": {FactoryApp, "x", ManifestOptions{WebURL: "http://factory.example.com"}, "web URL"},
		"a bad homepage":   {AgentsApp, "x", ManifestOptions{Homepage: "ftp://x"}, "homepage"},
	} {
		_, err := Manifest(tc.kind, tc.app, tc.o)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, tc.want)
		}
	}
}
