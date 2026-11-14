// SPDX-License-Identifier: Apache-2.0

package config

// The policy-management stages (MR9): each adds to the one before it.
const (
	// StageRead serves the settings pages and the evidence, read-only, and observes each
	// repository's policy file.
	StageRead = "read"
	// StageSimulate adds `maku policy simulate` and `diff`, and dual evaluation.
	StageSimulate = "simulate"
	// StageManage adds promotion, exceptions, approval memory, and classAuthority: repository
	// where a Repository sets it. The default.
	StageManage = "manage"
)

// stages is every stage, in order.
func stages() []string { return []string{StageRead, StageSimulate, StageManage} }

// switched is whether any of the six switches stage replaces is set.
func (pm PolicyManagement) switched() bool {
	return pm.Read || pm.Simulation || pm.DualEvaluation || pm.ApprovalMemory || pm.RepositoryAuthority || pm.Promotion
}

// EffectiveStage is the stage the factory and the broker run at: stage when set; else the highest
// stage the six deprecated switches cover; else manage, which only proposes pull requests.
func (pm PolicyManagement) EffectiveStage() string {
	switch {
	case pm.Stage != "":
		return pm.Stage
	case pm.Promotion || pm.ApprovalMemory || pm.RepositoryAuthority:
		return StageManage
	case pm.Simulation || pm.DualEvaluation:
		return StageSimulate
	case pm.Read:
		return StageRead
	}
	return StageManage
}

// Reads is whether each repository's policy file is observed: every stage.
func (pm PolicyManagement) Reads() bool { return true }

// Simulates is whether simulate, diff and dual evaluation run: simulate and manage.
func (pm PolicyManagement) Simulates() bool { return pm.EffectiveStage() != StageRead }

// Manages is whether promotion, exceptions, approval memory and repository authority run: manage.
func (pm PolicyManagement) Manages() bool { return pm.EffectiveStage() == StageManage }
