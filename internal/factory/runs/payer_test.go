// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// Spend per identity SI1: one payer per run, the person it is charged to beside the fleet, else its
// own principal; and one kind, task or room.
func TestARunHasOnePayerAndOneKind(t *testing.T) {
	for name, c := range map[string]struct {
		run         Run
		payer, kind string
	}{
		"a labelled task's run":       {Run{TaskID: "3buqdlot", Principal: PrincipalFactory, ChargedTo: "github:alice"}, "github:alice", KindTask},
		"a schedule's run":            {Run{TaskID: "4buqdlot", Principal: PrincipalFactory}, PrincipalFactory, KindTask},
		"a linked human's API run":    {Run{Principal: "human:291", ChargedTo: "github:bob"}, "github:bob", KindRoom},
		"an unlinked human's API run": {Run{Principal: "human:292", ChargedTo: "human:292"}, "human:292", KindRoom},
		"per-person caps off":         {Run{Principal: "human:293", RoomRef: "5buqdlot"}, "human:293", KindRoom},
	} {
		t.Run(name, func(t *testing.T) {
			if got := c.run.Payer(); got != c.payer {
				t.Errorf("payer %q, want %q", got, c.payer)
			}
			if got := c.run.Kind(); got != c.kind {
				t.Errorf("kind %q, want %q", got, c.kind)
			}
		})
	}
}

// SI1: a task's payer is the key its next run is charged to beside the fleet: the retrier, else the
// labeller, while per-person caps are on; the fleet for a task no person started, or with them off.
func TestATaskPaysAsItsNextRunWouldBeCharged(t *testing.T) {
	labelled := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{RequestedBy: "github:Alice"}}}
	retried := labelled.DeepCopy()
	retried.Status.RetriedBy = "github:bob"
	scheduled := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{RequestedBy: "system:scheduler"}}}
	cronRoutine := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, RequestedBy: "system:scheduler"}}}
	manualRoutine := &v1alpha1.Task{Spec: v1alpha1.TaskSpec{Source: v1alpha1.Source{Kind: v1alpha1.SourceKindRoutine, RequestedBy: "github:Alice"}}}
	retriedRoutine := cronRoutine.DeepCopy()
	retriedRoutine.Status.RetriedBy = "github:bob"
	for name, c := range map[string]struct {
		task *v1alpha1.Task
		on   bool
		want string
	}{
		"labelled":                  {labelled, true, "github:alice"},
		"retried":                   {retried, true, "github:bob"},
		"scheduled":                 {scheduled, true, PrincipalFactory},
		"labelled, caps off":        {labelled, false, PrincipalFactory},
		"cron routine, caps off":    {cronRoutine, false, "system:upkeep"},
		"cron routine, caps on":     {cronRoutine, true, "system:upkeep"},
		"manual routine, caps on":   {manualRoutine, true, "github:alice"},
		"manual routine, caps off":  {manualRoutine, false, "system:upkeep"},
		"retried routine, caps on":  {retriedRoutine, true, "github:bob"},
		"retried routine, caps off": {retriedRoutine, false, "system:upkeep"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := TaskPayer(c.task, c.on); got != c.want {
				t.Fatalf("%q, want %q", got, c.want)
			}
		})
	}
}
