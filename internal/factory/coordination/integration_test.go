// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"errors"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/taskid"
)

const (
	mergeA = "a1b2c3d4e5f60718293a4b5c6d7e8f9011223344"
	headB  = "b1b2c3d4e5f60718293a4b5c6d7e8f9011223344"
	mergeB = "c1b2c3d4e5f60718293a4b5c6d7e8f9011223344"
)

// integrationParent is the example plan's filed parent with contract's schema landed at mergeA.
func integrationParent(t *testing.T) *v1alpha1.Task {
	t.Helper()
	p := validPlan(t)
	h := PlanHash(p)
	cs := &v1alpha1.CoordinationStatus{PlanVersion: p.Version, Plan: &p, PlanHash: h,
		Confirmations: []v1alpha1.ParentConfirmation{{PlanHash: h, Actor: "zitadel:s", LinkedGitHubLogin: "Smana", ConfirmedAt: metav1.Now()}},
		Artifacts: []v1alpha1.LandedArtifact{{ChildID: "contract", Name: "consumer-v1-schema", Repository: "Smana/public-contracts",
			Path: "schemas/consumer-v1.json", MergeCommitSHA: mergeA, BlobSHA: strings.Repeat("1", 40), ContentSHA256: strings.Repeat("2", 64), SizeBytes: 17}}}
	return &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: p.ID, Namespace: "agent-system"},
		Spec:   v1alpha1.TaskSpec{Kind: v1alpha1.TaskKindCoordination, Repository: p.Primary.Repository, Issue: 20},
		Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseCoordinating, Reason: ReasonFiled, Coordination: cs}}
}

// MR10: a pre-dependent manifest is every landed dependency merge SHA plus the dependent's exact
// head; a final one every child's merge SHA. Every input is bound: a wrong repository, a missing
// artifact, both or neither SHA, a changed acceptance or one changed byte is another hash and
// another Task, and the canonical form ignores only order that carries no meaning.
func TestIntegrationManifestBindsExactSubjects(t *testing.T) {
	parent := integrationParent(t)
	cs := parent.Status.Coordination
	pre, err := BeforeDependentManifest(parent.Name, cs, "consumer", headB, map[string]string{"contract": mergeA})
	if err != nil {
		t.Fatal(err)
	}
	want := []v1alpha1.IntegrationSubject{{ChildID: "consumer", Repository: "Smana/public-consumer", HeadSHA: headB},
		{ChildID: "contract", Repository: "Smana/public-contracts", MergeCommitSHA: mergeA}}
	if !slices.Equal(pre.Subjects, want) || pre.Purpose != PurposeBeforeDependentMerge || pre.PlanHash != cs.PlanHash ||
		pre.PlanVersion != 3 || pre.ParentTask != parent.Name || len(pre.Acceptance) != 1 || len(pre.Artifacts) != 1 {
		t.Fatalf("pre manifest %+v", pre)
	}
	if err := ValidateManifest(pre, cs); err != nil {
		t.Fatalf("a manifest built from the parent is valid: %v", err)
	}
	final, err := FinalManifest(parent.Name, cs, map[string]string{"contract": mergeA, "consumer": mergeB})
	if err != nil {
		t.Fatal(err)
	}
	if final.Purpose != PurposeFinal || len(final.Subjects) != 2 || final.Subjects[0].MergeCommitSHA != mergeB || final.Subjects[1].MergeCommitSHA != mergeA ||
		final.Subjects[0].HeadSHA != "" || ManifestHash(final) == ManifestHash(pre) {
		t.Fatalf("final manifest %+v", final)
	}
	if _, err := FinalManifest(parent.Name, cs, map[string]string{"contract": mergeA}); !errors.Is(err, ErrIntegrationManifest) {
		t.Fatalf("a final manifest without every merge: %v", err)
	}
	if _, err := BeforeDependentManifest(parent.Name, cs, "consumer", headB, nil); !errors.Is(err, ErrIntegrationManifest) {
		t.Fatalf("a pre manifest without its dependency's merge: %v", err)
	}
	if _, err := BeforeDependentManifest(parent.Name, cs, "consumer", "main", map[string]string{"contract": mergeA}); !errors.Is(err, ErrIntegrationManifest) {
		t.Fatalf("a head that is not 40-hex: %v", err)
	}
	if _, err := BeforeDependentManifest(parent.Name, cs, "contract", headB, nil); !errors.Is(err, ErrIntegrationManifest) {
		t.Fatalf("an independent child has no pre-dependent manifest: %v", err)
	}

	swapped := pre
	swapped.Subjects = []v1alpha1.IntegrationSubject{pre.Subjects[1], pre.Subjects[0]}
	if ManifestHash(swapped) != ManifestHash(pre) {
		t.Fatal("subject order carries no meaning")
	}
	base := ManifestHash(pre)
	baseTask := IntegrationTask("agent-system", tierConfig(), parent, pre, 1)
	for _, tc := range []struct {
		name    string
		edit    func(m *IntegrationManifest)
		invalid bool
	}{
		{name: "a wrong repository", edit: func(m *IntegrationManifest) { m.Subjects[1].Repository = "Smana/other" }, invalid: true},
		{name: "a missing artifact", edit: func(m *IntegrationManifest) { m.Artifacts = nil }, invalid: true},
		{name: "both SHAs", edit: func(m *IntegrationManifest) { m.Subjects[0].MergeCommitSHA = mergeB }, invalid: true},
		{name: "neither SHA", edit: func(m *IntegrationManifest) { m.Subjects[0].HeadSHA = "" }, invalid: true},
		{name: "a changed acceptance", edit: func(m *IntegrationManifest) { m.Acceptance[0].Check = "true" }, invalid: true},
		{name: "one changed byte", edit: func(m *IntegrationManifest) { m.Subjects[0].HeadSHA = "b" + headB[1:39] + "5" }},
		{name: "another attempt's plan hash", edit: func(m *IntegrationManifest) { m.PlanHash = strings.Repeat("e", 64) }, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := pre
			m.Subjects = slices.Clone(pre.Subjects)
			m.Acceptance = slices.Clone(pre.Acceptance)
			m.Artifacts = slices.Clone(pre.Artifacts)
			tc.edit(&m)
			if ManifestHash(m) == base {
				t.Fatal("the hash did not change")
			}
			if IntegrationTask("agent-system", tierConfig(), parent, m, 1).Name == baseTask.Name {
				t.Fatal("the Task identity did not change")
			}
			if err := ValidateManifest(m, cs); tc.invalid != (err != nil) {
				t.Fatalf("validate: %v, want invalid=%v", err, tc.invalid)
			}
		})
	}
}

