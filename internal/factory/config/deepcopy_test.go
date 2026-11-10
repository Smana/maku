// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

// The CRs embed these types (factory resources D16), so the API machinery deep-copies them: a
// copy shares no map, slice or pointer with its source.
func TestDeepCopySharesNothing(t *testing.T) {
	src := Class{Live: true, AllowedFiles: []string{"^docs/"},
		Monitor: &Monitor{Flux: &FluxWatch{Source: "flux-system/flux-system"}},
		Author:  &Author{Adopt: &Adopt{Logins: []string{"renovate[bot]"}}}}
	cp := src.DeepCopy()
	cp.AllowedFiles[0] = "x"
	cp.Monitor.Flux.Source = "x"
	cp.Author.Adopt.Logins[0] = "x"
	if src.AllowedFiles[0] != "^docs/" || src.Monitor.Flux.Source != "flux-system/flux-system" ||
		src.Author.Adopt.Logins[0] != "renovate[bot]" {
		t.Fatalf("the copy shares memory with its source: %+v", src)
	}
	l := Loop{Models: map[string]ModelAliases{"reviewer": {"frontier": "kimi"}}}
	lc := l.DeepCopy()
	lc.Models["reviewer"]["frontier"] = "x"
	if l.Models["reviewer"]["frontier"] != "kimi" {
		t.Fatal("Loop.Models is shared")
	}
}
