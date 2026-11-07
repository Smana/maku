// SPDX-License-Identifier: Apache-2.0

package coordinationview

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/coordination"
)

const (
	repoA = "acme/api"
	repoB = "acme/secret-consumer"
)

// fakeAccess is the viewer's GitHub standing per repository: a login and a read answer, or an error
// at either step.
type fakeAccess struct {
	logins   map[string]string
	loginErr map[string]error
	read     map[string]bool
	readErr  map[string]error
	calls    int
}

func (f *fakeAccess) Login(_ context.Context, _, repository string) (string, error) {
	f.calls++
	if err := f.loginErr[repository]; err != nil {
		return "", err
	}
	return f.logins[repository], nil
}

func (f *fakeAccess) CanRead(_ context.Context, repository, _ string) (bool, error) {
	if err := f.readErr[repository]; err != nil {
		return false, err
	}
	return f.read[repository], nil
}

// fixture is a two-child family, b depending on a's contract, in which every field that could
// carry b's identity holds a unique marker containing "BMARK".
func fixture() coordination.CoordinationView {
	t := true
	plan := v1alpha1.CoordinationPlan{
		APIVersion: v1alpha1.CoordinationPlanAPIVersion, Kind: v1alpha1.CoordinationPlanKind,
		ID: "parent-1", Title: "Ship the v1 schema", Version: 3,
		Primary: v1alpha1.CoordinationPrimary{Repository: repoA, Rationale: "primary BMARK-rationale"},
		Children: []v1alpha1.CoordinationChild{
			{
				ID: "a", Repository: repoA, Maintainer: "github:alice", Class: "feature", Tier: "standard",
				Roles: []string{"implementer", "reviewer"}, BudgetMicroUSD: 5_000_000,
				Scope:    v1alpha1.TaskScope{State: "ready", Task: "add the schema", Touches: []string{"schema/"}, Plan: []string{"write it"}, Acceptance: []v1alpha1.TaskAcceptance{{Check: "go test", Proves: "it parses"}}},
				Produces: []v1alpha1.ArtifactSpec{{Name: "consumer-v1-schema", Kind: "file", Path: "schema/v1.json", MaxBytes: 4096}},
			},
			{
				ID: "b", Repository: repoB, Maintainer: "github:BMARK-maintainer", Rationale: "BMARK-child-rationale",
				Class: "feature", Tier: "standard", Roles: []string{"implementer", "reviewer"}, BudgetMicroUSD: 4_000_000,
				Scope:     v1alpha1.TaskScope{State: "ready", Task: "BMARK-scope-task", Touches: []string{"BMARK-touch"}, Plan: []string{"BMARK-plan"}, Acceptance: []v1alpha1.TaskAcceptance{{Check: "BMARK-check", Proves: "BMARK-proves"}}},
				DependsOn: []string{"a"},
				Consumes:  []v1alpha1.ArtifactConsumer{{From: "a", Artifact: "consumer-v1-schema", Required: true}},
			},
		},
		MergeOrder: []string{"a", "b"},
		Integration: v1alpha1.IntegrationPlan{
			Role: "triager", BeforeDependents: &t, BeforeDependentsRationale: "BMARK-before-rationale",
			RunMicroUSD: 500_000, MaxRuns: 2, Acceptance: []v1alpha1.TaskAcceptance{{Check: "BMARK-int-check", Proves: "BMARK-int-proves"}},
		},
		Budget: v1alpha1.CoordinationPlanBudget{PlanningMicroUSD: 250_000, TotalMicroUSD: 12_750_000},
	}
	return coordination.CoordinationView{
		ParentTask: "parent-1", Phase: "Coordinating", Reason: "children_running",
		Plan: plan, PlanHash: "abc123",
		ParentConfirmation: &coordination.ParentConfirmation{PlanHash: "abc123", Actor: "human:alice", LinkedGitHubLogin: "alice"},
		ChildAuthorizations: []coordination.ChildAuthorization{
			{ChildID: "a", ChildFingerprint: "fa", PlanVersion: 3, Actor: "human:alice", Repository: repoA, Permission: "maintain", FenceSHA: "f1"},
			{ChildID: "b", ChildFingerprint: "BMARK-fp", PlanVersion: 3, Actor: "human:BMARK", Repository: repoB, Permission: "admin", FenceSHA: "BMARK-fence"},
		},
		Children: []coordination.CoordinationChildView{
			{
				ID: "a", Repository: repoA, Task: "task-a", Room: "room-a", IssueURL: "https://github.com/acme/api/issues/1",
				PRURL: "https://github.com/acme/api/pull/2", Phase: "Merged", Reason: "merged", Fingerprint: "fa",
				DependsOn: []string{}, Consumes: []string{}, HeadSHA: "aaa", MergeCommitSHA: "mmm",
				AllocationMicroUSD: 5_000_000, SettledMicroUSD: 1_200_000, ReservedMicroUSD: 0,
				IntegrationAcceptedHead: "BMARK-accepted-with-b",
				IntegrationRelease:      &v1alpha1.IntegrationRelease{ChildID: "a", HeadSHA: "aaa", IntegrationTask: "int-BMARK", ManifestHash: "BMARK-manifest"},
			},
			{
				ID: "b", Repository: repoB, Task: "BMARK-task", Room: "BMARK-room", IssueURL: "https://github.com/BMARK/issues/9",
				PRURL: "https://github.com/BMARK/pull/10", Phase: "Running", Reason: "BMARK-reason", Fingerprint: "BMARK-fp",
				DependsOn: []string{"a"}, Consumes: []string{"a/consumer-v1-schema"}, HeadSHA: "BMARK-head",
				AllocationMicroUSD: 4_000_000, SettledMicroUSD: 700_000, ReservedMicroUSD: 900_000,
			},
		},
		Artifacts: []v1alpha1.LandedArtifact{
			{ChildID: "a", Name: "consumer-v1-schema", Repository: repoA, Path: "schema/v1.json", MergeCommitSHA: "mmm", BlobSHA: "blob", ContentSHA256: "c", SizeBytes: 12},
			{ChildID: "b", Name: "BMARK-artifact", Repository: repoB, Path: "BMARK-path", MergeCommitSHA: "BMARK-merge"},
		},
		IntegrationAttempts: []coordination.IntegrationAttemptView{
			{Task: "int-pre-only-a", Purpose: "pre", PlanHash: "abc123", ManifestHash: "m1", State: "Passed",
				Subjects: []v1alpha1.IntegrationSubject{{ChildID: "a", Repository: repoA, HeadSHA: "aaa"}}, RunMicroUSD: 500_000, CostMicroUSD: 100_000},
			{Task: "int-BMARK", Purpose: "final", PlanHash: "abc123", ManifestHash: "BMARK-manifest", State: "Failed", Reason: "BMARK-int-reason",
				Subjects: []v1alpha1.IntegrationSubject{{ChildID: "a", Repository: repoA, MergeCommitSHA: "mmm"}, {ChildID: "b", Repository: repoB, HeadSHA: "BMARK-head"}}, RunMicroUSD: 500_000, CostMicroUSD: 200_000},
		},
		MergeOrder: []string{"a", "b"},
		Budget: coordination.CoordinationBudgetView{
			PlanningMicroUSD: 250_000, TotalMicroUSD: 12_750_000, AllocatedMicroUSD: 9_000_000,
			ChildrenReservedMicroUSD: 900_000, ChildrenSettledMicroUSD: 1_900_000, IntegrationSettledMicroUSD: 300_000, IntegrationMaxRuns: 2, IntegrationRuns: 2,
		},
		Recovery:                &v1alpha1.RecoveryProposal{Code: "retry_child", ChildID: "b", Reason: "BMARK-recovery", PlanVersion: 3},
		PostCompletionIncidents: []coordination.CoordinationIncident{{ChildID: "b", Reason: "BMARK-incident", MergeCommitSHA: "BMARK-revert"}},
	}
}

