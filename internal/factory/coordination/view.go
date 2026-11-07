// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"fmt"
	"slices"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// RunUsage is one run's spend as the day ledger holds it: what its reservation still holds and what
// its meter settled.
type RunUsage struct {
	ReservedMicroUSD int64 `json:"reservedMicroUSD"`
	SettledMicroUSD  int64 `json:"settledMicroUSD"`
}

// LedgerUsage is the ledger's per-run spend, keyed by run id; a run it does not hold counts zero.
type LedgerUsage struct {
	ByRun map[string]RunUsage `json:"byRun"`
}

func (l LedgerUsage) of(runs []v1alpha1.RunRecord) (reserved, settled int64) {
	for _, r := range runs {
		u := l.ByRun[r.ID]
		reserved += u.ReservedMicroUSD
		settled += u.SettledMicroUSD
	}
	return reserved, settled
}

// View derives a parent's whole CoordinationView from the parent Task, its implementation children
// (by LabelCoordinationParent), its integration Tasks and the ledger's per-run spend. It is
// deterministic and UNFILTERED: an experience consumer must filter every child by the viewer's
// GitHub read before exposing anything (MR13). No human route serves this value.
func View(parent v1alpha1.Task, children, integrations []v1alpha1.Task, ledger LedgerUsage) CoordinationView {
	v := CoordinationView{
		ParentTask:          parent.Name,
		Phase:               parent.Status.Phase,
		Reason:              parent.Status.Reason,
		ChildAuthorizations: []ChildAuthorization{},
		Children:            []CoordinationChildView{},
		Artifacts:           []v1alpha1.LandedArtifact{},
		IntegrationAttempts: []IntegrationAttemptView{},
		MergeOrder:          []string{},
	}
	cs := parent.Status.Coordination
	if cs == nil || cs.Plan == nil {
		return v
	}
	v.Plan, v.PlanHash = *cs.Plan.DeepCopy(), cs.PlanHash
	for i := len(cs.Confirmations) - 1; i >= 0; i-- {
		if cs.Confirmations[i].PlanHash == cs.PlanHash {
			c := cs.Confirmations[i]
			v.ParentConfirmation = &c
			break
		}
	}
	v.ChildAuthorizations = append(v.ChildAuthorizations, cs.Authorizations...)
	v.Artifacts = append(v.Artifacts, cs.Artifacts...)
	v.MergeOrder = append(v.MergeOrder, cs.MergeOrder...)
	v.Recovery = cs.Recovery.DeepCopy()
	v.PostCompletionIncidents = append(v.PostCompletionIncidents, cs.Incidents...)

	v.Budget = CoordinationBudgetView{
		PlanningMicroUSD:   cs.Plan.Budget.PlanningMicroUSD,
		TotalMicroUSD:      cs.Plan.Budget.TotalMicroUSD,
		IntegrationMaxRuns: int(cs.Plan.Integration.MaxRuns),
	}
	if cs.Budget != nil {
		v.Budget.AllocatedMicroUSD = cs.Budget.AllocatedMicroUSD
	}

	byName := map[string]*v1alpha1.Task{}
	for i := range children {
		byName[children[i].Name] = &children[i]
	}
	for _, c := range cs.Plan.Children {
		cv := CoordinationChildView{
			ID: c.ID, Repository: c.Repository, Fingerprint: ChildFingerprint(*cs.Plan, c.ID),
			DependsOn: append([]string{}, c.DependsOn...), Consumes: []string{},
			AllocationMicroUSD: c.BudgetMicroUSD,
		}
		for _, k := range c.Consumes {
			cv.Consumes = append(cv.Consumes, k.From+"/"+k.Artifact)
		}
		if i := slices.IndexFunc(cs.Children, func(s v1alpha1.CoordinationChildStatus) bool { return s.ID == c.ID }); i >= 0 {
			s := cs.Children[i]
			cv.Task, cv.Phase, cv.Reason = s.Task, s.Phase, s.Reason
			if s.Issue > 0 {
				cv.IssueURL = fmt.Sprintf("https://github.com/%s/issues/%d", c.Repository, s.Issue)
			}
			if s.AllocationMicroUSD > 0 {
				cv.AllocationMicroUSD = s.AllocationMicroUSD
			}
			cv.IntegrationRelease = s.IntegrationRelease.DeepCopy()
		}
		if t := byName[cv.Task]; t != nil {
			cv.Room, cv.Phase, cv.Reason = t.Status.RoomRef, t.Status.Phase, t.Status.Reason
			if pr := t.Status.PullRequest; pr != nil {
				cv.PRURL, cv.HeadSHA, cv.MergeCommitSHA = pr.URL, pr.HeadSHA, pr.MergeCommitSHA
			}
			if lc := t.Status.Coordination; lc != nil {
				cv.IntegrationAcceptedHead = lc.IntegrationAcceptedHead
			}
			cv.ReservedMicroUSD, cv.SettledMicroUSD = ledger.of(t.Status.Runs)
		}
		v.Budget.ChildrenReservedMicroUSD += cv.ReservedMicroUSD
		v.Budget.ChildrenSettledMicroUSD += cv.SettledMicroUSD
		v.Children = append(v.Children, cv)
	}

	byInt := map[string]*v1alpha1.Task{}
	for i := range integrations {
		byInt[integrations[i].Name] = &integrations[i]
	}
	for _, a := range cs.IntegrationAttempts {
		av := IntegrationAttemptView{
			Task: a.Task, Purpose: a.Purpose, PlanHash: a.PlanHash, ManifestHash: a.ManifestHash,
			State: a.State, Reason: a.Reason, Subjects: append([]v1alpha1.IntegrationSubject{}, a.Subjects...),
			RunMicroUSD: a.RunMicroUSD, CostMicroUSD: a.CostMicroUSD,
		}
		if t := byInt[a.Task]; t != nil {
			reserved, settled := ledger.of(t.Status.Runs)
			v.Budget.IntegrationReservedMicroUSD += reserved
			v.Budget.IntegrationSettledMicroUSD += settled
			v.Budget.IntegrationRuns += len(t.Status.Runs)
			if r := t.Status.Result; r != nil && r.ManifestHash == a.ManifestHash {
				av.State, av.Reason = r.State, r.Reason
			}
		}
		v.IntegrationAttempts = append(v.IntegrationAttempts, av)
	}
	return v
}
