// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

const portable = "runs: {group: maku.ogenki.io}\n"

// PR3: runTokens writes the run-controller's git Secrets from the agents App (github.agents*), and
// only for the run-controller's AgentRun.
func TestRunTokens(t *testing.T) {
	const on = "runTokens: {enabled: true, perRun: true}\n"
	app := withAgentsApp(good, agentsID, agentsKey)
	c, err := Parse([]byte(app + portable + on))
	if err != nil {
		t.Fatal(err)
	}
	if id, key, ok := c.RunTokenApp(); !ok || id != agentsID || key != agentsKey {
		t.Fatalf("RunTokenApp = %q %q %v", id, key, ok)
	}
	for name, tc := range map[string]struct{ body, want string }{
		"no files": {good + portable + on, "runTokens.enabled needs github.agentsAppIDFile and github.agentsKeyFile"},
		// v4 step 8: the shared agent-git writer is removed, so enabled alone would write nothing.
		"no perRun":   {app + portable + "runTokens: {enabled: true}\n", "runTokens.enabled needs runTokens.perRun: the shared agent-git writer was removed in v4 step 8"},
		"the XR":      {app + "runs: {group: cloud.ogenki.io}\n" + on, "runTokens.enabled writes the run-controller's Secrets: runs.group must be maku.ogenki.io"},
		"the default": {app + on, "runTokens.enabled writes the run-controller's Secrets: runs.group must be maku.ogenki.io"},
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
		t.Fatal("no runTokens, no demo: the factory writes no run token (octo-sts or the operator does)")
	}
}

// v4 D10: runTokens.perRun mints one token per run. It needs the agents App (runTokens.enabled, or
// the demo's factory App), the run-controller's runs, and, outside the demo, two Apps (D12, PR9).
func TestRunTokensPerRun(t *testing.T) {
	const on = "runTokens: {enabled: true, perRun: true}\n"
	app := withAgentsApp(good, "/a/app_id", "/a/private_key")
	c, err := Parse([]byte(app + portable + on))
	if err != nil || !c.RunTokens.PerRun {
		t.Fatalf("%v", err)
	}
	for name, tc := range map[string]struct{ body, want string }{
		"without enabled": {good + portable + "runTokens: {perRun: true}\n", "runTokens.perRun needs runTokens.enabled"},
		"on the XR":       {app + "runs: {group: cloud.ogenki.io}\n" + on, "runs.group must be maku.ogenki.io"},
		"one App": {strings.Replace(app, "agentsLogin: ogenki-agents[bot]", "agentsLogin: ogenki-agent-factory[bot]", 1) + portable + on,
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
	c, err = Parse([]byte(withAgentsApp(demoGood, "/x/app_id", "/x/private_key") + "runs: {group: maku.ogenki.io}\nrunTokens: {enabled: true, perRun: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if id, _, ok := c.RunTokenApp(); !ok || id != "/x/app_id" {
		t.Fatalf("demo with runTokens: %q %v", id, ok)
	}
}
