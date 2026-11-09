// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/config"
)

// D8: migrating, then assembling the manifests with the residual file, gives the config the file
// gave: same repositories, classes, merge rules and catalogue.
func TestMigrateRoundTrips(t *testing.T) {
	want, err := config.Parse(file(t))
	if err != nil {
		t.Fatal(err)
	}
	manifests, residual, err := Migrate(file(t), "agent-system")
	if err != nil {
		t.Fatal(err)
	}
	objs, err := Decode(manifests)
	if err != nil {
		t.Fatalf("Decode(Migrate(...)): %v\n%s", err, manifests)
	}
	got, err := Assemble(residual, objs)
	if err != nil || len(got.Refused) != 0 {
		t.Fatalf("%v %+v", err, got.Refused)
	}
	for _, k := range CatalogueKeys {
		if strings.Contains(string(residual), "\n"+k+":") {
			t.Errorf("the residual file still has %s", k)
		}
	}
	if !equalRepos(t, want.Repos(), got.Config.Repos()) || !equalJSON(t, want.Tiers, got.Config.Tiers) ||
		!equalJSON(t, want.Templates, got.Config.Templates) || !equalJSON(t, want.Caps, got.Config.Caps) ||
		!equalJSON(t, want.Budgets, got.Config.Budgets) || !equalJSON(t, want.Triage, got.Config.Triage) {
		t.Fatalf("the round trip changed the config\nmanifests:\n%s\nresidual:\n%s", manifests, residual)
	}
}

func TestMigrateRefusesTheLegacyShape(t *testing.T) {
	if _, _, err := Migrate([]byte("factory: aws-0\nrepository: Smana/x\n"), "ns"); err == nil ||
		!strings.Contains(err.Error(), "into repositories[] first") {
		t.Fatalf("err = %v", err)
	}
}

func TestMigrateNamesAndSplitsTheMeter(t *testing.T) {
	in := "factory: f\nmeter: {url: u, prices: {a: 1}}\ntriage: {controlPercent: 5}\nrepositories:\n- name: A/x\n- name: B/x\n- name: C/zed\n"
	manifests, residual, err := Migrate([]byte(in), "ns")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"name: a-x", "name: b-x", "name: zed"} {
		if !strings.Contains(string(manifests), n) {
			t.Errorf("missing %q in\n%s", n, manifests)
		}
	}
	if !strings.Contains(string(residual), "url: u") || strings.Contains(string(residual), "prices") ||
		strings.Contains(string(residual), "triage") {
		t.Errorf("residual:\n%s", residual)
	}
}

func equalRepos(t *testing.T, a, b []config.Repository) bool {
	t.Helper()
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || !equalJSON(t, a[i].Maintainers, b[i].Maintainers) ||
			a[i].TriggerLabel != b[i].TriggerLabel || !equalJSON(t, a[i].Classes, b[i].Classes) ||
			!equalJSON(t, a[i].Merge, b[i].Merge) || !equalJSON(t, a[i].Schedules, b[i].Schedules) ||
			!equalJSON(t, a[i].Checks, b[i].Checks) || a[i].ClassAuthority != b[i].ClassAuthority {
			return false
		}
	}
	return true
}

func equalJSON(t *testing.T, a, b any) bool {
	t.Helper()
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	return bytes.Equal(x, y)
}
