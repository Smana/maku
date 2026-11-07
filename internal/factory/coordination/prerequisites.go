// SPDX-License-Identifier: Apache-2.0

// Package coordination is the multi-repository coordination core (v4 design MR1–MR17): one
// confirmed change across exactly two public repositories, each child under its own repository's
// authority. It holds the neutral data contracts, the execute gate, the plan's strict decoding and
// deterministic validation, its canonical hash and child fingerprints, and the fixed materiality
// table (MR12); the planner tool and the controllers come later, switched off by default.
package coordination

import (
	"context"
	"errors"
	"fmt"
)

// ReasonPrerequisites is the bounded reason an execute-time admission refused for missing or
// regressed capabilities (MR16). plan stays available.
const ReasonPrerequisites = "coordination_prerequisites"

// ErrPrerequisites is the refusal ReadyForExecute wraps; callers branch on it with errors.Is.
var ErrPrerequisites = errors.New(ReasonPrerequisites)

// ErrNotExecuting is GateExecute's refusal under off or plan: nothing is filed and nothing runs.
var ErrNotExecuting = errors.New("coordination_not_executing")

// modeExecute is config.CoordinationExecute; the gate reads the configured string as is.
const modeExecute = "execute"

// ExecutionPrerequisites proves, for one repository, the two capabilities execute needs (MR16):
// repository-declared class authority (v4 item 3) and hardened safe outputs with threat checks for
// every child and integration input (v4 item 4). It is a capability check, never a release string
// or the stage-1 mergepolicy.Watch observation.
type ExecutionPrerequisites interface {
	Ready(ctx context.Context, repository string) (repositoryClasses bool, hardenedOutputs bool, err error)
}

// ReadyForExecute asks p about every repository, at every admission, and refuses with
// ErrPrerequisites on a false, an error, a nil port or no repository at all: nothing is cached, so
// a regression refuses the next admission.
func ReadyForExecute(ctx context.Context, p ExecutionPrerequisites, repositories []string) error {
	if p == nil {
		return fmt.Errorf("%w: no prerequisite port", ErrPrerequisites)
	}
	if len(repositories) == 0 {
		return fmt.Errorf("%w: no repository to prove", ErrPrerequisites)
	}
	for _, r := range repositories {
		classes, hardened, err := p.Ready(ctx, r)
		switch {
		case err != nil:
			return fmt.Errorf("%w: %s: %w", ErrPrerequisites, r, err)
		case !classes:
			return fmt.Errorf("%w: %s: class authority is not repository-declared", ErrPrerequisites, r)
		case !hardened:
			return fmt.Errorf("%w: %s: safe outputs are not hardened", ErrPrerequisites, r)
		}
	}
	return nil
}

// GateExecute is the execute gate by mode: off and plan refuse with ErrNotExecuting without
// consulting p; execute is ReadyForExecute.
func GateExecute(ctx context.Context, mode string, p ExecutionPrerequisites, repositories []string) error {
	if mode != modeExecute {
		return ErrNotExecuting
	}
	return ReadyForExecute(ctx, p, repositories)
}
