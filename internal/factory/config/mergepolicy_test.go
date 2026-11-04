// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func policyProblems(c *Config) string {
	var out []string
	c.validateMergePolicy(func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) })
	return strings.Join(out, "\n")
}

func TestHarnessProfilesAndGuardrailsValidate(t *testing.T) {
	loops := map[string]Loop{"docs": {Judge: NoJudge}}
	for name, tc := range map[string]struct {
		cfg  Config
		want string
	}{
		"none is fine":     {Config{}, ""},
		"a plain profile":  {Config{HarnessProfiles: map[string]HarnessProfile{"plain": {}}}, ""},
		"a full profile":   {Config{Loops: loops, HarnessProfiles: map[string]HarnessProfile{"p": {Template: "solo", Tier: "light", Checks: true, TestFirst: true, Loop: "docs"}}}, ""},
		"adopted":          {Config{HarnessProfiles: map[string]HarnessProfile{"a": {Template: TemplateNone}}}, ""},
		"bad name":         {Config{HarnessProfiles: map[string]HarnessProfile{"Not_A_Name": {}}}, "is not a profile name"},
		"bad template":     {Config{HarnessProfiles: map[string]HarnessProfile{"p": {Template: "duo"}}}, "template"},
		"bad tier":         {Config{HarnessProfiles: map[string]HarnessProfile{"p": {Tier: "huge"}}}, "tier"},
		"unknown loop":     {Config{HarnessProfiles: map[string]HarnessProfile{"p": {Loop: "nope"}}}, "loop"},
		"testFirst alone":  {Config{HarnessProfiles: map[string]HarnessProfile{"p": {TestFirst: true}}}, "testFirst needs checks"},
		"none with checks": {Config{HarnessProfiles: map[string]HarnessProfile{"p": {Template: TemplateNone, Checks: true}}}, "runs nothing"},
		"negative cap":     {Config{MergeGuardrails: MergeGuardrails{MaxPerDay: -1}}, "maxPerDay"},
		"bad protected":    {Config{MergeGuardrails: MergeGuardrails{ProtectedFiles: []string{"("}}}, "protectedFiles"},
		"blank check":      {Config{MergeGuardrails: MergeGuardrails{MandatoryChecks: []string{""}}}, "mandatoryChecks"},
	} {
		t.Run(name, func(t *testing.T) {
			got := policyProblems(&tc.cfg)
			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A bare off is YAML's false (ScopeMode's reason): the profile must still read it.
func TestLoopRefReadsFalseAsOff(t *testing.T) {
	var p HarnessProfile
	if err := json.Unmarshal([]byte(`{"loop": false}`), &p); err != nil || p.Loop != LoopOff {
		t.Fatalf("%q %v", p.Loop, err)
	}
}

func TestDurationMarshalsAsItsString(t *testing.T) {
	b, err := json.Marshal(Monitor{FollowUpWindow: Duration{2 * time.Hour}})
	if err != nil || !strings.Contains(string(b), `"followUpWindow":"2h0m0s"`) {
		t.Fatalf("%s %v", b, err)
	}
	var m Monitor
	if err := json.Unmarshal(b, &m); err != nil || m.FollowUpWindow.Duration != 2*time.Hour {
		t.Fatalf("%v %v", m, err)
	}
}
