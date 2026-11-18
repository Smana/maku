// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/fanout"
)

// canStartOn calls task on a room on repo and returns canStart and canStartWhy.
func canStartOn(t *testing.T, e env, repo string) (bool, string) {
	t.Helper()
	got := call(t, e, viaRoomctl("dev"), "task", map[string]any{"task": roomID})
	if got.isError {
		t.Fatalf("task: %s", got.text)
	}
	var res taskBody
	if err := json.Unmarshal(got.structured, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return res.CanStart, res.CanStartWhy
}

// startEnv is the WF12 layout (#621): the planner on, start offered by default, the factory's
// catalogue serving catalogued, and allow as human.intake.plan.repositories (nil: unset).
func startEnv(t *testing.T, repo string, catalogued []string, allow map[string]bool, opts ...option) env {
	t.Helper()
	w := &gitHubWorld{now: time.Now(),
		writes:   map[string][]string{repo: {"dev"}},
		reads:    map[string][]string{repo: {"dev"}},
		unlinked: map[string]bool{}, inactive: map[string]bool{}}
	rm := taskFixtureRoom(roomID, repo, "Active", 1)
	rm.Spec.Owner = "human:dev"
	base := []option{mcpOn, withIntake(t, "session"),
		intakeOption(t, func(_ *fakeIntake, in *Intake) { in.Allow = allow }),
		withCatalogue(&fakeFactoryCatalogue{repos: catalogued}), w.option(),
		func(s *Server, _ *fanout.Hub, v *hubView) {
			s.StartDefault = true
			s.Rooms = roomClient(t, rm)
			v.mu.Lock()
			v.evs = []envelope.Event{progress(1, "working")}
			v.mu.Unlock()
		}}
	return setup(t, append(base, opts...)...)
}

// #621: with a factory catalogue and no human.intake.plan.repositories, start is offered on the
// catalogue's served repositories, which is what the planner plans for.
func TestCanStartReadsTheCatalogueWhenNoRepositoriesAreSet(t *testing.T) {
	e := startEnv(t, "Smana/ai-native-ref", []string{"Smana/ai-native-ref"}, nil)
	if ok, why := canStartOn(t, e, "Smana/ai-native-ref"); !ok {
		t.Fatalf("a writer on a catalogue repository cannot start: why=%q", why)
	}
}

// An explicit repositories list still narrows the catalogue.
func TestCanStartRepositoriesNarrowTheCatalogue(t *testing.T) {
	e := startEnv(t, "Smana/ai-native-ref", []string{"Smana/ai-native-ref", "Smana/other"}, map[string]bool{"Smana/other": true})
	if ok, why := canStartOn(t, e, "Smana/ai-native-ref"); ok || why != startNotOffered {
		t.Fatalf("got %v %q, want false %q", ok, why, startNotOffered)
	}
}

// Each cause of a start not offered has its own reason, and the broker logs it once per caller and
// repository.
func TestCanStartWhyNamesTheCause(t *testing.T) {
	e := startEnv(t, "Smana/ai-native-ref", []string{"Smana/elsewhere"}, nil)
	for range 3 {
		if ok, why := canStartOn(t, e, "Smana/ai-native-ref"); ok || why != startNotOffered {
			t.Fatalf("an unserved repository: got %v %q, want %q", ok, why, startNotOffered)
		}
	}
	if n := strings.Count(e.intake.logged(), startNotOffered); n != 1 {
		t.Fatalf("the reason is logged %d times, want once:\n%s", n, e.intake.logged())
	}

	unwired := startEnv(t, "Smana/ai-native-ref", []string{"Smana/ai-native-ref"}, nil,
		func(s *Server, _ *fanout.Hub, _ *hubView) { s.Identity, s.Access = nil, nil })
	// Without the D7 check the caller reads the room as an admin.
	got := call(t, unwired, viaRoomctl("dev", "X-Test-Admin", "1"), "task", map[string]any{"task": roomID})
	var res taskBody
	if err := json.Unmarshal(got.structured, &res); err != nil {
		t.Fatalf("%v: %s", err, got.text)
	}
	if res.CanStart || res.CanStartWhy != startAccessUnwired {
		t.Fatalf("no access check wired: got %v %q, want %q", res.CanStart, res.CanStartWhy, startAccessUnwired)
	}
}
