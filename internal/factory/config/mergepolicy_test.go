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
		"simulation alone": {Config{PolicyManagement: PolicyManagement{Simulation: true}}, ""},
		"dual with read":   {Config{PolicyManagement: PolicyManagement{Read: true, DualEvaluation: true}}, ""},
		"dual alone":       {Config{PolicyManagement: PolicyManagement{DualEvaluation: true}}, "dualEvaluation needs read"},
		"memory with read": {Config{PolicyManagement: PolicyManagement{Read: true, ApprovalMemory: true}}, ""},
		"memory alone":     {Config{PolicyManagement: PolicyManagement{ApprovalMemory: true}}, "approvalMemory needs read"},
		"a year of grant":  {Config{MergeGuardrails: MergeGuardrails{MaxGrantLifetime: Duration{MaxGrantLifetime}}}, ""},
		"negative grant":   {Config{MergeGuardrails: MergeGuardrails{MaxGrantLifetime: Duration{-time.Hour}}}, "maxGrantLifetime"},
		"too long a grant": {Config{MergeGuardrails: MergeGuardrails{MaxGrantLifetime: Duration{MaxGrantLifetime + time.Hour}}}, "maxGrantLifetime"},
	} {
		t.Run(name, func(t *testing.T) {
			got := policyProblems(&tc.cfg)
			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A grant lives 90 days unless the operator caps it lower (merge policy stage 5).
func TestGrantLifetimeDefaultsToNinetyDays(t *testing.T) {
	if got := (MergeGuardrails{}).GrantLifetime(); got != 90*24*time.Hour {
		t.Fatal(got)
	}
	if got := (MergeGuardrails{MaxGrantLifetime: Duration{time.Hour}}).GrantLifetime(); got != time.Hour {
		t.Fatal(got)
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
