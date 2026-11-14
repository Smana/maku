// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"reflect"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

// MP16: the evaluator is DecideArm's own rule with the class handed in, so under the central
// class it is DecideArm, decision and record alike, on every input.
func TestDecideArmUnderTheCentralClassIsDecideArm(t *testing.T) {
	for name, mutate := range map[string]func(*ArmInputs){
		"arms":             func(*ArmInputs) {},
		"a protected file": func(in *ArmInputs) { in.Stats[0].Path = "docs/specs/x.md" },
		"21 lines":         func(in *ArmInputs) { in.Stats[0].Additions = 20 },
		"the holdout runs": func(in *ArmInputs) { in.Checks.Runs[2].State = "PENDING" },
		"diff unread":      func(in *ArmInputs) { in.DiffRead, in.Stats = false, nil },
		"not the agent":    func(in *ArmInputs) { in.PR.Author = "someone" },
		"ci red":           func(in *ArmInputs) { in.Checks.Runs[0].State = "FAILURE" },
		"capped":           func(in *ArmInputs) { in.ClassArmedToday = 1 },
		"shadow": func(in *ArmInputs) {
			cl := declaredClass()
			cl.Live, cl.Shadow = false, true
			in.Cfg.Current().Repositories[0].Classes["dead-links"] = cl
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := gateInputs()
			mutate(&in)
			cl := gateOf(in.Cfg.Current().RepositoryFor(""), "dead-links")
			if got, want := DecideArmUnder(in, []config.Class{cl}, nil), DecideArm(in); !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}
}

// any: the class holds when one variant does; a wait outranks a refusal; with none holding, the
// first variant's refusal is the decision's.
func TestDecideArmUnderAny(t *testing.T) {
	strict := declaredClass()
	strict.MaxModifiedLines = 1 // the rig's retarget is two lines
	waits := declaredClass()
	waits.HoldoutChecks = []string{"Never reported"}
	for name, tc := range map[string]struct {
		variants        []config.Class
		verdict, reason string
	}{
		"the second holds":      {[]config.Class{strict, declaredClass()}, "arm", ""},
		"none holds":            {[]config.Class{strict, strict}, "human", "patch_too_large"},
		"a wait over a refusal": {[]config.Class{strict, waits}, "wait", "holdout_pending"},
		"a hold over a wait":    {[]config.Class{waits, declaredClass()}, "arm", ""},
		"no variant: no class":  {nil, "human", "class_unarmed"},
	} {
		t.Run(name, func(t *testing.T) {
			d := DecideArmUnder(gateInputs(), tc.variants, nil)
			if d.Verdict != tc.verdict || d.Reason != tc.reason {
				t.Fatalf("got %s:%s", d.Verdict, d.Reason)
			}
		})
	}
}

// requiresApproval (owner's Q3): a live class asks a human about a pull request in its subset; a
// shadow class records would-require-approval and sends nobody. The subset reads the diff, so it
// holds until the diff is read, as every condition on it does.
func TestDecideArmUnderAnApprovalSubset(t *testing.T) {
	shadow := declaredClass()
	shadow.Live, shadow.Shadow = false, true
	for name, tc := range map[string]struct {
		cl              config.Class
		approval        []string
		mutate          func(*ArmInputs)
		verdict, reason string
	}{
		"live, in the subset":     {declaredClass(), []string{"^website/"}, func(*ArmInputs) {}, "human", "approval_required"},
		"shadow, in the subset":   {shadow, []string{"^website/"}, func(*ArmInputs) {}, "shadow", "shadow_would_require_approval"},
		"live, outside it":        {declaredClass(), []string{"^website/content/security/"}, func(*ArmInputs) {}, "arm", ""},
		"the diff unread":         {declaredClass(), []string{"^website/"}, func(in *ArmInputs) { in.DiffRead, in.Stats = false, nil }, "arm", ""},
		"a refusal stays refused": {declaredClass(), []string{"^website/"}, func(in *ArmInputs) { in.Stats[0].Additions = 20 }, "human", "patch_too_large"},
	} {
		t.Run(name, func(t *testing.T) {
			in := gateInputs()
			tc.mutate(&in)
			d := DecideArmUnder(in, []config.Class{tc.cl}, tc.approval)
			if d.Verdict != tc.verdict || d.Reason != tc.reason {
				t.Fatalf("got %s:%s", d.Verdict, d.Reason)
			}
		})
	}
}

// A rename from a path in the subset is in the subset: the path it leaves is changed too.
func TestTheApprovalSubsetReadsTheRenamedFromPath(t *testing.T) {
	in := gateInputs()
	cl := declaredClass()
	cl.ModifyOnly = false
	in.Stats = []forge.FileStat{{Path: "docs/a.md", Previous: "docs/security/a.md", Status: "renamed"}}
	if d := DecideArmUnder(in, []config.Class{cl}, []string{"^docs/security/"}); d.Reason != "approval_required" {
		t.Fatalf("got %s:%s", d.Verdict, d.Reason)
	}
}
