// SPDX-License-Identifier: Apache-2.0

package statuswriter

import (
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/configsource"
	"github.com/Smana/maku/internal/factory/resources"
)

// MR13: each kind names the fields the next release refuses in a Deprecated condition, True with
// RemovedNextRelease; a clean object has none, and Ready never reads it.
func TestDeprecatedCondition(t *testing.T) {
	c := cfg(config.Repository{Name: repo, Merge: config.Merge{AutoMergesPerDay: 2},
		Bindings: []config.Binding{{Name: "docs", Mode: "off"}}})
	c.PolicyManagement = config.PolicyManagement{Read: true}
	p := policyObj("docs", 3)
	p.Spec.Harness, p.Spec.Requires = "plain", []string{"allowedFiles"}
	in := Inputs{Config: c, GitHubUp: true, Synced: configsource.Synced{At: now.Time},
		Outcomes: []configsource.Outcome{served(resources.KindFactory, "f"), served(resources.KindRepository, "r"), served(resources.KindMergePolicy, "docs")}}
	st := Desired(in, []v1alpha1.Factory{obj("f", 3)}, []v1alpha1.Repository{repoObj("r", repo, 3)}, []v1alpha1.MergePolicy{p}, now)

	for name, tc := range map[string]struct {
		cs  []metav1.Condition
		msg string
	}{
		"factory":    {st.Factories["f"].Conditions, "policyManagement.read"},
		"repository": {st.Repositories["r"].Conditions, "merge.autoMergesPerDay, mergePolicies[docs].mode: off"},
		"policy":     {st.MergePolicies["docs"].Conditions, "harness, requires"},
	} {
		if got := want(t, tc.cs, config.ConditionDeprecated, metav1.ConditionTrue, config.ReasonRemovedNextRelease); got.Message != tc.msg {
			t.Errorf("%s: %q, want %q", name, got.Message, tc.msg)
		}
	}
	want(t, st.Factories["f"].Conditions, ConditionReady, metav1.ConditionTrue, "Ready")

	clean := Desired(Inputs{Config: cfg(config.Repository{Name: repo}), GitHubUp: true, Synced: configsource.Synced{At: now.Time},
		Outcomes: in.Outcomes}, []v1alpha1.Factory{obj("f", 3)}, []v1alpha1.Repository{repoObj("r", repo, 3)}, []v1alpha1.MergePolicy{policyObj("docs", 3)}, now)
	for _, cs := range [][]metav1.Condition{clean.Factories["f"].Conditions, clean.Repositories["r"].Conditions, clean.MergePolicies["docs"].Conditions} {
		if apimeta.FindStatusCondition(cs, config.ConditionDeprecated) != nil {
			t.Errorf("a clean object is not deprecated: %+v", cs)
		}
	}
}
