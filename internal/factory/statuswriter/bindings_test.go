// SPDX-License-Identifier: Apache-2.0

package statuswriter

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/configsource"
	"github.com/Smana/maku/internal/factory/decision"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/resources"
)

// bindingsWorld is the spec's worked case, "A shared policy invalid for one repository only":
// dead-links at generation 4 breaks ai-native-ref's binding (it keeps generation 3) and serves
// cloud-native-ref; docs-links' generation 4 is refused, its consumers on 3; acme/app is served
// from a per-repository MergePolicy at generation 7.
func bindingsWorld() (Inputs, []v1alpha1.Repository, []v1alpha1.MergePolicy) {
	const anr, cnr = "Smana/ai-native-ref", "Smana/cloud-native-ref"
	c := cfg(
		config.Repository{Name: anr, Classes: map[string]config.Class{"docs-links": {Shadow: true}, "dead-links": {Shadow: true}},
			Merge: config.Merge{AutoMergesPerDay: 2, RequiredChecks: []string{"lint"}, VerifyChecks: []string{"lint"}}, MergeDefaulted: []string{"autoMergesPerDay"},
			Bound: []string{"docs-links", "dead-links"}},
		config.Repository{Name: cnr, Classes: map[string]config.Class{"docs-links": {Live: true}, "dead-links": {Shadow: true}},
			Bound: []string{"docs-links", "dead-links"}},
		config.Repository{Name: repo, Classes: map[string]config.Class{"docs-links": {Live: true}, "docs": {}}},
	)
	c.Generations.Repos = map[string]config.RepoGenerations{"acme/app": {Repository: 3, MergePolicy: 7, PolicyName: "app"}}

	anrDead := resources.BindingOutcome{Repository: "ai-native-ref", Name: "dead-links", Mode: "shadow", PolicyGeneration: 3, Served: true,
		Reason: config.ReasonMissingParameter, Message: "monitor.flux needs Repository.spec.merge.fluxSource"}
	cnrDead := resources.BindingOutcome{Repository: "cloud-native-ref", Name: "dead-links", Mode: "shadow", PolicyGeneration: 4, Accepted: true, Served: true}
	anrDocs := resources.BindingOutcome{Repository: "ai-native-ref", Name: "docs-links", Mode: "shadow", PolicyGeneration: 3, Accepted: true, Served: true}
	cnrDocs := resources.BindingOutcome{Repository: "cloud-native-ref", Name: "docs-links", Mode: "live", PolicyGeneration: 3, Accepted: true, Served: true}
	in := Inputs{Config: c, GitHubUp: true, Outcomes: []configsource.Outcome{
		{Kind: resources.KindRepository, Name: "ai-native-ref", Repository: anr, ObservedGeneration: 2, ServedGeneration: 2, Accepted: true,
			Bindings: []resources.BindingOutcome{anrDocs, anrDead}},
		{Kind: resources.KindRepository, Name: "cloud-native-ref", Repository: cnr, ObservedGeneration: 1, ServedGeneration: 1, Accepted: true,
			Bindings: []resources.BindingOutcome{cnrDocs, cnrDead}},
		{Kind: resources.KindRepository, Name: "app", Repository: repo, ObservedGeneration: 3, ServedGeneration: 3, Accepted: true},
		{Kind: resources.KindMergePolicy, Name: "dead-links", ObservedGeneration: 4, ServedGeneration: 4, Accepted: true,
			Consumers: []resources.BindingOutcome{anrDead, cnrDead}},
		{Kind: resources.KindMergePolicy, Name: "docs-links", ObservedGeneration: 4, ServedGeneration: 3, Reason: v1alpha1.ReasonInvalid,
			Message: "class docs-links: mode is the binding's", Consumers: []resources.BindingOutcome{anrDocs, cnrDocs}},
		{Kind: resources.KindMergePolicy, Name: "app", Repository: repo, ObservedGeneration: 7, ServedGeneration: 7, Accepted: true},
	}}
	in.Snapshot = func(string) (mergepolicy.Snapshot, bool) { return mergepolicy.Snapshot{HasClasses: true}, true }
	in.Decisions = func(r string) map[string]decision.Last {
		if r == cnr {
			return map[string]decision.Last{"dead-links": {Verdict: "shadow", Reason: "clean", At: now.Time}}
		}
		return nil
	}
	bound := func(name, repository string, gen int64) v1alpha1.Repository {
		r := repoObj(name, repository, gen)
		r.Spec.MergePolicies = []config.Binding{{Name: "docs-links", Mode: "shadow"}, {Name: "dead-links", Mode: "shadow"}}
		return r
	}
	repos := []v1alpha1.Repository{bound("ai-native-ref", anr, 2), bound("cloud-native-ref", cnr, 1), repoObj("app", repo, 3)}
	dead, docs := policyObj("dead-links", "", 4), policyObj("docs-links", "", 4)
	return in, repos, []v1alpha1.MergePolicy{dead, docs, policyObj("app", "app", 7)}
}

