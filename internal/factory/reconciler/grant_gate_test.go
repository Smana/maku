// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/grant"
)

const grantPolicy = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// activeGrant covers gateInputs' one file, in its repository and class, under grantPolicy.
func activeGrant() grant.Grant {
	return grant.Grant{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaa", Version: 2, Repository: "Smana/Docs", Class: "dead-links",
		Files: []string{`website/content/docs/[a-z]+\.md`}, PolicyHash: grantPolicy[:12], Status: grant.StatusActive,
		ExpiresAt: now.Add(time.Hour)}
}

// grantInputs are gateInputs in repository smana/docs, its grants read at now under grantPolicy.
func grantInputs(gs ...grant.Grant) ArmInputs {
	in := gateInputs()
	in.Task.Spec.Repository = "smana/docs"
	in.Cfg.Current().Repository = "smana/docs" // the task's repository is served: its one entry
	in.Grants = GrantRead{Read: true, List: gs, PolicyHash: grantPolicy, At: now}
	return in
}

// Approval memory (merge policy stage 5, MP9, MP10): in a live class, an active grant that covers
// every file the approval subset caught lets the decision arm, and the record names it. Anything
// short of that is the human approval it was; a shadow class never consults a grant.
func TestAGrantArmsInsideTheApprovalSubset(t *testing.T) {
	subset := []string{"^website/"}
	shadow := declaredClass()
	shadow.Live, shadow.Shadow = false, true
	for name, tc := range map[string]struct {
		cl              config.Class
		in              func() ArmInputs
		verdict, reason string
		grant           string
	}{
		"a covering grant arms": {declaredClass(), func() ArmInputs { return grantInputs(activeGrant()) }, "arm", "", "aaaaaaaaaaaaaaaaaaaaaaaaaa:2"},
		"no grant":              {declaredClass(), func() ArmInputs { return grantInputs() }, "human", "approval_required", ""},
		"a failed lookup": {declaredClass(), func() ArmInputs {
			in := grantInputs(activeGrant())
			in.Grants.Read = false // MP13: whatever was listed, an unread lookup never authorizes
			return in
		}, "human", "approval_required", ""},
		"a shadow class": {shadow, func() ArmInputs { return grantInputs(activeGrant()) }, "shadow", "shadow_would_require_approval", ""},
		"suspended": {declaredClass(), func() ArmInputs {
			g := activeGrant()
			g.Status = grant.StatusSuspended
			return grantInputs(g)
		}, "human", "approval_required", ""},
		"expired at the read": {declaredClass(), func() ArmInputs {
			g := activeGrant()
			g.ExpiresAt = now
			return grantInputs(g)
		}, "human", "approval_required", ""},
		"another policy": {declaredClass(), func() ArmInputs {
			g := activeGrant()
			g.PolicyHash = "ffffffffffff"
			return grantInputs(g)
		}, "human", "approval_required", ""},
		"another class": {declaredClass(), func() ArmInputs {
			g := activeGrant()
			g.Class = "docs-links"
			return grantInputs(g)
		}, "human", "approval_required", ""},
		"another repository": {declaredClass(), func() ArmInputs {
			g := activeGrant()
			g.Repository = "smana/other"
			return grantInputs(g)
		}, "human", "approval_required", ""},
		"a file outside it": {declaredClass(), func() ArmInputs {
			g := activeGrant()
			g.Files = []string{`website/content/blog/.+`}
			return grantInputs(g)
		}, "human", "approval_required", ""},
		"its shape holds": {declaredClass(), func() ArmInputs {
			g := activeGrant()
			g.DiffShape = "links-only"
			return grantInputs(g)
		}, "arm", "", "aaaaaaaaaaaaaaaaaaaaaaaaaa:2"},
		"its shape fails": {declaredClass(), func() ArmInputs {
			g := activeGrant()
			g.DiffShape = "semver-patch"
			return grantInputs(g)
		}, "human", "approval_required", ""},
		"the lowest id of two": {declaredClass(), func() ArmInputs {
			g := activeGrant()
			g.ID = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
			return grantInputs(g, activeGrant())
		}, "arm", "", "aaaaaaaaaaaaaaaaaaaaaaaaaa:2"},
	} {
		t.Run(name, func(t *testing.T) {
			d := DecideArmUnder(tc.in(), []config.Class{tc.cl}, subset)
			if d.Verdict != tc.verdict || d.Reason != tc.reason || d.Rows.Grant != tc.grant {
				t.Fatalf("got %s:%s grant=%q", d.Verdict, d.Reason, d.Rows.Grant)
			}
		})
	}
}

// MP9: a grant never broadens its class. A refusal of the class stays refused, a cap stays a cap,
// and outside the subset there is nothing for a grant to do.
func TestAGrantNeverBypassesTheClass(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate          func(*ArmInputs)
		approval        []string
		verdict, reason string
	}{
		"too many lines": {func(in *ArmInputs) { in.Stats[0].Additions = 20 }, []string{"^website/"}, "human", "patch_too_large"},
		"capped":         {func(in *ArmInputs) { in.ClassArmedToday = 1 }, []string{"^website/"}, "human", "auto_merge_cap"},
		"paused":         {func(in *ArmInputs) { in.Paused = true }, []string{"^website/"}, "human", "class_paused"},
		"outside the subset, no grant read": {func(in *ArmInputs) { in.Grants = GrantRead{} },
			[]string{"^docs/security/"}, "arm", ""},
	} {
		t.Run(name, func(t *testing.T) {
			in := grantInputs(activeGrant())
			tc.mutate(&in)
			d := DecideArmUnder(in, []config.Class{declaredClass()}, tc.approval)
			if d.Verdict != tc.verdict || d.Reason != tc.reason || d.Rows.Grant != "" {
				t.Fatalf("got %s:%s grant=%q", d.Verdict, d.Reason, d.Rows.Grant)
			}
		})
	}
}
