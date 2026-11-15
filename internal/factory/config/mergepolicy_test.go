// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
)

func policyProblems(c *Config) string {
	var out []string
	c.validateMergePolicy(func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) })
	return strings.Join(out, "\n")
}

// A harnessProfiles key and a scope's profile are one name: envelope copies the pattern, as it sits
// below config, so the two readers are held to the same boundaries here.
func TestAProfileNameIsTheScopes(t *testing.T) {
	scope := func(profile string) error {
		s := envelope.Scope{State: envelope.ScopeReady, Task: "Fix the link.", Touches: []string{"docs/guide.md"}, Plan: []string{"Fix it."},
			Acceptance: []envelope.Acceptance{{Check: "task docs", Proves: "the site builds"}}, Profile: profile}
		return s.Validate()
	}
	for name, ok := range map[string]bool{
		"a": true, "a1": true, "review-floor": true, "0-a": true,
		"a" + strings.Repeat("b", 38) + "c": true,
		"a" + strings.Repeat("b", 39) + "c": false,
		"":                                  false, "-a": false, "a-": false, "A": false, "a_b": false, "a.b": false, "a b": false, "é": false,
	} {
		if profileRE.MatchString(name) != ok {
			t.Errorf("config %q: %v, want %v", name, !ok, ok)
		}
		if (scope(name) == nil) != ok && name != "" { // an empty profile is no profile on a scope
			t.Errorf("envelope %q: %v, want %v", name, !ok, ok)
		}
	}
}

func TestHarnessProfilesAndGuardrailsValidate(t *testing.T) {
	loops := map[string]Loop{"docs": {Judge: NoJudge}}
	for name, tc := range map[string]struct {
		cfg  Config
		want string
	}{
		"none is fine":     {Config{}, ""},
		"a plain profile":  {Config{HarnessProfiles: map[string]HarnessProfile{"plain": {}}}, ""},
		"a full profile":   {Config{Loops: loops, HarnessProfiles: map[string]HarnessProfile{"p": {Template: "solo", Tier: "light", Checks: true, Loop: "docs"}}}, ""},
		"adopted":          {Config{HarnessProfiles: map[string]HarnessProfile{"a": {Template: TemplateNone}}}, ""},
		"bad name":         {Config{HarnessProfiles: map[string]HarnessProfile{"Not_A_Name": {}}}, "is not a workflow name"},
		"bad template":     {Config{HarnessProfiles: map[string]HarnessProfile{"p": {Template: "duo"}}}, "template"},
		"bad tier":         {Config{HarnessProfiles: map[string]HarnessProfile{"p": {Tier: "huge"}}}, "tier"},
		"unknown loop":     {Config{HarnessProfiles: map[string]HarnessProfile{"p": {Loop: "nope"}}}, "loop"},
		"escalation":       {Config{HarnessProfiles: map[string]HarnessProfile{"p": {Escalation: &Escalation{NotConverged: "q"}}, "q": {}}}, ""},
		"bad escalation":   {Config{HarnessProfiles: map[string]HarnessProfile{"p": {Escalation: &Escalation{OutOfBudget: "q"}}}}, `escalation outOfBudget "q" is not a workflow`},
		"none escalates":   {Config{HarnessProfiles: map[string]HarnessProfile{"p": {Template: TemplateNone, Escalation: &Escalation{NotConverged: "q"}}, "q": {}}}, "runs nothing"},
		"none with checks": {Config{HarnessProfiles: map[string]HarnessProfile{"p": {Template: TemplateNone, Checks: true}}}, "runs nothing"},
		"negative cap":     {Config{MergeGuardrails: MergeGuardrails{MaxPerDay: -1}}, "maxPerDay"},
		"bad protected":    {Config{MergeGuardrails: MergeGuardrails{ProtectedFiles: []string{"("}}}, "protectedFiles"},
		"blank check":      {Config{MergeGuardrails: MergeGuardrails{MandatoryChecks: []string{""}}}, "mandatoryChecks"},
		"stage simulate":   {Config{PolicyManagement: PolicyManagement{Stage: StageSimulate}}, ""},
		"stage read":       {Config{PolicyManagement: PolicyManagement{Stage: StageRead}}, ""},
		"stage manage":     {Config{PolicyManagement: PolicyManagement{Stage: StageManage}}, ""},
		"an operator":      {Config{PolicyManagement: PolicyManagement{Operators: []string{"Smana"}}}, ""},
		"a bad operator":   {Config{PolicyManagement: PolicyManagement{Operators: []string{"not a login"}}}, "operators"},
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

// IsOperator is case-insensitive, as GitHub logins are.
func TestIsOperator(t *testing.T) {
	pm := PolicyManagement{Operators: []string{"Smana"}}
	if !pm.IsOperator("smana") || pm.IsOperator("") || pm.IsOperator("other") {
		t.Fatal(pm)
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

// MR7: guardrails ship with defaults: a zero cap takes its default, a set one stays, and the
// default protected paths come first, then the platform's, once each.
func TestGuardrailDefaults(t *testing.T) {
	got := MergeGuardrails{}.WithDefaults()
	if !slices.Equal(got.ProtectedFiles, []string{`^\.github/`, `^\.policy\.yml$`, `^CODEOWNERS$`}) ||
		got.MaxPatchFiles != 10 || got.MaxModifiedLines != 300 || got.MaxPerDay != 5 {
		t.Fatalf("defaults %+v", got)
	}
	in := MergeGuardrails{MaxPatchFiles: 4, ProtectedFiles: []string{`^\.github/`, `^infra/`}, MandatoryChecks: []string{"scan"}}
	got = in.WithDefaults()
	if got.MaxPatchFiles != 4 || got.MaxModifiedLines != 300 || !slices.Equal(got.MandatoryChecks, []string{"scan"}) ||
		!slices.Equal(got.ProtectedFiles, []string{`^\.github/`, `^\.policy\.yml$`, `^CODEOWNERS$`, `^infra/`}) {
		t.Fatalf("with a platform's %+v", got)
	}
	if !slices.Equal(in.ProtectedFiles, []string{`^\.github/`, `^infra/`}) {
		t.Fatal("WithDefaults changed its receiver's list")
	}
}