func binding(t *testing.T, st v1alpha1.RepositoryStatus, name string) v1alpha1.BindingStatus {
	t.Helper()
	for _, b := range st.Classes {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("no class %s in %+v", name, st.Classes)
	return v1alpha1.BindingStatus{}
}

// B10, Q8: the per-binding status on the Repository and the consumers on the MergePolicy.
func TestDesiredBindings(t *testing.T) {
	in, repos, policies := bindingsWorld()
	out := Desired(in, nil, repos, policies, now)

	anr := out.Repositories["ai-native-ref"]
	if b := binding(t, anr, "dead-links"); b.Accepted || b.Reason != config.ReasonMissingParameter || b.PolicyGeneration != 3 || b.Mode != "shadow" || b.Message == "" {
		t.Fatalf("ai-native-ref dead-links %+v", b)
	}
	c := want(t, anr.Conditions, v1alpha1.ConditionBindingsValid, metav1.ConditionFalse, "BindingsRefused")
	if c.Message != "refused: dead-links (MissingParameter)" || c.ObservedGeneration != 2 {
		t.Fatalf("BindingsValid %+v", c)
	}
	if anr.Authority != config.ClassAuthorityCentral || anr.ClassCount != 2 || anr.LiveCount != 0 {
		t.Fatalf("ai-native-ref %+v", anr)
	}
	if anr.Merge == nil || anr.Merge.AutoMergesPerDay != 2 || strings.Join(anr.MergeDefaulted, ",") != "autoMergesPerDay" {
		t.Fatalf("merge %+v, defaulted %v", anr.Merge, anr.MergeDefaulted)
	}
	want(t, anr.Conditions, ConditionRepositoryFileValid, metav1.ConditionTrue, "Valid")
	want(t, anr.Conditions, ConditionInSync, metav1.ConditionTrue, "InSync")

	cnr := out.Repositories["cloud-native-ref"]
	if b := binding(t, cnr, "dead-links"); !b.Accepted || b.PolicyGeneration != 4 || b.LastDecision != "shadow:clean" || b.LastDecisionAt == nil {
		t.Fatalf("cloud-native-ref dead-links %+v", b)
	}
	want(t, cnr.Conditions, v1alpha1.ConditionBindingsValid, metav1.ConditionTrue, "BindingsValid")
	if cnr.LiveCount != 1 || cnr.ClassCount != 2 {
		t.Fatalf("cloud-native-ref counts %d %d", cnr.ClassCount, cnr.LiveCount)
	}

	dead := out.MergePolicies["dead-links"]
	if dead.ConsumerCount != 2 || dead.CurrentCount != 1 || dead.LiveCount != 0 || len(dead.Consumers) != 2 {
		t.Fatalf("dead-links %+v", dead)
	}
	if cs := dead.Consumers[0]; cs.Repository != "ai-native-ref" || cs.Accepted || cs.Reason != config.ReasonMissingParameter || cs.ServedGeneration != 3 {
		t.Fatalf("ai-native-ref consumer %+v", cs)
	}
	want(t, dead.Conditions, v1alpha1.ConditionAccepted, metav1.ConditionTrue, "Served")
	if dead.Authority != "" || dead.ClassCount != 0 || len(dead.Classes) != 0 {
		t.Fatalf("a class policy carries no per-repository fields: %+v", dead)
	}

	docs := out.MergePolicies["docs-links"]
	c = want(t, docs.Conditions, v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonInvalid)
	if !strings.HasPrefix(c.Message, "serving generation 3; generation 4 refused") {
		t.Fatalf("docs-links Accepted %q", c.Message)
	}
	if docs.ConsumerCount != 2 || docs.CurrentCount != 0 || docs.LiveCount != 1 || docs.Consumers[1].ServedGeneration != 3 {
		t.Fatalf("docs-links %+v", docs)
	}

	// The per-repository shape fills the same Repository fields, and keeps its own status.
	app := out.Repositories["app"]
	if b := binding(t, app, "docs-links"); !b.Accepted || b.PolicyGeneration != 7 || b.Mode != "live" {
		t.Fatalf("legacy docs-links %+v", b)
	}
	if b := binding(t, app, "docs"); b.Mode != "off" {
		t.Fatalf("legacy docs %+v", b)
	}
	if app.ClassCount != 2 || app.LiveCount != 1 || app.Authority != config.ClassAuthorityCentral || app.MergePolicy != "app" {
		t.Fatalf("legacy repository %+v", app)
	}
	if c := cond(t, app.Conditions, ConditionRepositoryFileValid); c.Status != metav1.ConditionTrue {
		t.Fatalf("legacy file condition %+v", c)
	}
	for _, c := range app.Conditions {
		if c.Type == v1alpha1.ConditionBindingsValid {
			t.Fatal("a per-repository shape has no bindings to judge")
		}
	}
	if p := out.MergePolicies["app"]; p.ClassCount != 2 || p.Authority != config.ClassAuthorityCentral || p.ConsumerCount != 0 {
		t.Fatalf("legacy policy %+v", p)
	}
}

// A long binding message is cut to the schema's 1024 characters.
func TestDesiredBindingMessageBounded(t *testing.T) {
	in, repos, policies := bindingsWorld()
	in.Outcomes[0].Bindings[1].Message = strings.Repeat("x", 2000)
	out := Desired(in, nil, repos, policies, metav1.NewTime(time.Now()))
	if n := len(binding(t, out.Repositories["ai-native-ref"], "dead-links").Message); n != 1024 {
		t.Fatalf("message of %d characters", n)
	}
}
