// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"strings"
	"testing"
)

// The /preflight shape the components serve and the collector reads.
func TestDecodeComponent(t *testing.T) {
	got, err := DecodeComponent(strings.NewReader(`{"component":"broker","version":"v0.45.0","checks":[{"check":"database","outcome":"pass"}]}`))
	if err != nil || got.Component != "broker" || got.Version != "v0.45.0" || len(got.Checks) != 1 || got.Checks[0].Component != "broker" {
		t.Fatalf("%+v %v", got, err)
	}
	for name, body := range map[string]string{
		"not JSON":         "<html>",
		"an unknown field": `{"component":"broker","checks":[],"extra":1}`,
		"no component":     `{"checks":[]}`,
		"oversized":        `{"component":"broker","version":"` + strings.Repeat("v", maxComponentBytes) + `","checks":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeComponent(strings.NewReader(body)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
