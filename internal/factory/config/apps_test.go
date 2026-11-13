// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// withAgentsApp names the agents App in good's github block (D12, plan PR9).
func withAgentsApp(body, id, key string) string {
	const at = "github: {appIDFile: /etc/agent-factory-github/app_id, "
	if !strings.Contains(body, at) {
		panic("withAgentsApp: no " + at)
	}
	return strings.Replace(body, at, "github: {agentsAppIDFile: "+id+", agentsKeyFile: "+key+", appIDFile: /etc/agent-factory-github/app_id, ", 1)
}

const agentsID, agentsKey = "/etc/agent-factory-agents/app_id", "/etc/agent-factory-agents/private_key"

// D12: github.agentsAppIDFile and agentsKeyFile name the agents App. runTokens' own pair stays an
// alias; RunTokenApp resolves the new pair over the old, and two different Apps are refused.
func TestAgentsAppNamesTheRunTokenApp(t *testing.T) {
	for name, body := range map[string]string{
		"the agents pair alone":     withAgentsApp(good, agentsID, agentsKey) + portable + "runTokens: {enabled: true}\n",
		"the same App under both":   withAgentsApp(good, agentsID, agentsKey) + portable + "runTokens: {enabled: true, appIDFile: " + agentsID + ", privateKeyFile: " + agentsKey + "}\n",
		"per run, the agents alone": withAgentsApp(good, agentsID, agentsKey) + portable + "runTokens: {enabled: true, perRun: true}\n",
	} {
		c, err := Parse([]byte(body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if gotID, gotKey, ok := c.RunTokenApp(); !ok || gotID != agentsID || gotKey != agentsKey {
			t.Errorf("%s: RunTokenApp = %q %q %v", name, gotID, gotKey, ok)
		}
	}
	// Naming the agents App alone writes no run token: runTokens.enabled still decides.
	c, err := Parse([]byte(withAgentsApp(good, agentsID, agentsKey)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.RunTokenApp(); ok {
		t.Error("the agents App named without runTokens.enabled: no writer")
	}
}

func TestAgentsAppRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"different Apps": {withAgentsApp(good, agentsID, agentsKey) + portable + "runTokens: {enabled: true, appIDFile: /x/app_id, privateKeyFile: /x/private_key}\n",
			"github.agentsAppIDFile and runTokens.appIDFile name different Apps"},
		"a different key": {withAgentsApp(good, agentsID, agentsKey) + portable + "runTokens: {enabled: true, appIDFile: " + agentsID + ", privateKeyFile: /x/private_key}\n",
			"github.agentsKeyFile and runTokens.privateKeyFile name different keys"},
		"half a pair": {withAgentsApp(good, agentsID, "''"),
			"github.agentsAppIDFile and github.agentsKeyFile are both set or both empty"},
		"no App at all": {good + portable + "runTokens: {enabled: true}\n",
			"runTokens.enabled needs github.agentsAppIDFile and github.agentsKeyFile (or runTokens.appIDFile and runTokens.privateKeyFile)"},
		"per run, the agents App is the factory's": {withAgentsApp(good, "/etc/agent-factory-github/app_id", "/etc/agent-factory-github/private_key") + portable + "runTokens: {enabled: true, perRun: true}\n",
			"runTokens.perRun: the agents App is the factory App; D12 needs two"},
	} {
		if _, err := Parse([]byte(tc.body)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

// D12, plan PR9: a merger that signs with the factory App's key is the factory App, so every
// repository's mergerLogin must be factoryLogin, or the gate reads its own merges as a human's.
func TestMergerFoldedIntoTheFactoryApp(t *testing.T) {
	const own = "mergerAppIDFile: /etc/agent-factory-merger/app_id, mergerKeyFile: /etc/agent-factory-merger/private_key"
	folded := strings.Replace(good, own, "mergerAppIDFile: /etc/agent-factory-github/app_id, mergerKeyFile: /etc/agent-factory-github/private_key", 1)
	if folded == good {
		t.Fatal("the edit changed nothing")
	}
	const want = "merge.mergerLogin must be factoryLogin when the merger signs with the factory App"
	if _, err := Parse([]byte(folded)); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("folded with mergerLogin ogenki-agent-merger[bot]: %v, want %q", err, want)
	}
	c, err := Parse([]byte(strings.Replace(folded, "mergerLogin: ogenki-agent-merger[bot]", "mergerLogin: ogenki-agent-factory[bot]", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if !c.MergerFolded() {
		t.Error("MergerFolded: the merger names the factory App's files")
	}
	// Two Apps: the merger's own login stays its own.
	if c, err := Parse([]byte(good)); err != nil || c.MergerFolded() {
		t.Fatalf("separate merger: %v", err)
	}
}
