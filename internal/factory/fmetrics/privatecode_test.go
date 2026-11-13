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

// Review of #540: a repository the config no longer serves leaves both per-repository fence gauges,
// so AgentFactoryPrivateCodeUnacknowledged and AgentFactoryRepositoryNotOwned clear.
func TestForgetRepositoryDropsItsFenceSeries(t *testing.T) {
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.ListRepositories("Smana/private", "Smana/kept")
	s.PrivateCodeUnacknowledged(t.Context(), "Smana/private", true)
	s.RepositoryOwned(t.Context(), "Smana/private", false)
	s.PrivateCodeUnacknowledged(t.Context(), "Smana/kept", true)
	s.RepositoryOwned(t.Context(), "Smana/kept", true)
	s.ForgetRepository("smana/PRIVATE")
	body := scrape()
	for _, gone := range []string{`maku_factory_private_code_unacknowledged{repository="Smana/private"}`, `maku_factory_repository_owned{repository="Smana/private"}`} {
		if strings.Contains(body, gone) {
			t.Errorf("still served: %s", gone)
		}
	}
	for _, kept := range []string{`maku_factory_private_code_unacknowledged{repository="Smana/kept"} 1`, `maku_factory_repository_owned{repository="Smana/kept"} 1`} {
		if !strings.Contains(body, kept+"\n") {
			t.Errorf("missing %s\n%s", kept, body)
		}
	}
}
