// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

const portable = "runs: {group: maku.ogenki.io}\n"

// PR3: runTokens writes the run-controller's git Secrets from the App it names: both files, and
// only for the run-controller's AgentRun (cloud-native-ref's gets its tokens from octo-sts).
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
		"no key file": {good + portable + "runTokens: {enabled: true, appIDFile: /a}\n", "runTokens.enabled needs runTokens.appIDFile and runTokens.privateKeyFile"},
		"no files":    {good + portable + "runTokens: {enabled: true}\n", "runTokens.enabled needs runTokens.appIDFile and runTokens.privateKeyFile"},
		"the XR":      {good + "runs: {group: cloud.ogenki.io}\n" + on, "runTokens.enabled writes the run-controller's Secrets: runs.group must be maku.ogenki.io"},
		"the default": {good + on, "runTokens.enabled writes the run-controller's Secrets: runs.group must be maku.ogenki.io"},
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
