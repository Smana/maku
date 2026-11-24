// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/factory/stages"
	"github.com/Smana/maku/internal/policy"
)

func TestStagesRouteAuthAndFold(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC))
	tk := &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "aaaaaaaa", Namespace: "agent-system"},
		Spec:       v1alpha1.TaskSpec{Repository: coordPrimary, PredictedClass: "docs"},
		Status: v1alpha1.TaskStatus{
			Phase:   v1alpha1.PhaseDone,
			EndedAt: &now,
			Outcome: "accepted",
			Usage:   v1alpha1.Usage{CostMicroUSD: 490_000},
			Stages: []v1alpha1.StageRecord{
				{Stage: "implement", Runs: 1, MicroUSD: 400_000},
				{Stage: "review", Runs: 1, MicroUSD: 90_000, Effect: "approve_agreed"},
			},
		},
	}
	s := catalogueServer(false)
	s.Groups = policy.Groups{Member: "agents-member", Admin: "agents-admin"}
	member := coordHuman
	member.Groups = []string{"agents-member"}
	noRole := coordHuman
	noRole.Groups = nil
	s.Auth = byToken{
		"member":  member,
		"no-role": noRole,
	}
	sch := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(sch)
	s.Rooms = fake.NewClientBuilder().WithScheme(sch).WithObjects(tk).Build()
	s.Namespace = "agent-system"
	s.StagesCache = stages.NewCache(func() time.Time { return now.Time })

	h := s.Handler()

	// Member gets 200 with folded report.
	w := getAs(h, "member", "/v1/stages?repository="+coordPrimary)
	if w.Code != http.StatusOK {
		t.Fatalf("member status = %d body=%s", w.Code, w.Body.String())
	}
	var rep stages.Report
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Repository != coordPrimary || rep.Tasks != 1 || rep.Merged != 1 || len(rep.Classes) != 1 {
		t.Fatalf("rep = %+v", rep)
	}
	if got := rep.Classes[0].Stage("review").Effects["approve_agreed"]; got != 1 {
		t.Fatalf("review effects = %+v", rep.Classes[0].Stage("review").Effects)
	}

	// Caller with no role gets 403.
	if w := getAs(h, "no-role", "/v1/stages?repository="+coordPrimary); w.Code != http.StatusForbidden {
		t.Fatalf("no-role status = %d, want 403", w.Code)
	}

	// last=0 gets 400.
	if w := getAs(h, "member", "/v1/stages?repository="+coordPrimary+"&last=0"); w.Code != http.StatusBadRequest {
		t.Fatalf("last=0 status = %d, want 400", w.Code)
	}

	// Missing repository gets 400.
	if w := getAs(h, "member", "/v1/stages"); w.Code != http.StatusBadRequest {
		t.Fatalf("missing repo status = %d, want 400", w.Code)
	}
}

func TestCatalogueReadsTypicalCostFromWarmCacheOnly(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	s := catalogueServer(false)
	s.StagesCache = stages.NewCache(func() time.Time { return now })

	h := s.Handler()
	// Cold cache: TypicalUSD == 0.
	w := getAs(h, "human", "/v1/catalogue/"+coordPrimary)
	if w.Code != http.StatusOK {
		t.Fatalf("cold status = %d %s", w.Code, w.Body.String())
	}
	var cold catalogue.Catalogue
	if err := json.Unmarshal(w.Body.Bytes(), &cold); err != nil {
		t.Fatal(err)
	}
	for _, cl := range cold.Classes {
		if cl.TypicalUSD != 0 {
			t.Fatalf("cold cache had TypicalUSD=%v for %s", cl.TypicalUSD, cl.Name)
		}
	}

	// Warm cache: TypicalUSD populated.
	s.StagesCache.Store(coordPrimary, stages.Report{
		Repository: coordPrimary,
		Classes:    []stages.ClassReport{{Class: "review", Merged: 34, TypicalUSD: 0.41}},
	})
	w = getAs(h, "human", "/v1/catalogue/"+coordPrimary)
	if w.Code != http.StatusOK {
		t.Fatalf("warm status = %d %s", w.Code, w.Body.String())
	}
	var warm catalogue.Catalogue
	if err := json.Unmarshal(w.Body.Bytes(), &warm); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cl := range warm.Classes {
		if cl.Name == "review" {
			found = true
			if cl.TypicalUSD != 0.41 || cl.TypicalOf != 34 {
				t.Fatalf("warm review class = %+v, want typicalUSD=0.41 typicalOf=34", cl)
			}
		}
	}
	if !found {
		t.Fatalf("review class missing from catalogue: %+v", warm.Classes)
	}
}
