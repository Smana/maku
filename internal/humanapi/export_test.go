// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/intake"
)

// WholePlanBroker is a real broker handler for the terminal's end-to-end tests (package
// humanapi_test): the planner answers with the three-issue golden plan, human.intake.wholePlans is
// on, and every request authenticates as "dev", a maintainer who writes Smana/ai-native-ref. The
// handoff routes it serves are the ones maku start calls, unstubbed.
func WholePlanBroker(t *testing.T) http.Handler {
	t.Helper()
	w := world(nil)
	w.set(func() { w.writes = map[string][]string{"smana/ai-native-ref": {"dev"}} })
	e := setup(t, mcpOn, withIntake(t, "session"), actorOn(&actLog{}), withCatalogue(carefulCatalogue{}),
		withHandoffs(newMemHandoffStore()), w.option(), func(s *Server, _ *fanout.Hub, _ *hubView) {
			s.Intake.WholePlans, s.StartDefault = true, true
		})
	e.intake.replyWith(wholePlanModelReply(t, planFixture(t, "three")))
	routes := e.srv.Routes()
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Test-User", "dev")
		routes.ServeHTTP(rw, r)
	})
}

// wholePlanModelReply is the gateway's answer carrying every issue of p, in order, as the model's draft.
func wholePlanModelReply(t *testing.T, p intake.Plan) []byte {
	t.Helper()
	issues := make([]any, 0, len(p.Issues))
	for _, is := range p.Issues {
		issues = append(issues, map[string]any{"title": is.Title, "body": is.Body, "class": is.Class, "dependsOn": is.DependsOn})
	}
	draft, err := json.Marshal(map[string]any{"repository": p.Repository, "issues": issues})
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(string(draft))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(`{"choices":[{"message":{"content":` + string(content) + `}}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":20}}`)
}
