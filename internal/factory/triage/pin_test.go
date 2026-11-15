// SPDX-License-Identifier: Apache-2.0

package triage

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/intake"
)

// pinFixture is intake's testdata/pins.json: the rows intake.CheckPin holds too (Review Focus 1).
type pinFixture struct {
	Repository string        `json:"repository"`
	Config     config.Config `json:"config"`
	Pins       []struct {
		Name       string  `json:"name"`
		Class      string  `json:"class"`
		Profile    string  `json:"profile"`
		Maintainer bool    `json:"maintainer"`
		Public     bool    `json:"public"`
		Want       string  `json:"want"`
		Triage     *string `json:"triage"`
	} `json:"pins"`
}

func readPins(t *testing.T) pinFixture {
	t.Helper()
	raw, err := os.ReadFile("../../intake/testdata/pins.json")
	if err != nil {
		t.Fatal(err)
	}
	var f pinFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	f.Config.HarnessProfiles = f.Config.Workflows // what Parse does with the key (WF9)
	return f
}

func loginOf(maintainer bool) string {
	if maintainer {
		return "smana" // case-insensitive, as GitHub reads a login
	}
	return "someone"
}

// P6: triage holds every fixture row, through the intake field and through a label alike.
func TestResolvePinFixture(t *testing.T) {
	f := readPins(t)
	for _, row := range f.Pins {
		want := row.Want
		if row.Triage != nil {
			want = *row.Triage
		}
		t.Run(row.Name, func(t *testing.T) {
			for via, pin := range map[string]Pin{
				"field": ResolvePin(&f.Config, f.Repository, nil, row.Profile, loginOf(row.Maintainer)),
				"label": ResolvePin(&f.Config, f.Repository, []LabelPin{{Name: row.Profile, Actor: loginOf(row.Maintainer)}}, "", ""),
			} {
				if pin.Name != row.Profile || pin.Refused != want {
					t.Errorf("%s: %+v, want refused %q", via, pin, want)
				}
				if pin.Refused != "" {
					_ = PinRefusal(row.Class, pin).Sentence()
				}
			}
		})
	}
}

// Triage's own refusals and edges: two profiles, a label and a field that disagree, an agreeing
// pair, and a field from a non-maintainer beside a maintainer's label.
func TestResolvePinConflict(t *testing.T) {
	f := readPins(t)
	cfg := &f.Config
	for name, tc := range map[string]struct {
		labels []LabelPin
		field  string
		login  string
		want   Pin
	}{
		"no pin": {want: Pin{}},
		"two labels": {labels: []LabelPin{{"standard", "Smana"}, {"careful", "Smana"}},
			want: Pin{Name: "standard", Refused: intake.ProfileConflict}},
		"label and field disagree": {labels: []LabelPin{{"standard", "Smana"}}, field: "careful", login: "Smana",
			want: Pin{Name: "standard", Refused: intake.ProfileConflict}},
		"label and field agree": {labels: []LabelPin{{"standard", "Smana"}}, field: "standard", login: "Smana",
			want: Pin{Name: "standard"}},
		"a non-maintainer's label": {labels: []LabelPin{{"standard", "someone"}}, want: Pin{Name: "standard", Refused: intake.NotMaintainer}},
		"a maintainer's label, any field login": {labels: []LabelPin{{"standard", "Smana"}}, field: "standard", login: "someone",
			want: Pin{Name: "standard"}},
	} {
		if got := ResolvePin(cfg, f.Repository, tc.labels, tc.field, tc.login); got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
}

// Stage B (WF4): a pin is refused only for who pins, which workflow, and how many; the class, its
// mode and the task's data no longer refuse one.
func TestPinRefusalsAfterStageB(t *testing.T) {
	f := readPins(t)
	for code, labels := range map[string][]LabelPin{
		intake.NotMaintainer:   {{Name: "quick", Actor: "mallory"}},
		intake.ProfileUnknown:  {{Name: "nope", Actor: "Smana"}},
		intake.ProfileConflict: {{Name: "quick", Actor: "Smana"}, {Name: "careful", Actor: "Smana"}},
	} {
		if got := ResolvePin(&f.Config, f.Repository, labels, "", "").Refused; got != code {
			t.Errorf("want %s, got %s", code, got)
		}
	}
	if got := ResolvePin(&f.Config, f.Repository, []LabelPin{{Name: "quick", Actor: "Smana"}}, "", ""); !got.Honoured() {
		t.Fatalf("a maintainer's quick, below the default: %+v", got)
	}
}
