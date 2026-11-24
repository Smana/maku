// SPDX-License-Identifier: Apache-2.0

package stages

import (
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

func stage(name string, micro int64, effect string) v1alpha1.StageRecord {
	return v1alpha1.StageRecord{Stage: name, Runs: 1, MicroUSD: micro, Effect: effect}
}

func ended(class, outcome string, st ...v1alpha1.StageRecord) v1alpha1.Task {
	phase := v1alpha1.PhaseDone
	switch outcome {
	case "escalated", "failed":
		phase = v1alpha1.PhaseStopped
	case "closed":
		phase = v1alpha1.PhaseClosed
	}
	var total int64
	for _, s := range st {
		total += s.MicroUSD
	}
	now := metav1.Now()
	return v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "aaaaaaaa"},
		Spec:       v1alpha1.TaskSpec{Repository: "owner/repo", PredictedClass: class},
		Status: v1alpha1.TaskStatus{
			Phase:   phase,
			EndedAt: &now,
			Outcome: outcome,
			Usage:   v1alpha1.Usage{CostMicroUSD: total},
			Stages:  st,
		},
	}
}

func TestTheReportFoldsStagesByClass(t *testing.T) {
	tasks := []v1alpha1.Task{
		ended("docs", "accepted", stage("implement", 400_000, ""), stage("review", 90_000, "approve_agreed")),
		ended("docs", "failed", stage("implement", 2_550_000, "")),
	}
	rep := Fold("owner/repo", tasks, 500)
	if rep.Repository != "owner/repo" || rep.Tasks != 2 || rep.Merged != 1 || len(rep.Classes) != 1 {
		t.Fatalf("rep = %+v", rep)
	}
	c := rep.Classes[0]
	if c.Class != "docs" || c.Tasks != 2 || c.Merged != 1 || c.TypicalUSD != 0 {
		t.Fatalf("%+v", c)
	}
	if got := c.Stage("review").Effects["approve_agreed"]; got != 1 {
		t.Fatalf("review effects %+v", c.Stage("review").Effects)
	}
	if got := c.Stage("implement").USD; got != 2.95 {
		t.Fatalf("implement usd = %v, want 2.95", got)
	}
}

func TestATypicalCostNeedsThirtyMergedTasks(t *testing.T) {
	makeTasks := func(n int) []v1alpha1.Task {
		out := make([]v1alpha1.Task, 0, n)
		for i := range n {
			micro := int64(300_000 + i*10_000) // $0.30 .. $0.59 for n=30
			tk := ended("docs", "accepted", stage("implement", micro, ""))
			tk.Name = fmt.Sprintf("a%07d", i)
			out = append(out, tk)
		}
		return out
	}

	rep29 := Fold("owner/repo", makeTasks(29), 500)
	if rep29.Classes[0].TypicalUSD != 0 {
		t.Fatalf("29 merged gave TypicalUSD = %v, want 0", rep29.Classes[0].TypicalUSD)
	}

	rep30 := Fold("owner/repo", makeTasks(30), 500)
	// Sorted $0.30..$0.59: middle two are $0.44 and $0.45 -> median $0.445 -> rounded to $0.45.
	if got := rep30.Classes[0].TypicalUSD; got != 0.45 {
		t.Fatalf("30 merged TypicalUSD = %v, want 0.45", got)
	}
	if got := rep30.Classes[0].P90USD; got != 0.56 {
		t.Fatalf("30 merged P90USD = %v, want 0.56", got)
	}
}

func TestCacheColdReturnsZeroAndWarmReturnsTypical(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	c := NewCache(func() time.Time { return now })
	if usd, of, ok := c.Typical("owner/repo", "docs"); ok || usd != 0 || of != 0 {
		t.Fatalf("cold cache returned (%v, %d, %v)", usd, of, ok)
	}
	c.Store("owner/repo", Report{
		Repository: "owner/repo",
		Classes:    []ClassReport{{Class: "docs", Merged: 35, TypicalUSD: 0.41}},
	})
	if usd, of, ok := c.Typical("owner/repo", "docs"); !ok || usd != 0.41 || of != 35 {
		t.Fatalf("warm cache returned (%v, %d, %v), want (0.41, 35, true)", usd, of, ok)
	}
	now = now.Add(CacheTTL + time.Second)
	if _, _, ok := c.Typical("owner/repo", "docs"); ok {
		t.Fatal("expired cache still returned ok")
	}
}
