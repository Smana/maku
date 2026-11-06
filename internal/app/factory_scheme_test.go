// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/Smana/maku/internal/runwatch"
)

// Every runs kind the factory is deployed with builds a scheme that knows it: cloud-native-ref's XRD
// (production) and the portable AgentRun of charts/agent-factory's profile: demo, whose Go type the
// scheme already holds. The demo's case once panicked at start-up, after the GitHub App lookup, so an
// install test with a dummy App never reached it.
func TestTheFactorySchemeBuildsForEveryRunsKind(t *testing.T) {
	for name, k := range map[string]runwatch.Kind{
		"production (the XRD)":         runwatch.DefaultKind,
		"the demo (the portable kind)": {Group: "maku.ogenki.io", Namespace: "maku-runs", NamePrefix: "run-"},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := factoryScheme(k)
			if err != nil {
				t.Fatal(err)
			}
			if !s.Recognizes(k.GVK()) {
				t.Fatalf("the scheme does not know %v", k.GVK())
			}
		})
	}
}
