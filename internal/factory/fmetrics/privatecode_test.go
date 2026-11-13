// SPDX-License-Identifier: Apache-2.0

package fmetrics

import (
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// v4 D3, step 7 R1: 1 while a served repository's code is private and unacknowledged, else 0, by
// repository as the last fence pass read it.
func TestPrivateCodeUnacknowledgedGauge(t *testing.T) {
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.ListRepositories("Smana/private")
	s.PrivateCodeUnacknowledged(t.Context(), "Smana/private", true)
	s.PrivateCodeUnacknowledged(t.Context(), "Smana/unlisted", false)
	body := scrape()
	for _, want := range []string{
		`maku_factory_private_code_unacknowledged{repository="Smana/private"} 1`,
		`maku_factory_private_code_unacknowledged{repository="other"} 0`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
	s.PrivateCodeUnacknowledged(t.Context(), "Smana/private", false)
	if want := `maku_factory_private_code_unacknowledged{repository="Smana/private"} 0`; !strings.Contains(scrape(), want+"\n") {
		t.Errorf("missing %s", want)
	}
}
