// SPDX-License-Identifier: Apache-2.0

package coordinationview

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/coordination"
)

const (
	sha2 = "2222222222222222222222222222222222222222"
	shaM = "3333333333333333333333333333333333333333"
)

// checkoutView is a filed family: contract merged, consumer (Children[0]) open on its PR head.
func checkoutView() View {
	return View{CoordinationView: coordination.CoordinationView{
		ParentTask: "parent-1", PlanHash: "abc123",
		Plan: v1alpha1.CoordinationPlan{Version: 3, Children: []v1alpha1.CoordinationChild{
			{ID: "contract", Repository: repoA}, {ID: "consumer", Repository: repoB, DependsOn: []string{"contract"},
				Consumes: []v1alpha1.ArtifactConsumer{{From: "contract", Artifact: "schema", Required: true}}},
		}},
		Children: []coordination.CoordinationChildView{
			{ID: "consumer", Repository: repoB, Task: "bbbbcccc", Room: "bbbbcccc", IssueURL: "https://github.com/acme/secret-consumer/issues/9",
				PRURL: "https://github.com/acme/secret-consumer/pull/10", HeadSHA: sha2, DependsOn: []string{"contract"}, Consumes: []string{"contract/schema"}},
			{ID: "contract", Repository: repoA, Task: "aaaabbbb", Room: "aaaabbbb", IssueURL: "https://github.com/acme/api/issues/1",
				PRURL: "https://github.com/acme/api/pull/2", MergeCommitSHA: shaM},
		},
		Artifacts:  []v1alpha1.LandedArtifact{{ChildID: "contract", Name: "schema", Repository: repoA, Path: "schema.json", MergeCommitSHA: shaM}},
		MergeOrder: []string{"contract", "consumer"},
		IntegrationAttempts: []coordination.IntegrationAttemptView{{Task: "int-1", Purpose: "beforeDependentMerge", ManifestHash: "mh", State: "passed",
			Subjects: []v1alpha1.IntegrationSubject{{ChildID: "contract", MergeCommitSHA: shaM}, {ChildID: "consumer", HeadSHA: sha2}}}},
	}}
}

func TestCheckoutDocumentLinksAllFamilyState(t *testing.T) {
	d, err := Checkout(checkoutView())
	if err != nil {
		t.Fatal(err)
	}
	if d.APIVersion != CheckoutAPIVersion || d.Kind != CheckoutKind || d.ParentTask != "parent-1" || d.PlanVersion != 3 || d.PlanHash != "abc123" {
		t.Fatalf("%+v", d)
	}
	got := []string{}
	for _, c := range d.Children {
		got = append(got, c.ID)
	}
	if !reflect.DeepEqual(got, []string{"contract", "consumer"}) {
		t.Fatalf("not in merge order: %v", got)
	}
	contract, consumer := d.Children[0], d.Children[1]
	if contract.Worktree != "contract" || contract.MergeCommitSHA != shaM || contract.HeadSHA != "" || contract.Room != "aaaabbbb" ||
		contract.Issue != "https://github.com/acme/api/issues/1" || contract.PR != "https://github.com/acme/api/pull/2" || len(contract.Artifacts) != 1 {
		t.Fatalf("contract = %+v", contract)
	}
	if consumer.HeadSHA != sha2 || !reflect.DeepEqual(consumer.DependsOn, []string{"contract"}) ||
		len(consumer.Artifacts) != 1 || consumer.Artifacts[0].Name != "schema" {
		t.Fatalf("consumer = %+v (consumes the landed schema)", consumer)
	}
	if len(d.IntegrationAttempts) != 1 || d.IntegrationAttempts[0].ManifestHash != "mh" || len(d.IntegrationAttempts[0].Subjects) != 2 {
		t.Fatalf("%+v", d.IntegrationAttempts)
	}
	h := ManifestHash(d)
	if len(h) != 64 || strings.ToLower(h) != h || ManifestHash(d) != h {
		t.Fatalf("hash %q", h)
	}
	d.Children[1].Worktree = "/abs/consumer"
	if ManifestHash(d) == h {
		t.Fatal("the hash ignores a worktree path")
	}
}

func TestCheckoutRefusesAnyRestrictedChild(t *testing.T) {
	v := checkoutView()
	v.RestrictedChildren = 1
	if _, err := Checkout(v); !errors.Is(err, ErrCheckoutRestricted) {
		t.Fatalf("%v", err)
	}
}

func TestCheckoutFailsClosed(t *testing.T) {
	for name, mut := range map[string]func(*View){
		"a wrong issue URL":      func(v *View) { v.Children[1].IssueURL = "https://evil.example/issues/9" },
		"a PR in another repo":   func(v *View) { v.Children[0].PRURL = "https://github.com/acme/api/pull/10" },
		"both SHA kinds":         func(v *View) { v.Children[0].MergeCommitSHA = shaM },
		"no SHA":                 func(v *View) { v.Children[0].HeadSHA = "" },
		"a malformed SHA":        func(v *View) { v.Children[0].HeadSHA = "HEAD" },
		"an unfiled child":       func(v *View) { v.Children = v.Children[1:] },
		"a malformed child id":   func(v *View) { v.Children[0].ID, v.Plan.Children[1].ID = "../x", "../x" },
		"a child room not a C2":  func(v *View) { v.Children[1].Room = "../../x" },
		"a plan without a child": func(v *View) { v.Plan.Children = nil },
	} {
		t.Run(name, func(t *testing.T) {
			v := checkoutView()
			mut(&v)
			if _, err := Checkout(v); err == nil || !errors.Is(err, ErrCheckoutNotReady) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