// MR2, MR10, MR17: an integration Task is the primary's, keyed by its manifest hash and attempt,
// requested by the parent's confirmer, hashed to the plan, with one attempt's fixed cap and its
// own lineage: no childID, scope, splitter field, template or issue.
func TestIntegrationManifestTask(t *testing.T) {
	parent := integrationParent(t)
	cs := parent.Status.Coordination
	m, err := BeforeDependentManifest(parent.Name, cs, "consumer", headB, map[string]string{"contract": mergeA})
	if err != nil {
		t.Fatal(err)
	}
	it := IntegrationTask("agent-system", tierConfig(), parent, m, 2)
	key := taskid.CoordinationIntegrationKey(parent.Name, PurposeBeforeDependentMerge, ManifestHash(m), 2)
	s := it.Spec
	switch {
	case it.Name != taskid.Name(key) || s.Source.Key != key || it.Labels[v1alpha1.LabelCoordinationParent] != parent.Name:
		t.Fatalf("identity %s %+v %v", it.Name, s.Source, it.Labels)
	case s.Kind != v1alpha1.TaskKindIntegration || s.Source.Kind != v1alpha1.SourceKindCoordination || s.Source.RequestedBy != "github:smana" ||
		s.Source.ContentSHA256 != cs.PlanHash || s.Repository != "Smana/public-contracts" || s.DataClass != "public":
		t.Fatalf("spec %+v", s)
	case s.CoordinationParent == nil || s.CoordinationParent.Task != parent.Name || s.CoordinationParent.PlanVersion != 3 || s.CoordinationParent.ChildID != "":
		t.Fatalf("lineage %+v", s.CoordinationParent)
	case s.Integration == nil || s.Integration.Purpose != m.Purpose || s.Integration.PlanHash != cs.PlanHash || !slices.Equal(s.Integration.Subjects, m.Subjects):
		t.Fatalf("integration %+v", s.Integration)
	case s.Budget.RunMicroUSD != 510000 || s.Budget.TaskMicroUSD != 510000 || s.Budget.Tier != "standard":
		t.Fatalf("budget %+v", s.Budget)
	case s.Issue != 0 || s.Template != "" || s.Scope != nil || s.Parent != nil || s.Split != nil || s.Coordination != nil || len(s.CoordinationArtifacts) != 0:
		t.Fatalf("an integration Task carries no issue, template, scope or other lineage: %+v", s)
	}
	got, err := ManifestOf(it, cs)
	if err != nil || ManifestHash(got) != ManifestHash(m) {
		t.Fatalf("the Task's manifest is the one it was created for: %v", err)
	}
	moved := cs.DeepCopy()
	moved.PlanHash = strings.Repeat("e", 64)
	if _, err := ManifestOf(it, moved); !errors.Is(err, ErrIntegrationStale) {
		t.Fatalf("a manifest over a replaced plan: %v", err)
	}
	gone := cs.DeepCopy()
	gone.Artifacts = nil
	if _, err := ManifestOf(it, gone); !errors.Is(err, ErrIntegrationStale) {
		t.Fatalf("a manifest whose artifact is no longer recorded: %v", err)
	}
}

func tierConfig() *config.Config {
	return &config.Config{Tiers: map[string]config.Tier{"standard": {Model: "agent-default", RunTokens: 1_500_000, TaskTokens: 3_000_000, RunMinutes: 45}}}
}
