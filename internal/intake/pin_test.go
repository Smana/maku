// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/intake"
)

// pinFixture is testdata/pins.json, the rows intake and triage both hold (Review Focus 1).
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
	raw, err := os.ReadFile("testdata/pins.json")
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

// P6: intake checks a pin against the catalogue the way triage will check it against config.
func TestCheckPinFixture(t *testing.T) {
	f := readPins(t)
	for _, row := range f.Pins {
		t.Run(row.Name, func(t *testing.T) {
			c, ok := catalogue.Build(&f.Config, f.Repository, row.Maintainer)
			if !ok {
				t.Fatal("fixture repository is not served")
			}
			got := ""
			if r := intake.CheckPin(c, row.Class, row.Profile); r != nil {
				got = r.Code
				_ = r.Sentence() // every code it answers is in the closed list
			}
			if got != row.Want {
				t.Fatalf("CheckPin(%s, %s) = %q, want %q", row.Class, row.Profile, got, row.Want)
			}
		})
	}
}

// No pin, nothing to check: the factory's choice is never refused.
func TestCheckPinFactorysChoice(t *testing.T) {
	f := readPins(t)
	c, _ := catalogue.Build(&f.Config, f.Repository, false)
	if r := intake.CheckPin(c, "review", ""); r != nil {
		t.Fatalf("empty pin refused: %+v", r)
	}
}

// The refusal carries what its sentence names: the class and the workflow.
func TestCheckPinRefusalFields(t *testing.T) {
	f := readPins(t)
	c, _ := catalogue.Build(&f.Config, f.Repository, true)
	r := intake.CheckPin(c, "docs-links", "ghost")
	if r == nil || r.Class != "docs-links" || r.Profile != "ghost" || r.Code != intake.ProfileUnknown {
		t.Fatalf("unknown = %+v", r)
	}
}
