// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Review #518: a factory with runs.gatewayIdentity stamps every claim it creates spec.meteredBy
// gateway, so a run-controller in model.mode secret refuses it rather than run it unmetered.
// Without the stamp, the claim is today's byte for byte.
func TestCreateStampsMeteredBy(t *testing.T) {
	for name, c := range map[string]struct {
		by   string
		want string
	}{
		"no gateway identity": {"", ""},
		"gateway identity":    {MeteredByGateway, "gateway"},
	} {
		t.Run(name, func(t *testing.T) {
			cl := newClient()
			cl.MeteredBy = c.by
			var got string
			var found bool
			cl.Created = func(_ context.Context, claim *unstructured.Unstructured) {
				got, found, _ = unstructured.NestedString(claim.Object, "spec", "meteredBy")
			}
			if err := cl.Create(t.Context(), spec()); err != nil {
				t.Fatal(err)
			}
			if got != c.want || found != (c.want != "") {
				t.Fatalf("spec.meteredBy %q (found %t), want %q", got, found, c.want)
			}
		})
	}
}

func TestSpecRefusesAnUnknownMeteredBy(t *testing.T) {
	s := spec()
	s.MeteredBy = "secret"
	if err := s.Validate(); err == nil {
		t.Fatal("spec.meteredBy is gateway or empty")
	}
}
