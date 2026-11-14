// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"encoding/json"
	"fmt"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/mergepolicy"
)

// CaseVersion is the conformance fixtures' format. A change to ArmInputs that reads an old fixture
// differently bumps it, and the fixtures are rewritten against the new one.
const CaseVersion = 1

// Case is one decision as a fixture (merge policy MP16): its inputs, what GitHub recorded of the
// head's pushes (the final provenance row), and the decision the central config makes on them.
// The config is the caller's: a fixture holds none.
type Case struct {
	Version int       `json:"version"`
	Name    string    `json:"name"`
	Inputs  ArmInputs `json:"inputs"`
	// Pushed is factory, stranger or unrecorded: factoryPushed's answer on an agents pull request.
	Pushed string  `json:"pushed"`
	Want   Outcome `json:"want"`
}

// Outcome is a decision's verdict and reason.
type Outcome struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"`
}

// ReadCase decodes one fixture strictly.
func ReadCase(raw []byte) (Case, error) {
	var c Case
	if err := json.Unmarshal(raw, &c); err != nil {
		return Case{}, fmt.Errorf("case: %w", err)
	}
	if c.Version != CaseVersion {
		return Case{}, fmt.Errorf("case %q: version %d, this build reads %d", c.Name, c.Version, CaseVersion)
	}
	if c.Inputs.Task == nil {
		return Case{}, fmt.Errorf("case %q: inputs.Task is required", c.Name)
	}
	if _, ok := pushes[c.Pushed]; !ok {
		return Case{}, fmt.Errorf("case %q: pushed %q is factory, stranger or unrecorded", c.Name, c.Pushed)
	}
	return c, nil
}

var pushes = map[string]pushCheck{"factory": pushFactory, "stranger": pushStranger, "unrecorded": pushUnrecorded}

// The changes a simulation names, the spec's impact preview.
const (
	ChangeUnchanged        = "unchanged"
	ChangeNewlyMerged      = "newly_auto_merged"
	ChangeNewlyBlocked     = "newly_blocked"
	ChangeNewlyApproval    = "newly_approval_required"
	ChangeOther            = "changed"
	reasonApprovalRequired = "approval_required"
	reasonShadowApproval   = "shadow_would_require_approval"
)

// Row is one case simulated: the central decision, the repository policy's, and the change.
// Conforms is false when the central decision is no longer the fixture's: the runtime moved, and
// the simulation of that case proves nothing until the fixture is rewritten.
type Row struct {
	Name, Class         string
	Central, Repository Outcome
	Change              string
	Conforms            bool
}

// Simulate decides each case twice with the gate's own evaluator, under cfg's central class and
// under policy's, and names the change. It has no side effect: nothing is read from GitHub.
func Simulate(cfg *config.Config, policy map[string]mergepolicy.Resolved, cases []Case) []Row {
	out := make([]Row, 0, len(cases))
	for _, c := range cases {
		in := c.Inputs
		in.Cfg = cfg
		class := in.Task.Spec.PredictedClass
		cl := gateOf(cfg.RepositoryFor(in.Task.Spec.Repository), class)
		pushed := pushes[c.Pushed]
		central := provenance(DecideArm(in), cl.Adopted(), pushed)
		var repo ArmDecision
		if r, ok := policy[class]; ok {
			repo = provenance(DecideArmUnder(in, &r.Class, r.Approval), r.Class.Adopted(), pushed)
		} else {
			repo = DecideArmUnder(in, nil, nil)
		}
		a, b := outcome(central), outcome(repo)
		out = append(out, Row{Name: c.Name, Class: class, Central: a, Repository: b, Change: change(a, b), Conforms: a == c.Want})
	}
	return out
}

func outcome(d ArmDecision) Outcome { return Outcome{Verdict: d.Verdict, Reason: d.Reason} }

// change names how the repository's decision differs from the central one.
func change(central, repo Outcome) string {
	classArm := func(o Outcome) bool { return o.Verdict == "arm" && o.Reason == "" }
	switch {
	case central == repo:
		return ChangeUnchanged
	case repo.Reason == reasonApprovalRequired || repo.Reason == reasonShadowApproval:
		return ChangeNewlyApproval
	case classArm(repo):
		return ChangeNewlyMerged
	case classArm(central):
		return ChangeNewlyBlocked
	}
	return ChangeOther
}
