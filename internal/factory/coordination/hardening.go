// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"

	"github.com/Smana/maku/internal/hardening"
)

// ClassAuthority answers whether repository's class policy is authoritative (merge policy
// self-service, stage 4): the first half of ExecutionPrerequisites. No production one exists yet.
type ClassAuthority interface {
	Authoritative(ctx context.Context, repository string) (bool, error)
}

// Prerequisites is the production ExecutionPrerequisites: class authority composed with hardening
// readiness (hardening design, execution prerequisites). It adds nothing to ReadyForExecute's
// fail-closed contract, and a Report's Missing never reaches the refusal text.
type Prerequisites struct {
	Classes   ClassAuthority
	Hardening hardening.Readiness
}

// Ready asks both halves afresh.
func (p Prerequisites) Ready(ctx context.Context, repository string) (bool, bool, error) {
	classes, err := p.Classes.Authoritative(ctx, repository)
	if err != nil {
		return false, false, err
	}
	r, err := p.Hardening.Hardened(ctx, repository)
	if err != nil {
		return classes, false, err
	}
	return classes, r.Hardened, nil
}
