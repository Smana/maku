// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

const portable = "runs: {group: maku.ogenki.io}\n"

// PR3: runTokens writes the run-controller's git Secrets from the App it names: both files. The
// default runs group is the run-controller's (v4 step 8, D5), so it needs none.
func TestRunTokens(t *testing.T) {
	const on = "runTokens: {enabled: true, appIDFile: /etc/agent-factory-run-tokens/app_id, privateKeyFile: /etc/agent-factory-run-tokens/private_key}\n"
	c, err := Parse([]byte(good + portable + on))
	if err != nil {
		t.Fatal(err)
	}
	if id, key, ok := c.RunTokenApp(); !ok || id != "/etc/agent-factory-run-tokens/app_id" || key != "/etc/agent-factory-run-tokens/private_key" {
		t.Fatalf("RunTokenApp = %q %q %v", id, key, ok)
	}
	for name, tc := range map[string]struct{ body, want string }{
		"no key file": {good + portable + "runTokens: {enabled: true, appIDFile: /a}\n", "(or runTokens.appIDFile and runTokens.privateKeyFile)"},
		"no files":    {good + portable + "runTokens: {enabled: true}\n", "(or runTokens.appIDFile and runTokens.privateKeyFile)"},
		"the XR":      {good + "runs: {group: cloud.ogenki.io}\n" + on, "removed in v4 step 8 (D5)"},
	} {
		if _, err := Parse([]byte(tc.body)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	off, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := off.RunTokenApp(); ok {
		t.Fatal("no runTokens, no demo: the factory writes no run token (the operator does)")
	}
	if _, err := Parse([]byte(good + on)); err != nil {
		t.Fatalf("the default group: %v", err)
	}
}

// v4 D10: runTokens.perRun mints one token per run. It needs the agents App (runTokens.enabled, or
// the demo's factory App), the run-controller's runs, and, outside the demo, two Apps (D12, PR9).
func TestRunTokensPerRun(t *testing.T) {
	const on = "runTokens: {enabled: true, perRun: true, appIDFile: /a/app_id, privateKeyFile: /a/private_key}\n"
	c, err := Parse([]byte(good + portable + on))
	if err != nil || !c.RunTokens.PerRun {
		t.Fatalf("%v", err)
	}
	for name, tc := range map[string]struct{ body, want string }{
		"without enabled": {good + portable + "runTokens: {perRun: true}\n", "runTokens.perRun needs runTokens.enabled"},
		"on the XR":       {good + "runs: {group: cloud.ogenki.io}\n" + on, "removed in v4 step 8 (D5)"},
		"one App": {strings.Replace(good, "agentsLogin: ogenki-agents[bot]", "agentsLogin: ogenki-agent-factory[bot]", 1) + portable + on,
			"runTokens.perRun: agentsLogin and factoryLogin name one App; D12 needs two"},
	} {
		if _, err := Parse([]byte(tc.body)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	// The demo serves one App for both: perRun mints from the factory's own.
	d, err := Parse([]byte(demoGood + "runTokens: {perRun: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if id, _, ok := d.RunTokenApp(); !ok || !d.RunTokens.PerRun || id != d.GitHub.AppIDFile {
		t.Fatalf("demo perRun: %q %v", id, ok)
	}
}

// The demo implies the writer, from the factory's own App unless runTokens names another.
func TestRunTokensInTheDemo(t *testing.T) {
	c, err := Parse([]byte(demoGood))
	if err != nil {
		t.Fatal(err)
	}
	if id, key, ok := c.RunTokenApp(); !ok || id != c.GitHub.AppIDFile || key != c.GitHub.PrivateKeyFile {
		t.Fatalf("demo RunTokenApp = %q %q %v", id, key, ok)
	}
	c, err = Parse([]byte(demoGood + "runTokens: {enabled: true, appIDFile: /x/app_id, privateKeyFile: /x/private_key}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if id, _, ok := c.RunTokenApp(); !ok || id != "/x/app_id" {
		t.Fatalf("demo with runTokens: %q %v", id, ok)
	}
}
