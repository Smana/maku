// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"bytes"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/coordinationview"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/summary"
)

func coordFamily() coordinationview.View {
	v := coordination.CoordinationView{
		ParentTask: "parent-1", Phase: "Coordinating", PlanHash: "0123456789abcdef",
		Plan: v1alpha1.CoordinationPlan{
			ID: "parent-1", Version: 3,
			Children: []v1alpha1.CoordinationChild{
				{ID: "contract", Repository: "acme/api"},
				{ID: "consumer", Repository: "acme/web", DependsOn: []string{"contract"},
					Consumes: []v1alpha1.ArtifactConsumer{{From: "contract", Artifact: "consumer-v1-schema", Required: true}}},
			},
			Integration: v1alpha1.IntegrationPlan{Role: "triager"},
			Budget:      v1alpha1.CoordinationPlanBudget{TotalMicroUSD: 12_750_000},
		},
		ParentConfirmation: &coordination.ParentConfirmation{PlanHash: "0123456789abcdef"},
		Children: []coordination.CoordinationChildView{
			{ID: "contract", Repository: "acme/api", Task: "t1", Room: "t1", Phase: "Merged", Reason: "merged", PRURL: "https://github.com/acme/api/pull/2"},
			{ID: "consumer", Repository: "acme/web", Task: "t2", Room: "t2", Phase: "Running", Reason: "<!here> [x](https://evil.example) *b*"},
		},
		Artifacts: []v1alpha1.LandedArtifact{{ChildID: "contract", Name: "consumer-v1-schema"}},
		Budget:    coordination.CoordinationBudgetView{TotalMicroUSD: 12_750_000, ChildrenSettledMicroUSD: 1_900_000, IntegrationSettledMicroUSD: 100_000},
		Recovery:  &v1alpha1.RecoveryProposal{Code: "retry_child", ChildID: "consumer"},
	}
	return coordinationview.View{CoordinationView: v}
}

func TestCoordinationLines(t *testing.T) {
	o := Options{Format: Markdown, Rooms: "https://rooms.example"}
	got := strings.Join(CoordinationLines(coordFamily(), o), "\n")
	for _, want := range []string{
		"Coordination: plan v3 0123456 · 2 children",
		"contract (acme/api): Merged · merged · [PR](https://github.com/acme/api/pull/2)",
		"[room](https://rooms.example/r/t1)",
		"consumer (acme/web): Running",
		"consumer needs contract/consumer-v1-schema: landed",
		"Spent $2.00 of $12.75 ceiling",
		"Recovery: `retry_child` (consumer)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Count(got, "](https://evil.example)") != strings.Count(got, "`[x](https://evil.example)`") || !strings.Contains(got, `\<\!here\>`) {
		t.Fatalf("hostile reason formed markup:\n%s", got)
	}

	slack := strings.Join(CoordinationLines(coordFamily(), Options{Format: Slack}), "\n")
	if strings.Contains(slack, "<!here>") {
		t.Fatalf("a Slack mention survived:\n%s", slack)
	}
}

func TestCoordinationLinesRestricted(t *testing.T) {
	v := coordFamily()
	v.Plan.Children = v.Plan.Children[:1]
	v.Children = v.Children[:1]
	v.Budget = coordination.CoordinationBudgetView{}
	v.Recovery = nil
	v.RestrictedChildren = 1
	got := strings.Join(CoordinationLines(v, Options{Format: Text}), "\n")
	if !strings.Contains(got, "1 child restricted") || strings.Contains(got, "consumer") || strings.Contains(got, "acme/web") || strings.Contains(got, "ceiling") {
		t.Fatalf("restricted lines:\n%s", got)
	}
}

func TestCardShowsCoordination(t *testing.T) {
	s := summary.Summary{Room: "parent-1"}
	coordinationview.Attach(&s, coordFamily())
	var b bytes.Buffer
	if err := Card(&b, s, Options{Format: Text}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "Coordination: plan v3") {
		t.Fatalf("card:\n%s", b.String())
	}
	var plain bytes.Buffer
	if err := Card(&plain, summary.Summary{Room: "r"}, Options{Format: Text}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.String(), "Coordination") {
		t.Fatal("a room without a family shows coordination")
	}
}

func TestOverviewShowsCoordinationCompactly(t *testing.T) {
	rows := []Row{{ID: "pppppppp", Phase: "Active", State: "running", Repository: "acme/api", Task: "Coordinating", LastSeq: 1,
		Coordination: &summary.CoordinationOverview{PlanVersion: 2, RestrictedChildren: 1, RecoveryCode: "retry_child",
			Children: []summary.CoordinationChildState{{ID: "contract", Phase: "Merged"}, {ID: "consumer", Phase: "<b>Running</b>"}}}}}
	var b bytes.Buffer
	if err := Overview(&b, rows, Options{Format: Text, All: true}); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{"family v2: contract Merged, consumer", "+1 restricted", "recovery retry_child"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	var md bytes.Buffer
	if err := Overview(&md, rows, Options{Format: Markdown, All: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(md.String(), "<b>") || !strings.Contains(md.String(), "family v2") {
		t.Fatalf("markdown overview:\n%s", md.String())
	}
}
