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

// EffectiveStage is the stage the factory and the broker run at: stage when set, else manage,
// which only proposes pull requests.
func (pm PolicyManagement) EffectiveStage() string {
	if pm.Stage != "" {
		return pm.Stage
	}
	return StageManage
}

// Reads is whether each repository's policy file is observed: every stage.
func (pm PolicyManagement) Reads() bool { return true }

// Simulates is whether simulate, diff and dual evaluation run: simulate and manage.
func (pm PolicyManagement) Simulates() bool { return pm.EffectiveStage() != StageRead }

// Manages is whether promotion, exceptions, approval memory and repository authority run: manage.
func (pm PolicyManagement) Manages() bool { return pm.EffectiveStage() == StageManage }
