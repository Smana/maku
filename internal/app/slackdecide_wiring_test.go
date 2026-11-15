// SPDX-License-Identifier: Apache-2.0

package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Smana/maku/internal/humanapi"
)

// D14: human.slackDecide exists in the wiring or it does not. On, POST /api/slack/decisions is a
// route (no bearer is 403, the sponsored authenticator's refusal); off, it is the mux's 404,
// exactly as /mcp is when MCP is false. Routes() alone is enough: the mux is what is under test,
// and a Server with no Sponsored refuses every bearer rather than serving one.
func TestSlackAppWiring(t *testing.T) {
	off := &humanapi.Server{}
	for _, path := range []string{"/api/slack/decisions", "/api/slack/stops", "/api/slack/retries"} {
		if code := postRoute(t, off.Routes(), path); code != http.StatusNotFound {
			t.Fatalf("%s off: %d", path, code)
		}
	}
	on := &humanapi.Server{
		SlackDecide: &humanapi.SlackDecider{Principal: "system:slack-app", TeamID: "T01ABC"},
		SlackStop:   &humanapi.SlackActor{Principal: "system:slack-app", TeamID: "T01ABC"},
		SlackRetry:  &humanapi.SlackActor{Principal: "system:slack-app", TeamID: "T01ABC"},
	}
	for _, path := range []string{"/api/slack/decisions", "/api/slack/stops", "/api/slack/retries"} {
		if code := postRoute(t, on.Routes(), path); code != http.StatusForbidden {
			t.Fatalf("%s on: %d", path, code)
		}
	}
}

// postRoute posts an empty body to path on h and returns the status: the round trip a route's
// presence, or its absence, is read from.
func postRoute(t *testing.T, h http.Handler, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil))
	return rec.Code
}
