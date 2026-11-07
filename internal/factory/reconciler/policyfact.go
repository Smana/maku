// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"maps"
	"slices"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
)

// policyOf is the merge policy that governs t, for its room (merge policy view D1): its class, the
// class's mode and conditions as the gate reads them (gateOf), and the repository's class table,
// clamped to the envelope's bounds so the broker never refuses the facts for good (D3). Nil when
// the repository declares no class and the task names none: there is no policy to show.
func policyOf(cfg *config.Config, rc *config.Repository, t *v1alpha1.Task) *envelope.PolicyFact {
	class := t.Spec.PredictedClass
	if rc == nil || (len(rc.Classes) == 0 && class == "") {
		return nil
	}
	cl := gateOf(rc, class)
	_, declared := rc.Classes[class]
	gateOn := cfg.NeedsMerger()
	// With the gate off the hand-off (merge_gate_off) comes before any approval is read, so no
	// approval merges; an adopted class keeps the class path (AM1).
	p := &envelope.PolicyFact{Mode: modeOf(cl), GateOff: !gateOn, Adopted: cl.Adopted(),
		ApproveToMerge: gateOn && rc.Merge.ApproveToMerge && !cl.Adopted()}
	if envelope.ValidClassName(class) {
		p.Class = class
	} else if class != "" {
		p.Truncated = true
	}
	if declared || cl.DiffShape != "" {
		p.Conditions = conditionsOf(cl, &p.Truncated)
	}
	if pr := t.Status.PullRequest; pr != nil {
		p.MergedHow = pr.MergedHow
	}
	for _, name := range slices.Sorted(maps.Keys(rc.Classes)) {
		if len(p.Classes) == envelope.MaxPolicyClasses || !envelope.ValidClassName(name) {
			p.Truncated = true
			continue
		}
		c := rc.Classes[name]
		p.Classes = append(p.Classes, envelope.PolicyClass{Name: name, Mode: modeOf(c), Adopted: c.Adopted()})
	}
	return p
}

// modeOf is the gate's outcome for a class: live arms, shadow decides and never arms, neither is
// a maintainer's merge (R32).
func modeOf(c config.Class) string {
	switch {
	case c.Live:
		return envelope.PolicyLive
	case c.Shadow:
		return envelope.PolicyShadow
	}
	return envelope.PolicyUnarmed
}

func conditionsOf(cl config.Class, cut *bool) *envelope.PolicyConditions {
	clip := func(l []string) []string {
		out, c := envelope.Clip(l)
		*cut = *cut || c
		return out
	}
	c := &envelope.PolicyConditions{AllowedFiles: clip(cl.AllowedFiles), ProtectedFiles: clip(cl.ProtectedFiles),
		ModifyOnly: cl.ModifyOnly, MaxPatchFiles: cl.MaxPatchFiles, MaxModifiedLines: cl.MaxModifiedLines,
		DiffShape: cl.DiffShape, HoldoutChecks: clip(cl.HoldoutChecks), MaxPerDay: cl.MaxPerDay,
		RequiredLabel: cl.RequiredLabel, Oracle: cl.Oracle != nil}
	if cl.Adopted() {
		c.AdoptLogins = clip(cl.Author.Adopt.Logins)
	}
	return c
}
