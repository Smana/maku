// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"fmt"
	"reflect"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
)

func TestPolicyOf(t *testing.T) {
	task := func(class string) *v1alpha1.Task {
		return &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Repository: "o/r", PredictedClass: class}}
	}
	repo := func(approve bool, classes map[string]config.Class) *config.Config {
		return &config.Config{Repositories: []config.Repository{{Name: "o/r", Classes: classes,
			Merge: config.Merge{ApproveToMerge: approve}}}}
	}
	adopt := config.Class{Shadow: true, Author: &config.Author{Adopt: &config.Adopt{Logins: []string{"renovate[bot]"}, Branches: []string{"^renovate/"}}}}
	many := map[string]config.Class{}
	for i := range 20 {
		many[fmt.Sprintf("c%02d", i)] = config.Class{Live: true, AllowedFiles: []string{`^x`}}
	}
	merged := task("docs-links")
	merged.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 3, MergedHow: "approved"}

	for _, tc := range []struct {
		name  string
		cfg   *config.Config
		task  *v1alpha1.Task
		check func(t *testing.T, p *envelope.PolicyFact)
	}{
		{"a live class with approve-to-merge", repo(true, map[string]config.Class{"fix": {Live: true, MaxPatchFiles: 5, AllowedFiles: []string{`^src/`}}}), task("fix"),
			func(t *testing.T, p *envelope.PolicyFact) {
				want := envelope.PolicyFact{Class: "fix", Mode: envelope.PolicyLive, ApproveToMerge: true,
					Conditions: &envelope.PolicyConditions{AllowedFiles: []string{`^src/`}, MaxPatchFiles: 5},
					Classes:    []envelope.PolicyClass{{Name: "fix", Mode: envelope.PolicyLive}}}
				if !reflect.DeepEqual(*p, want) {
					t.Fatalf("policy = %+v, want %+v", *p, want)
				}
			}},
		{"a shadow class", repo(false, map[string]config.Class{"fix": {Shadow: true, AllowedFiles: []string{`^a`}}}), task("fix"),
			func(t *testing.T, p *envelope.PolicyFact) {
				if p.Mode != envelope.PolicyShadow || p.ApproveToMerge || p.GateOff {
					t.Fatalf("policy = %+v, want shadow, approve off, gate on", *p)
				}
			}},
		{"a class the repository does not declare is unarmed", repo(true, map[string]config.Class{"fix": {Live: true, AllowedFiles: []string{`^a`}}}), task("docs"),
			func(t *testing.T, p *envelope.PolicyFact) {
				if p.Class != "docs" || p.Mode != envelope.PolicyUnarmed || p.Conditions != nil {
					t.Fatalf("policy = %+v, want docs, unarmed, no conditions", *p)
				}
			}},
		{"an adopted class never merges on approval", repo(true, map[string]config.Class{"deps": adopt}), task("deps"),
			func(t *testing.T, p *envelope.PolicyFact) {
				if !p.Adopted || p.ApproveToMerge || !reflect.DeepEqual(p.Conditions.AdoptLogins, []string{"renovate[bot]"}) || !p.Classes[0].Adopted {
					t.Fatalf("policy = %+v, conditions %+v", *p, p.Conditions)
				}
			}},
		{"no live or shadow class: the gate is off", repo(true, map[string]config.Class{"docs": {}}), task("docs"),
			func(t *testing.T, p *envelope.PolicyFact) {
				if !p.GateOff || p.Mode != envelope.PolicyUnarmed || p.ApproveToMerge { // merge_gate_off hands off before any approval is read
					t.Fatalf("policy = %+v, want gate off, unarmed", *p)
				}
			}},
		{"docs-links reads its built-in shape", repo(false, map[string]config.Class{"docs-links": {Live: true}}), task("docs-links"),
			func(t *testing.T, p *envelope.PolicyFact) {
				if p.Conditions == nil || p.Conditions.DiffShape != "links-only" {
					t.Fatalf("conditions = %+v, want links-only", p.Conditions)
				}
			}},
		{"twenty classes are cut to sixteen, sorted, and say so", repo(false, many), task("c00"),
			func(t *testing.T, p *envelope.PolicyFact) {
				if len(p.Classes) != envelope.MaxPolicyClasses || !p.Truncated || p.Classes[0].Name != "c00" || p.Classes[15].Name != "c15" {
					t.Fatalf("classes = %d (truncated %v), first %v", len(p.Classes), p.Truncated, p.Classes[0])
				}
			}},
		{"how the merger merged is carried", repo(true, map[string]config.Class{"docs-links": {Live: true}}), merged,
			func(t *testing.T, p *envelope.PolicyFact) {
				if p.MergedHow != "approved" {
					t.Fatalf("mergedHow = %q", p.MergedHow)
				}
			}},
		{"a repository without classes and a task without a class has no policy", &config.Config{Repositories: []config.Repository{{Name: "o/r"}}}, task(""),
			func(t *testing.T, p *envelope.PolicyFact) {
				if p != nil {
					t.Fatalf("policy = %+v, want nil", *p)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := policyOf(tc.cfg, tc.cfg.RepositoryFor(tc.task.Spec.Repository), tc.task)
			tc.check(t, p)
			if p != nil {
				if err := (envelope.TaskFacts{Phase: "Implementing", Policy: p}).Validate(); err != nil {
					t.Fatalf("the broker would refuse it: %v", err)
				}
			}
		})
	}
}
