// SPDX-License-Identifier: Apache-2.0

package triage

import (
	"slices"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/intake"
)

// LabelPin is one workflow:<name> label on a task's issue and its last applier.
type LabelPin struct {
	Name, Actor string
}

// Pin is a task's pinned workflow as triage resolved it: Name is the workflow a person asked for,
// "" when none did; Refused is a code of intake's closed list, "" when the pin is honoured.
type Pin struct {
	Name, Refused string
}

// Honoured is whether the pin stands: a person chose the profile and the factory agreed.
func (p Pin) Honoured() bool { return p.Name != "" && p.Refused == "" }

// NoTestsPaths is p refused because its repository declares no tests paths: the check that reads
// the repository, which the reconciler makes on an honoured tests-first pin.
func (p Pin) NoTestsPaths() Pin {
	p.Refused = intake.ProfileNoTestsPaths
	return p
}

// ResolvePin is the factory's check of a pin (P6), the spec's Triage steps 2 to 4 against config:
// one workflow named, by a maintainer (a label's last applier, or the intake's resolved sponsor
// login), and a workflow the factory defines. Since the workflow left the class (WF4), a maintainer
// may pin any workflow on any class, one below the repository's default included: the gate holds an
// unreviewed change for a human. The tests-paths check reads the repository, so the reconciler makes
// it on an honoured pin that runs the tester. intake.CheckPin is the same rule against the
// catalogue, held to the same fixture.
func ResolvePin(cfg *config.Config, repo string, labels []LabelPin, field, login string) Pin {
	names := make([]string, 0, len(labels)+1)
	for _, l := range labels {
		if !slices.Contains(names, l.Name) {
			names = append(names, l.Name)
		}
	}
	if field != "" && !slices.Contains(names, field) {
		names = append(names, field)
	}
	switch len(names) {
	case 0:
		return Pin{}
	case 1:
	default:
		return Pin{Name: names[0], Refused: intake.ProfileConflict}
	}
	pin := Pin{Name: names[0]}
	rc := cfg.RepositoryFor(repo)
	maintainer := field != "" && rc.IsMaintainer(login)
	for _, l := range labels {
		maintainer = maintainer || rc.IsMaintainer(l.Actor)
	}
	switch {
	case !maintainer:
		pin.Refused = intake.NotMaintainer
	case !cfg.IsWorkflow(pin.Name):
		pin.Refused = intake.ProfileUnknown
	}
	return pin
}

// PinRefusal is the refusal pin names, for a task of class.
func PinRefusal(class string, pin Pin) intake.ProfileRefusal {
	return intake.ProfileRefusal{Code: pin.Refused, Class: class, Profile: pin.Name}
}
