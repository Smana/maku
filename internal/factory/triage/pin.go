// SPDX-License-Identifier: Apache-2.0

package triage

import (
	"slices"

	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/intake"
)

// LabelPin is one profile:<name> label on a task's issue and its last applier.
type LabelPin struct {
	Name, Actor string
}

// Pin is a task's pinned profile as triage resolved it: Name is the profile a person asked for,
// "" when none did; Refused is a code of intake's closed list, "" when the pin is honoured.
type Pin struct {
	Name, Refused string
}

// Honoured is whether the pin stands: a person chose the profile and the factory agreed.
func (p Pin) Honoured() bool { return p.Name != "" && p.Refused == "" }

// ResolvePin is the factory's check of a pin (P6), the spec's Triage steps 2 to 4 against config:
// one profile named, by a maintainer (a label's last applier, or the intake's resolved sponsor
// login); the profile known, the class with profiles, not live or shadow, the floor or an allowed
// variant; tests first only on public data. The tests-paths check reads the repository, so the
// reconciler makes it on an honoured tests-first pin. intake.CheckPin is the same rule against the
// catalogue, held to the same fixture.
func ResolvePin(cfg *config.Config, repo, class string, labels []LabelPin, field, login string, public bool) Pin {
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
	cl, _ := rc.Class(class)
	p, known := cfg.HarnessProfiles[pin.Name]
	mode := catalogue.ModeOf(cl, cfg.NeedsMerger())
	switch {
	case !maintainer:
		pin.Refused = intake.NotMaintainer
	case !known:
		pin.Refused = intake.ProfileUnknown
	case cl.Harness == "":
		pin.Refused = intake.ClassNoProfiles
	case mode == catalogue.ModeLive || mode == catalogue.ModeShadow:
		pin.Refused = intake.ClassProfileFixed
	case pin.Name != cl.Harness && !slices.Contains(cl.HarnessAllowed, pin.Name):
		pin.Refused = intake.ProfileNotAllowed
	case p.TestFirst && !public:
		pin.Refused = intake.ProfileInternal
	}
	return pin
}

// PinRefusal is the refusal pin names, with the fields its sentence reads from config.
func PinRefusal(cfg *config.Config, repo, class string, pin Pin) intake.ProfileRefusal {
	cl, _ := cfg.RepositoryFor(repo).Class(class)
	return intake.ProfileRefusal{Code: pin.Refused, Class: class, Profile: pin.Name, Floor: cl.Harness,
		Mode: catalogue.ModeOf(cl, cfg.NeedsMerger()), Allowed: slices.Clone(cl.HarnessAllowed)}
}
