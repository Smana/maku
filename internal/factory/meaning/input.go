// SPDX-License-Identifier: Apache-2.0

package meaning

import "github.com/Smana/maku/internal/factory/config"

// ModeOf is a resolved class's mode: live, shadow, else disabled.
func ModeOf(cl config.Class) string {
	switch {
	case cl.Live:
		return config.ModeLive
	case cl.Shadow:
		return config.ModeShadow
	}
	return config.ModeDisabled
}

// For is the Input of repository rc's class name (its binding of the policy, resolved), under the
// rules in force; false when rc declares no such class.
func For(rc *config.Repository, name string) (Input, bool) {
	cl, ok := rc.Class(name)
	if !ok {
		return Input{}, false
	}
	return Input{Name: name, Class: cl, Mode: ModeOf(cl), VerifyFor: rc.Merge.VerifyFor.Duration,
		RevertWindow: rc.Merge.RevertWindow.Duration, AutoRevert: rc.Merge.AutoRevert, Rules: InForce}, true
}

// OfRepository is the sentence of repository rc's class name, nil when rc declares no such class.
func OfRepository(rc *config.Repository, name string) *Sentence {
	in, ok := For(rc, name)
	if !ok {
		return nil
	}
	s := Build(in)
	return &s
}

// OfPolicy is the sentence of a policy read without a repository: its own fields, no mode.
func OfPolicy(name string, spec config.PolicySpec) Sentence {
	return Build(Input{Name: name, Class: spec.Class(), Rules: InForce})
}
