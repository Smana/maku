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
				"field": ResolvePin(&f.Config, f.Repository, row.Class, nil, row.Profile, loginOf(row.Maintainer), row.Public),
				"label": ResolvePin(&f.Config, f.Repository, row.Class,
					[]LabelPin{{Name: row.Profile, Actor: loginOf(row.Maintainer)}}, "", "", row.Public),
			} {
				if pin.Name != row.Profile || pin.Refused != want {
					t.Errorf("%s: %+v, want refused %q", via, pin, want)
				}
				if pin.Refused != "" {
					_ = PinRefusal(&f.Config, f.Repository, row.Class, pin).Sentence()
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
		"two labels": {labels: []LabelPin{{"review-pair", "Smana"}, {"review-frontier", "Smana"}},
			want: Pin{Name: "review-pair", Refused: intake.ProfileConflict}},
		"label and field disagree": {labels: []LabelPin{{"review-pair", "Smana"}}, field: "review-frontier", login: "Smana",
			want: Pin{Name: "review-pair", Refused: intake.ProfileConflict}},
		"label and field agree": {labels: []LabelPin{{"review-pair", "Smana"}}, field: "review-pair", login: "Smana",
			want: Pin{Name: "review-pair"}},
		"a non-maintainer's label": {labels: []LabelPin{{"review-pair", "someone"}}, want: Pin{Name: "review-pair", Refused: intake.NotMaintainer}},
		"a maintainer's label, any field login": {labels: []LabelPin{{"review-pair", "Smana"}}, field: "review-pair", login: "someone",
			want: Pin{Name: "review-pair"}},
	} {
		if got := ResolvePin(cfg, f.Repository, "review", tc.labels, tc.field, tc.login, true); got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
}