func readsBoth() *fakeAccess {
	return &fakeAccess{
		logins: map[string]string{repoA: "alice", repoB: "alice"},
		read:   map[string]bool{repoA: true, repoB: true},
	}
}

func encode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCoordinationFilterIntersectionHasNoAdminBypass(t *testing.T) {
	ctx := context.Background()
	full := readsBoth
	cases := []struct {
		name       string
		viewer     Viewer
		access     func() *fakeAccess
		restricted int
	}{
		{"an ordinary reader of both sees everything", Viewer{Sub: "u1"}, full, 0},
		{"an admin without read on b gets no widening", Viewer{Sub: "admin"}, func() *fakeAccess {
			f := full()
			f.read[repoB] = false
			return f
		}, 1},
		{"an unlinked viewer for b is restricted", Viewer{Sub: "u2"}, func() *fakeAccess {
			f := full()
			delete(f.logins, repoB)
			return f
		}, 1},
		{"a login error for b is restricted", Viewer{Sub: "u3"}, func() *fakeAccess {
			f := full()
			f.loginErr = map[string]error{repoB: errors.New("zitadel down")}
			return f
		}, 1},
		{"an access error for b is restricted", Viewer{Sub: "u4"}, func() *fakeAccess {
			f := full()
			f.readErr = map[string]error{repoB: errors.New("github 502")}
			return f
		}, 1},
		{"a sponsored principal is bounded by its repositories", Viewer{Sub: "system:slack", Sponsored: true, Repositories: map[string]bool{repoA: true}}, func() *fakeAccess {
			return &fakeAccess{}
		}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := fixture()
			before := encode(t, raw)
			got := Filter(ctx, raw, tc.viewer, tc.access())
			if encode(t, raw) != before {
				t.Fatal("Filter mutated the raw view")
			}
			if got.RestrictedChildren != tc.restricted {
				t.Fatalf("restrictedChildren = %d, want %d", got.RestrictedChildren, tc.restricted)
			}
			out := encode(t, got)
			if tc.restricted == 0 {
				if !reflect.DeepEqual(got.CoordinationView, fixture()) {
					t.Fatalf("an unrestricted view changed:\n%s", out)
				}
				return
			}
			if strings.Contains(out, "BMARK") || strings.Contains(out, repoB) {
				t.Fatalf("a restricted marker survived: %s", out)
			}
			if got.Budget != (coordination.CoordinationBudgetView{}) || got.Plan.Budget != (v1alpha1.CoordinationPlanBudget{}) {
				t.Fatalf("family budget not zeroed: %+v %+v", got.Budget, got.Plan.Budget)
			}
			if got.Plan.Primary.Rationale != "" || got.Plan.Integration.Acceptance != nil || got.Plan.Integration.BeforeDependentsRationale != "" {
				t.Fatal("family prose survived a restriction")
			}
			if len(got.Children) != 1 || got.Children[0].ID != "a" {
				t.Fatalf("children = %+v", got.Children)
			}
			a := got.Children[0]
			if a.Room != "room-a" || a.PRURL != "https://github.com/acme/api/pull/2" || a.SettledMicroUSD != 1_200_000 || a.HeadSHA != "aaa" {
				t.Fatalf("the allowed child lost its own fields: %+v", a)
			}
			if a.IntegrationRelease != nil || a.IntegrationAcceptedHead != "" {
				t.Fatal("an integration release survived a restriction")
			}
			if len(got.IntegrationAttempts) != 1 || got.IntegrationAttempts[0].Task != "int-pre-only-a" {
				t.Fatalf("attempts = %+v", got.IntegrationAttempts)
			}
			if got.Recovery != nil || got.PostCompletionIncidents != nil {
				t.Fatal("b's recovery or incident survived")
			}
			if !reflect.DeepEqual(got.MergeOrder, []string{"a"}) || !reflect.DeepEqual(got.Plan.MergeOrder, []string{"a"}) {
				t.Fatalf("merge order = %v / %v", got.MergeOrder, got.Plan.MergeOrder)
			}
			if got.Phase != "Coordinating" || got.Reason != "children_running" || got.ParentConfirmation == nil {
				t.Fatal("the coarse parent state was lost")
			}
		})
	}
}

