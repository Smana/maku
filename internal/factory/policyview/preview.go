// SPDX-License-Identifier: Apache-2.0

// Package policyview is the settings editor's server side (merge policy stage 6): a proposed
// policy file checked strictly against the central config, its structural change from the
// repository's current policy (MP8), its simulation on the conformance fixtures with the gate's own
// evaluator (MP16), and a decision's timeline from its record. It decides nothing and writes
// nothing: the maintainer submits the file as their own pull request (MP1).
package policyview

import (
	"errors"
	"strings"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/reconciler"
)

// Change is one class's structural verdict, as the page shows it.
type Change struct {
	Class string   `json:"class"`
	Kind  string   `json:"kind"`
	Why   []string `json:"why,omitempty"`
}

// SimRow is one fixture decided under the central config and under the proposed policy.
type SimRow struct {
	Name       string             `json:"name"`
	Class      string             `json:"class"`
	Central    reconciler.Outcome `json:"central"`
	Repository reconciler.Outcome `json:"repository"`
	Change     string             `json:"change"`
	// Conforms is false when the fixture recorded another central decision than this config makes:
	// that row illustrates nothing until the fixture is rewritten.
	Conforms bool `json:"conforms"`
}

// Simulation is the impact preview. Enabled is policyManagement.simulation: off (a simulator that
// disagreed with the gate) shows no rows rather than rows nobody may trust.
type Simulation struct {
	Enabled bool           `json:"enabled"`
	Rows    []SimRow       `json:"rows,omitempty"`
	Counts  map[string]int `json:"counts,omitempty"`
}

// Strictness is the counterfactual of the class's current gate: the newest decisions a class row
// refused, and those a human then merged unchanged at that head (the tally's --strictness).
type Strictness struct {
	Class                  string `json:"class"`
	Refused                int    `json:"refused"`
	RefusedMergedUnchanged int    `json:"refusedMergedUnchanged"`
}

// Preview is a proposed policy file's check. Valid false lists the errors and nothing else.
type Preview struct {
	Valid      bool         `json:"valid"`
	Errors     []string     `json:"errors,omitempty"`
	Structural []Change     `json:"structural,omitempty"`
	Simulation Simulation   `json:"simulation"`
	Strictness []Strictness `json:"strictness,omitempty"`
	// Proposal is the file to submit and the GitHub page to submit it from.
	Proposal *evidence.Proposal `json:"proposal,omitempty"`
}

// ErrUnchanged is a proposal identical to the file on the default branch.
var ErrUnchanged = errors.New("the proposal is the file as it is")

// Check previews file for the repository rc, whose current state rep is: strict parsing, the
// factory it must keep naming, its bindings resolved under the central guardrails and profiles
// (ValidateFile), the structural change from the file's current bindings, and the simulation. Errors are the parser's and validator's own messages: the file
// is the caller's, so they may read about it.
func Check(cfg *config.Config, rc *config.Repository, rep evidence.Report, file string) Preview {
	bad := func(msgs ...string) Preview { return Preview{Errors: msgs} }
	if strings.TrimSpace(file) == strings.TrimSpace(rep.File) {
		return bad(ErrUnchanged.Error())
	}
	next, err := mergepolicy.Parse([]byte(file))
	if err != nil {
		return bad(strings.Split(err.Error(), "\n")...)
	}
	if next.Factory != cfg.Factory {
		return bad("factory: is read-only here: it must stay " + cfg.Factory)
	}
	checked, err := mergepolicy.ValidateFile(next, rc, cfg)
	if err != nil {
		return bad(strings.Split(err.Error(), "\n")...)
	}
	policy := checked.Classes
	pv := Preview{Valid: true, Proposal: &evidence.Proposal{Path: evidence.PolicyPath, File: file,
		EditURL: evidence.EditURL(rc.Name, rep.DefaultBranch)}}
	if old := current(cfg, rep); old != nil {
		if cs, err := mergepolicy.Structural(old, next, rc, cfg); err == nil {
			for _, c := range cs {
				pv.Structural = append(pv.Structural, Change{Class: c.Class, Kind: c.Kind, Why: c.Why})
			}
		} else {
			pv.Errors = append(pv.Errors, "structural comparison unavailable: "+err.Error())
		}
	}
	pv.Simulation = simulate(cfg, rc, policy)
	for _, c := range rep.Classes {
		if c.Evidence == nil {
			continue // still being read: the strictness is the evidence's
		}
		pv.Strictness = append(pv.Strictness, Strictness{Class: c.Class, Refused: c.Evidence.Shadow.Refused,
			RefusedMergedUnchanged: c.Evidence.Shadow.RefusedMergedUnchanged})
	}
	return pv
}

// current is the policy the proposal changes: the file's bindings when it declares any and is
// valid; nil otherwise, and there is no structural change to show.
func current(cfg *config.Config, rep evidence.Report) *mergepolicy.File {
	if rep.Policy != nil && rep.Policy.Declares() && mergepolicy.Validate(rep.Policy, cfg) == nil {
		return rep.Policy
	}
	return nil
}

func simulate(cfg *config.Config, rc *config.Repository, policy map[string]mergepolicy.Resolved) Simulation {
	if !cfg.PolicyManagement.Simulation {
		return Simulation{}
	}
	cases, err := reconciler.ConformanceCases(rc.Name)
	if err != nil {
		return Simulation{}
	}
	s := Simulation{Enabled: true, Counts: map[string]int{}}
	for _, r := range reconciler.Simulate(cfg, policy, cases) {
		s.Rows = append(s.Rows, SimRow{Name: r.Name, Class: r.Class, Central: r.Central, Repository: r.Repository,
			Change: r.Change, Conforms: r.Conforms})
		s.Counts[r.Change]++
	}
	return s
}

// Changes are the simulation's change names, in the order the page lists them.
func Changes() []string {
	return []string{reconciler.ChangeNewlyMerged, reconciler.ChangeNewlyBlocked, reconciler.ChangeNewlyApproval,
		reconciler.ChangeUnchanged, reconciler.ChangeOther}
}
