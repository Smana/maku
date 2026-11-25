// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"slices"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// DecideRecorded replays a policy over a pull request whose fixed-base evidence was already
// accepted. It restores that evidence's shape for the counterfactual; checks and the diff stay the
// recorded ones, and DecideArmUnder remains the only policy evaluator.
func DecideRecorded(in ArmInputs, cl *config.Class, reviewer string) ArmDecision {
	cfg := in.Cfg
	in.ReportedHead = in.PR.HeadSHA
	in.Task.Status.Verdict = "approve"
	if len(in.Task.Status.Runs) == 0 {
		in.Task.Status.Runs = []v1alpha1.RunRecord{{ID: "recorded", Role: "implementer"}}
	}
	if in.PR.Trailer("Agent-Run") == "" {
		in.PR.HeadMessage += "\n\nAgent-Run: recorded"
	}
	if reviewer != "" && reviewer != "none" && reviewer != "-" {
		in.Verifiers = []v1alpha1.RunRecord{{Role: "reviewer", Verdict: "approve", HeadSHA: in.PR.HeadSHA}}
	}
	for name, t := range cfg.Templates {
		if slices.Contains(t.Roles, "reviewer") {
			in.Task.Spec.Template = name
			break
		}
	}
	if cl != nil && cl.Adopted() {
		in.Adoption = Adoption{Read: true, CommitAuthors: []string{in.PR.Author}, Pushed: pushFactory, LabelledBy: in.PR.Author}
	}
	var approval []string
	if cl != nil {
		approval = approvalPaths(*cl)
	}
	return DecideArmUnder(in, cl, approval)
}
