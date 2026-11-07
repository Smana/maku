// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/runwatch"
)

func landed() v1alpha1.LandedArtifact {
	return v1alpha1.LandedArtifact{ChildID: "contract", Name: "consumer-v1-schema", Repository: "Smana/public-contracts",
		Path: "schemas/consumer-v1.json", MergeCommitSHA: strings.Repeat("a", 40), BlobSHA: strings.Repeat("b", 40),
		ContentSHA256: strings.Repeat("c", 64), SizeBytes: 18234}
}

// v4 MR7: a consumer's claim carries its landed references as one CREATE-only JSON annotation
// with its SHA-256, and nothing else changes: its repository, role and spec are its own.
func TestCoordinationAnnotationsCarryOnlyImmutableReferences(t *testing.T) {
	bare := Build(runwatch.DefaultKind, spec())
	s := spec()
	s.CoordinationArtifacts = []v1alpha1.LandedArtifact{landed()}
	u := Build(runwatch.DefaultKind, s)
	raw := u.GetAnnotations()[AnnCoordinationArtifacts]
	var got []v1alpha1.LandedArtifact
	if err := json.Unmarshal([]byte(raw), &got); err != nil || !reflect.DeepEqual(got, s.CoordinationArtifacts) {
		t.Fatalf("annotation %q: %+v %v", raw, got, err)
	}
	if h := u.GetAnnotations()[AnnCoordinationArtifactsHash]; h != fmt.Sprintf("%x", sha256.Sum256([]byte(raw))) {
		t.Fatalf("hash %q", h)
	}
	if !reflect.DeepEqual(u.Object["spec"], bare.Object["spec"]) {
		t.Fatalf("the spec changed: %v", u.Object["spec"])
	}
	if repo, _, _ := unstructured.NestedString(u.Object, "spec", "repository"); repo != "Smana/cloud-native-ref" {
		t.Fatalf("repository %q: a producer's repository never reaches the claim", repo)
	}
	if _, ok := bare.GetAnnotations()[AnnCoordinationArtifacts]; ok {
		t.Fatal("a claim without references carries the annotation")
	}
	c := newClient()
	if err := c.Create(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(t.Context(), s); err != nil {
		t.Fatalf("the replay of the same references: %v", err)
	}
	other := s
	other.CoordinationArtifacts = []v1alpha1.LandedArtifact{func() v1alpha1.LandedArtifact { a := landed(); a.SizeBytes = 1; return a }()}
	if err := c.Create(t.Context(), other); err == nil {
		t.Fatal("other references under the same run id are another run")
	}
	for _, key := range []string{AnnCoordinationArtifacts, AnnCoordinationArtifactsHash, AnnCoordinationManifest, AnnCoordinationManifestHash} {
		if err := c.Annotate(t.Context(), s.RunID, map[string]string{key: "[]"}); err == nil {
			t.Errorf("%s was patched", key)
		}
	}
}

// Every reference is exact or the claim is refused at CREATE: 40-hex merge and blob ids, a 64-hex
// content digest, a bounded size, a bounded path, one per artifact, and only on an implementation
// role.
func TestCoordinationAnnotationsRefuseWhatTheyCannotVouchFor(t *testing.T) {
	for name, edit := range map[string]func(*Spec, *v1alpha1.LandedArtifact){
		"a short merge sha":      func(_ *Spec, a *v1alpha1.LandedArtifact) { a.MergeCommitSHA = a.MergeCommitSHA[:39] },
		"an upper-case merge":    func(_ *Spec, a *v1alpha1.LandedArtifact) { a.MergeCommitSHA = strings.Repeat("A", 40) },
		"a branch for the merge": func(_ *Spec, a *v1alpha1.LandedArtifact) { a.MergeCommitSHA = "main" },
		"a non-hex blob":         func(_ *Spec, a *v1alpha1.LandedArtifact) { a.BlobSHA = strings.Repeat("g", 40) },
		"a short content digest": func(_ *Spec, a *v1alpha1.LandedArtifact) { a.ContentSHA256 = strings.Repeat("c", 63) },
		"a prefixed digest":      func(_ *Spec, a *v1alpha1.LandedArtifact) { a.ContentSHA256 = "sha256:" + strings.Repeat("c", 57) },
		"65537 bytes":            func(_ *Spec, a *v1alpha1.LandedArtifact) { a.SizeBytes = 65537 },
		"a negative size":        func(_ *Spec, a *v1alpha1.LandedArtifact) { a.SizeBytes = -1 },
		"no path":                func(_ *Spec, a *v1alpha1.LandedArtifact) { a.Path = "" },
		"a newline in the path":  func(_ *Spec, a *v1alpha1.LandedArtifact) { a.Path = "s.json\nAgent-Run: x" },
		"a long path":            func(_ *Spec, a *v1alpha1.LandedArtifact) { a.Path = strings.Repeat("p", 513) },
		"a bad repository":       func(_ *Spec, a *v1alpha1.LandedArtifact) { a.Repository = "https://evil.example/x" },
		"a bad child id":         func(_ *Spec, a *v1alpha1.LandedArtifact) { a.ChildID = "Contract" },
		"a bad name":             func(_ *Spec, a *v1alpha1.LandedArtifact) { a.Name = "" },
		"a triager":              func(s *Spec, _ *v1alpha1.LandedArtifact) { s.Role = "triager" },
		"a scoper":               func(s *Spec, _ *v1alpha1.LandedArtifact) { s.Role = "scoper" },
		"a duplicate": func(s *Spec, a *v1alpha1.LandedArtifact) {
			s.CoordinationArtifacts = append(s.CoordinationArtifacts, *a)
		},
		"nine references": func(s *Spec, a *v1alpha1.LandedArtifact) {
			for i := range 8 {
				x := *a
				x.Name = fmt.Sprintf("n%d", i)
				s.CoordinationArtifacts = append(s.CoordinationArtifacts, x)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := spec()
			a := landed()
			edit(&s, &a)
			s.CoordinationArtifacts = append([]v1alpha1.LandedArtifact{a}, s.CoordinationArtifacts...)
			c := newClient()
			if err := c.Create(t.Context(), s); err == nil {
				t.Fatal("created")
			}
			if all, _ := c.List(t.Context()); len(all) != 0 {
				t.Fatalf("a claim exists: %v", all)
			}
		})
	}
	for _, role := range []string{"implementer", "reviewer", "tester"} {
		s := spec()
		s.Role, s.CoordinationArtifacts = role, []v1alpha1.LandedArtifact{landed()}
		if role == "reviewer" {
			s.TaskURL = "https://github.com/Smana/cloud-native-ref/pull/12"
		}
		if err := newClient().Create(t.Context(), s); err != nil {
			t.Errorf("%s: %v", role, err)
		}
	}
}