func TestCoordinationFilterStripsReferencesToARestrictedProducer(t *testing.T) {
	// b allowed, a (the producer) restricted: b keeps its own state but no edge to a.
	raw := fixture()
	acc := readsBoth()
	acc.read[repoA] = false
	got := Filter(context.Background(), raw, Viewer{Sub: "u"}, acc)
	if got.RestrictedChildren != 1 || len(got.Children) != 1 || got.Children[0].ID != "b" {
		t.Fatalf("got %+v", got.Children)
	}
	b := got.Children[0]
	if len(b.DependsOn) != 0 || len(b.Consumes) != 0 {
		t.Fatalf("b still references a: %v %v", b.DependsOn, b.Consumes)
	}
	pb := got.Plan.Children[0]
	if len(pb.DependsOn) != 0 || len(pb.Consumes) != 0 {
		t.Fatalf("plan child still references a: %+v", pb)
	}
	for _, art := range got.Artifacts {
		if art.ChildID == "a" {
			t.Fatal("a's artifact survived")
		}
	}
	if len(got.ChildAuthorizations) != 1 || got.ChildAuthorizations[0].ChildID != "b" {
		t.Fatalf("authorizations = %+v", got.ChildAuthorizations)
	}
}

func TestCoordinationFilterChildWithoutRepositoryIsRestricted(t *testing.T) {
	// A view child the plan does not name, with no repository, cannot be checked: fail closed.
	raw := fixture()
	raw.Children = append(raw.Children, coordination.CoordinationChildView{ID: "ghost", Room: "ghost-room"})
	got := Filter(context.Background(), raw, Viewer{Sub: "u"}, readsBoth())
	if got.RestrictedChildren != 1 || strings.Contains(encode(t, got), "ghost-room") {
		t.Fatalf("ghost child not restricted: %+v", got)
	}
}
